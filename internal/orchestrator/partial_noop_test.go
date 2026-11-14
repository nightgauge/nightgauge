package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// #3163: a zero-change feature-dev on a partial-delivery re-run is never a
// failure, and an operator-revised plan is not lost to one model's reading of
// the remainder.

func TestDecidePartialNoOp(t *testing.T) {
	scope := &state.PartialRerun{PR: 1749, Criteria: []string{"A is handled"}}
	revised := operatorRevision{Revised: true, FilesToModify: []string{"a.go"}}
	for _, tc := range []struct {
		name     string
		scope    *state.PartialRerun
		rev      operatorRevision
		dispute  string
		relapped bool
		want     partialNoOpAction
		detail   string
	}{
		{name: "unscoped run keeps the gate verdict", scope: nil, rev: revised, want: partialNoOpNotScoped},
		{name: "#1750: right verdict, no revision", scope: scope, want: partialNoOpBook, detail: "wrote no changes"},
		{name: "#1051: ignored operator revision earns a lap", scope: scope, rev: revised, want: partialNoOpRelap},
		{name: "#1051 second refusal books the no-op", scope: scope, rev: revised, relapped: true,
			want: partialNoOpBook, detail: "twice on an operator-revised plan"},
		{name: "named dispute ends as the dispute", scope: scope, rev: revised, dispute: "Task 2: a.go has no such hook",
			want: partialNoOpDispute, detail: "Task 2: a.go has no such hook"},
		{name: "revision naming no files is not binding", scope: scope, rev: operatorRevision{Revised: true},
			want: partialNoOpBook},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decidePartialNoOp(1051, tc.scope, tc.rev, tc.dispute, tc.relapped)
			if d.Action != tc.want {
				t.Fatalf("action = %v, want %v", d.Action, tc.want)
			}
			if tc.want == partialNoOpBook || tc.want == partialNoOpDispute {
				if ClassifyTerminalKind(d.Detail) != TerminalKindNotPipelineActionable {
					t.Errorf("detail classifies as %q, want not_pipeline_actionable: %s", ClassifyTerminalKind(d.Detail), d.Detail)
				}
				if !strings.Contains(d.Detail, tc.detail) {
					t.Errorf("detail %q lacks %q", d.Detail, tc.detail)
				}
			}
			if tc.want == partialNoOpRelap && !strings.Contains(d.Steer, "a.go") {
				t.Errorf("relap steer does not name the plan's files: %q", d.Steer)
			}
		})
	}
}

// operatorPlanRunner wraps partialRerunRunner: planning writes an
// operator-revised plan, and feature-dev runs devLap for each lap.
type operatorPlanRunner struct {
	*partialRerunRunner
	root   string
	mu     sync.Mutex
	devRun int
	devLap func(lap int, p StageRunParams)
}

func (r *operatorPlanRunner) RunStage(ctx context.Context, p StageRunParams) (*StageRunResult, error) {
	res, err := r.partialRerunRunner.RunStage(ctx, p)
	switch p.Stage {
	case state.StageFeaturePlanning:
		var plan map[string]any
		data, _ := os.ReadFile(p.OutputFile)
		_ = json.Unmarshal(data, &plan)
		plan["revision_reasons"] = []string{operatorSteerOriginMarker + ": add the integration test"}
		plan["files_to_modify"] = []string{"internal/x/x.go"}
		plan["files_to_create"] = []string{"internal/x/x_integration_test.go"}
		out, _ := json.Marshal(plan)
		_ = os.WriteFile(p.OutputFile, out, 0o644)
	case state.StageFeatureDev:
		r.mu.Lock()
		r.devRun++
		lap := r.devRun
		r.mu.Unlock()
		if r.devLap != nil {
			r.devLap(lap, p)
		}
	}
	return res, err
}

func (r *operatorPlanRunner) devPrompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if c.Stage == state.StageFeatureDev {
			out = append(out, c.Prompt)
		}
	}
	return out
}

// notLevelBranch makes the branch-vs-base comparison decline to stop the run,
// as it did for platform#1750: the issue context names the base as the
// branch, which BranchNoDiff reports as not level.
func notLevelBranch(t *testing.T, root string, issue int) {
	t.Helper()
	path, err := stagecontext.ContextPath(root, issue, "issue")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"branch": "main", "base_branch": "main"})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeOperatorSteerFeedback(t *testing.T, root string, issue int) {
	t.Helper()
	if err := WriteOperatorSteer(root, issue, "Add the integration test the PR skipped.", "feature-planning"); err != nil {
		t.Fatal(err)
	}
}

