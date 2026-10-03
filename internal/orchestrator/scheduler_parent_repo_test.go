package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// #2369: isBlocked's circular-parent auto-fix matched the parent by number
// alone. For a sub-issue whose epic lives in another repository, an open
// blocker that merely shares the epic's number was removed from GitHub and the
// sub-issue dispatched over it.
func TestIsBlocked_SameNumberAsACrossRepoParentStaysABlocker(t *testing.T) {
	issueSvc := newMockIssueSvc()
	s := &Scheduler{issueSvc: issueSvc, owner: "o"}
	item := types.BoardItem{
		Number:       21,
		Repo:         "o/app",
		ParentNumber: 20,
		ParentRepo:   "o/platform",
		BlockedBy: []types.BlockingRef{
			{Number: 20, Repo: "o/app", Title: "unrelated, same number", State: "OPEN"},
		},
	}

	blocked, err := s.isBlocked(context.Background(), item)
	if err != nil {
		t.Fatalf("isBlocked error: %v", err)
	}
	if !blocked {
		t.Error("o/app#21 reported unblocked: its open blocker o/app#20 is not its parent epic o/platform#20")
	}
	if n := len(issueSvc.removeBlockedByCalls); n != 0 {
		t.Errorf("RemoveBlockedBy called %d times: a legitimate relationship was deleted (%+v)", n, issueSvc.removeBlockedByCalls)
	}
}

// The auto-fix still fires for the real parent when it lives in another
// repository, and removes the relationship to the parent's own repository.
func TestIsBlocked_CrossRepoParentAsBlockerIsStillCircular(t *testing.T) {
	issueSvc := newMockIssueSvc()
	s := &Scheduler{issueSvc: issueSvc, owner: "o"}
	item := types.BoardItem{
		Number:       21,
		Repo:         "o/app",
		ParentNumber: 20,
		ParentRepo:   "o/platform",
		BlockedBy: []types.BlockingRef{
			{Number: 20, Repo: "o/platform", Title: "Epic", State: "OPEN"},
		},
	}

	blocked, err := s.isBlocked(context.Background(), item)
	if err != nil {
		t.Fatalf("isBlocked error: %v", err)
	}
	if blocked {
		t.Error("o/app#21 blocked by its own parent epic must be auto-fixed, not held")
	}
	if len(issueSvc.removeBlockedByCalls) != 1 {
		t.Fatalf("RemoveBlockedBy called %d times, want 1", len(issueSvc.removeBlockedByCalls))
	}
	call := issueSvc.removeBlockedByCalls[0]
	if want := (forge.IssueRef{Owner: "o", Repo: "platform", Number: 20}); call.blocker != want {
		t.Errorf("removed blocker = %s, want %s", call.blocker, want)
	}
}

func TestBlockerIsOwnParent(t *testing.T) {
	item := types.BoardItem{Number: 21, Repo: "o/app", ParentNumber: 20}
	for _, tc := range []struct {
		name       string
		parentRepo string
		blocker    types.BlockingRef
		want       bool
	}{
		{"same-repo parent, blocker repo unset", "", types.BlockingRef{Number: 20}, true},
		{"same-repo parent, blocker repo set", "o/app", types.BlockingRef{Number: 20, Repo: "O/App"}, true},
		{"cross-repo parent is the blocker", "o/platform", types.BlockingRef{Number: 20, Repo: "o/platform"}, true},
		{"cross-repo parent, same-numbered local blocker", "o/platform", types.BlockingRef{Number: 20, Repo: "o/app"}, false},
		{"cross-repo parent, blocker repo unset", "o/platform", types.BlockingRef{Number: 20}, false},
		{"different number", "", types.BlockingRef{Number: 19}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := item
			it.ParentRepo = tc.parentRepo
			if got := blockerIsOwnParent(it, tc.blocker); got != tc.want {
				t.Errorf("blockerIsOwnParent = %v, want %v", got, tc.want)
			}
		})
	}
	if blockerIsOwnParent(types.BoardItem{Number: 21, Repo: "o/app"}, types.BlockingRef{Number: 0}) {
		t.Error("an item without a parent has no parent to be blocked by")
	}
}
