package orchestrator

import (
	"os"
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
