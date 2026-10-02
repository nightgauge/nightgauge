package depgraph

import (
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// A board item's parent becomes the node's epic in the epic's OWN repository
// (#2350). EpicRepo is kept only when it differs from the node's repository,
// so a same-repository parent, or one whose repository the reader could not
// tell, reads as before.
func TestBoardItemToNode_ParentKeepsItsRepository(t *testing.T) {
	for _, tc := range []struct {
		name, parentRepo string
		wantEpicRepo     string
		wantEpic         NodeID
	}{
		{"parent in another repository", "example-org/platform", "example-org/platform", NodeID{Repo: "example-org/platform", Number: 20}},
		{"parent in the same repository", "example-org/app", "", NodeID{Repo: "example-org/app", Number: 20}},
		{"same repository, other case", "Example-Org/App", "", NodeID{Repo: "example-org/app", Number: 20}},
		{"repository unknown", "", "", NodeID{Repo: "example-org/app", Number: 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := boardItemToNode(&types.BoardItem{
				Number: 21, Repo: "example-org/app", ParentNumber: 20, ParentRepo: tc.parentRepo,
			}, "example-org/app")
			if node.EpicRepo != tc.wantEpicRepo {
				t.Errorf("EpicRepo = %q, want %q", node.EpicRepo, tc.wantEpicRepo)
			}
			if got, ok := node.EpicID(); !ok || got != tc.wantEpic {
				t.Errorf("EpicID = %v, %v; want %v", got, ok, tc.wantEpic)
			}
		})
	}
}

func TestNode_IsSubIssueOfComparesRepositoryAndNumber(t *testing.T) {
	sub := &Node{Repo: "o/app", Number: 21, EpicNumber: 20, EpicRepo: "o/platform"}
	if !sub.IsSubIssueOf(&Node{Repo: "o/platform", Number: 20}) {
		t.Error("the epic in its own repository must be the sub-issue's parent")
	}
	if !sub.IsSubIssueOf(&Node{Repo: "O/Platform", Number: 20}) {
		t.Error("repository names compare case-insensitively")
	}
	if sub.IsSubIssueOf(&Node{Repo: "o/app", Number: 20}) {
		t.Error("the same-numbered issue in the sub-issue's repository is not its parent (#2350)")
	}
	sameRepo := &Node{Repo: "o/app", Number: 22, EpicNumber: 20}
	if !sameRepo.IsSubIssueOf(&Node{Repo: "o/app", Number: 20}) {
		t.Error("an empty EpicRepo means the node's own repository")
	}
	orphan := &Node{Repo: "o/app", Number: 5}
	if _, ok := orphan.EpicID(); ok || orphan.IsSubIssueOf(&Node{Repo: "o/app", Number: 0}) {
		t.Error("a node without a parent has no epic")
	}
	var none *Node
	if _, ok := none.EpicID(); ok || none.IsSubIssueOf(sub) || sub.IsSubIssueOf(nil) {
		t.Error("nil nodes have no parent and are no parent")
	}
}
