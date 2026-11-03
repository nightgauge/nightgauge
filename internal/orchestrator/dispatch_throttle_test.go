package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

func throttleAt(max int, resumeAt *time.Time) *platform.WorkspaceThrottle {
	return &platform.WorkspaceThrottle{MaxConcurrent: max, ResumeAt: resumeAt}
}

// The dispatch ceiling follows the workspace throttle (#2352): set, raised
// above the configured concurrency, cleared, unknown, and past its resumeAt.
func TestDispatchThrottle_Ceiling(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	d := NewDispatchThrottle()
	d.now = func() time.Time { return now }

	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("no throttle: ceiling = %d, want the configured 3", got)
	}
	d.Set(throttleAt(1, nil), true)
	if got := d.Ceiling(3); got != 1 {
		t.Fatalf("throttled to 1: ceiling = %d, want 1", got)
	}
	d.Set(throttleAt(5, nil), true)
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("a cap above the configured concurrency: ceiling = %d, want 3", got)
	}
	d.Set(throttleAt(0, nil), true)
	if got := d.Ceiling(3); got != 0 {
		t.Fatalf("throttled to 0: ceiling = %d, want 0", got)
	}
	d.Set(nil, true)
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("cleared: ceiling = %d, want 3", got)
	}
	// Unknown (no signed-in session) caps nothing, whatever it is handed.
	d.Set(throttleAt(1, nil), false)
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("unknown: ceiling = %d, want 3", got)
	}
	if throttle, known := d.Snapshot(); throttle != nil || known {
		t.Fatalf("unknown: snapshot = %+v, %v", throttle, known)
	}

	resumeAt := now.Add(time.Hour)
	d.Set(throttleAt(1, &resumeAt), true)
	if got := d.Ceiling(3); got != 1 {
		t.Fatalf("before its resumeAt: ceiling = %d, want 1", got)
	}
	now = resumeAt
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("at its resumeAt: ceiling = %d, want 3", got)
	}
	if throttle, known := d.Snapshot(); throttle != nil || !known {
		t.Fatalf("lifted: snapshot = %+v, %v; want none, known", throttle, known)
	}

	var nilThrottle *DispatchThrottle
	if got := nilThrottle.Ceiling(2); got != 2 {
		t.Fatalf("a nil throttle: ceiling = %d, want 2", got)
	}
}

// Listeners hear every change once, nothing for the same throttle again, and
// the throttle lifting at its resumeAt without a further Set.
// A followed throttle that has not been read yet is unread, not unknown
// (#2352): it caps nothing, but the daemon tells a headless scheduler to keep
// what it learned. A known throttle stays known through a later failed read,
// and a lost session makes it unknown again.
func TestDispatchThrottle_ReportsAFollowedThrottleNotReadYet(t *testing.T) {
	d := NewDispatchThrottle()
	if th, known, unread := d.Report(); th != nil || known || unread {
		t.Fatalf("followed by nothing: %v %v %v, want unknown, not unread", th, known, unread)
	}
	d.MarkUnread()
	if th, known, unread := d.Report(); th != nil || known || !unread {
		t.Fatalf("a session, no read yet: %v %v %v, want unread", th, known, unread)
	}
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("unread: ceiling = %d, want 3", got)
	}
	d.Set(throttleAt(1, nil), true)
	d.MarkUnread() // a later read that failed
	if th, known, unread := d.Report(); th == nil || th.MaxConcurrent != 1 || !known || unread {
		t.Fatalf("known, then a failed read: %v %v %v, want the throttle, known", th, known, unread)
	}
	d.Set(nil, false)
	if th, known, unread := d.Report(); th != nil || known || unread {
		t.Fatalf("the session is gone: %v %v %v, want unknown, not unread", th, known, unread)
	}
}

