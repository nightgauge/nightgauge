package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// #340 — the Go scheduler owns model resolution on the IPC path.
//
// IpcStageRunner is where the escalation/downgrade decision is MADE (on the
// stage result) and where the next dispatch's model is HANDED OFF (on the
// pipeline.runStage wire). These tests pin both halves plus the wire shape.
// The other half of the loop — the extension actually spawning on the value —
// is pinned in tests/services/SkillRunner.ipcModelAuthority.test.ts, because
// no Go test can observe the CLI argv.

// newEscalatingStageRunner builds a runner over a real RetryEngine so the
// escalation/downgrade evaluation in RunStage exercises production logic
// rather than a stub.
func newEscalatingStageRunner(buf *bytes.Buffer) (*IpcStageRunner, *orchestrator.RetryEngine) {
	srv := &Server{
		writer:         buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
	}
	engine := orchestrator.NewRetryEngine(orchestrator.DefaultRetryConfig())
	return NewIpcStageRunner(srv, engine), engine
}

// runStageWithResult drives one RunStage to completion by delivering `result`
// once the runner is waiting on it.
func runStageWithResult(
	t *testing.T,
	runner *IpcStageRunner,
	params orchestrator.StageRunParams,
	result StageResultParams,
) *orchestrator.StageRunResult {
	t.Helper()
	type outcome struct {
		res *orchestrator.StageRunResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := runner.RunStage(context.Background(), params)
		done <- outcome{r, err}
	}()

	deadline := time.After(2 * time.Second)
	for {
		if runner.DeliverStageResult(result) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("runner never registered a pending result channel")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	select {
	case o := <-done:
		return o.res
	case <-time.After(2 * time.Second):
		t.Fatal("RunStage did not return")
		return nil
	}
}

// TestRunStage_EscalationReachesTheNextDispatch is the #340 regression test on
// the Go side: a failed stage escalates the shared RetryEngine, and the model
// the scheduler resolves from it is what goes out on the wire. Before #340 this
// value was emitted and then discarded by the TypeScript executor, so the retry
// re-ran on the tier that had just failed.
func TestRunStage_EscalationReachesTheNextDispatch(t *testing.T) {
	var buf bytes.Buffer
	runner, engine := newEscalatingStageRunner(&buf)

	attempt := orchestrator.StageRunParams{
		Stage:       state.StageFeatureDev,
		IssueNumber: 340,
		Repo:        "nightgauge/nightgauge",
		Model:       "sonnet",
		Timeout:     30 * time.Second,
		RunID:       testRunID,
	}
	res := runStageWithResult(t, runner, attempt, StageResultParams{
		Stage:       string(state.StageFeatureDev),
		IssueNumber: 340,
		Success:     false,
		ExitCode:    1,
		ErrorText:   "stage feature-dev exited 1",
	})
	if !res.EscalationRecorded {
		t.Fatal("EscalationRecorded = false, want true — a failed stage must escalate")
	}

	escalated := engine.CurrentModel(string(state.StageFeatureDev))
	if escalated != "opus" {
		t.Fatalf("CurrentModel = %q, want opus", escalated)
	}

	// The scheduler's next dispatch resolves through the same engine, so the
	// escalated tier is what RunStageParams must carry.
	buf.Reset()
	retry := attempt
	retry.Model = escalated
	ctx, cancel := context.WithCancel(context.Background())
	emitted := runStageAndCapture(t, runner, ctx, cancel, retry, &buf)

	if emitted.Model != "opus" {
		t.Errorf("wire model = %q, want opus — the escalated tier must reach the executor", emitted.Model)
	}
}

// TestRunStage_ModelUnavailableDowngradeReachesTheNextDispatch covers the #42
// sticky downgrade: when the API rejects a tier, every later stage resolving to
// it runs the substitute. Re-dispatching the rejected tier fails identically.
func TestRunStage_ModelUnavailableDowngradeReachesTheNextDispatch(t *testing.T) {
	var buf bytes.Buffer
	runner, engine := newEscalatingStageRunner(&buf)

	attempt := orchestrator.StageRunParams{
		Stage:       state.StageFeatureDev,
		IssueNumber: 42,
		Repo:        "nightgauge/nightgauge",
		Model:       "sonnet",
		Timeout:     30 * time.Second,
		RunID:       testRunID,
	}
	res := runStageWithResult(t, runner, attempt, StageResultParams{
		Stage:       string(state.StageFeatureDev),
		IssueNumber: 42,
		Success:     false,
		ExitCode:    1,
		ErrorText:   "API Error: model not found: claude-sonnet-5",
	})
	if !res.FallbackRecorded {
		t.Fatal("FallbackRecorded = false, want true — an API model rejection must downgrade")
	}
	if res.FallbackFromModel != "sonnet" || res.FallbackToModel != "haiku" {
		t.Fatalf("fallback = %q → %q, want sonnet → haiku", res.FallbackFromModel, res.FallbackToModel)
	}

	// resolveDispatchModel routes the next stage through ApplyDowngrades.
	substituted := engine.ApplyDowngrades("sonnet")
	if substituted != "haiku" {
		t.Fatalf("ApplyDowngrades(sonnet) = %q, want haiku", substituted)
	}

	buf.Reset()
	retry := attempt
	retry.Model = substituted
	ctx, cancel := context.WithCancel(context.Background())
	emitted := runStageAndCapture(t, runner, ctx, cancel, retry, &buf)

	if emitted.Model != "haiku" {
		t.Errorf("wire model = %q, want haiku — the sticky downgrade must reach the executor", emitted.Model)
	}
}

// TestRunStage_WireCarriesNoPrompt pins the deletion of the write-only Prompt
// field (#340). The scheduler still composes a prompt for the auto/CLI runner,
// which spawns Claude itself; the extension composes its own (skill render +
// platform-resolved body + behavioral preamble) and never read this one. A
// populated field nobody reads is indistinguishable from one that broke, so the
// assertion is on the raw JSON — a re-added struct field with a Go-side value
// would otherwise be invisible to a typed unmarshal into the current shape.
func TestRunStage_WireCarriesNoPrompt(t *testing.T) {
	var buf bytes.Buffer
	runner := newTestStageRunner(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	runStageAndCapture(t, runner, ctx, cancel, orchestrator.StageRunParams{
		Stage:       state.StageFeatureDev,
		IssueNumber: 340,
		Model:       "sonnet",
		Timeout:     30 * time.Second,
		RunID:       testRunID,
		Prompt:      "PROMPT-THE-EXTENSION-NEVER-READS",
	}, &buf)

	out := buf.String()
	if strings.Contains(out, "PROMPT-THE-EXTENSION-NEVER-READS") {
		t.Errorf("pipeline.runStage carried the scheduler prompt: %s", out)
	}

	var raw map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var evt struct {
			Event string                     `json:"event"`
			Data  map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &evt); err == nil && evt.Event == "pipeline.runStage" {
			raw = evt.Data
			break
		}
	}
	if raw == nil {
		t.Fatalf("no pipeline.runStage event in output: %s", out)
	}
	if _, ok := raw["prompt"]; ok {
		t.Error("pipeline.runStage payload still has a `prompt` key")
	}
	if _, ok := raw["model"]; !ok {
		t.Error("pipeline.runStage payload must carry `model` — it is the authoritative decision")
	}
}
