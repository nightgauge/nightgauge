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

	"github.com/nightgauge/nightgauge/internal/gittest"
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

// allRefusalStageSkills is the full set of stage skill directories a run needs
// to reach pr-merge without a render refusal. Tests that want a render refusal
// omit exactly one entry.
var allRefusalStageSkills = []string{
	"nightgauge-issue-pickup",
	"nightgauge-feature-planning",
	"nightgauge-feature-dev",
	"nightgauge-feature-validate",
	"nightgauge-pr-create",
	"nightgauge-pr-merge",
}

// refusalGit runs a git command in dir and fails the test on error.
func refusalGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gittest.Command(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// refusalTrackedAtHEAD reports whether path (repo-relative, slash-separated) is
// in the HEAD tree — i.e. whether the recovery commit actually captured it.
func refusalTrackedAtHEAD(t *testing.T, dir, path string) bool {
	t.Helper()
	for _, line := range strings.Split(refusalGit(t, dir, "ls-tree", "-r", "--name-only", "HEAD"), "\n") {
		if strings.TrimSpace(line) == path {
			return true
		}
	}
	return false
}

// seedRefusalRepo builds a git-backed workspace whose only uncommitted content
// is what the caller asks for: every named stage skill is written AND
// committed, so `git status` is clean at the moment the run starts.
//
// Committing the skills matters. An untracked SKILL.md is "blocking" work by
// reclaim.ClassifyStatus's reckoning (it is not the pipeline's own bookkeeping
// exhaust), so a test that left them untracked would see the rescue fire on
// scaffolding it created itself and would pass for the wrong reason.
func seedRefusalRepo(t *testing.T, root string, stageSkills []string) {
	t.Helper()
	gitInitRepo(t, root)
	for _, dir := range stageSkills {
		writeSkillFile(t, root, dir)
	}
	refusalGit(t, root, "add", "-A")
	refusalGit(t, root, "commit", "-m", "chore: seed stage skills")
}

// seedUncommittedDevWork writes the deliverable shape feature-dev hands off:
// implementation on disk, deliberately NOT committed. feature-dev does not
// commit (AGENTS.md #1608) — it verifies what it changed and hands off to
// feature-validate, which is the stage that commits and pushes.
func seedUncommittedDevWork(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := "src/feature.go"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)),
		[]byte("package src\n\n// The whole feature-dev implementation, uncommitted.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// dropStageContextAfter returns a telemetry double that deletes a stage's
// output context the moment that stage completes — after #2870's post-stage
// output validation and before the NEXT stage's prerequisite gate reads it,
// which is the only window the prerequisite refusal is reachable from.
func dropStageContextAfter(runner *refusalCapturingStageRunner, stage state.PipelineStage) *hookTelemetry {
	return &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
		if e.EventType != "stage_completed" || e.Stage != string(stage) {
			return
		}
		if path := runner.outputFile(stage); path != "" {
			_ = os.Remove(path)
		}
	}}
}

