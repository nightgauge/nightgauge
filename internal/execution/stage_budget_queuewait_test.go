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

// queueWaitFireDeadline bounds how long a test waits for the breach. It is a
// failure deadline, not an assertion: timers never fire early, so every
// assertion below is a lower bound or a value the enforcer recorded (#2950).
const queueWaitFireDeadline = 10 * time.Second

// TestStageBudgetWallClock_PRMergeLeavesOutTheQueueWait is #2770 AC 1 at the
// enforcer: pr-merge enqueues 100 ms in and the queue holds the PR for 600 ms.
// The interval is written closed, with explicit timestamps anchored to the
// enforcer's own start, so no sleep decides where it falls and the 100 ms
// before the enqueue always counts (#2950). The wall clock cannot fire before
// the stage has counted its 400 ms limit outside the interval, and Observed
// is that counted time, not the elapsed time.
func TestStageBudgetWallClock_PRMergeLeavesOutTheQueueWait(t *testing.T) {
	root := layouttest.Repo(t)
	e := prMergeEnforcer(root, 7)
	wait := armOverChild(t, e)
	enqueued, merged := e.started.Add(100*time.Millisecond), e.started.Add(700*time.Millisecond)
	if err := queuewait.Begin(root, 7, queuewait.Self(), enqueued); err != nil {
		t.Fatal(err)
	}
	if err := queuewait.End(root, 7, queuewait.Self(), merged); err != nil {
		t.Fatal(err)
	}
	// The timer reads the record when it fires at the limit; a record written
	// after that is a harness failure, not an enforcer verdict.
	if e.fired() {
		t.Fatalf("HARNESS ERROR: the interval was written %s after arm, past the %s limit", time.Since(e.started), queueWaitTestLimit)
	}
	fired, at := wait(queueWaitFireDeadline)
	if !fired {
		t.Fatalf("the wall clock never fired within %s", queueWaitFireDeadline)
	}
	excl := merged.Sub(enqueued)
	if got := e.excluded(e.started, time.Now()); absDuration(got-excl) > time.Millisecond {
		t.Errorf("excluded %s, want the written interval %s", got, excl)
	}
	if at < queueWaitTestLimit+excl {
		t.Errorf("fired %s in, inside the queue wait: want no earlier than limit + %s", at, excl)
	}
	b := e.result()
	if b.Dimension != StageBudgetWallClock || b.Limit != queueWaitTestLimit.Milliseconds() {
		t.Errorf("breach = %+v, want wall_clock at limit %d", b, queueWaitTestLimit.Milliseconds())
	}
	if counted := (at - excl).Milliseconds(); b.Observed < b.Limit || b.Observed > counted+1 {
		t.Errorf("observed %dms, want the counted time (>= limit %d, <= %dms)", b.Observed, b.Limit, counted)
	}
}

// TestStageBudgetWallClock_PRMergeWithoutQueueWaitFiresAtTheLimit: with no
// record, every millisecond, review fixes included, counts.
func TestStageBudgetWallClock_PRMergeWithoutQueueWaitFiresAtTheLimit(t *testing.T) {
	e := prMergeEnforcer(layouttest.Repo(t), 7)
	assertFiresWithNothingExcluded(t, e, armOverChild(t, e))
}

// TestStageBudgetWallClock_DeadWritersOpenIntervalDoesNotExtend is AC 2 at
// the enforcer: a killed writer left its interval open from the spawn; it
// excludes nothing, so the budget fires. Were it excluded, the enforcer
// would never fire at all.
func TestStageBudgetWallClock_DeadWritersOpenIntervalDoesNotExtend(t *testing.T) {
	root := layouttest.Repo(t)
	dead := pidtest.Reaped(t, runstate.ProcessAlive)
	if err := queuewait.Begin(root, 7, queuewait.Writer{PID: dead, Start: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := prMergeEnforcer(root, 7)
	assertFiresWithNothingExcluded(t, e, armOverChild(t, e))
}

// assertFiresWithNothingExcluded checks a breach fired at the limit with no
// time left out, without an upper bound on when: that bound measured the
// scheduler, not the enforcer (#2950).
func assertFiresWithNothingExcluded(t *testing.T, e *stageBudgetEnforcer, wait func(time.Duration) (bool, time.Duration)) {
	t.Helper()
	fired, at := wait(queueWaitFireDeadline)
	if !fired {
		t.Fatalf("the wall clock never fired within %s", queueWaitFireDeadline)
	}
	if got := e.excluded(e.started, time.Now()); got != 0 {
		t.Errorf("excluded %s, want nothing left out", got)
	}
	if at < queueWaitTestLimit {
		t.Errorf("fired %s in, before the %s limit", at, queueWaitTestLimit)
	}
	b := e.result()
	if b.Dimension != StageBudgetWallClock || b.Limit != queueWaitTestLimit.Milliseconds() || b.Observed < b.Limit {
		t.Errorf("breach = %+v, want wall_clock at limit %d with observed >= limit", b, queueWaitTestLimit.Milliseconds())
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
