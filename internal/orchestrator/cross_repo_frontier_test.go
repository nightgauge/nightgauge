package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestEvaluateDeps_CrossRepoFrontier pins the workspace-wide dispatch
// frontier (#2573): an issue in o/b is blocked by o/a#7, and o/b has an
// unrelated #7. The blocker's state in ITS OWN repository decides dispatch,
// whether or not o/a#7 is on a board, and an unknown state fails closed.
func TestEvaluateDeps_CrossRepoFrontier(t *testing.T) {
	const dep = "o/a#7"
	sameNumberOtherRepo := &depgraph.Node{Repo: "o/b", Number: 7, State: "CLOSED", BoardStatus: "Done"}

	tests := []struct {
		name        string
		nodes       []*depgraph.Node
		resolved    map[string]string
		wantBlocked bool
	}{
		{
			name:        "off-board blocker CLOSED in its repository dispatches",
			resolved:    map[string]string{dep: "CLOSED"},
			wantBlocked: false,
		},
		{
			name:        "off-board blocker OPEN in its repository is blocked",
			resolved:    map[string]string{dep: "OPEN"},
			wantBlocked: true,
		},
		{
			name:        "on-board blocker CLOSED dispatches",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "CLOSED", BoardStatus: "Done"}},
			wantBlocked: false,
		},
		{
			name:        "on-board blocker OPEN is blocked",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "OPEN", BoardStatus: "In progress"}},
			wantBlocked: true,
		},
		{
			name:        "unresolved off-board blocker fails closed",
			wantBlocked: true,
		},
		{
			name:        "a closed same-numbered issue in the dependent's repository does not satisfy it",
			nodes:       []*depgraph.Node{sameNumberOtherRepo},
			wantBlocked: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := buildTestGraph(tc.nodes, nil)
			res := evaluateDeps([]string{dep}, g, tc.resolved, nil)
			if res.blocked != tc.wantBlocked {
				t.Fatalf("evaluateDeps = %+v, want blocked=%v", res, tc.wantBlocked)
			}
			if res.blocked && res.blocker != dep {
				t.Errorf("blocker = %q, want %q", res.blocker, dep)
			}
		})
	}
}
