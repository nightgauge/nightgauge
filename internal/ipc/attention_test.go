package ipc

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// testRepos are the repo slugs the attention tests raise against. They are
// REGISTERED on the test server's resolver because attention.raise rejects a
// repo this daemon has no configuration for (#305 review) — an unconfigured
// (repo, issue) pair is an unbounded card-injection primitive.
var testRepos = []string{"octocat/acme", "o/r"}

// newAttentionTestServer builds a minimal Server backed by a real, store-wired
// autonomous scheduler rooted at a temp workspace, with testRepos registered
// and pointed at that same root.
func newAttentionTestServer(t *testing.T) *Server {
	t.Helper()
	root := layouttest.Repo(t)
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), root)
	if as.Attention() == nil {
		t.Fatal("attention store not wired")
	}
	resolver := NewClientResolver(nil, false)
	for _, slug := range testRepos {
		owner, name := splitSlug(slug)
		resolver.RegisterRepo(owner, name, root)
	}
	s := &Server{
		autonomousScheduler: as,
		writer:              io.Discard,
		workspaceRoot:       root,
		resolver:            resolver,
		activeRuntimes:      make(map[string]*runEntry),
		methods:             make(map[string]Handler),
	}
	// Register the real method table so tests can drive the daemon through the
	// SAME entry points a socket caller uses (#305 round-4 review). The
	// budget-ceiling corroboration rules are about what
	// `pipeline.notifyStageTransition` can and cannot establish, and a test that
	// seeded `activeRuntimes` by hand asserted their premise instead of
	// exercising it.
	s.registerMethods()
	return s
}

func splitSlug(slug string) (owner, name string) {
	for i := 0; i < len(slug); i++ {
		if slug[i] == '/' {
			return slug[:i], slug[i+1:]
		}
	}
	return "", slug
}

func TestAttentionIPCRoundTrip(t *testing.T) {
	s := newAttentionTestServer(t)
	store := s.attentionStore()

	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
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
	if _, _, err := store.Raise(attention.DecisionRequest{
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

// The Approve & re-queue card hands the issue to the shared architecture-hold
// release (#2717): it adds no Go-side queue item (one that never woke
// fillSlots stalled the fleet) and keeps the Failed entry, now ReleasePending,
// so the release can retry a failed board write.
func TestReleaseApprovedArchitecture_UsesSharedReleaseAndAddsNoQueueItem(t *testing.T) {
	root := layouttest.Repo(t)
	statePath, err := orchestrator.AutonomousStatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	seed := orchestrator.AutonomousState{Failed: []orchestrator.FailedItem{{
		Repo: "o/r", Number: 7, Kind: orchestrator.TerminalKindArchitectureApprovalRequired, AttemptCount: 1,
	}}}
	b, _ := json.Marshal(seed)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	// A configured board with no forge client: the release cannot read the
	// PR list, so it keeps the entry pending instead of dropping it.
	as := orchestrator.NewAutonomousScheduler(nil, nil,
		[]depgraph.RepoConfig{{Owner: "o", Name: "r", Project: 3}}, nil,
		orchestrator.DefaultAutonomousConfig(), root)
	t.Cleanup(func() { as.Shutdown(time.Second) })
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root})
	s := &Server{autonomousScheduler: as, scheduler: sched, writer: io.Discard, workspaceRoot: root}

	if err := s.releaseApprovedArchitecture(context.Background(), "o/r", 7, "approved:architecture"); err != nil {
		t.Fatalf("releaseApprovedArchitecture: %v", err)
	}

	if n := len(sched.QueuePendingItems()); n != 0 {
		t.Errorf("queue has %d pending items, want 0 — Ready on the board is the re-queue", n)
	}
	failed := as.Status().Failed
	if len(failed) != 1 || !failed[0].ReleasePending {
		t.Fatalf("Failed = %+v, want the hold kept as ReleasePending for the shared release", failed)
	}
}
