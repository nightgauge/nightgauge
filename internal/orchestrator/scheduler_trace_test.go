package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// cliAuthFailureText is the corpus row #533 was fixed against: the reason a
// grok CLI stage prints on stderr when the machine is logged out. It is an
// AUTH failure, and ClassifyTerminalKind routes it to adapter_auth_failed —
// which is what makes it a discriminating fixture here. A text that classified
// as "" would let the assertions below pass on a tree where the terminal-kind
// derivation had been removed entirely.
const cliAuthFailureText = "Error: Not signed in. To authenticate without a browser, run: grok login --device-code"

// failingStageRunner reports a stage failure in ONE of the two shapes the
// scheduler must treat identically:
//
//   - CLI shape (ipcShape=false): err == nil, ExitCode 1, and the adapter's own
//     stderr reason carried on result.ErrorText. This is what execution.Manager
//     produces — a non-zero process exit is not a Go error.
//   - IPC shape (ipcShape=true): a non-nil Go error carrying the same text and
//     an EMPTY ErrorText, which is what the extension's IPC runner produces.
//
// Every consumer of the failure text must reach the same conclusion from both.
type failingStageRunner struct {
	mu       sync.Mutex
	rt       *state.RuntimeState
	calls    int
	ipcShape bool
}

func (r *failingStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls++
	if r.rt == nil {
		r.rt = params.Runtime
	}
	r.mu.Unlock()
	if r.ipcShape {
		return &StageRunResult{ExitCode: 1}, errors.New(cliAuthFailureText)
	}
	return &StageRunResult{ExitCode: 1, ErrorText: cliAuthFailureText}, nil
}

// runID returns the run identity the scheduler minted for this run, which is
// the key trace events are filed under.
func (r *failingStageRunner) runID(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rt == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState — the stage never dispatched")
	}
	return r.rt.Snapshot().RunID
}

// readStageExitPayloads returns every KindStageExit payload in the run's trace
// file, decoded. trace.Event carries the payload as an interface{}, so it is
// round-tripped through JSON rather than type-asserted.
func readStageExitPayloads(t *testing.T, root, runID string) []trace.StageExitPayload {
	t.Helper()
	events, err := trace.ReadRun(root, runID)
	if err != nil {
		t.Fatalf("trace.ReadRun(%s, %s): %v", root, runID, err)
	}
	var out []trace.StageExitPayload
	for _, ev := range events {
		if ev.Kind != trace.KindStageExit {
			continue
		}
		raw, err := json.Marshal(ev.Payload)
		if err != nil {
			t.Fatalf("re-marshal stage_exit payload: %v", err)
		}
		var p trace.StageExitPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode stage_exit payload %s: %v", raw, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		t.Fatalf("no %s event in the run's trace (%d events total)", trace.KindStageExit, len(events))
	}
	return out
}

// TestTraceStageExit_CLIFailureCarriesTerminalKind is the #566 / #533 pin on the
// trace stage-exit payload (scheduler.go, the `if !exitPayload.Success` block).
//
// Before #533 the terminal kind was derived from `err.Error()` behind an
// `err != nil` guard. CLI-mode failures arrive with err == nil and the reason on
// the result, so EVERY CLI stage's trace exit was written with an empty
// terminal_kind — the ADR-013 lifecycle trace, the thing a retro reads to see
// why a run died, was structurally blind on the whole CLI path.
//
// The IPC twin below is not decoration: it pins the two shapes as EQUIVALENT,
// so a future change cannot "fix" one path by moving the derivation onto the
// other.
func TestTraceStageExit_CLIFailureCarriesTerminalKind(t *testing.T) {
	shapes := []struct {
		name     string
		ipcShape bool
		issue    int
	}{
		{name: "cli_shape_err_nil_reason_on_result", ipcShape: false, issue: 5661},
		{name: "ipc_shape_non_nil_err_empty_error_text", ipcShape: true, issue: 5662},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			root := t.TempDir()
			seedRefusalRepo(t, root, allRefusalStageSkills)

			runner := &failingStageRunner{ipcShape: shape.ipcShape}
			s := newRefusalScheduler(root, runner)
			s.runPipeline(context.Background(), types.BoardItem{
				Number: shape.issue, Repo: "nightgauge/nightgauge", ID: "item-trace",
			})

			payloads := readStageExitPayloads(t, root, runner.runID(t))
			p := payloads[0]

			if p.Success {
				t.Fatalf("stage_exit payload reports success=true for an exit-1 stage: %+v", p)
			}
			if p.TerminalKind != TerminalKindAdapterAuthFailed {
				t.Errorf("stage_exit terminal_kind = %q, want %q\n"+
					"the stage failed with %q; a trace exit with no terminal kind is exactly the "+
					"#533 blindness this pins — the retro path reads this field to learn why the run died",
					p.TerminalKind, TerminalKindAdapterAuthFailed, cliAuthFailureText)
			}
			if p.ExitCode != 1 {
				t.Errorf("stage_exit exit_code = %d, want 1", p.ExitCode)
			}
		})
	}
}
