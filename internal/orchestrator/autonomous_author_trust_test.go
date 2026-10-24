package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// unrefinedIssuesServer returns an httptest server that responds to
// ListIssuesExcludingLabels' GraphQL query with two open issues: #1 authored
// by a stranger with read access and #2 by a maintainer with write access.
func unrefinedIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveRepoLabels(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"repository": {
					"issues": {
						"pageInfo": {"hasNextPage": false, "endCursor": ""},
						"nodes": [
							{
								"number": 1,
								"title": "Stranger's issue",
								"createdAt": "2026-01-01T00:00:00Z",
								"author": {"login": "stranger", "__typename": "User"},
								"labels": {"nodes": []}
							},
							{
								"number": 2,
								"title": "Owner's issue",
								"createdAt": "2026-01-01T00:00:00Z",
								"author": {"login": "maintainer", "__typename": "User"},
								"labels": {"nodes": []}
							}
						]
					}
				}
			}
		}`))
	}))
}

// TestRunRefinementCycle_SkipsUntrustedAuthor is the AC #3 assertion for #270:
// runRefinementCycle must never dispatch refinement for an untrusted-author
// candidate, even though ListIssuesExcludingLabels only filters on labels.
func TestRunRefinementCycle_SkipsUntrustedAuthor(t *testing.T) {
	srv := unrefinedIssuesServer(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead, "maintainer": trust.LevelWrite})
	sched.SetTrustGate(gate)
	// Both fixtures are off-board issues — tier 3 under #1514, which the
	// default refuses. This test is about the author-trust gate, not the tier
	// gate, so the backlog sweep is turned on to keep them candidates.
	cfg := DefaultAutonomousConfig()
	cfg.RefinementBacklog = true
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r", Project: 1},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"

	dispatched := make(chan int, 2)
	as.WithRefinementRunner(func(_ context.Context, owner, repo string, issueNumber int) error {
		dispatched <- issueNumber
		return nil
	})

	as.runRefinementCycle(context.Background())
	as.drainBackground()

	select {
	case got := <-dispatched:
		if got != 2 {
			t.Fatalf("expected only trusted issue #2 to be dispatched, got #%d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the trusted candidate to be dispatched")
	}

	// The untrusted candidate must never arrive. No grace window: the drain
	// above joined every goTracked(refineIssue) spawn this cycle made, so the
	// absence is decided by the join, not sampled by a clock.
	select {
	case got := <-dispatched:
		t.Fatalf("unexpected second dispatch for issue #%d — untrusted author #1 should have been skipped", got)
	default:
		// Expected: nothing else dispatched.
	}
}

// TestIsTriagedAndUnblocked_RejectsUntrustedAuthor covers the Backlog->Ready
// promotion gate: a fully-triaged Backlog item is still refused when no
// writer vouched for it (#270, #2130), and a nil predicate refuses too.
func TestIsTriagedAndUnblocked_RejectsUntrustedAuthor(t *testing.T) {
	node := &depgraph.Node{
		Repo: "nightgauge/nightgauge", Number: 3216, State: "OPEN",
		BoardStatus: "Backlog", Priority: "P1", Labels: []string{"type:bug"},
		AuthorLogin: "stranger",
	}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{node.ID().String(): node}}
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("a nil eligibility predicate must refuse")
	}
	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelTriage})
	s := &Scheduler{trustGate: gate}
	if isTriagedAndUnblocked(node, g, g.Adjacency(), s.nodeEligible(context.Background())) {
		t.Error("a fully-triaged Backlog item by a triager must NOT be promoted")
	}
	if got := triageRejectionReason(node, g, g.Adjacency(), s.nodeEligible(context.Background())); got != "not-vouched-for" {
		t.Errorf("rejection reason = %q, want not-vouched-for", got)
	}
	node.AuthorLogin = "writer"
	gate2, _ := levelsGate(map[string]trust.Level{"writer": trust.LevelWrite})
	s.trustGate = gate2
	if !isTriagedAndUnblocked(node, g, g.Adjacency(), s.nodeEligible(context.Background())) {
		t.Error("a fully-triaged Backlog item by a writer must be promoted")
	}
}

// Each refinement and promotion cycle starts a fresh eligibility cycle, so an
// edit made after an approval (or a revoked permission) takes effect on the
// next cycle, never served from a previous cycle's cache (#2130).
func TestAutonomousCyclesResetTheEligibilityCache(t *testing.T) {
	gate, forge := levelsGate(map[string]trust.Level{"writer": trust.LevelWrite})
	as := &AutonomousScheduler{trustGateOverride: gate}
	node := &depgraph.Node{Repo: "o/r", Number: 1, AuthorLogin: "writer"}
	ctx := context.Background()
	as.eligibility().decideNode(ctx, node)
	as.eligibility().decideNode(ctx, node)
	if got := forge.calls(); got != 1 {
		t.Fatalf("permission calls within one cycle = %d, want 1", got)
	}
	as.eligibility().resetTrustCycle()
	as.eligibility().decideNode(ctx, node)
	if got := forge.calls(); got != 2 {
		t.Fatalf("permission calls after a reset = %d, want 2", got)
	}
}