// assertRefusalRescuedTheWork is the shared post-condition for both #620
// refusal sites when the worktree held uncommitted work.
//
// It pins the composite the #3365 incident and the #3542 recovery exist to
// prevent, and which setting a terminal kind at these sites had silently
// switched back on: the defer's rescue is gated on `terminalFailureKind == ""`,
// so a site that books a kind and returns disables it — so the work was
// stranded in the worktree AND the board went back to Ready for a re-dispatch
// that would redo it from scratch in a fresh worktree.
//
// #875 SPLIT THE TWO FACTS THIS USED TO CONFLATE. Before it, the only way to
// keep the board-revert protection was to RENAME the run's failure
// worktree_uncommitted, because skipBoardRevert was keyed on the terminal kind
// — which is how a run that could not compose its SKILL.md came to file its
// post-mortem under a hygiene condition that had nothing to do with why it
// stopped. skipBoardRevert now reads the run's `workRecovered` flag, so:
//
//   - the terminal KIND is the first cause (assertion 4) — what the record,
//     docs/OUTCOME_RECORDING.md and the retro path consume;
//   - the recovery MARKER stays in the stage error prose (assertions 2 and 3)
//     — what the autonomous path re-derives "did the work survive" from, and
//     what suppresses the LifetimeIssueFailures increment;
//   - the board-revert skip reads neither, and is unconditional on a rescue.
//
// The revert itself is still not directly observable here (skipBoardRevert
// lives inside the #257 content-pinned fence, and this fixture leaves
// s.stateSvc nil), which is exactly why it must no longer be inferred from the
// kind.
func assertRefusalRescuedTheWork(t *testing.T, root string, runner *refusalCapturingStageRunner,
	refusedStage state.PipelineStage, commitsBefore int, workFile string, wantReasonFragments []string) {
	t.Helper()

	if runner.runtime == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState")
	}
	snap := runner.runtime.Snapshot()
	if snap.Stage != refusedStage {
		t.Fatalf("snap.Stage = %q, want %q (the refused stage)", snap.Stage, refusedStage)
	}

	// 1. The work is in a commit, not stranded on disk.
	subjects := gitLog(t, root)
	if len(subjects) != commitsBefore+1 {
		t.Fatalf("commit count %d -> %d, want exactly one recovery commit — the uncommitted "+
			"implementation was left in the worktree and the re-dispatch would redo it. Log: %v",
			commitsBefore, len(subjects), subjects)
	}
	if !strings.Contains(subjects[0], "[auto-recovery]") {
		t.Errorf("newest commit subject = %q, want the [auto-recovery] marker", subjects[0])
	}
	if !refusalTrackedAtHEAD(t, root, workFile) {
		t.Errorf("%s is not in the HEAD tree — the rescue committed something, but not the work", workFile)
	}

	// 2. The refusal reason survived the rescue. The defer's own copy REPLACES
	//    the stage error with a bare recovery marker; prefixing keeps both.
	gotReason, ok := snap.StageErrors[string(snap.Stage)]
	if !ok || gotReason == "" {
		t.Fatalf("snap.StageErrors[%q] = (%q, ok=%v), want the refusal reason", snap.Stage, gotReason, ok)
	}
	for _, want := range append([]string{TerminalKindWorktreeUncommitted}, wantReasonFragments...) {
		if !strings.Contains(gotReason, want) {
			t.Errorf("stage error = %q, want it to contain %q", gotReason, want)
		}
	}

	// 3. The RECOVERABILITY marker still survives prose re-derivation.
	//    internal/terminalkind/table.json orders the worktree-uncommitted rule
	//    ahead of validation-error, so autonomous.go's onPipelineComplete
	//    wrapper and NotifyComplete's defense-in-depth re-classify still see
	//    "the work survived" and still skip the LifetimeIssueFailures
	//    increment. #875 changed which fact the KIND carries, not this one.
	if got := ClassifyTerminalKind(gotReason); got != TerminalKindWorktreeUncommitted {
		t.Errorf("ClassifyTerminalKind(%q) = %q, want %q — the recovered run must not read as a "+
			"plain validation_error to the readers that re-derive recoverability from text",
			gotReason, got, TerminalKindWorktreeUncommitted)
	}

	// 4. The recorded kind is the FIRST CAUSE — the refusal that stopped the
	//    run — not the condition the rescue happened to find on its way out
	//    (#875). This is the record docs/OUTCOME_RECORDING.md and the retro
	//    path read; a downstream symptom booked here is corpus poisoning.
	rec := recordForIssue(t, root, 620)
	if rec.TerminalFailureKind == TerminalKindWorktreeUncommitted {
		t.Errorf("rec.TerminalFailureKind = %q — the rescue overwrote the cause again. The "+
			"uncommitted work is not why this run could not proceed; the refusal above is",
			rec.TerminalFailureKind)
	}
	if rec.TerminalFailureKind != TerminalKindValidationError {
		t.Errorf("rec.TerminalFailureKind = %q, want %q (the refusal's own kind)",
			rec.TerminalFailureKind, TerminalKindValidationError)
	}
}

