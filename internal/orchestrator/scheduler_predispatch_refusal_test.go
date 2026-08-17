package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// refusalCapturingStageRunner succeeds every stage it is asked to run, writes
// the minimal output context the #2870 post-stage check requires, and captures
// both the *state.RuntimeState and each stage's OutputFile path.
//
// Capturing the runtime pointer is the only way a test can inspect the run
// after runPipeline returns — the scheduler unregisters the run from
// activeRuntimes via a defer right after registration (#370 / ADR-017), so a
// post-return lookup by issue number finds nothing. This mirrors
// budgetCapturingStageRunner (the #444 test double); it is kept separate
// because these tests also need the recorded OutputFile path.
type refusalCapturingStageRunner struct {
	mu          sync.Mutex
	runtime     *state.RuntimeState
	calls       map[state.PipelineStage]int
	outputFiles map[state.PipelineStage]string
}

func newRefusalCapturingStageRunner() *refusalCapturingStageRunner {
	return &refusalCapturingStageRunner{
		calls:       make(map[state.PipelineStage]int),
		outputFiles: make(map[state.PipelineStage]string),
	}
}

func (r *refusalCapturingStageRunner) count(stage state.PipelineStage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[stage]
}

func (r *refusalCapturingStageRunner) outputFile(stage state.PipelineStage) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outputFiles[stage]
}

