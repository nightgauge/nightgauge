package github

import (
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// #2369: `epic validate` reported any blocker carrying the epic's number as a
// circular blocker. For a sub-issue in another repository that is an
// unrelated issue, and issue-audit's CIRCULAR_BLOCKER repair removes the edge.
func TestBlockerIsEpic(t *testing.T) {
	const epicRepo, epicNumber = "o/platform", 20
	for _, tc := range []struct {
		name    string
		subRepo string
		blocker types.BlockingRef
		want    bool
	}{
		{"same-repo sub, blocker repo unset", "o/platform", types.BlockingRef{Number: 20}, true},
		{"cross-repo sub blocked by the epic", "o/app", types.BlockingRef{Number: 20, Repo: "o/platform"}, true},
		{"repository names compare case-insensitively", "o/app", types.BlockingRef{Number: 20, Repo: "O/Platform"}, true},
		{"cross-repo sub, same-numbered local blocker", "o/app", types.BlockingRef{Number: 20, Repo: "o/app"}, false},
		{"cross-repo sub, blocker repo unset", "o/app", types.BlockingRef{Number: 20}, false},
		{"different number", "o/platform", types.BlockingRef{Number: 21, Repo: "o/platform"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockerIsEpic(tc.blocker, tc.subRepo, epicRepo, epicNumber); got != tc.want {
				t.Errorf("blockerIsEpic = %v, want %v", got, tc.want)
			}
		})
	}
}
