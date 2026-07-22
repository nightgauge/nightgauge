package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// fakeResolver records the resolve call and returns a canned outcome/error.
type fakeResolver struct {
	mu       sync.Mutex
	calls    int
	gotID    string
	gotOpt   string
	gotActor string
	gotSteer string
	outcome  AttentionResolveOutcome
	err      error
}

func (f *fakeResolver) ApplyRelayedResolve(_ context.Context, id, opt, actor, steer string) (AttentionResolveOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotID, f.gotOpt, f.gotActor, f.gotSteer = id, opt, actor, steer
	return f.outcome, f.err
}

// ackRecorder is an httptest server standing in for the platform agent-command
// ack endpoint (POST /v1/agents/:agentId/commands/:commandId/ack).
func ackRecorder(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var acked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path: /v1/agents/{agentId}/commands/{commandId}/ack
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPost && len(parts) == 6 && parts[0] == "v1" && parts[1] == "agents" && parts[5] == "ack" {
			mu.Lock()
			acked = append(acked, parts[4]) // commandId
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"runId":"r-1"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return srv, &acked
}

func resolveCmd(t *testing.T, id, requestID, optionID, actor, steer string) PendingCommand {
	t.Helper()
	payload, err := json.Marshal(AttentionResolvePayload{
		RequestID: requestID,
		OptionID:  optionID,
		Verb:      "autonomous.resume",
		Actor:     actor,
		SteerText: steer,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return PendingCommand{ID: id, Type: AttentionResolveCommandType, Payload: payload}
}

func TestConsume_ValidApplies_ExecutesAndAcks(t *testing.T) {
	srv, acked := ackRecorder(t)
	defer srv.Close()
	c := onlineClient(t, srv.URL)

	res := &fakeResolver{outcome: AttentionResolveOutcome{Applied: true}}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	cmd := resolveCmd(t, "cmd-1", "dr_req_aaaaaaaa", "resume", "octocat", "skip flaky test")
	outcome, err := consumer.Consume(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !outcome.Applied {
		t.Errorf("outcome.Applied = false, want true")
	}
	if res.calls != 1 {
		t.Fatalf("resolver called %d times, want 1", res.calls)
	}
	if res.gotID != "dr_req_aaaaaaaa" || res.gotOpt != "resume" || res.gotActor != "octocat" || res.gotSteer != "skip flaky test" {
		t.Errorf("resolver got (%q,%q,%q,%q)", res.gotID, res.gotOpt, res.gotActor, res.gotSteer)
	}
	if len(*acked) != 1 || (*acked)[0] != "cmd-1" {
		t.Errorf("acked = %v, want [cmd-1]", *acked)
	}
}

func TestConsume_InvalidOptionRejected_Acked(t *testing.T) {
	srv, acked := ackRecorder(t)
	defer srv.Close()
	c := onlineClient(t, srv.URL)

	// The resolver rejects (store.Resolve → ValidateOption fails on an unknown
	// option / unregistered verb).
	res := &fakeResolver{err: errRejected}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	cmd := resolveCmd(t, "cmd-2", "dr_req_bbbbbbbb", "bogus", "octocat", "")
	outcome, err := consumer.Consume(context.Background(), cmd)
	if err != nil {
		t.Fatalf("a rejection must be acked, not returned as a transport error, got: %v", err)
	}
	if outcome.Applied || outcome.AlreadyResolved {
		t.Errorf("rejected resolve should not report applied/already-resolved: %+v", outcome)
	}
	if res.calls != 1 {
		t.Fatalf("resolver called %d times, want 1", res.calls)
	}
	if len(*acked) != 1 || (*acked)[0] != "cmd-2" {
		t.Errorf("rejected command not acked (acked=%v) — it would be redelivered forever", *acked)
	}
}

func TestConsume_AlreadyResolved_AckedAsAlreadyResolved(t *testing.T) {
	srv, acked := ackRecorder(t)
	defer srv.Close()
	c := onlineClient(t, srv.URL)

	res := &fakeResolver{outcome: AttentionResolveOutcome{AlreadyResolved: true}}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	cmd := resolveCmd(t, "cmd-3", "dr_req_cccccccc", "resume", "dashboard", "")
	outcome, err := consumer.Consume(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !outcome.AlreadyResolved || outcome.Applied {
		t.Errorf("outcome = %+v, want AlreadyResolved only", outcome)
	}
	if len(*acked) != 1 || (*acked)[0] != "cmd-3" {
		t.Errorf("already-resolved command not acked: %v", *acked)
	}
}

func TestConsume_WrongType_Ignored(t *testing.T) {
	srv, acked := ackRecorder(t)
	defer srv.Close()
	c := onlineClient(t, srv.URL)
	res := &fakeResolver{outcome: AttentionResolveOutcome{Applied: true}}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	cmd := PendingCommand{ID: "cmd-x", Type: "pipeline.run", Payload: json.RawMessage(`{}`)}
	if _, err := consumer.Consume(context.Background(), cmd); err != nil {
		t.Fatalf("Consume of a foreign type should be a silent no-op, got: %v", err)
	}
	if res.calls != 0 {
		t.Errorf("resolver called for a foreign command type")
	}
	if len(*acked) != 0 {
		t.Errorf("a foreign command must not be acked by this consumer: %v", *acked)
	}
}

func TestConsume_MalformedPayload_AckedAndErrored(t *testing.T) {
	srv, acked := ackRecorder(t)
	defer srv.Close()
	c := onlineClient(t, srv.URL)
	res := &fakeResolver{}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	cmd := PendingCommand{ID: "cmd-bad", Type: AttentionResolveCommandType, Payload: json.RawMessage(`{not json`)}
	if _, err := consumer.Consume(context.Background(), cmd); err == nil {
		t.Fatalf("malformed payload should return a parse error")
	}
	if res.calls != 0 {
		t.Errorf("resolver should not run on a malformed payload")
	}
	if len(*acked) != 1 || (*acked)[0] != "cmd-bad" {
		t.Errorf("poison command must be acked so it is not redelivered: %v", *acked)
	}
}

// writeSSEFrame writes one `event: command` frame (with a `:ping` keepalive
// before it) to an SSE response and flushes.
func writeSSEFrame(t *testing.T, w http.ResponseWriter, commandID, requestID, optionID string) {
	t.Helper()
	payload := fmt.Sprintf(
		`{"commandId":%q,"type":"attention_resolve","commandType":"attention_resolve","payload":{"requestId":%q,"optionId":%q,"verb":"autonomous.resume","args":{},"actor":"dashboard","steerText":null},"createdAt":"2026-07-20T00:00:00.000Z","expiresAt":"2026-07-20T00:05:00.000Z"}`,
		commandID, requestID, optionID,
	)
	// `:ping` keepalive comment then the real command frame.
	if _, err := fmt.Fprintf(w, ":ping\n\nevent: command\ndata: %s\n\n", payload); err != nil {
		t.Fatalf("write SSE frame: %v", err)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestAttentionCommandStream_DispatchesAndAcks proves the SSE consumer parses an
// `event: command` frame (ignoring `:ping` keepalives), dispatches it to the
// resolver, and acks over POST /v1/agents/:id/commands/:cmd/ack.
func TestAttentionCommandStream_DispatchesAndAcks(t *testing.T) {
	var mu sync.Mutex
	var acked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent-1/commands":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSEFrame(t, w, "cmd-sse-1", "dr_sse_1", "resume")
			<-r.Context().Done() // hold the stream open until the client cancels
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			mu.Lock()
			acked = append(acked, parts[4]) // commandId
			mu.Unlock()
			_, _ = w.Write([]byte(`{"runId":"r-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := onlineClient(t, srv.URL)
	res := &fakeResolver{outcome: AttentionResolveOutcome{Applied: true}}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runAttentionCommandStream(ctx, c, consumer, "agent-1", nil, 5*time.Millisecond, 20*time.Millisecond)

	waitFor(t, "resolver dispatch", func() bool {
		res.mu.Lock()
		defer res.mu.Unlock()
		return res.calls == 1
	})
	if res.gotID != "dr_sse_1" || res.gotOpt != "resume" || res.gotActor != "dashboard" {
		t.Errorf("resolver got (%q,%q,%q), want (dr_sse_1,resume,dashboard)", res.gotID, res.gotOpt, res.gotActor)
	}
	waitFor(t, "ack", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(acked) == 1 && acked[0] == "cmd-sse-1"
	})
}

// TestAttentionCommandStream_404SignalsAgentGone proves an HTTP 404 fires
// onAgentGone exactly once and stops the stream (the re-register signal).
func TestAttentionCommandStream_404SignalsAgentGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
	}))
	defer srv.Close()

	c := onlineClient(t, srv.URL)
	consumer := NewAttentionCommandConsumer(&fakeResolver{}, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	var gone atomic.Int32
	done := make(chan struct{})
	go func() {
		runAttentionCommandStream(context.Background(), c, consumer, "agent-1", func() { gone.Add(1) }, 5*time.Millisecond, 20*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return after 404")
	}
	if gone.Load() != 1 {
		t.Fatalf("onAgentGone fired %d times, want exactly 1", gone.Load())
	}
}

// TestAttentionCommandStream_ReconnectsAfterDisconnect proves a mid-stream
// disconnect triggers a reconnect that dispatches the next command.
func TestAttentionCommandStream_ReconnectsAfterDisconnect(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack") {
			_, _ = w.Write([]byte(`{"runId":"r"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agents/agent-1/commands" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := conns.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// First connection: send one frame then RETURN (server closes → client
		// sees EOF and must reconnect). Second: send a second frame then hold.
		writeSSEFrame(t, w, fmt.Sprintf("cmd-%d", n), fmt.Sprintf("dr_%d", n), "resume")
		if n >= 2 {
			<-r.Context().Done()
		}
	}))
	defer srv.Close()

	c := onlineClient(t, srv.URL)
	res := &fakeResolver{outcome: AttentionResolveOutcome{Applied: true}}
	consumer := NewAttentionCommandConsumer(res, NewCommandService(c).AcknowledgeAgentCommand, "agent-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runAttentionCommandStream(ctx, c, consumer, "agent-1", nil, 5*time.Millisecond, 20*time.Millisecond)

	// Two dispatches proves the client reconnected after the first disconnect.
	waitFor(t, "reconnect + second dispatch", func() bool {
		res.mu.Lock()
		defer res.mu.Unlock()
		return res.calls >= 2
	})
	if conns.Load() < 2 {
		t.Fatalf("server saw %d connections, want >= 2 (reconnect)", conns.Load())
	}
}

// storeResolver drives the REAL single-writer store through the resolver
// interface — used by the race test to exercise the store's CAS.
type storeResolver struct{ store *attention.Store }

func (r storeResolver) ApplyRelayedResolve(ctx context.Context, id, opt, actor, steer string) (AttentionResolveOutcome, error) {
	res, err := r.store.Resolve(ctx, id, opt, actor, steer, "", attention.NoopExecutor{})
	if err != nil {
		return AttentionResolveOutcome{}, err
	}
	return AttentionResolveOutcome{
		Applied:         !res.AlreadyResolved,
		AlreadyResolved: res.AlreadyResolved,
		VerbErr:         res.VerbErr,
	}, nil
}

// TestAttentionResolve_RaceLocalVsCommand asserts CAS safety when a local resolve
// and an incoming dashboard command resolve the SAME request concurrently. Run
// under `go test -race`: exactly one wins, the other is a safe no-op, and the
// store is never corrupted. (ADR 015 §D: local resolution wins; the late command
// is acked as already-resolved.)
func TestAttentionResolve_RaceLocalVsCommand(t *testing.T) {
	store := attention.New(t.TempDir())
	req := attention.DecisionRequest{
		ID:             "dr_race-00000001",
		IdempotencyKey: "race:fleet",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          "race",
		Body:           "why",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme", Issue: 7},
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}
	if _, err := store.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	var ackCount int32
	ackFn := func(_ context.Context, _, _ string) (string, error) {
		atomic.AddInt32(&ackCount, 1)
		return "", nil
	}
	consumer := NewAttentionCommandConsumer(storeResolver{store}, ackFn, "agent-1")

	var wg sync.WaitGroup
	wg.Add(2)
	// Goroutine A: a local IPC-style resolve.
	go func() {
		defer wg.Done()
		_, _ = store.Resolve(context.Background(), req.ID, "go", "local-user", "", "", attention.NoopExecutor{})
	}()
	// Goroutine B: the dashboard-relayed command for the same request.
	go func() {
		defer wg.Done()
		_, _ = consumer.Consume(context.Background(), resolveCmd(t, "cmd-race", req.ID, "leave", "dashboard", ""))
	}()
	wg.Wait()

	got, ok, err := store.Get(req.ID)
	if err != nil || !ok {
		t.Fatalf("Get after race: ok=%v err=%v", ok, err)
	}
	if got.Lifecycle.State != attention.StateResolved {
		t.Fatalf("final state = %q, want resolved (exactly one writer must win)", got.Lifecycle.State)
	}
	if got.Lifecycle.Resolved == nil {
		t.Fatalf("resolved record missing after race")
	}
	if opt := got.Lifecycle.Resolved.OptionID; opt != "go" && opt != "leave" {
		t.Fatalf("resolved option = %q, want one of go|leave", opt)
	}
	if atomic.LoadInt32(&ackCount) != 1 {
		t.Errorf("command consumer should ack exactly once, acked %d", ackCount)
	}
}

var errRejected = &resolveRejectedError{}

type resolveRejectedError struct{}

func (*resolveRejectedError) Error() string {
	return "attention: option \"bogus\" is not declared on request"
}