func (r *refusalCapturingStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls[params.Stage]++
	r.outputFiles[params.Stage] = params.OutputFile
	if r.runtime == nil {
		r.runtime = params.Runtime
	}
	r.mu.Unlock()

	if params.OutputFile != "" {
		if err := os.MkdirAll(filepath.Dir(params.OutputFile), 0o755); err == nil {
			payload := map[string]any{
				"schema_version":   "1.0",
				"issue_number":     params.IssueNumber,
				"plan_file":        "plan.md",
				"approach":         "test",
				"files_to_create":  []string{},
				"files_to_modify":  []string{},
				"files_to_read":    []string{},
				"validation_steps": []string{},
				"ok":               true,
			}
			data, _ := json.Marshal(payload)
			_ = os.WriteFile(params.OutputFile, data, 0o644)
		}
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// hookTelemetry is a telemetryService double whose only job is to run onEvent
// for each emitted pipeline event.
//
// It is used purely as a deterministic "between stages" seam: `stage_completed`
// is the one callback the scheduler fires AFTER #2870's post-stage output
// validation and BEFORE the next iteration's prerequisite gate, which is
// exactly the window the scenario needs. The scenario it stands in for is real
// — an upstream stage's context file that is gone by the time the next stage
// asks for it (worktree swept, cross-root state split, external cleanup).
type hookTelemetry struct {
	onEvent func(platform.PipelineEvent)
}

func (h *hookTelemetry) EmitPipelineEvent(_ context.Context, event platform.PipelineEvent) {
	if h.onEvent != nil {
		h.onEvent(event)
	}
}

func (h *hookTelemetry) PushPipelineRun(_ context.Context, _ state.V2RunRecord) {}

func (h *hookTelemetry) SyncQueue(_ context.Context, _ []platform.QueueSyncItem) {}

// newRefusalScheduler builds the minimal scheduler these two tests need.
func newRefusalScheduler(root string, runner StageRunner) *Scheduler {
	return &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
	}
}

// recordForIssue returns the daily-JSONL run record for the issue.
func recordForIssue(t *testing.T, root string, issueNumber int) *state.V2RunRecord {
	t.Helper()
	records := readDailyJSONLRecords(t, root)
	for i := range records {
		if records[i].IssueNumber == issueNumber {
			return &records[i]
		}
	}
	t.Fatalf("no run record for #%d (got %d records)", issueNumber, len(records))
	return nil
}

// TestPrerequisiteMissingRefusal_RecordsTheReasonUnderTheCurrentStage pins the
// first of the two #620 sites: the pre-dispatch prerequisite gate.
//
// Pre-fix the gate was `log.Printf(...); return` — no BeginStage, no
// SetStageError, no terminal kind. The run was booked terminal with an empty
// cause: snap.Stage still named the previously COMPLETED stage (BeginStage is
// what advances it), that stage had no StageErrors entry, so the terminal
// defer's `snap.StageErrors[string(snap.Stage)]` lookup missed and
// ClassifyTerminalKind("") returned "". The log line was the only surviving
// evidence.
//
// Strictly worse than #444, which recorded the reason under a key no
// snap.Stage reader looked at; this one recorded no reason at all.
//
// The assertion below is the reader-shaped access every affected consumer
// performs — internal/ipc/server.go's terminal-notify errMsg derivation,
// scheduler_exit_record.go's fallback, the outcome-recording site in
// scheduler.go, and autonomous.go's onPipelineComplete wrapper all index
// StageErrors by the CURRENT stage.
func TestPrerequisiteMissingRefusal_RecordsTheReasonUnderTheCurrentStage(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"nightgauge-issue-pickup",
		"nightgauge-feature-planning",
		"nightgauge-feature-dev",
		"nightgauge-feature-validate",
		"nightgauge-pr-create",
		"nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}

	runner := newRefusalCapturingStageRunner()
	s := newRefusalScheduler(root, runner)

	// Remove issue-pickup's handoff after it has been written and validated,
	// before feature-planning's prerequisite gate reads it. See hookTelemetry
	// for why this callback is the seam.
	s.telemetrySvc = &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
		if e.EventType != "stage_completed" || e.Stage != string(state.StageIssuePickup) {
			return
		}
		if path := runner.outputFile(state.StageIssuePickup); path != "" {
			_ = os.Remove(path)
		}
	}}
	s.telemetryEnabled = true

	item := types.BoardItem{Number: 620, Repo: "nightgauge/nightgauge", ID: "item-620"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageIssuePickup); got != 1 {
		t.Fatalf("issue-pickup ran %d times, want 1", got)
	}
	if got := runner.count(state.StageFeaturePlanning); got != 0 {
		t.Fatalf("feature-planning was dispatched %d time(s) — the prerequisite gate refuses BEFORE dispatch", got)
	}
	if runner.runtime == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState — issue-pickup did not dispatch")
	}

	snap := runner.runtime.Snapshot()

	// BeginStage must have advanced the runtime onto the REFUSED stage, or the
	// StageErrors key and snap.Stage name different stages and every
	// snap.Stage-keyed reader resolves nothing (the #444 invariant).
	if snap.Stage != state.StageFeaturePlanning {
		t.Fatalf("snap.Stage = %q, want %q (the refused stage)", snap.Stage, state.StageFeaturePlanning)
	}

	gotReason, ok := snap.StageErrors[string(snap.Stage)]
	if !ok || gotReason == "" {
		t.Fatalf("snap.StageErrors[string(snap.Stage)] = (%q, ok=%v), want the prerequisite refusal reason — "+
			"pre-fix this site recorded nothing at all, so every reader that performs exactly this "+
			"lookup saw a run that simply stopped", gotReason, ok)
	}
	// The reason must name the real cause, not a generic string. "missing
	// prerequisite" is also the literal internal/terminalkind/table.json routes
	// to validation_error, so the text and the explicit kind agree wherever the
	// kind is re-derived from the text (autonomous.go's wrapper).
	for _, want := range []string{
		"missing prerequisite",
		string(state.StageFeaturePlanning),
		"issue",
	} {
		if !strings.Contains(gotReason, want) {
			t.Errorf("stage error = %q, want it to contain %q", gotReason, want)
		}
	}
	if ClassifyTerminalKind(gotReason) != TerminalKindValidationError {
		t.Errorf("ClassifyTerminalKind(%q) = %q, want %q — the recorded text and the recorded kind "+
			"must not disagree, because autonomous.go's onPipelineComplete wrapper re-derives the "+
			"kind from this exact string", gotReason, ClassifyTerminalKind(gotReason), TerminalKindValidationError)
	}

	rec := recordForIssue(t, root, item.Number)
	if rec.Outcome != "failed" {
		t.Errorf("rec.Outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind == "" {
		t.Fatalf("rec.TerminalFailureKind = %q — a run refused at the prerequisite gate must not be "+
			"booked terminal with an empty cause", rec.TerminalFailureKind)
	}
	if rec.TerminalFailureKind != TerminalKindValidationError {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindValidationError)
	}
	stageDetail, ok := rec.Stages[string(state.StageFeaturePlanning)]
	if !ok {
		t.Fatalf("no feature-planning stage detail on the run record — the refused stage left no trace")
	}
	if !strings.Contains(stageDetail.Error, "missing prerequisite") {
		t.Errorf("record stage error = %q, want it to name the missing prerequisite", stageDetail.Error)
	}
}

