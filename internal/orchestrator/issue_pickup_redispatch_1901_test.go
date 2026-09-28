package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// #1901: a dispatch that fails after issue-pickup pushed its branch, then a
// re-dispatch that composes a DIFFERENT name for the same issue (here the
// title was edited; in the field a second truncation rule did it), must
// continue on the first branch. Exactly one branch for the issue may exist on
// origin afterwards.
func TestDeterministicIssuePickup_RedispatchReusesExistingBranch(t *testing.T) {
	root := pickupRepo(t)

	wt1 := filepath.Join(t.TempDir(), "wt1")
	gitIn(t, root, "worktree", "add", "-q", "--detach", wt1)
	first, err := NewDeterministicIssuePickupRunner().Run(context.Background(), pickupInput(wt1))
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if !first.Pushed {
		t.Fatalf("first dispatch did not push %s", first.Branch)
	}

	// The failed run's cleanup: worktree reclaimed, local ref dropped, origin's
	// copy left in place.
	gitIn(t, root, "worktree", "remove", "--force", wt1)
	gitIn(t, root, "branch", "-D", first.Branch)

	wt2 := filepath.Join(t.TempDir(), "wt2")
	gitIn(t, root, "worktree", "add", "-q", "--detach", wt2)
	in := pickupInput(wt2)
	in.Issue.Title = "issue-pickup has no deterministic runner"
	second, err := NewDeterministicIssuePickupRunner().Run(context.Background(), in)
	if err != nil {
		t.Fatalf("re-dispatch: %v", err)
	}
	if second.Branch != first.Branch {
		t.Errorf("re-dispatch branch = %q, want the existing %q", second.Branch, first.Branch)
	}
	if second.Action != "reused-remote" {
		t.Errorf("re-dispatch action = %q, want reused-remote", second.Action)
	}
	if got := headOf(t, wt2); got != first.Branch {
		t.Errorf("worktree HEAD = %q, want %q", got, first.Branch)
	}

	out, err := gittest.Command(root, "ls-remote", "--heads", "origin", "*/1904-*").CombinedOutput()
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	if refs := strings.Fields(strings.TrimSpace(string(out))); len(refs) != 2 {
		t.Errorf("origin holds %d refs for #1904, want exactly one:\n%s", len(refs)/2, out)
	}
}