func TestDispatchThrottle_AnnouncesChangesAndTheLift(t *testing.T) {
	d := NewDispatchThrottle()
	changes := make(chan struct{}, 16)
	d.OnChange(func() { changes <- struct{}{} })
	count := func() int {
		n := 0
		for {
			select {
			case <-changes:
				n++
			default:
				return n
			}
		}
	}

	d.Set(throttleAt(1, nil), true)
	d.Set(throttleAt(1, nil), true)
	if n := count(); n != 1 {
		t.Fatalf("a throttle set twice announced %d changes, want 1", n)
	}
	d.Set(throttleAt(2, nil), true)
	d.Set(nil, true)
	d.Set(nil, false)
	if n := count(); n != 3 {
		t.Fatalf("raise, clear and unknown announced %d changes, want 3", n)
	}

	// The lift at resumeAt is armed on the throttle's clock and announced
	// when it fires, with no further Set. The clock and the timer are the
	// test's, so no scheduling delay can make the resumeAt pass before Set.
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	var armed []time.Duration
	var fire func()
	d.afterFunc = func(wait time.Duration, f func()) liftTimer {
		armed = append(armed, wait)
		fire = f
		return fakeLiftTimer{}
	}
	resumeAt := now.Add(20 * time.Millisecond)
	d.Set(throttleAt(1, &resumeAt), true)
	if n := count(); n != 1 {
		t.Fatalf("setting a timed throttle announced %d changes, want 1", n)
	}
	if len(armed) != 1 || armed[0] != 20*time.Millisecond {
		t.Fatalf("armed lifts = %v, want one at resumeAt", armed)
	}
	if got := d.Ceiling(3); got != 1 {
		t.Fatalf("before the lift: ceiling = %d, want 1", got)
	}
	now = resumeAt
	fire()
	if n := count(); n != 1 {
		t.Fatalf("the lift at resumeAt announced %d changes, want 1", n)
	}
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("after the announced lift: ceiling = %d, want 3", got)
	}

	// A throttle whose resumeAt has passed already is announced as the
	// change it is, and arms no lift.
	past := now.Add(-time.Second)
	d.Set(throttleAt(2, &past), true)
	if n := count(); n != 1 {
		t.Fatalf("a throttle already lifted announced %d changes, want 1", n)
	}
	if len(armed) != 1 {
		t.Fatalf("armed a lift for a resumeAt in the past: %v", armed)
	}
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("a throttle already lifted: ceiling = %d, want 3", got)
	}
}

type fakeLiftTimer struct{}

func (fakeLiftTimer) Stop() bool { return true }

// The autonomous scheduler holds its own dispatch to the throttle, and never
// work it hands to the extension, which caps that work itself (#2352). A
// running pipeline is never counted out: a cap below the running count opens
// nothing until enough finish.
func TestAutonomousScheduler_DispatchCeilingFollowsTheThrottle(t *testing.T) {
	newScheduler := func() *AutonomousScheduler {
		return &AutonomousScheduler{
			trustGateOverride: allWritersGate(),
			config:            AutonomousConfig{MaxConcurrent: 3},
			state:             &AutonomousState{},
			rescanCh:          make(chan struct{}, 1),
		}
	}
	d := NewDispatchThrottle()

	// Without the extension (the Go queue).
	as := newScheduler()
	as.SetDispatchThrottle(d)
	if got := as.effectiveAvailableSlots(); got != 3 {
		t.Fatalf("no throttle: %d slots, want 3", got)
	}
	d.Set(throttleAt(1, nil), true)
	select {
	case <-as.rescanCh:
	default:
		t.Fatal("a throttle change did not wake the dispatch loop")
	}
	if got := as.effectiveAvailableSlots(); got != 1 {
		t.Fatalf("throttled to 1: %d slots, want 1", got)
	}
	as.state.Running = []RunningItem{{Repo: "o/a", Number: 1}, {Repo: "o/a", Number: 2}}
	if got := as.effectiveAvailableSlots(); got != 0 {
		t.Fatalf("throttled to 1 with 2 running: %d slots, want 0 (and nothing stopped)", got)
	}
	if len(as.state.Running) != 2 {
		t.Fatal("the throttle touched a running pipeline")
	}
	d.Set(nil, true)
	if got := as.effectiveAvailableSlots(); got != 1 {
		t.Fatalf("cleared with 2 running: %d slots, want 1", got)
	}

	// Handing work to the extension: the extension caps it, so not here.
	ext := newScheduler()
	ext.OnDispatch(func(owner, repo string, issueNumber int, title string, openPRNumber int) {})
	ext.SetDispatchThrottle(d)
	d.Set(throttleAt(1, nil), true)
	if got := ext.effectiveAvailableSlots(); got != 3 {
		t.Fatalf("dispatching through the extension: %d slots, want 3 (no second cap)", got)
	}
}

