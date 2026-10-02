package orchestrator

import (
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

	resumeAt := time.Now().Add(20 * time.Millisecond)
	d.Set(throttleAt(1, &resumeAt), true)
	if n := count(); n != 1 {
		t.Fatalf("setting a timed throttle announced %d changes, want 1", n)
	}
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("the throttle lifting at its resumeAt was never announced")
	}
	if got := d.Ceiling(3); got != 3 {
		t.Fatalf("after the announced lift: ceiling = %d, want 3", got)
	}
}

// The autonomous scheduler holds its own dispatch to the throttle, and never
// work it hands to the extension, which caps that work itself (#2352). A
// running pipeline is never counted out: a cap below the running count opens
// nothing until enough finish.
func TestAutonomousScheduler_DispatchCeilingFollowsTheThrottle(t *testing.T) {
	newScheduler := func() *AutonomousScheduler {
		return &AutonomousScheduler{
			config:   AutonomousConfig{MaxConcurrent: 3},
			state:    &AutonomousState{},
			rescanCh: make(chan struct{}, 1),
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
	ext.OnDispatch(func(owner, repo string, issueNumber int, title string) {})
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
