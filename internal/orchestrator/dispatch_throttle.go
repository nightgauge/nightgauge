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
	mu        sync.Mutex
	throttle  *platform.WorkspaceThrottle
	known     bool
	now       func() time.Time
	liftTimer *time.Timer
	listeners []func()
}

// NewDispatchThrottle returns a throttle that caps nothing until Set.
func NewDispatchThrottle() *DispatchThrottle {
	return &DispatchThrottle{now: time.Now}
}

// Set applies a throttle, or clears it with nil. known is false when the
// throttle cannot be followed (no signed-in session), which also caps nothing.
func (d *DispatchThrottle) Set(throttle *platform.WorkspaceThrottle, known bool) {
	d.mu.Lock()
	if !known {
		throttle = nil
	}
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
	if throttle != nil && throttle.ResumeAt != nil {
		if wait := throttle.ResumeAt.Sub(d.now()); wait > 0 {
			d.liftTimer = time.AfterFunc(wait, d.notify)
		}
	}
	d.mu.Unlock()
	if changed {
		d.notify()
	}
}

// Snapshot returns the throttle in force now (nil when none is), and whether
// the throttle is known at all.
func (d *DispatchThrottle) Snapshot() (*platform.WorkspaceThrottle, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.throttle == nil || !d.throttle.InForce(d.now()) {
		return nil, d.known
	}
	copied := *d.throttle
	return &copied, d.known
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

func (d *DispatchThrottle) notify() {
	d.mu.Lock()
	listeners := append([]func(){}, d.listeners...)
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