// The auto-scheduler loop starts nothing while as many pipelines run as the
// throttle allows (#2352).
func TestScheduler_ThrottleHoldsDispatch(t *testing.T) {
	s := &Scheduler{repoRunning: map[string]int{}}
	if s.throttleHoldsDispatch() {
		t.Fatal("no throttle set: dispatch held")
	}
	d := NewDispatchThrottle()
	s.SetDispatchThrottle(d)
	if s.throttleHoldsDispatch() {
		t.Fatal("no throttle in force: dispatch held")
	}
	d.Set(throttleAt(2, nil), true)
	s.repoRunning["o/a"] = 1
	if s.throttleHoldsDispatch() {
		t.Fatal("1 running under a cap of 2: dispatch held")
	}
	s.repoRunning["o/b"] = 1
	if !s.throttleHoldsDispatch() {
		t.Fatal("2 running under a cap of 2: dispatch not held")
	}
	d.Set(nil, true)
	if s.throttleHoldsDispatch() {
		t.Fatal("cleared: dispatch held")
	}
}

// How many pipelines the throttle lets start (#2352): everything asked for
// without one, what its cap leaves above the running ones with one, and
// nothing at or above the cap.
func TestScheduler_ThrottleRoom(t *testing.T) {
	s := &Scheduler{repoRunning: map[string]int{"o/a": 1}}
	if got := s.throttleRoom(3); got != 3 {
		t.Fatalf("no throttle: room = %d, want 3", got)
	}
	if s.followsDispatchThrottle() {
		t.Fatal("follows a throttle before one was set")
	}
	d := NewDispatchThrottle()
	s.SetDispatchThrottle(d)
	if !s.followsDispatchThrottle() {
		t.Fatal("does not follow the throttle it was given")
	}
	if got := s.throttleRoom(3); got != 3 {
		t.Fatalf("no throttle in force: room = %d, want 3", got)
	}
	d.Set(throttleAt(2, nil), true)
	if got := s.throttleRoom(3); got != 1 {
		t.Fatalf("cap 2 with 1 running: room = %d, want 1", got)
	}
	s.repoRunning["o/b"] = 2
	if got := s.throttleRoom(3); got != 0 {
		t.Fatalf("cap 2 with 3 running: room = %d, want 0", got)
	}
	d.Set(throttleAt(10, nil), true)
	if got := s.throttleRoom(3); got != 3 {
		t.Fatalf("cap above the running and wanted: room = %d, want 3", got)
	}
}

