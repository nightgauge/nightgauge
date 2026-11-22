// Skip parity between the corpus's two writers (#304).
//
// The learning corpus is one undiscriminated file read by consumers that cannot
// tell which path produced a row, so a terminal state that records NOTHING on
// one writer and a row on the other puts two meanings in `success` and
// `costUsd`. The extension writer skips a #305 blocked-dependency deferral and a
// #3296 network_unavailable failure (internal/ipc/outcome_record.go); the
// scheduler skipped only the second, and the docs claimed both.
//
// What the missing skip did to the loop verdicts: a deferral is a failed run at
// ~$0 that ran no AI stage. Five of them in the recent half of a 20-run corpus
// flip cost-optimization to `closing` ("cost per run decreasing") and
// reliability to `degrading` ("failure rate increasing") — both strings are
// exported per-loop by the continuous-improvement skill and drive proposal
// generation.
package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// terminalSkipScheduler builds a scheduler that WILL record outcomes, whose
// first stage always fails with errText. Backtracks and escalations are
// disabled so the run reaches its terminal defer on the first failure.
func terminalSkipScheduler(t *testing.T, root, errText string) *Scheduler {
	t.Helper()
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
	return &Scheduler{
		trustGate:    allWritersGate(),
		repoRunning:  make(map[string]int),
		mergeLocks:   make(map[string]*sync.Mutex),
		retryEngine:  NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine: NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:  NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:     newMockIssueSvc(),
		execMgr:      execution.NewManager(root, nil),
		stageRunner: newStallStageRunner(
			map[state.PipelineStage]int{state.StageIssuePickup: 99}, errText),
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		recordOutcomes: true,
	}
}

func runTerminalSkipPipeline(t *testing.T, issue int, errText string) (root string) {
	t.Helper()
	root = gitWorkspace(t)
	s := terminalSkipScheduler(t, root, errText)
	s.runPipeline(context.Background(), types.BoardItem{
		Number: issue,
		Repo:   "acme/widget",
		ID:     "item-id",
		Title:  "terminal skip parity",
		Labels: []string{"type:feature"},
	})
	return root
}

func corpusRowCount(t *testing.T, root string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(layouttest.PipelineDir(t, root), "history", "outcomes.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read outcomes.jsonl: %v", err)
	}
	n := 0
	for _, line := range splitNonEmptyLines(string(data)) {
		if line != "" {
			n++
		}
	}
	return n
}

func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if line := s[start:i]; line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if line := s[start:]; line != "" {
		out = append(out, line)
	}
	return out
}

// A blocked-dependency deferral records NOTHING on the autonomous path, exactly
// as it already did on the extension path.
//
// Serial: swaps package var reconcileExecGh (via stubReconcileGhUnreachable).
func TestRunPipeline_BlockedDependencyDeferralRecordsNoOutcome(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := runTerminalSkipPipeline(t, 305,
		"[blocked-dependency] issue #305 dispatched while blockedBy #300 is still open — deferring")

	if got := corpusRowCount(t, root); got != 0 {
		t.Errorf("corpus has %d rows after a blocked-dependency deferral, want 0 — a deferral is not a failure and ran no AI stage, so booking it success:false at ~$0 credits the cost loop and blames the reliability loop for a run that never executed",
			got)
	}
}

// The network_unavailable skip (#3296) stays.
//
// Serial: swaps package var reconcileExecGh (via stubReconcileGhUnreachable).
func TestRunPipeline_NetworkUnavailableRecordsNoOutcome(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := runTerminalSkipPipeline(t, 3296, ErrNetworkUnavailable.Error())

	if got := corpusRowCount(t, root); got != 0 {
		t.Errorf("corpus has %d rows after a network_unavailable failure, want 0", got)
	}
}

// CONTROL: an ordinary failure still records. Without this the two tests above
// would pass just as well against a writer that was switched off entirely.
//
// Serial: swaps package var reconcileExecGh (via stubReconcileGhUnreachable).
func TestRunPipeline_OrdinaryFailureStillRecordsAnOutcome(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := runTerminalSkipPipeline(t, 999, "stage exited 1: the model produced no usable output")

	if got := corpusRowCount(t, root); got != 1 {
		t.Errorf("corpus has %d rows after an ordinary failure, want 1 — the skips must be scoped to the two non-signal terminal kinds, not disable recording",
			got)
	}
}

