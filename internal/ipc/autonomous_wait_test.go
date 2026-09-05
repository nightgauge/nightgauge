// #494 — the autonomous.* IPC handlers must report the scheduler state they
// actually observed, not the state a 50ms nap assumed they would reach.
//
// The handlers used to call Stop()/Run() and then sleep a flat
// 50 * time.Millisecond before sampling Status(). A wall-clock guess is not a
// synchronisation primitive: when the dispatch loop is mid-cycle the guess
// expires first, and the handler hands the extension a status the scheduler
// has not reached — `autonomous.stop` answering "running" is the visible
// shape. These tests pin the two halves of the replacement: no sleep survives
// in the handlers, and the response tracks the real transition.
package ipc

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// newAutonomousTestScheduler builds a scheduler with no forge client and no
// repos, the same shape autonomous_resume_starts_goroutine_test.go uses: the
// graph build fails immediately without touching the network, which is all
// these tests need from a cycle.
func newAutonomousTestScheduler(t *testing.T) *orchestrator.AutonomousScheduler {
	t.Helper()
	tmpDir := t.TempDir()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: tmpDir,
		Adapter:       nil,
	})
	cfg := orchestrator.DefaultAutonomousConfig()
	cfg.MaxConcurrent = 1
	return orchestrator.NewAutonomousScheduler(sched, nil, nil, nil, cfg, tmpDir)
}

// TestAutonomousHandlers_NoWallClockSleep is the source-level half of the
// first acceptance criterion. It is deliberately a text scan: the four sleeps
// lived in four different handlers, and a behavioural test for each would pin
// only the ones whose transition can be widened from outside the scheduler.
func TestAutonomousHandlers_NoWallClockSleep(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, `s.methods["autonomous.start"]`)
	end := strings.Index(text, `s.methods["autonomous.complete"]`)
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not locate the autonomous.* handler span (start=%d end=%d)", start, end)
	}
	span := text[start:end]
	if !strings.Contains(span, "time.Sleep(") {
		return
	}
	var offenders []string
	for i := 0; ; {
		j := strings.Index(span[i:], "time.Sleep(")
		if j < 0 {
			break
		}
		off := start + i + j
		offenders = append(offenders, "server.go:"+strconv.Itoa(1+strings.Count(text[:off], "\n")))
		i += j + len("time.Sleep(")
	}
	t.Fatalf("autonomous.* handlers must synchronize on scheduler state, not sleep; found time.Sleep at %s",
		strings.Join(offenders, ", "))
}

// TestAutonomousStop_ReportsObservedState is the behavioural half. The
// dispatch loop is held inside the one expensive call on its path — the
// dependency-graph build — for longer than the old handler's 50ms guess.
// That is the ordinary case under load, since a real build talks to the
// forge. autonomous.stop must not return until the loop has actually drained
// stopCh and run complete().
func TestAutonomousStop_ReportsObservedState(t *testing.T) {
	as := newAutonomousTestScheduler(t)

	// inBuild makes the test's own sequencing deterministic: the stop handler
	// is not called until the scheduler is provably inside the build, so the
	// window the old sleep had to cover is not itself a guess.
	inBuild := make(chan struct{})
	var once sync.Once
	as.SetBuildGraph(func(context.Context) (*depgraph.Graph, error) {
		once.Do(func() {
			close(inBuild)
			// Hold the loop well past the 50ms the handler used to assume
			// was enough, and well inside the deadline the fix waits out.
			time.Sleep(250 * time.Millisecond)
		})
		return nil, errors.New("test: no graph")
	})

	server := NewServer(nil, WithAutonomousScheduler(as))
	startHandler, ok := server.methods["autonomous.start"]
	if !ok {
		t.Fatal("autonomous.start handler not registered")
	}
	stopHandler, ok := server.methods["autonomous.stop"]
	if !ok {
		t.Fatal("autonomous.stop handler not registered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for as.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	if _, err := startHandler(ctx, nil); err != nil {
		t.Fatalf("autonomous.start returned error: %v", err)
	}
	select {
	case <-inBuild:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler never reached a graph build")
	}

	began := time.Now()
	res, err := stopHandler(ctx, nil)
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("autonomous.stop returned error: %v", err)
	}
	// Read the live state at the instant the handler returned, so both
	// assertions describe the same moment.
	stillRunning := as.IsRunning()
	st, ok := res.(orchestrator.AutonomousState)
	if !ok {
		t.Fatalf("autonomous.stop returned %T, want orchestrator.AutonomousState", res)
	}
	if stillRunning {
		t.Errorf("autonomous.stop returned while the scheduler goroutine was still running: the handler reported a transition it did not observe")
	}
	if st.Status == "running" {
		t.Errorf("autonomous.stop reported Status=%q — the response must reflect the observed post-transition state", st.Status)
	}
	// The handler must be woken BY the transition, not released by its own
	// deadline. The loop is held for 250ms and the deadline is seconds away,
	// so anything near the deadline means the wait is polling a timer rather
	// than observing the state change.
	if elapsed > time.Second {
		t.Errorf("autonomous.stop took %s to return after a 250ms cycle — the wait must be woken by the state transition, not by its deadline", elapsed)
	}
}

// TestAutonomousStop_DeadlineReportsActualState pins the other half of the
// contract: the bounded wait must not turn into the old lie in slower form.
// The dispatch loop here never observes the stop inside the deadline, and the
// handler is required to come back promptly reporting the scheduler as it
// really is — still running — rather than the stopped state the caller asked
// for. This is new coverage, not a regression pin: the sleep-based handler
// returned "running" here too, for the wrong reason.
func TestAutonomousStop_DeadlineReportsActualState(t *testing.T) {
	as := newAutonomousTestScheduler(t)

	inBuild := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	as.SetBuildGraph(func(context.Context) (*depgraph.Graph, error) {
		once.Do(func() {
			close(inBuild)
			<-release
		})
		return nil, errors.New("test: no graph")
	})

	server := NewServer(nil, WithAutonomousScheduler(as))
	server.autonomousWaitTimeout = 150 * time.Millisecond
	startHandler := server.methods["autonomous.start"]
	stopHandler := server.methods["autonomous.stop"]

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		close(release)
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for as.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	if _, err := startHandler(ctx, nil); err != nil {
		t.Fatalf("autonomous.start returned error: %v", err)
	}
	select {
	case <-inBuild:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler never reached a graph build")
	}

	began := time.Now()
	res, err := stopHandler(ctx, nil)
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("autonomous.stop returned error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("autonomous.stop took %s — the wait must be bounded by the deadline, not by the scheduler", elapsed)
	}
	st, ok := res.(orchestrator.AutonomousState)
	if !ok {
		t.Fatalf("autonomous.stop returned %T, want orchestrator.AutonomousState", res)
	}
	if !as.IsRunning() {
		t.Fatal("test premise broken: the scheduler was supposed to still be wedged in its cycle")
	}
	if st.Status != "running" {
		t.Errorf("autonomous.stop reported Status=%q after its deadline expired, but the scheduler is still running — a timed-out wait must report the observed state, not the requested one", st.Status)
	}
}
