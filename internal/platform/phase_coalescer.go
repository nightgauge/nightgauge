package platform

import (
	"context"
	"sync"
	"time"
)

// phaseCoalesceWindow is the minimum spacing between two phase events POSTed
// for one run (#3455): at most one per second, the latest state winning.
const phaseCoalesceWindow = time.Second

// phaseCoalescer rate-limits phase-bearing stage_progress events per run. The
// first event of a quiet run is sent at once; any event arriving within the
// window of the last send replaces the run's pending event, and one timer
// sends whatever is pending when the window ends. Intermediate phases of a
// burst are dropped on purpose; the final state always reaches the platform.
//
// submit never blocks: the mutex guards only the per-run slot swap, and the
// send itself goes through the service's normal goroutine, offline queue and
// retry path.
type phaseCoalescer struct {
	mu   sync.Mutex
	runs map[string]*phaseRun

	window    time.Duration
	now       func() time.Time
	afterFunc func(d time.Duration, f func()) // injectable for tests
	send      func(ctx context.Context, event PipelineEvent)
}

// phaseRun is one run's coalescing state.
type phaseRun struct {
	lastSent   time.Time
	pending    *PipelineEvent
	pendingCtx context.Context
	timerArmed bool
}

func newPhaseCoalescer(send func(ctx context.Context, event PipelineEvent)) *phaseCoalescer {
	return &phaseCoalescer{
		runs:   map[string]*phaseRun{},
		window: phaseCoalesceWindow,
		now:    time.Now,
		afterFunc: func(d time.Duration, f func()) {
			time.AfterFunc(d, f)
		},
		send: send,
	}
}

// phaseCoalescer returns the service's coalescer, creating it on first use.
func (s *AnalyticsService) phaseCoalescer() *phaseCoalescer {
	s.phasesOnce.Do(func() {
		if s.phases == nil {
			s.phases = newPhaseCoalescer(s.emitAsync)
		}
	})
	return s.phases
}

// submit sends event now when the run's window is open, or parks it as the
// run's pending event (replacing any earlier one) for the trailing flush.
func (c *phaseCoalescer) submit(ctx context.Context, event PipelineEvent) {
	key := event.RunID
	c.mu.Lock()
	run := c.runs[key]
	now := c.now()
	if run == nil {
		run = &phaseRun{}
		c.runs[key] = run
	}
	if run.lastSent.IsZero() || now.Sub(run.lastSent) >= c.window {
		run.lastSent = now
		c.mu.Unlock()
		c.send(ctx, event)
		c.scheduleCleanup(key, now)
		return
	}
	ev := event
	run.pending = &ev
	run.pendingCtx = ctx
	if run.timerArmed {
		c.mu.Unlock()
		return
	}
	run.timerArmed = true
	wait := c.window - now.Sub(run.lastSent)
	c.mu.Unlock()
	c.afterFunc(wait, func() { c.flush(key) })
}

// flush sends the run's pending event, if any, and opens a fresh window.
func (c *phaseCoalescer) flush(key string) {
	c.mu.Lock()
	run := c.runs[key]
	if run == nil || run.pending == nil {
		if run != nil {
			run.timerArmed = false
		}
		c.mu.Unlock()
		return
	}
	ev, ctx := *run.pending, run.pendingCtx
	run.pending, run.pendingCtx = nil, nil
	run.timerArmed = false
	now := c.now()
	run.lastSent = now
	c.mu.Unlock()
	c.send(ctx, ev)
	c.scheduleCleanup(key, now)
}

// discard drops the run's pending phase event, if any (#3455 review). Any
// other event of the run supersedes it: a held phase sent after its stage's
// stage_completed, stage_failed or the run's pipeline_done would arrive late
// and present a finished stage as still in progress. The armed timer then
// finds nothing pending and sends nothing.
func (c *phaseCoalescer) discard(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.runs[key]; run != nil {
		run.pending, run.pendingCtx = nil, nil
	}
}

// scheduleCleanup forgets a run once a full window has passed since sentAt
// with nothing pending, so the map does not grow with every run ever seen.
func (c *phaseCoalescer) scheduleCleanup(key string, sentAt time.Time) {
	c.afterFunc(c.window, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		run := c.runs[key]
		if run != nil && run.pending == nil && !run.timerArmed && run.lastSent.Equal(sentAt) {
			delete(c.runs, key)
		}
	})
}

// size reports how many runs the coalescer is tracking (tests).
func (c *phaseCoalescer) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runs)
}
