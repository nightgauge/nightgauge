package execution

import (
	"os/exec"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/pidtest"
	"github.com/nightgauge/nightgauge/internal/queuewait"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// armOverChild arms e on a throwaway child, as the manager arms it on a
// stage, and returns when the breach fired (or after wait).
func armOverChild(t *testing.T, e *stageBudgetEnforcer) func(wait time.Duration) (fired bool, at time.Duration) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { e.disarm(); _ = cmd.Process.Kill(); <-exited })
	e.grace = 50 * time.Millisecond
	e.arm(cmd.Process, exited)
	return func(wait time.Duration) (bool, time.Duration) {
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			if e.fired() {
				return true, time.Since(e.started)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return e.fired(), time.Since(e.started)
	}
}

const queueWaitTestLimit = 400 * time.Millisecond

func prMergeEnforcer(root string, issue int) *stageBudgetEnforcer {
	e := newStageBudgetEnforcer(config.ResolvedStageBudget{MaxWallClock: queueWaitTestLimit}, StreamFormatClaude, time.Minute)
	e.excluded = stageBudgetExclusion("pr-merge", issue, root)
	return e
}

// TestStageBudgetWallClock_PRMergeLeavesOutTheQueueWait is #2770 AC 1 at the
// enforcer: pr-merge enqueues 100 ms in, the queue holds the PR for 600 ms
// (written through queuewait.Track, the writer `pr merge --issue` uses), and
// the wall clock does not fire at its 400 ms limit; it fires once the stage
// has run 400 ms of its own, so about 1 s in, with that as observed.
func TestStageBudgetWallClock_PRMergeLeavesOutTheQueueWait(t *testing.T) {
	root := layouttest.Repo(t)
	e := prMergeEnforcer(root, 7)
	wait := armOverChild(t, e)
	begin, end := queuewait.Track(root, 7, func(err error) { t.Error(err) })

	time.Sleep(100 * time.Millisecond) // review fixes before the enqueue: counted
	begin()
	if fired, at := wait(600 * time.Millisecond); fired {
		t.Fatalf("the wall clock fired %s in, inside the queue wait", at)
	}
	end()
	fired, at := wait(3 * time.Second)
	if !fired {
		t.Fatal("the wall clock never fired once the queue wait ended")
	}
	if at < 900*time.Millisecond {
		t.Errorf("fired %s in, want no earlier than limit + queue wait (~1s)", at)
	}
	b := e.result()
	if b.Dimension != StageBudgetWallClock || b.Limit != queueWaitTestLimit.Milliseconds() {
		t.Errorf("breach = %+v, want wall_clock at limit %d", b, queueWaitTestLimit.Milliseconds())
	}
	if b.Observed < b.Limit || time.Duration(b.Observed)*time.Millisecond >= at {
		t.Errorf("observed %dms, want the counted time (>= limit, < elapsed %s)", b.Observed, at)
	}
}

// TestStageBudgetWallClock_PRMergeWithoutQueueWaitFiresAtTheLimit: with no
// record, every millisecond, review fixes included, counts.
func TestStageBudgetWallClock_PRMergeWithoutQueueWaitFiresAtTheLimit(t *testing.T) {
	e := prMergeEnforcer(layouttest.Repo(t), 7)
	fired, at := armOverChild(t, e)(3 * time.Second)
	if !fired || at > queueWaitTestLimit+500*time.Millisecond {
		t.Fatalf("fired=%v at %s, want a breach at about the %s limit", fired, at, queueWaitTestLimit)
	}
}

// TestStageBudgetWallClock_DeadWritersOpenIntervalDoesNotExtend is AC 2 at
// the enforcer: a killed writer left its interval open from the spawn; it
// excludes nothing, so the budget fires at its limit.
func TestStageBudgetWallClock_DeadWritersOpenIntervalDoesNotExtend(t *testing.T) {
	root := layouttest.Repo(t)
	dead := pidtest.Reaped(t, runstate.ProcessAlive)
	if err := queuewait.Begin(root, 7, queuewait.Writer{PID: dead, Start: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fired, at := armOverChild(t, prMergeEnforcer(root, 7))(3 * time.Second)
	if !fired || at > queueWaitTestLimit+500*time.Millisecond {
		t.Fatalf("fired=%v at %s, want a breach at about the %s limit", fired, at, queueWaitTestLimit)
	}
}

// TestStageBudgetExclusion_OnlyPRMerge: no other stage leaves time out.
func TestStageBudgetExclusion_OnlyPRMerge(t *testing.T) {
	if stageBudgetExclusion("feature-validate", 7, "/x") != nil {
		t.Error("feature-validate got a queue-wait exclusion")
	}
	if stageBudgetExclusion("pr-merge", 0, "/x") != nil {
		t.Error("pr-merge with no issue got a queue-wait exclusion")
	}
}
