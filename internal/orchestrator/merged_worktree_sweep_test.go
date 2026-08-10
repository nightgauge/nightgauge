package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNewScheduler_ConstructionReclaimsNothing is the #403 inversion, pinned:
// constructing a Scheduler must not delete anything.
//
// NewScheduler → loadQueue is on the construction path of every process that
// needs a Scheduler for something else entirely — `nightgauge queue
// add|list|run|remove|clear` and the deps-gate/baseline-gate promote commands
// all build one through getQueueScheduler. A sweep riding that path means
// `queue list`, a printf loop, runs `git worktree remove --force` and `git
// branch -D` as a side effect of being constructed, on behalf of a process
// that cannot see any other process's in-flight runs. Whatever protection the
// sweep is given, a constructor is the wrong place to exercise it.
func TestNewScheduler_ConstructionReclaimsNothing(t *testing.T) {
	root, wt := mergedWorktreeRepo(t, 706)

	_ = NewScheduler(nil, SchedulerConfig{WorkspaceRoot: root})

	if _, err := os.Stat(wt); err != nil {
		t.Errorf("constructing a Scheduler reclaimed %s (%v) — construction must never delete directories", wt, err)
	}
}

// defaultBranchWorktreeRepo builds a repo whose pipeline worktree is PARKED ON
// THE DEFAULT BRANCH — the sweep's second reclaim door, the one that does no
// content comparison at all. The primary is parked elsewhere because a worktree
// holding `main` makes `git checkout main` fail in the primary clone, which is
// the production state this door exists for (#332).
func defaultBranchWorktreeRepo(t *testing.T, issue int) (string, string) {
	t.Helper()
	root, merged := mergedWorktreeRepo(t, issue)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), root, err, out)
		}
	}
	// Drop the merged-branch worktree so the repo holds exactly one candidate
	// and the assertion below cannot be satisfied by the other door.
	git("worktree", "remove", "--force", merged)
	git("checkout", "-q", "-b", "parked-elsewhere")
	wt := filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue+1))
	git("worktree", "add", wt, "main")
	return root, wt
}

// TestMergedWorktreeSweep_DefaultBranchReclaimIsNotLoggedAsMerged is #410 gap 3.
//
// runMergedWorktreeSweep logged ONE line for every reclaim — "content already
// on <base>" — but `execution.classifyWorktree` has TWO reclaim doors, and the
// default-branch door does no content comparison whatsoever. So the only
// operator-visible record of the most destructive reclaim asserted a check that
// never ran. A log that states a reason the code did not evaluate is worse than
// no log: it is evidence pointing away from the actual authorization.
func TestMergedWorktreeSweep_DefaultBranchReclaimIsNotLoggedAsMerged(t *testing.T) {
	root, wt := defaultBranchWorktreeRepo(t, 410)

	as := &AutonomousScheduler{
		scheduler: &Scheduler{workspaceRoot: root},
		state:     &AutonomousState{},
	}

	out := captureLog(t, func() { as.sweepMergedWorktrees() })

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("fixture did not exercise the default-branch door — %s survived (err=%v); log:\n%s", wt, err, out)
	}
	if strings.Contains(out, "content already on") {
		t.Errorf("the default-branch door reclaimed on the strength of the checkout alone, but the log claims a content comparison; got:\n%s", out)
	}
	if !strings.Contains(out, "default branch") {
		t.Errorf("the reclaim log does not name what actually authorized it; got:\n%s", out)
	}
	if !strings.Contains(out, "no content comparison") {
		t.Errorf("the reclaim log does not state that no content comparison ran; got:\n%s", out)
	}
}
