package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestPrioritize_UnresolvedRepositoryNameFailsClosed: an issue body that names
// a repository the workspace cannot identify ("Blocked by core #12") yields an
// edge to the name as written. The dispatcher holds the issue, says why, and
// never reads the dependency as the declaring repository's own #12 — closed
// here, which is how such a line used to dispatch over its real blocker before
// the #2349 review.
func TestPrioritize_UnresolvedRepositoryNameFailsClosed(t *testing.T) {
	const repo = "owner/repo"
	aliases := depgraph.WorkspaceRepoAliases([]string{repo})
	refs := depgraph.ParseDependencyRefs("Blocked by core #12", repo, aliases)
	if len(refs) != 1 || !refs[0].Unresolved {
		t.Fatalf("refs = %+v, want one Unresolved reference", refs)
	}
	nodes := []*depgraph.Node{
		{Repo: repo, Number: 12, Title: "unrelated, closed", State: "CLOSED", BoardStatus: "Done", Priority: "P2", Size: "S", Weight: 1},
		{Repo: repo, Number: 100, Title: "declares the dependency", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "S", Weight: 1},
	}
	edges := []depgraph.Edge{{
		From:       depgraph.NodeID{Repo: repo, Number: 100},
		To:         depgraph.NodeID{Repo: refs[0].Repo, Number: refs[0].Number},
		Type:       "crossRepo",
		Source:     refs[0].Source,
		SourceLine: refs[0].SourceLine,
	}}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		repos:  []depgraph.RepoConfig{{Owner: "owner", Name: "repo", Project: 1}},
		state:  &AutonomousState{},
	}
	mock := newMockIssueSvc()
	as.resolveDepStatesFn = func(ctx context.Context, keys []string) map[string]string {
		return resolveIssueStatesByKey(ctx, mock, keys)
	}

	for _, c := range as.prioritize(context.Background(), g) {
		if c.Number == 100 {
			t.Fatalf("#100 dispatched over a dependency on a repository the workspace cannot name")
		}
	}
	if got := as.state.LastRejectionReasons["blocked-by-offboard-dep"]; got != 1 {
		t.Errorf("rejection reasons = %+v, want blocked-by-offboard-dep=1", as.state.LastRejectionReasons)
	}
	if len(mock.batchCalls) != 0 {
		t.Errorf("a repository name that is not owner/repo was looked up on GitHub: %+v", mock.batchCalls)
	}
	res := evaluateDeps([]string{"core#12"}, g, nil, nil)
	if !res.blocked || !strings.Contains(res.status, "names a repository that is not one of the workspace's") {
		t.Errorf("evaluateDeps = %+v, want blocked with the unresolvable-repository reason", res)
	}
}