// A wave's sub-issues wait while the throttle holds dispatch, and the wait
// ends with the context (#2352).
func TestScheduler_WaitForThrottleRoom(t *testing.T) {
	s := &Scheduler{repoRunning: map[string]int{"o/a": 1}}
	d := NewDispatchThrottle()
	s.SetDispatchThrottle(d)
	d.Set(throttleAt(1, nil), true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := s.waitForThrottleRoom(ctx, 2); got != 0 {
		t.Fatalf("held, context ended: room = %d, want 0", got)
	}
	d.Set(throttleAt(2, nil), true)
	if got := s.waitForThrottleRoom(context.Background(), 2); got != 1 {
		t.Fatalf("cap 2 with 1 running: room = %d, want 1", got)
	}
}

// Pending queue items reserve board-scan slots only while fillSlots can still
// take them (#2717): fresh, and in a repository that is not halted. With one
// run and a ceiling of 3, two stuck items used to stop the whole fleet.
func TestAutonomousScheduler_PendingReservationIsBoundedAndHaltAware(t *testing.T) {
	newScheduler := func(items ...QueueItem) *AutonomousScheduler {
		return &AutonomousScheduler{
			trustGateOverride: allWritersGate(),
			config:            AutonomousConfig{MaxConcurrent: 3},
			state:             &AutonomousState{Running: []RunningItem{{Repo: "o/a", Number: 1}}},
			rescanCh:          make(chan struct{}, 1),
			scheduler:         &Scheduler{queue: items},
		}
	}
	now := time.Now().UTC()
	fresh := func(repo string, n int) QueueItem {
		return QueueItem{Repo: repo, IssueNumber: n, Status: "pending", AddedAt: now}
	}
	stale := func(repo string, n int) QueueItem {
		return QueueItem{Repo: repo, IssueNumber: n, Status: "pending", AddedAt: now.Add(-2 * pendingReservationTTL)}
	}

	t.Run("fresh items reserve their slots", func(t *testing.T) {
		as := newScheduler(fresh("o/a", 2), fresh("o/b", 3))
		got := as.slotAvailability()
		if got.available != 0 || got.pending != 2 || got.reserved != 2 {
			t.Fatalf("slots = %+v, want 0 available, 2 pending, 2 reserved", got)
		}
	})

	t.Run("expired items give the slots back and are named once", func(t *testing.T) {
		buf := withCapturedLog(t)
		as := newScheduler(stale("o/a", 2), stale("o/b", 3))
		got := as.slotAvailability()
		if got.available != 2 || got.pending != 2 || got.reserved != 0 {
			t.Fatalf("slots = %+v, want 2 available, 2 pending, 0 reserved", got)
		}
		for _, item := range []string{"o/a#2", "o/b#3"} {
			if n := strings.Count(buf.String(), "pending queue item "+item+" has not been taken"); n != 1 {
				t.Errorf("warnings naming %s = %d, want 1; log %q", item, n, buf.String())
			}
		}
		as.slotAvailability()
		if n := strings.Count(buf.String(), "has not been taken"); n != 2 {
			t.Errorf("warnings after a second cycle = %d, want still 2 (deduplicated)", n)
		}
	})

	t.Run("an item in a halted repository reserves nothing", func(t *testing.T) {
		as := newScheduler(fresh("o/halted", 2))
		as.state.PausedRepos = map[string]*RepoPauseRecord{"o/halted": {Repo: "o/halted"}}
		if got := as.slotAvailability(); got.available != 2 || got.reserved != 0 || got.pending != 1 {
			t.Fatalf("slots = %+v, want 2 available, 0 reserved, 1 pending", got)
		}
	})

	t.Run("non-pending items are not counted", func(t *testing.T) {
		as := newScheduler(QueueItem{Repo: "o/a", IssueNumber: 2, Status: "processing", AddedAt: now})
		if got := as.slotAvailability(); got.available != 2 || got.pending != 0 {
			t.Fatalf("slots = %+v, want 2 available, 0 pending", got)
		}
	})
}

// The `no effective slots available` line reports the pending queue items and
// how many held a slot, so a stall behind queue reservations is visible in the
// log rather than reading as "running=1" with nothing else to go on (#2717).
func TestRunCycle_NoSlotsLogReportsPendingCount(t *testing.T) {
	eval := &fakeBaselineEvaluator{green: map[string]bool{}}
	now := time.Now().UTC()
	as := newBaselineSweepScheduler(t, eval,
		QueueItem{Repo: "acme/web", IssueNumber: 2, Status: "pending", AddedAt: now},
		QueueItem{Repo: "acme/web", IssueNumber: 3, Status: "pending", AddedAt: now},
	)
	as.config.MaxConcurrent = 3
	as.state.Running = []RunningItem{{Repo: "acme/web", Number: 1, Title: "busy"}}
	buf := withCapturedLog(t)

	as.runCycle(context.Background())
	as.drainBackground()

	if !strings.Contains(buf.String(), "running=1, pending=2, reserved=2), skipping graph build") {
		t.Fatalf("no-slots log does not report the pending count; got %q", buf.String())
	}
}