// fixedVerdictGate fails its stage with one structured terminal kind.
type fixedVerdictGate struct{ kind, reason string }

func (fixedVerdictGate) Name() string { return "fixed-verdict" }

func (g fixedVerdictGate) Verify(context.Context, int, string) gates.GateResult {
	return gates.GateResult{GateName: g.Name(), Kind: gates.KindFail, Reason: g.reason, TerminalKind: g.kind}
}

// publicationVerdictRun is a re-run scoped to a remainder that a publication
// verdict stops at planning: not_pipeline_actionable, and no partial no-op.
func publicationVerdictRun(t *testing.T) (*Scheduler, string) {
	t.Helper()
	s, root := partialRerunScheduler(t, &partialRerunRunner{}, false)
	s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StageFeaturePlanning: fixedVerdictGate{TerminalKindNotPipelineActionable,
			"[not-pipeline-actionable] publication-boundary verdict"},
	})
	return s, root
}

// claimsFilesRunner's feature-dev reports a changed file and leaves none on
// disk, which is what the gate stamps dev_produced_no_changes.
type claimsFilesRunner struct{ *partialRerunRunner }

func (r claimsFilesRunner) RunStage(ctx context.Context, p StageRunParams) (*StageRunResult, error) {
	res, err := r.partialRerunRunner.RunStage(ctx, p)
	if p.Stage != state.StageFeatureDev || err != nil {
		return res, err
	}
	data, err := os.ReadFile(p.OutputFile)
	if err != nil {
		return nil, err
	}
	var dev map[string]any
	if err := json.Unmarshal(data, &dev); err != nil {
		return nil, err
	}
	dev["files_changed"] = map[string]any{"created": []string{}, "modified": []string{"internal/x/x.go"}, "deleted": []string{}}
	if data, err = json.Marshal(dev); err != nil {
		return nil, err
	}
	return res, os.WriteFile(p.OutputFile, data, 0o644)
}

// emptyDiffPartialRun is a partial re-run whose feature-dev gate stamps the
// empty diff dev_produced_no_changes before the run is booked a no-op.
func emptyDiffPartialRun(t *testing.T) (*Scheduler, string) {
	t.Helper()
	s, root := partialRerunScheduler(t, claimsFilesRunner{&partialRerunRunner{}}, true)
	s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}
	return s, root
}

