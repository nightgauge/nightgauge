package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// unrefinedIssuesServer returns an httptest server that responds to
// ListIssuesExcludingLabels' GraphQL query with two open issues: #1 authored
// by NONE (a stranger — untrusted) and #2 authored by OWNER (trusted).
func unrefinedIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
								"authorAssociation": "NONE",
								"labels": {"nodes": []}
							},
							{
								"number": 2,
								"title": "Owner's issue",
								"createdAt": "2026-01-01T00:00:00Z",
								"authorAssociation": "OWNER",
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
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r", Project: 1},
	}, nil, DefaultAutonomousConfig(), t.TempDir())
	as.state.Status = "running"

	dispatched := make(chan int, 2)
	as.OnRefinementDispatch(func(owner, repo string, issueNumber int) {
		dispatched <- issueNumber
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

// TestIsTriagedAndUnblocked_RejectsUntrustedAuthor is the AC #2/#4 coverage for
// the Backlog->Ready promotion gate: a fully-triaged Backlog item must still
// be refused when its author is untrusted (#270's "the one sound existing
// gate" extended to also check identity).
func TestIsTriagedAndUnblocked_RejectsUntrustedAuthor(t *testing.T) {
	node := &depgraph.Node{
		Repo: "nightgauge/nightgauge", Number: 3216, State: "OPEN",
		BoardStatus: "Backlog", Priority: "P1", Labels: []string{"type:bug"},
		AuthorAssociation: "NONE",
	}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{node.ID().String(): node}}
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("a fully-triaged Backlog item with an untrusted author must NOT be promoted")
	}
}

// TestIsTriagedAndUnblocked_ConfiguredOverrideAppliesToTriageGate verifies the
// autonomous.trusted_author_associations escape hatch reaches the triage gate.
func TestIsTriagedAndUnblocked_ConfiguredOverrideAppliesToTriageGate(t *testing.T) {
	node := &depgraph.Node{
		Repo: "nightgauge/nightgauge", Number: 3216, State: "OPEN",
		BoardStatus: "Backlog", Priority: "P1", Labels: []string{"type:bug"},
		AuthorAssociation: "CONTRIBUTOR",
	}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{node.ID().String(): node}}
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Fatal("CONTRIBUTOR should not be promoted under the default trusted set")
	}
	if !isTriagedAndUnblocked(node, g, g.Adjacency(), []string{"CONTRIBUTOR"}) {
		t.Error("CONTRIBUTOR should be promoted once configured as a trusted association")
	}
}
