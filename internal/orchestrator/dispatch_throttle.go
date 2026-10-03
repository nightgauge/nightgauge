package orchestrator

import (
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// DispatchThrottle is the platform workspace throttle that work the Go side
// dispatches itself is held to (#2352): the autonomous scheduler when it
// dispatches without the extension, and the auto-scheduler loop. The
// extension caps the work it runs at its own dispatch (#2337), so work handed
// to it is never capped a second time here.
//
// While a throttle is in force, no new run starts above the lower of the
// configured concurrency and its MaxConcurrent. A running pipeline is never
// stopped, and a cap below the running count starts nothing until enough of
// them finish. The throttle stops counting at its ResumeAt without a further
// read, and every change, including that one, is announced to OnChange
// listeners so a waiting dispatcher looks again at once.
//
// Safe for concurrent use.
type DispatchThrottle struct {
	mu       sync.Mutex
	throttle *platform.WorkspaceThrottle
	known    bool
	// unread is true while the throttle is followed (a signed-in session
	// exists) but not known: no read has succeeded since the session came.
	unread bool
	now    func() time.Time
	// afterFunc arms the lift at resumeAt: time.AfterFunc, but in tests.
	afterFunc func(time.Duration, func()) liftTimer
	liftTimer liftTimer
	listeners []func()
	// changed is closed at the next announced change; see Changed.
	changed chan struct{}
}

// liftTimer is the timer that announces the throttle lifting at its resumeAt.
type liftTimer interface{ Stop() bool }

// NewDispatchThrottle returns a throttle that caps nothing until Set.
func NewDispatchThrottle() *DispatchThrottle {
	return &DispatchThrottle{
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) liftTimer { return time.AfterFunc(d, f) },
	}
}

// Set applies a throttle, or clears it with nil. known is false when the
// throttle cannot be followed (no signed-in session), which also caps nothing.
func (d *DispatchThrottle) Set(throttle *platform.WorkspaceThrottle, known bool) {
	d.mu.Lock()
	if !known {
		throttle = nil
	}
	d.unread = false
	if throttle != nil {
		copied := *throttle
		throttle = &copied
	}
	changed := known != d.known || !sameThrottle(d.throttle, throttle)
	d.throttle = throttle
	d.known = known
	if d.liftTimer != nil {
		d.liftTimer.Stop()
		d.liftTimer = nil
	}
	// A resumeAt already past arms nothing: the throttle is not in force, and
	// this Set announces that as its change.
	if throttle != nil && throttle.ResumeAt != nil {
		if wait := throttle.ResumeAt.Sub(d.now()); wait > 0 {
			d.liftTimer = d.afterFunc(wait, d.notify)
		}
	}
	d.mu.Unlock()
	if changed {
		d.notify()
	}
}

// MarkUnread records that the throttle is followed, since a signed-in
// session exists, but not known yet: its first read is in flight, or every
// read so far failed (#2352). A daemon also marks it while it has not yet
// learned whether a session exists, from its start until the extension
// pushes its session or says it has none. Like an unknown throttle it caps nothing, but a
// headless scheduler asking the daemon keeps the throttle it learned before,
// where a daemon that follows none lifts it. A known throttle is left as it
// is: a read that fails changes nothing.
func (d *DispatchThrottle) MarkUnread() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.known {
		d.unread = true
	}
}

// Report is what the daemon tells a headless scheduler (#2352): the throttle
// in force (nil when none is), whether it is known, and, when it is not,
// whether it is followed but not read yet (MarkUnread).
func (d *DispatchThrottle) Report() (throttle *platform.WorkspaceThrottle, known, unread bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inForceLocked(), d.known, !d.known && d.unread
}

// Snapshot returns the throttle in force now (nil when none is), and whether
// the throttle is known at all.
func (d *DispatchThrottle) Snapshot() (*platform.WorkspaceThrottle, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inForceLocked(), d.known
}

// inForceLocked is a copy of the throttle in force now, nil when none is.
func (d *DispatchThrottle) inForceLocked() *platform.WorkspaceThrottle {
	if d.throttle == nil || !d.throttle.InForce(d.now()) {
		return nil
	}
	copied := *d.throttle
	return &copied
}

// Ceiling is the concurrency dispatch may reach: configured, or the
// throttle's cap when that is lower and the throttle is in force.
func (d *DispatchThrottle) Ceiling(configured int) int {
	if d == nil {
		return configured
	}
	throttle, _ := d.Snapshot()
	if throttle != nil && throttle.MaxConcurrent < configured {
		return throttle.MaxConcurrent
	}
	return configured
}

// OnChange registers a listener called after the throttle changes or lifts
// at its ResumeAt. Listeners run outside the throttle's lock.
func (d *DispatchThrottle) OnChange(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listeners = append(d.listeners, fn)
}

// Changed returns a channel closed at the next change, the lift at resumeAt
// included, so a dispatcher waiting for room looks again at once.
func (d *DispatchThrottle) Changed() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	return d.changed
}

func (d *DispatchThrottle) notify() {
	d.mu.Lock()
	listeners := append([]func(){}, d.listeners...)
	if d.changed != nil {
		close(d.changed)
		d.changed = nil
	}
	d.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

func sameThrottle(a, b *platform.WorkspaceThrottle) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.MaxConcurrent != b.MaxConcurrent {
		return false
	}
	if a.ResumeAt == nil || b.ResumeAt == nil {
		return a.ResumeAt == nil && b.ResumeAt == nil
	}
	return a.ResumeAt.Equal(*b.ResumeAt)
}