// #3487: both the booking and nothing but the booking makes a run a no-op:
// cancelled/no_op, a success-shaped pipeline_done, a cancelled latch and no
// corpus row. Driven through runPipeline's real writers.
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestRunPipeline_PartialNoOpIsNotAFailure(t *testing.T) {
	const issue = 3487
	const marker = "[not-pipeline-actionable]"
	crash := func(errText string) func(*testing.T) (*Scheduler, string) {
		return func(t *testing.T) (*Scheduler, string) {
			stubReconcileGhUnreachable(t)
			root := gitWorkspace(t)
			return terminalSkipScheduler(t, root, errText), root
		}
	}
	// beforePRCreate reaches the no-diff stop before pr-create.
	beforePRCreate := func(scoped bool) func(*testing.T) (*Scheduler, string) {
		return func(t *testing.T) (*Scheduler, string) {
			runner := &partialRerunRunner{}
			s, root := partialRerunScheduler(t, runner, false)
			s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
				return hooks.PartialDeliveryStatus{Criteria: []string{}}, nil
			}
			if scoped {
				s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
				// feature-dev leaves a diff, so the run passes its own stop, and
				// feature-validate reverts it.
				var made string
				runner.onStage = func(p StageRunParams) {
					switch p.Stage {
					case state.StageFeatureDev:
						wt := runWorktreeFor(t, root, "issue-3487")
						if wt == "" {
							t.Error("feature-dev ran with no run worktree: the fixture cannot leave a diff")
							return
						}
						made = filepath.Join(wt, "change_test.go")
						if err := os.WriteFile(made, []byte("package x\n"), 0o644); err != nil {
							t.Error(err)
						}
					case state.StageFeatureValidate:
						if err := os.Remove(made); err != nil {
							t.Error(err)
						}
					}
				}
			}
			s.issueSvc.(*mockIssueSvc).addIssue("acme", "widget", issue, &types.Issue{
				NodeID: "I_3487", Number: issue, State: "OPEN", Body: "- [ ] " + partialRerunCriterion + "\n",
			})
			s.noDiffCommenter = &recordingCommenter{}
			return s, root
		}
	}

	// atPRCreateSettlement is parked by pr-create's own no-diff settlement.
	atPRCreateSettlement := func(t *testing.T) (*Scheduler, string) {
		s, root, _ := settlementRun(t, true, &scriptedPRCreateRunner{
			attempt: func(int) pmstages.PRCreateResult { return noDiffPunt() },
		})
		return s, root
	}

	for _, tc := range []struct {
		name  string
		build func(*testing.T) (*Scheduler, string)
		noOp  bool
		kind  string // asserted when set
	}{
		{"partial no-op over an empty-diff gate", emptyDiffPartialRun, true, TerminalKindNotPipelineActionable},
		{"partial no-diff stop before pr-create", beforePRCreate(true), true, TerminalKindNotPipelineActionable},
		{"partial no-diff settlement in pr-create", atPRCreateSettlement, true, TerminalKindNotPipelineActionable},
		{"unscoped no-diff stop before pr-create", beforePRCreate(false), false, TerminalKindNotPipelineActionable},
		{"publication verdict on a scoped run", publicationVerdictRun, false, ""},
		{"first-run gate no-op", crash("premature turn end: stage exited 0 with no state change"), false, ""},
		{"crash", crash("stage exited 1: the model produced no usable output"), false, ""},
		{"crash quoting the marker", crash("stage exited 1: agent said " + marker), false, ""},
		{"text that opens with the marker", crash(marker + " nothing to build"), false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, root := tc.build(t)
			s.recordOutcomes = true
			var mu sync.Mutex
			var done []bool
			s.telemetryEnabled = true
			s.telemetrySvc = &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
				if e.EventType == "pipeline_done" && e.Success != nil {
					mu.Lock()
					done = append(done, *e.Success)
					mu.Unlock()
				}
			}}
			var snap *state.RuntimeState
			s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })
			_, kind := s.runPipeline(context.Background(), types.BoardItem{
				Number: issue, Repo: "acme/widget", ID: "item-id", NodeID: "I_3487", Title: "no-op", Labels: []string{"type:feature"},
			})

			if tc.kind != "" && kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if snap == nil || snap.PartialNoOp != tc.noOp {
				t.Fatalf("booking flag != %v (snapshot %v)", tc.noOp, snap != nil)
			}
			outcome, outcomeType, rows := "failed", "", 1
			if tc.noOp {
				outcome, outcomeType, rows = "cancelled", OutcomeTypeNoOp, 0
			}
			mu.Lock()
			defer mu.Unlock()
			if len(done) != 1 || done[0] != tc.noOp {
				t.Errorf("pipeline_done success = %v, want %v", done, tc.noOp)
			}
			if got := corpusRowCount(t, root); got != rows {
				t.Errorf("corpus rows = %d, want %d", got, rows)
			}
			files, err := filepath.Glob(filepath.Join(layouttest.PipelineDir(t, root), "history", "20*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("history files = %v (%v)", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var rec state.V2RunRecord
			if err := json.Unmarshal([]byte(splitNonEmptyLines(string(data))[0]), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != outcome || (tc.noOp && rec.OutcomeType != outcomeType) || (!tc.noOp && rec.OutcomeType == OutcomeTypeNoOp) {
				t.Errorf("record = %s/%s, want %s/%s", rec.Outcome, rec.OutcomeType, outcome, outcomeType)
			}
			if got := schedulerTerminalOutcome(false, kind, snap.PartialNoOp); got != outcome {
				t.Errorf("run state = %q, want %q", got, outcome)
			}
		})
	}
}
