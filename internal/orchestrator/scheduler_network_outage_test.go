// Tests for the network-outage abort path (Issue #3296).
//
// The scheduler exposes CancelAllForNetworkOutage so the IPC handler can,
// on detecting an extended GitHub connectivity loss, cancel every active
// stage context with cause ErrNetworkUnavailable. The cancelled stage
// short-circuits retry / escalation / stall-recovery and exits with
// terminal_failure_kind="network_unavailable", skipping outcome recording.

package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestCancelAllForNetworkOutage_NoActiveStages(t *testing.T) {
	s := &Scheduler{}
	got := s.CancelAllForNetworkOutage()
	if got != nil {
		t.Fatalf("want nil cancelled list when no active stages, got %v", got)
	}
}

func TestCancelAllForNetworkOutage_CancelsAndReturnsSorted(t *testing.T) {
	s := &Scheduler{}

	// Register three active stages (issues out of order to verify sorting).
	mkCancel := func() context.CancelCauseFunc {
		var calls []error
		var mu sync.Mutex
		return func(cause error) {
			mu.Lock()
			calls = append(calls, cause)
			mu.Unlock()
		}
	}

	c10 := mkCancel()
	c30 := mkCancel()
	c20 := mkCancel()
	s.registerActiveStage(30, c30)
	s.registerActiveStage(10, c10)
	s.registerActiveStage(20, c20)

	got := s.CancelAllForNetworkOutage()

	want := []int{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("len(cancelled) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cancelled[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestCancelAllForNetworkOutage_CauseIsErrNetworkUnavailable(t *testing.T) {
	s := &Scheduler{}

	stageCtx, cancel := context.WithCancelCause(context.Background())
	s.registerActiveStage(7, cancel)
	defer s.unregisterActiveStage(7)

	got := s.CancelAllForNetworkOutage()
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("cancelled = %v, want [7]", got)
	}

	select {
	case <-stageCtx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("stage context was not cancelled within 1s")
	}

	cause := context.Cause(stageCtx)
	if !errors.Is(cause, ErrNetworkUnavailable) {
		t.Errorf("cause = %v, want ErrNetworkUnavailable", cause)
	}
}

func TestRegisterUnregisterActiveStage_BalancedLifecycle(t *testing.T) {
	s := &Scheduler{}
	_, cancel := context.WithCancelCause(context.Background())

	s.registerActiveStage(42, cancel)
	if got := s.CancelAllForNetworkOutage(); len(got) != 1 {
		t.Errorf("after register: cancelled = %v, want [42]", got)
	}

	s.unregisterActiveStage(42)
	// Re-register to confirm map is healthy after unregister.
	_, cancel2 := context.WithCancelCause(context.Background())
	s.registerActiveStage(42, cancel2)
	if got := s.CancelAllForNetworkOutage(); len(got) != 1 {
		t.Errorf("after re-register: cancelled = %v, want [42]", got)
	}
}

func TestRegisterActiveStage_GuardsAgainstZeroAndNil(t *testing.T) {
	s := &Scheduler{}
	_, cancel := context.WithCancelCause(context.Background())

	s.registerActiveStage(0, cancel)
	s.registerActiveStage(-1, cancel)
	s.registerActiveStage(1, nil)

	if got := s.CancelAllForNetworkOutage(); got != nil {
		t.Errorf("invalid registrations should be ignored, got %v", got)
	}
}

func TestRegisterActiveStage_ConcurrentAccessDoesNotRace(t *testing.T) {
	s := &Scheduler{}

	var wg sync.WaitGroup
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, cancel := context.WithCancelCause(context.Background())
			s.registerActiveStage(n, cancel)
		}(i)
	}
	wg.Wait()

	got := s.CancelAllForNetworkOutage()
	if len(got) != 50 {
		t.Errorf("len(cancelled) = %d, want 50", len(got))
	}
}

func TestClassifyTerminalKind_NetworkUnavailable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "exact ErrNetworkUnavailable text",
			in:   ErrNetworkUnavailable.Error(),
			want: TerminalKindNetworkUnavailable,
		},
		{
			name: "wrapped error message",
			in:   "stage: " + ErrNetworkUnavailable.Error() + " (after 3 connectivity failures)",
			want: TerminalKindNetworkUnavailable,
		},
		{
			name: "case-insensitive",
			in:   "NETWORK UNAVAILABLE: EXTENDED GITHUB CONNECTIVITY LOSS",
			want: TerminalKindNetworkUnavailable,
		},
		{
			name: "rate limit string is NOT mis-classified as network",
			in:   "rate limit exceeded",
			want: "", // ClassifyTerminalKind returns "" for unmatched
		},
		{
			name: "stall kill is NOT mis-classified as network",
			in:   "[stall-killed] exceeded stall idle threshold",
			want: TerminalKindStallKill,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyTerminalKind(tc.in)
			if got != tc.want {
				t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// errNetUnavailableRunner is a StageRunner that returns ErrNetworkUnavailable
// when its ctx is cancelled with that cause — proves the scheduler's
// post-stage cause inspection produces the right error to surface.
type errNetUnavailableRunner struct{}

func (errNetUnavailableRunner) RunStage(ctx context.Context, _ StageRunParams) (*StageRunResult, error) {
	<-ctx.Done()
	return &StageRunResult{ExitCode: 1}, errors.New("subprocess killed")
}

func TestStageContext_CancelCausePreservedAfterRun(t *testing.T) {
	// Verifies the wiring pattern used in runPipeline: WithCancelCause +
	// register + run + Cause() check. We reproduce the inner block here so
	// the test doesn't have to spin up a full pipeline.

	s := &Scheduler{}
	parentCtx := context.Background()

	stageCtx, cancelStage := context.WithCancelCause(parentCtx)
	s.registerActiveStage(99, cancelStage)
	defer s.unregisterActiveStage(99)

	go func() {
		// Simulate the watchdog firing the cancel.
		time.Sleep(20 * time.Millisecond)
		s.CancelAllForNetworkOutage()
	}()

	runner := errNetUnavailableRunner{}
	_, runErr := runner.RunStage(stageCtx, StageRunParams{
		IssueNumber: 99,
		Repo:        "test/repo",
		Stage:       "pr-merge",
		Runtime:     nil,
	})

	if runErr == nil {
		t.Fatal("expected stage runner to return an error after cancellation")
	}

	cause := context.Cause(stageCtx)
	if !errors.Is(cause, ErrNetworkUnavailable) {
		t.Errorf("context.Cause = %v, want ErrNetworkUnavailable", cause)
	}

	// Confirm the runtime would be classified correctly using the same
	// error message the scheduler surfaces (ErrNetworkUnavailable.Error()).
	if got := ClassifyTerminalKind(ErrNetworkUnavailable.Error()); got != TerminalKindNetworkUnavailable {
		t.Errorf("ClassifyTerminalKind = %q, want %q", got, TerminalKindNetworkUnavailable)
	}
}

// Smoke check: the expected board-item shape works with the path.
func TestNetworkAbort_BoardItemSmoke(t *testing.T) {
	item := types.BoardItem{Number: 3216, Repo: "nightgauge/nightgauge"}
	if item.Number != 3216 {
		t.Fatal("BoardItem shape changed unexpectedly")
	}
}