// TestSkillRenderFailureRefusal_RecordsTheReasonUnderTheCurrentStage pins the
// second #620 site: the skill-render failure.
//
// Pre-fix this was `log.Printf("#%d: %v", item.Number, err); return` — the
// error text existed for exactly one log line and was then discarded. Same
// mechanism and same consequence as the prerequisite site above.
//
// The scenario is the ordinary one: the stage's SKILL.md cannot be located in
// any skills root, so skillrender.Render returns an error and there is no
// prompt to dispatch.
func TestSkillRenderFailureRefusal_RecordsTheReasonUnderTheCurrentStage(t *testing.T) {
	root := t.TempDir()
	// Deliberately NOT writing nightgauge-feature-planning: the run reaches
	// feature-planning with its prerequisite satisfied and fails to compose the
	// skill.
	writeSkillFile(t, root, "nightgauge-issue-pickup")

	runner := newRefusalCapturingStageRunner()
	s := newRefusalScheduler(root, runner)

	item := types.BoardItem{Number: 620, Repo: "nightgauge/nightgauge", ID: "item-620"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageIssuePickup); got != 1 {
		t.Fatalf("issue-pickup ran %d times, want 1", got)
	}
	if got := runner.count(state.StageFeaturePlanning); got != 0 {
		t.Fatalf("feature-planning was dispatched %d time(s) — a stage with no composable SKILL.md "+
			"has no prompt to dispatch", got)
	}
	if runner.runtime == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState — issue-pickup did not dispatch")
	}

	snap := runner.runtime.Snapshot()
	if snap.Stage != state.StageFeaturePlanning {
		t.Fatalf("snap.Stage = %q, want %q (the refused stage)", snap.Stage, state.StageFeaturePlanning)
	}

	gotReason, ok := snap.StageErrors[string(snap.Stage)]
	if !ok || gotReason == "" {
		t.Fatalf("snap.StageErrors[string(snap.Stage)] = (%q, ok=%v), want the skill-render refusal reason — "+
			"pre-fix the render error survived only as a log line", gotReason, ok)
	}
	for _, want := range []string{
		"skill render failed",
		string(state.StageFeaturePlanning),
		"SKILL.md",
	} {
		if !strings.Contains(gotReason, want) {
			t.Errorf("stage error = %q, want it to contain %q", gotReason, want)
		}
	}
	// Distinguishability (the #620 requirement): the two refusals are different
	// operator actions — "look upstream, the handoff is missing" versus "look at
	// the skills tree, this stage's skill did not compose" — and must never
	// share a message.
	if strings.Contains(gotReason, "missing prerequisite") {
		t.Errorf("stage error = %q must not reuse the prerequisite refusal's wording — one message for "+
			"two causes is the defect this fix removes", gotReason)
	}

	rec := recordForIssue(t, root, item.Number)
	if rec.Outcome != "failed" {
		t.Errorf("rec.Outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind == "" {
		t.Fatalf("rec.TerminalFailureKind = %q — a run refused at the skill render must not be booked "+
			"terminal with an empty cause", rec.TerminalFailureKind)
	}
	if rec.TerminalFailureKind != TerminalKindValidationError {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindValidationError)
	}
	stageDetail, ok := rec.Stages[string(state.StageFeaturePlanning)]
	if !ok {
		t.Fatalf("no feature-planning stage detail on the run record — the refused stage left no trace")
	}
	if !strings.Contains(stageDetail.Error, "skill render failed") {
		t.Errorf("record stage error = %q, want it to name the render failure", stageDetail.Error)
	}
}