// AC 4 (platform#1750): a scoped re-run whose feature-dev records zero
// changes, on a branch the no-diff stop does not end, is booked
// not_pipeline_actionable — not premature turn end.
func TestPartialNoOp_NotLevelZeroChangesIsNotAFailure(t *testing.T) {
	inner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, inner, true)
	inner.onStage = func(p StageRunParams) {
		if p.Stage == state.StageFeatureDev {
			notLevelBranch(t, root, p.IssueNumber)
		}
	}
	s.partialDeliveryRead = pendingRemainder(1749, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	ok, kind := s.runPipeline(context.Background(), types.BoardItem{Number: 1750, Repo: "acme/platform", ID: "item-1750", NodeID: "I_1750"})

	if ok {
		t.Fatal("run reported success")
	}
	if kind != TerminalKindNotPipelineActionable {
		t.Fatalf("terminal kind = %q, want %q", kind, TerminalKindNotPipelineActionable)
	}
	if ranStage(inner, state.StageFeatureValidate) {
		t.Fatal("feature-validate ran after a zero-change feature-dev")
	}
	detail := snap.StageErrors[string(state.StageFeatureDev)]
	if strings.Contains(detail, "premature turn end") || !strings.Contains(detail, "[#3163]") {
		t.Fatalf("feature-dev error = %q, want the #3163 no-op detail", detail)
	}
}

// AC 5 (the #1051 shape): an operator-revised plan, every
// pre-revision criterion already met, and a feature-dev that writes nothing on
// its first lap. The steer reaches feature-dev's prompt (AC 3), the lap is
// re-dispatched once with the rule restated, and the run proceeds with the
// change the second lap makes — never skill-no-op.
func TestPartialNoOp_1051OperatorRevisionEndsInAChange(t *testing.T) {
	inner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, inner, true)
	runner := &operatorPlanRunner{partialRerunRunner: inner, root: root}
	s.stageRunner = runner
	writeOperatorSteerFeedback(t, root, 1051)
	runner.devLap = func(lap int, p StageRunParams) {
		if lap == 1 {
			notLevelBranch(t, root, p.IssueNumber)
			return
		}
		// Lap 2 follows the plan: a real file and a dev context naming it.
		wd := p.WorktreePath
		if wd == "" {
			wd = root
		}
		file := filepath.Join(wd, "internal", "x", "x_integration_test.go")
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		_ = os.WriteFile(file, []byte("package x\n"), 0o644)
		data, _ := json.Marshal(map[string]any{
			"schema_version":     "1.0",
			"issue_number":       p.IssueNumber,
			"files_changed":      map[string]any{"created": []string{"internal/x/x_integration_test.go"}, "modified": []string{}, "deleted": []string{}},
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"created_at":         "2026-11-14T00:00:00Z",
		})
		_ = os.WriteFile(p.OutputFile, data, 0o644)
	}
	s.partialDeliveryRead = pendingRemainder(1050, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}

	s.runPipeline(context.Background(), types.BoardItem{Number: 1051, Repo: "acme/dashboard", ID: "item-1051", NodeID: "I_1051"})

	prompts := runner.devPrompts()
	if len(prompts) != 2 {
		t.Fatalf("feature-dev ran %d time(s), want 2 (one re-dispatch)", len(prompts))
	}
	for i, p := range prompts {
		if !strings.Contains(p, operatorRevisionPromptHeading) || !strings.Contains(p, "Add the integration test the PR skipped.") {
			t.Errorf("lap %d prompt lacks the operator block and steer", i+1)
		}
		if !strings.Contains(p, "internal/x/x.go") {
			t.Errorf("lap %d prompt does not name the plan's files", i+1)
		}
	}
	if !strings.Contains(prompts[1], "Operator-Revised Plan Ignored") {
		t.Error("the re-dispatched lap does not restate the rule")
	}
	if !ranStage(inner, state.StageFeatureValidate) {
		t.Fatal("the run did not proceed with the second lap's change")
	}
	planningPath, _ := stagecontext.ContextPath(root, 1051, "planning")
	data, _ := os.ReadFile(planningPath)
	if !strings.Contains(string(data), `"operator_steer"`) {
		t.Errorf("planning context has no operator_steer: %s", data)
	}
}

// AC 2 / AC 5 variant: a first lap that names the plan step it disputes ends
// as that dispute, with no second lap.
func TestPartialNoOp_1051NamedDisputeEndsAsDispute(t *testing.T) {
	inner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, inner, true)
	runner := &operatorPlanRunner{partialRerunRunner: inner, root: root}
	s.stageRunner = runner
	runner.devLap = func(_ int, p StageRunParams) {
		notLevelBranch(t, root, p.IssueNumber)
		data, _ := json.Marshal(map[string]any{
			"schema_version":     "1.0",
			"issue_number":       p.IssueNumber,
			"files_changed":      map[string]any{"created": []string{}, "modified": []string{}, "deleted": []string{}},
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"feedback": []map[string]any{{
				"signal_type": "PLAN_REVISION_NEEDED", "emitted_by_stage": "feature-dev",
				"backtrack_target_stage": "feature-planning", "severity": "blocking",
				"rationale": "plan step is wrong", "evidence": []string{"Task 1: internal/x/x.go has no Handle func"},
			}},
			"created_at": "2026-11-14T00:00:00Z",
		})
		_ = os.WriteFile(p.OutputFile, data, 0o644)
	}
	s.partialDeliveryRead = pendingRemainder(1050, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	_, kind := s.runPipeline(context.Background(), types.BoardItem{Number: 1052, Repo: "acme/dashboard", ID: "item-1052", NodeID: "I_1052"})

	if n := len(runner.devPrompts()); n != 1 {
		t.Fatalf("feature-dev ran %d time(s), want 1", n)
	}
	if kind != TerminalKindNotPipelineActionable {
		t.Fatalf("terminal kind = %q, want %q", kind, TerminalKindNotPipelineActionable)
	}
	if detail := snap.StageErrors[string(state.StageFeatureDev)]; !strings.Contains(detail, "Task 1: internal/x/x.go has no Handle func") {
		t.Fatalf("feature-dev error = %q, want the named dispute", detail)
	}
}

