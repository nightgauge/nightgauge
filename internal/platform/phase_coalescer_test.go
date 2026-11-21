package platform

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock drives a phaseCoalescer deterministically: afterFunc records
// timers, advance fires those that are due.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	f  func()
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(d), f: f})
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			due = append(due, t.f)
		} else {
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func newTestCoalescer() (*phaseCoalescer, *fakeClock, *[]PipelineEvent) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	var mu sync.Mutex
	sent := &[]PipelineEvent{}
	c := newPhaseCoalescer(func(_ context.Context, e PipelineEvent) {
		mu.Lock()
		defer mu.Unlock()
		*sent = append(*sent, e)
	})
	c.now = clock.Now
	c.afterFunc = clock.AfterFunc
	return c, clock, sent
}

func phaseEvent(run, name string, idx int) PipelineEvent {
	return PipelineEvent{RunID: run, EventType: "stage_progress", Stage: "feature-dev",
		Phase: &PipelinePhase{Name: name, Index: idx, Total: 10, Status: "started"}}
}

func TestPhaseCoalescer_BurstSendsFirstAndLatest(t *testing.T) {
	c, clock, sent := newTestCoalescer()
	for i := 1; i <= 5; i++ {
		c.submit(context.Background(), phaseEvent("run-1", "p", i))
		clock.Advance(100 * time.Millisecond)
	}
	if len(*sent) != 1 || (*sent)[0].Phase.Index != 1 {
		t.Fatalf("within the window: sent %d events, want only the first", len(*sent))
	}
	clock.Advance(time.Second)
	if len(*sent) != 2 {
		t.Fatalf("after the window: sent %d events, want 2", len(*sent))
	}
	if got := (*sent)[1].Phase.Index; got != 5 {
		t.Errorf("trailing event carries index %d, want the latest (5)", got)
	}
}

func TestPhaseCoalescer_AtMostOnePerSecond(t *testing.T) {
	c, clock, sent := newTestCoalescer()
	var at []time.Time
	c.send = func(_ context.Context, e PipelineEvent) { *sent = append(*sent, e); at = append(at, clock.now) }
	for i := 1; i <= 40; i++ {
		c.submit(context.Background(), phaseEvent("run-1", "p", 1+i%10))
		clock.Advance(100 * time.Millisecond)
	}
	clock.Advance(2 * time.Second)
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < time.Second {
			t.Fatalf("sends %d and %d are %v apart, want >= 1s", i-1, i, gap)
		}
	}
	if len(*sent) < 4 {
		t.Errorf("sent %d events over 4s of phases, want >= 4", len(*sent))
	}
}

func TestPhaseCoalescer_RunsAreIndependent(t *testing.T) {
	c, _, sent := newTestCoalescer()
	c.submit(context.Background(), phaseEvent("run-1", "p", 1))
	c.submit(context.Background(), phaseEvent("run-2", "p", 1))
	if len(*sent) != 2 {
		t.Fatalf("sent %d, want one per run (2)", len(*sent))
	}
}

func TestPhaseCoalescer_QuietRunSendsImmediately(t *testing.T) {
	c, clock, sent := newTestCoalescer()
	c.submit(context.Background(), phaseEvent("run-1", "p", 1))
	clock.Advance(1500 * time.Millisecond)
	c.submit(context.Background(), phaseEvent("run-1", "p", 2))
	if len(*sent) != 2 {
		t.Fatalf("sent %d, want 2 (window had elapsed)", len(*sent))
	}
}

func TestPhaseCoalescer_StateForgottenAfterFlush(t *testing.T) {
	c, clock, _ := newTestCoalescer()
	c.submit(context.Background(), phaseEvent("run-1", "p", 1))
	c.submit(context.Background(), phaseEvent("run-1", "p", 2))
	clock.Advance(time.Second) // trailing flush
	clock.Advance(time.Second) // cleanup after the flush's window
	clock.Advance(time.Second)
	if n := c.size(); n != 0 {
		t.Errorf("coalescer tracks %d runs after going quiet, want 0", n)
	}
}

// TestAnalyticsService_EmitPipelineEvent_RoutesOnlyPhaseEvents pins that only
// phase-bearing stage_progress events enter the coalescer.
func TestAnalyticsService_EmitPipelineEvent_RoutesOnlyPhaseEvents(t *testing.T) {
	s := NewAnalyticsService(&Client{})
	c, _, sent := newTestCoalescer()
	s.phases = c
	s.phasesOnce.Do(func() {})
	ph := phaseEvent("run-1", "p", 1)
	s.EmitPipelineEvent(context.Background(), ph)
	s.EmitPipelineEvent(context.Background(), ph)
	if len(*sent) != 1 {
		t.Fatalf("coalescer received %d sends, want 1 (second within window)", len(*sent))
	}
	if c.runs["run-1"].pending == nil {
		t.Error("second phase event was not parked as pending")
	}
}

// A held phase must never trail its stage's terminal event (#3455 review):
// discarding it leaves the armed timer nothing to send.
func TestPhaseCoalescer_DiscardDropsHeldPhase(t *testing.T) {
	c, clock, sent := newTestCoalescer()
	c.submit(context.Background(), phaseEvent("run-1", "p", 1))
	c.submit(context.Background(), phaseEvent("run-1", "p", 2))
	c.submit(context.Background(), phaseEvent("run-2", "p", 1))
	c.submit(context.Background(), phaseEvent("run-2", "p", 2))
	c.discard("run-1")
	clock.Advance(2 * time.Second)
	var run1, run2 int
	for _, e := range *sent {
		if e.RunID == "run-1" {
			run1++
		} else {
			run2++
		}
	}
	if run1 != 1 || run2 != 2 {
		t.Fatalf("sent run-1=%d run-2=%d, want 1 and 2: discard must drop only run-1's held phase", run1, run2)
	}
}

// Only a stage boundary drops the held phase (#3455 review round 2): a
// token-only stage_progress event must leave the latest phase pending.
func TestAnalyticsService_EmitPipelineEvent_DiscardsOnlyOnStageBoundary(t *testing.T) {
	s := NewAnalyticsService(&Client{})
	c, _, _ := newTestCoalescer()
	s.phases = c
	s.phasesOnce.Do(func() {})
	ctx := context.Background()
	s.EmitPipelineEvent(ctx, phaseEvent("run-1", "p", 1))
	s.EmitPipelineEvent(ctx, phaseEvent("run-1", "p", 2))
	s.EmitPipelineEvent(ctx, PipelineEvent{RunID: "run-1", EventType: "stage_progress", Stage: "feature-dev"})
	if c.runs["run-1"].pending == nil {
		t.Fatal("token-only stage_progress dropped the held phase; the latest phase must still win")
	}
	s.EmitPipelineEvent(ctx, PipelineEvent{RunID: "run-1", EventType: "stage_completed", Stage: "feature-dev"})
	if c.runs["run-1"].pending != nil {
		t.Fatal("stage_completed left the held phase pending; it would trail the terminal event")
	}
}
