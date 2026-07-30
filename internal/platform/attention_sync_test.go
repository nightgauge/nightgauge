package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// fakeLister is a test double for the attention store's read side.
type fakeLister struct {
	mu   sync.Mutex
	reqs []attention.DecisionRequest
}

func (f *fakeLister) List(_ attention.ListFilter) ([]attention.DecisionRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]attention.DecisionRequest, len(f.reqs))
	copy(out, f.reqs)
	return out, nil
}

func (f *fakeLister) set(reqs []attention.DecisionRequest) {
	f.mu.Lock()
	f.reqs = reqs
	f.mu.Unlock()
}

func sampleRequest(id string, state attention.State) attention.DecisionRequest {
	return attention.DecisionRequest{
		SchemaVersion:  attention.SchemaVersion,
		ID:             id,
		IdempotencyKey: "cascade-pause:fleet",
		Kind:           attention.KindResume,
		Severity:       attention.SeverityBlockingFleet,
		Title:          "Fleet stopped — cascade circuit breaker tripped",
		Body:           "Multiple failures tripped the breaker.",
		Producer:       "cascade-breaker",
		Context:        attention.Context{Repo: "octocat/acme-web", Issue: 42},
		Options: []attention.Option{
			{ID: "resume", Label: "Resume fleet", Verb: attention.VerbAutonomousResume},
			{ID: "keep-paused", Label: "Keep paused", Verb: attention.VerbNoop},
		},
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		ExpiresAt:     time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		DefaultAction: "keep-paused",
		Lifecycle:     attention.Lifecycle{State: state},
	}
}

// echoAcceptAll is a faithful stub of a fully-accepting `PUT
// /v1/attention/sync`: it echoes every request it received back in `items`.
//
// Echoing matters. `items` is the acknowledgement set (#214) — a stub that
// returns `items:[]` while claiming success models a server that accepted
// nothing, which is exactly the state the client must now refuse to watermark.
// The previous stub did that and passed only because the client ignored the
// field, which is how the bug survived its own test.
func echoAcceptAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Requests []attention.DecisionRequest `json:"requests"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	resp, _ := json.Marshal(map[string]any{
		"synced":   len(body.Requests),
		"items":    body.Requests,
		"rejected": []any{},
	})
	_, _ = w.Write(resp)
}

// echoRejecting is the partial-acceptance stub: it mirrors every card EXCEPT
// the ids in reject, which it reports in `rejected` — the real endpoint's
// per-item validation isolation, which returns 200 either way.
func echoRejecting(reject map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Requests []attention.DecisionRequest `json:"requests"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)

		items := make([]attention.DecisionRequest, 0, len(body.Requests))
		rejected := make([]map[string]string, 0)
		for _, req := range body.Requests {
			if reason, skip := reject[req.ID]; skip {
				rejected = append(rejected, map[string]string{"id": req.ID, "reason": reason})
				continue
			}
			items = append(items, req)
		}
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]any{
			"synced":   len(items),
			"items":    items,
			"rejected": rejected,
		})
		_, _ = w.Write(resp)
	}
}

// onlineClient returns a platform Client pointed at srv, forced online, with an
// agent id set so the sync body carries agent_id/machine_id.
func onlineClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: baseURL, APIKey: "test-key", AgentID: "00000000-0000-4000-8000-000000000001"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)
	return c
}

func TestAttentionSync_PayloadConstruction(t *testing.T) {
	var got []byte
	var authHdr string
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/attention/sync" && r.Method == http.MethodPut {
			atomic.AddInt32(&hits, 1)
			got, _ = io.ReadAll(r.Body)
			authHdr = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"synced":1,"items":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-000000000001", attention.StateOpen)})

	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 PUT /v1/attention/sync, got %d", hits)
	}
	if authHdr != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", authHdr)
	}

	var body attentionSyncBody
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("unmarshal sync body: %v", err)
	}
	// Mirror-only until registration: agent_id must be omitted (the machine id is
	// not a registered platform agent — sending it as agent_id 500s the sweep,
	// #341). machine_id still scopes the mirror.
	if body.AgentID != "" {
		t.Errorf("agent_id = %q, want empty (mirror-only, agent_id omitted)", body.AgentID)
	}
	if body.MachineID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("machine_id = %q, want the machine id", body.MachineID)
	}
	// Verify agent_id is truly absent from the wire (omitempty), not just "".
	var topRaw map[string]json.RawMessage
	if err := json.Unmarshal(got, &topRaw); err != nil {
		t.Fatalf("unmarshal raw body: %v", err)
	}
	if _, present := topRaw["agent_id"]; present {
		t.Errorf("wire body carries agent_id key in mirror-only mode — must be omitted")
	}
	if len(body.Requests) != 1 {
		t.Fatalf("requests len = %d, want 1", len(body.Requests))
	}
	r := body.Requests[0]
	if r.ID != "dr_01912d3e-7f4a-7b1e-8c2a-000000000001" {
		t.Errorf("request id = %q", r.ID)
	}
	if r.IdempotencyKey != "cascade-pause:fleet" || r.Producer != "cascade-breaker" {
		t.Errorf("request identity fields lost: key=%q producer=%q", r.IdempotencyKey, r.Producer)
	}
	if r.Lifecycle.State != attention.StateOpen {
		t.Errorf("lifecycle.state = %q, want open", r.Lifecycle.State)
	}

	// Verify the raw JSON is snake_case end-to-end (byte-for-byte the store shape).
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(got, &raw)
	var reqsRaw []map[string]json.RawMessage
	_ = json.Unmarshal(raw["requests"], &reqsRaw)
	for _, key := range []string{"schema_version", "idempotency_key", "default_action", "created_at", "expires_at"} {
		if _, ok := reqsRaw[0][key]; !ok {
			t.Errorf("wire request missing snake_case key %q", key)
		}
	}
}