// AC 4 booking: the no-op detail feeds neither the cascade breaker nor the
// per-issue lifetime cap. Two of them tripped the breaker in session 99.
func TestPartialNoOp_BookingSkipsCascadeAndLifetimeCap(t *testing.T) {
	stubReconcileGhUnreachable(t)
	as := newAutonomousForCascadeTest(t, 2, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}
	scope := &state.PartialRerun{PR: 1, Criteria: []string{"x"}}
	for _, n := range []int{1750, 1051, 1052} {
		addRunning(as, "acme/repo", n, "partial re-run")
		d := decidePartialNoOp(n, scope, operatorRevision{}, "", false)
		as.onPipelineComplete("acme/repo", n, false, false, TerminalKindNotPipelineActionable, d.Detail, false)
		as.drainBackground()
		if got := as.state.LifetimeIssueFailures[fmt.Sprintf("acme/repo#%d", n)]; got != 0 {
			t.Errorf("#%d LifetimeIssueFailures = %d, want 0", n, got)
		}
	}
	if as.cascadeTracker.IsTripped() || as.state.Status == "safety_tripped" {
		t.Fatal("partial-rerun no-ops tripped the cascade breaker")
	}
	if got := as.cascadeTracker.CountInWindow(time.Now()); got != 0 {
		t.Fatalf("cascade window holds %d failure(s), want 0", got)
	}
}

// Review round 2: the usual zero-change case is a branch still level with its
// base. The operator-revision relap must still apply there (the level-branch
// stop must not pre-empt it), and a second lap that writes nothing must reach
// the gate's no-op path, not lap 1's dev context judged stale.
func TestPartialNoOp_LevelBranchRelapsThenBooksNoOp(t *testing.T) {
	inner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, inner, true)
	runner := &operatorPlanRunner{partialRerunRunner: inner, root: root}
	s.stageRunner = runner
	writeOperatorSteerFeedback(t, root, 1053)
	var lap1 []byte
	devFileAtStart := false
	inner.onStage = func(p StageRunParams) {
		if p.Stage == state.StageFeatureDev {
			_, err := os.Stat(p.OutputFile)
			devFileAtStart = err == nil
		}
	}
	runner.devLap = func(lap int, p StageRunParams) {
		if lap != 1 {
			// Lap 2 writes nothing: undo the stub runner's write, leaving
			// whatever was on disk when the lap started. Real laps are minutes
			// apart, so a lap 1 file still there is past the staleness slack.
			if !devFileAtStart {
				_ = os.Remove(p.OutputFile)
				return
			}
			_ = os.WriteFile(p.OutputFile, lap1, 0o644)
			old := time.Now().Add(-time.Hour)
			_ = os.Chtimes(p.OutputFile, old, old)
			return
		}
		data, _ := json.Marshal(map[string]any{
			"schema_version":     "1.0",
			"issue_number":       p.IssueNumber,
			"files_changed":      map[string]any{"created": []string{}, "modified": []string{}, "deleted": []string{}},
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"created_at":         "2026-11-14T00:00:00Z",
		})
		lap1 = data
		_ = os.WriteFile(p.OutputFile, data, 0o644)
	}
	s.partialDeliveryRead = pendingRemainder(1050, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	_, kind := s.runPipeline(context.Background(), types.BoardItem{Number: 1053, Repo: "acme/dashboard", ID: "item-1053", NodeID: "I_1053"})

	if n := len(runner.devPrompts()); n != 2 {
		t.Fatalf("feature-dev ran %d time(s) on a level branch, want 2 (one re-dispatch)", n)
	}
	if kind != TerminalKindNotPipelineActionable {
		t.Fatalf("terminal kind = %q, want %q", kind, TerminalKindNotPipelineActionable)
	}
	detail := snap.StageErrors[string(state.StageFeatureDev)]
	if strings.Contains(detail, "stale") || strings.Contains(detail, "premature turn end") {
		t.Fatalf("feature-dev error = %q, want a no-op ending", detail)
	}
}
