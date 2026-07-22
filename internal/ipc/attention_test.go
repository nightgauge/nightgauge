package ipc

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// newAttentionTestServer builds a minimal Server backed by a real, store-wired
// autonomous scheduler rooted at a temp workspace.
func newAttentionTestServer(t *testing.T) *Server {
	t.Helper()
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), t.TempDir())
	if as.Attention() == nil {
		t.Fatal("attention store not wired")
	}
	return &Server{autonomousScheduler: as, writer: io.Discard}
}

func TestAttentionIPCRoundTrip(t *testing.T) {
	s := newAttentionTestServer(t)
	store := s.attentionStore()

	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "roundtrip:1",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          "round trip",
		Body:           "why",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme"}, // fleet-ish: no run_id
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	ctx := context.Background()

	// list → one open request
	listRaw, _ := json.Marshal(AttentionListParams{})
	lres, err := s.handleAttentionList(ctx, listRaw)
	if err != nil {
		t.Fatalf("handleAttentionList: %v", err)
	}
	if got := len(lres.(AttentionListResult).Requests); got != 1 {
		t.Fatalf("list returned %d, want 1", got)
	}

	// acknowledge → ok
	ackRaw, _ := json.Marshal(AttentionAcknowledgeParams{ID: id, Actor: "octocat"})
	ares, err := s.handleAttentionAcknowledge(ctx, ackRaw)
	if err != nil {
		t.Fatalf("handleAttentionAcknowledge: %v", err)
	}
	if !ares.(AttentionAcknowledgeResult).Ok {
		t.Error("acknowledge not ok")
	}

	// resolve (noop verb) → ok, not already-resolved
	resRaw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: "go", Actor: "octocat", Note: "done"})
	rres, err := s.handleAttentionResolve(ctx, resRaw)
	if err != nil {
		t.Fatalf("handleAttentionResolve: %v", err)
	}
	rr := rres.(AttentionResolveResult)
	if !rr.Ok || rr.AlreadyResolved {
		t.Errorf("resolve result = %+v, want ok && !alreadyResolved", rr)
	}

	// list again → gone from the open set
	lres2, _ := s.handleAttentionList(ctx, listRaw)
	if got := len(lres2.(AttentionListResult).Requests); got != 0 {
		t.Fatalf("open list after resolve = %d, want 0", got)
	}

	// the persisted request is terminal
	got, _, _ := store.Get(id)
	if got.Lifecycle.State != attention.StateResolved {
		t.Errorf("state = %q, want resolved", got.Lifecycle.State)
	}
}

func TestAttentionResolveRejectsUnknownOption(t *testing.T) {
	s := newAttentionTestServer(t)
	store := s.attentionStore()
	id, _ := attention.NewID()
	if _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "reject:1",
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityFYI,
		Title:          "t",
		Producer:       "test",
		Options:        []attention.Option{{ID: "ok", Verb: attention.VerbNoop}},
		DefaultAction:  attention.ExpireNoop,
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	resRaw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: "smuggled"})
	if _, err := s.handleAttentionResolve(context.Background(), resRaw); err == nil {
		t.Fatal("expected rejection of an undeclared option id")
	}
}