// TestAttentionSync_LateBoundAgentID proves the fix for #341: a push before
// registration omits agent_id (mirror-only), and after SetAgentID every push
// carries the registered agent id. It also proves SetAgentID clears the
// watermark so the same (unchanged) request is re-pushed once, backfilling
// agent_id on the already-mirrored row.
func TestAttentionSync_LateBoundAgentID(t *testing.T) {
	var mu sync.Mutex
	var bodies []attentionSyncBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b attentionSyncBody
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"synced":1,"items":[]}`))
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-00000000aaaa", attention.StateOpen)})

	// First sweep: mirror-only, agent_id omitted.
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll pre-register: %v", err)
	}

	// Register: late-bind the platform-assigned agent id.
	svc.SetAgentID("11111111-1111-4111-8111-111111111111")

	// Second sweep: the same unchanged request must re-push (watermark cleared)
	// and now carry agent_id.
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll post-register: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 pushes (pre + post register re-push), got %d", len(bodies))
	}
	if bodies[0].AgentID != "" {
		t.Errorf("pre-register push agent_id = %q, want empty", bodies[0].AgentID)
	}
	if bodies[1].AgentID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("post-register push agent_id = %q, want the registered id", bodies[1].AgentID)
	}
	// machine_id is stable across both pushes.
	for i, b := range bodies {
		if b.MachineID != "00000000-0000-4000-8000-000000000001" {
			t.Errorf("push %d machine_id = %q, want the machine id", i, b.MachineID)
		}
	}
}

func TestAttentionSync_WatermarkSkipsUnchanged(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		echoAcceptAll(w, r)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-000000000002", attention.StateOpen)})

	// First sweep pushes the request.
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll #1: %v", err)
	}
	if hits != 1 {
		t.Fatalf("after first sweep hits = %d, want 1", hits)
	}

	// Second sweep, request unchanged → watermark skips it, no HTTP.
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll #2: %v", err)
	}
	if hits != 1 {
		t.Fatalf("unchanged request re-pushed: hits = %d, want 1", hits)
	}

	// Lifecycle change (open → resolved) makes it dirty again → one more push.
	resolved := sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-000000000002", attention.StateResolved)
	resolved.Lifecycle.Resolved = &attention.ResolvedRecord{Actor: "octocat", At: time.Now().UTC().Format(time.RFC3339Nano), OptionID: "resume"}
	lister.set([]attention.DecisionRequest{resolved})
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll #3: %v", err)
	}
	if hits != 2 {
		t.Fatalf("changed request not re-pushed: hits = %d, want 2", hits)
	}
}

func TestAttentionSync_OfflineNoOp(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Client left OFFLINE (default) — no setMode(ModeOnline).
	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k", AgentID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAttentionSyncService(c)
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-000000000003", attention.StateOpen)})

	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll offline: %v", err)
	}
	svc.OnTransition(context.Background(), ptr(sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-000000000003", attention.StateOpen)))
	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("offline uploader made %d HTTP call(s) — must be a no-op", hits)
	}
}

func TestAttentionSync_NilClientNoOp(t *testing.T) {
	svc := NewAttentionSyncService(nil)
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-000000000004", attention.StateOpen)})
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll with nil client should be a no-op, got: %v", err)
	}
}

func ptr(r attention.DecisionRequest) *attention.DecisionRequest { return &r }

// TestAttentionSync_UnacknowledgedStaysDirty is the #214 regression.
//
// The endpoint validates per item: a card it refuses is skipped and the call
// still returns 200. Watermarking on the status rather than the acknowledgement
// set told this service the card was mirrored, which disarmed the 30-second
// reconciliation sweep — the one mechanism that would have re-sent it. That is
// how every auto_resolved card stayed invisible on the dashboard for months
// while both sides reported success.
func TestAttentionSync_UnacknowledgedStaysDirty(t *testing.T) {
	const acceptedID = "dr_01912d3e-7f4a-7b1e-8c2a-00000000000a"
	const rejectedID = "dr_01912d3e-7f4a-7b1e-8c2a-00000000000b"

	var pushes int32
	sawRejected := make(chan int, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushes, 1)
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Requests []attention.DecisionRequest `json:"requests"`
		}
		_ = json.Unmarshal(raw, &body)
		n := 0
		for _, req := range body.Requests {
			if req.ID == rejectedID {
				n++
			}
		}
		sawRejected <- n
		r.Body = io.NopCloser(bytes.NewReader(raw))
		echoRejecting(map[string]string{
			rejectedID: "lifecycle.state: unrecognized value",
		})(w, r)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{
		sampleRequest(acceptedID, attention.StateOpen),
		sampleRequest(rejectedID, attention.StateOpen),
	})

	for i := 1; i <= 3; i++ {
		if err := svc.SyncAll(context.Background(), lister); err != nil {
			t.Fatalf("SyncAll #%d: %v", i, err)
		}
	}

	// Three sweeps, three pushes: the rejected card keeps the batch dirty.
	if got := atomic.LoadInt32(&pushes); got != 3 {
		t.Fatalf("pushes = %d, want 3 — an unacknowledged card must be retried every sweep", got)
	}
	close(sawRejected)
	for n := range sawRejected {
		if n != 1 {
			t.Fatalf("a sweep carried %d copies of the rejected card, want 1", n)
		}
	}

	// The accepted card must NOT be re-sent — only the rejected one stays dirty.
	svc.mu.Lock()
	_, acceptedWatermarked := svc.watermark[acceptedID]
	_, rejectedWatermarked := svc.watermark[rejectedID]
	misses := svc.unacked[rejectedID]
	svc.mu.Unlock()

	if !acceptedWatermarked {
		t.Error("accepted card was not watermarked — it will be re-pushed forever")
	}
	if rejectedWatermarked {
		t.Error("rejected card was watermarked: the sweep can never recover it (this is #214)")
	}
	if misses != 3 {
		t.Errorf("unacked[%s] = %d, want 3 — repeated rejection must be counted, not silent", rejectedID, misses)
	}
}

// TestAttentionSync_UnparseableBodyFallsBackToStatus pins the fail-safe
// direction. "Cannot tell what was accepted" must not collapse into "nothing
// was accepted": that would re-push the entire dirty set every 30 seconds
// forever. A stale card is recoverable; a permanent push storm against a server
// we cannot parse is not an improvement.
func TestAttentionSync_UnparseableBodyFallsBackToStatus(t *testing.T) {
	var pushes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&pushes, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`<!doctype html><html>gateway</html>`))
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{
		sampleRequest("dr_01912d3e-7f4a-7b1e-8c2a-00000000000c", attention.StateOpen),
	})

	for i := 0; i < 3; i++ {
		if err := svc.SyncAll(context.Background(), lister); err != nil {
			t.Fatalf("SyncAll #%d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&pushes); got != 1 {
		t.Fatalf("pushes = %d, want 1 — an unreadable 2xx body must not spin the sweep", got)
	}
}

// TestAttentionSync_RecoversOnceServerAccepts is the end state the operator
// sees: once the mirror is upgraded to understand the state, the card the sweep
// kept retrying lands, and stops being retried. This is the half that makes the
// retry loop terminate rather than run forever.
func TestAttentionSync_RecoversOnceServerAccepts(t *testing.T) {
	const id = "dr_01912d3e-7f4a-7b1e-8c2a-00000000000d"

	var accept atomic.Bool
	var pushes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushes, 1)
		if accept.Load() {
			echoAcceptAll(w, r)
			return
		}
		echoRejecting(map[string]string{id: "lifecycle.state: unrecognized value"})(w, r)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest(id, attention.StateAutoResolved)})

	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll (rejecting): %v", err)
	}

	// Server is upgraded and now accepts the state.
	accept.Store(true)
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll (accepting): %v", err)
	}
	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll (settled): %v", err)
	}

	if got := atomic.LoadInt32(&pushes); got != 2 {
		t.Fatalf("pushes = %d, want 2 — the card should settle once accepted", got)
	}
	svc.mu.Lock()
	_, watermarked := svc.watermark[id]
	misses := svc.unacked[id]
	svc.mu.Unlock()
	if !watermarked {
		t.Error("card was accepted but not watermarked")
	}
	if misses != 0 {
		t.Errorf("unacked[%s] = %d after acceptance, want 0", id, misses)
	}
}
