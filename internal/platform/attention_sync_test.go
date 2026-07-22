package platform

import (
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"synced":1,"items":[]}`))
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
