package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// #1496: the scan's rejection summary tells an "In review" item with no open
// PR from the legitimate PR-backed case, and lists the former for
// `autonomous status`. A repo whose PR query failed stays in the plain bucket:
// nobody looked, so it is not reported as "no PR".
func TestPrioritize_SplitsInReviewWithoutOpenPR(t *testing.T) {
	g := depgraph.NewGraph()
	add := func(repo string, n int) {
		g.Nodes[fmt.Sprintf("%s#%d", repo, n)] = &depgraph.Node{
			Repo: repo, Number: n, State: "OPEN", BoardStatus: "In review", Title: "t",
		}
	}
	add("o/a", 1) // open PR backs it
	add("o/a", 2) // no open PR — stranded
	add("o/b", 3) // PR query failed for o/b — unverified

	as := &AutonomousScheduler{
		trustGateOverride: allWritersGate(),
		config:            AutonomousConfig{MaxConcurrent: 5},
		repos: []depgraph.RepoConfig{
			{Owner: "o", Name: "a", Project: 1},
			{Owner: "o", Name: "b", Project: 1},
		},
		state:            &AutonomousState{},
		inReviewPRBacked: map[string]bool{"o/a#1": true},
		prQueryOKRepos:   map[string]bool{"o/a": true},
	}
	as.resolveDepStatesFn = func(context.Context, []string) map[string]string { return nil }

	as.prioritize(context.Background(), g)

	reasons := as.state.LastRejectionReasons
	if got := reasons[`status="In review" (no-open-pr)`]; got != 1 {
		t.Errorf(`no-open-pr bucket = %d, want 1 (reasons %+v)`, got, reasons)
	}
	if got := reasons[`status="In review"`]; got != 2 {
		t.Errorf(`plain In review bucket = %d, want 2: the PR-backed item and the unverified repo's (reasons %+v)`, got, reasons)
	}
	if len(as.state.InReviewWithoutPR) != 1 || as.state.InReviewWithoutPR[0] != (InReviewWithoutPRItem{Repo: "o/a", Number: 2, Title: "t"}) {
		t.Errorf("InReviewWithoutPR = %+v, want only o/a#2", as.state.InReviewWithoutPR)
	}
}