// TestPrerequisiteMissingRefusal_RescuesUncommittedWork pins the #3542 rescue
// at the prerequisite gate.
//
// The refusal lands on feature-validate — the stage immediately after
// feature-dev, which hands off without committing — so the run reaches the gate
// with a complete implementation uncommitted in the worktree. Before this fix
// the site set terminalFailureKind and returned, which is exactly the condition
// that switches the terminal defer's rescue off.
func TestPrerequisiteMissingRefusal_RescuesUncommittedWork(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)
	workFile := seedUncommittedDevWork(t, root)
	commitsBefore := len(gitLog(t, root))

	runner := newRefusalCapturingStageRunner()
	s := newRefusalScheduler(root, runner)
	s.telemetrySvc = dropStageContextAfter(runner, state.StageFeatureDev)
	s.telemetryEnabled = true

	item := types.BoardItem{Number: 620, Repo: "nightgauge/nightgauge", ID: "item-620"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageFeatureValidate); got != 0 {
		t.Fatalf("feature-validate was dispatched %d time(s) — the prerequisite gate refuses BEFORE dispatch", got)
	}
	assertRefusalRescuedTheWork(t, root, runner, state.StageFeatureValidate, commitsBefore, workFile,
		[]string{"missing prerequisite", string(state.StageFeatureValidate)})
}

// TestSkillRenderFailureRefusal_RescuesUncommittedWork pins the #3542 rescue at
// the skill-render site — the failure scenario in full.
//
// feature-dev finishes and hands off without committing (AGENTS.md #1608).
// feature-validate's SKILL.md will not compose (here: absent; in production a
// broken overlay, a missing skills root, or an adapter mismatch does the same),
// so the stage is refused before dispatch with the entire implementation
// uncommitted on disk.
func TestSkillRenderFailureRefusal_RescuesUncommittedWork(t *testing.T) {
	root := t.TempDir()
	// Every stage skill EXCEPT feature-validate's.
	var withoutValidate []string
	for _, dir := range allRefusalStageSkills {
		if dir == "nightgauge-feature-validate" {
			continue
		}
		withoutValidate = append(withoutValidate, dir)
	}
	seedRefusalRepo(t, root, withoutValidate)
	workFile := seedUncommittedDevWork(t, root)
	commitsBefore := len(gitLog(t, root))

	runner := newRefusalCapturingStageRunner()
	s := newRefusalScheduler(root, runner)

	item := types.BoardItem{Number: 620, Repo: "nightgauge/nightgauge", ID: "item-620"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageFeatureValidate); got != 0 {
		t.Fatalf("feature-validate was dispatched %d time(s) — a stage with no composable SKILL.md "+
			"has no prompt to dispatch", got)
	}
	assertRefusalRescuedTheWork(t, root, runner, state.StageFeatureValidate, commitsBefore, workFile,
		[]string{"skill render failed", string(state.StageFeatureValidate)})
}

// TestPreDispatchRefusal_CleanWorktreeStaysValidationError is the other half of
// the A/B: identical setup, minus the uncommitted work.
//
// With nothing to rescue there is nothing to reclassify, so the refusal keeps
// validation_error — the kind whose corpus row names this exact producer — and
// the board revert that sends a genuinely empty worktree back for re-dispatch
// is correct. This is what stops the rescue from being wired in as an
// unconditional reclassification that would launder every pre-dispatch refusal
// into a recoverable non-event.
func TestPreDispatchRefusal_CleanWorktreeStaysValidationError(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)
	commitsBefore := len(gitLog(t, root))

	runner := newRefusalCapturingStageRunner()
	s := newRefusalScheduler(root, runner)
	s.telemetrySvc = dropStageContextAfter(runner, state.StageFeatureDev)
	s.telemetryEnabled = true

	item := types.BoardItem{Number: 620, Repo: "nightgauge/nightgauge", ID: "item-620"}
	s.runPipeline(context.Background(), item)

	if got := len(gitLog(t, root)); got != commitsBefore {
		t.Errorf("commit count %d -> %d — the rescue must not manufacture a commit on a clean worktree",
			commitsBefore, got)
	}
	rec := recordForIssue(t, root, item.Number)
	if rec.TerminalFailureKind != TerminalKindValidationError {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindValidationError)
	}
	if runner.runtime == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState")
	}
	gotReason := runner.runtime.Snapshot().StageErrors[string(state.StageFeatureValidate)]
	if strings.Contains(gotReason, TerminalKindWorktreeUncommitted) {
		t.Errorf("stage error = %q carries the recovery marker with nothing recovered", gotReason)
	}
	if !strings.Contains(gotReason, "missing prerequisite") {
		t.Errorf("stage error = %q, want it to name the missing prerequisite", gotReason)
	}
}
