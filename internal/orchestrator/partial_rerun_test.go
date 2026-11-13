package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// The AC 4 scenario 1 remainder: a missing test file.
const partialRerunCriterion = "An integration test `internal/x/x_integration_test.go` exists"

// partialRerunRunner exits 0 on every stage, writes a context reporting no
// changed files, records each dispatch's stage and prompt, and runs onStage
// inside the dispatch.
type partialRerunRunner struct {
	mu      sync.Mutex
	calls   []StageRunParams
	onStage func(StageRunParams)
}

func (r *partialRerunRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	hook := r.onStage
	r.mu.Unlock()
	if hook != nil {
		hook(params)
	}
	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0o755)
		payload := map[string]any{
			"schema_version":     "1.0",
			"issue_number":       params.IssueNumber,
			"plan_file":          "plan.md",
			"approach":           "test",
			"files_changed":      map[string]any{"created": []string{}, "modified": []string{}, "deleted": []string{}},
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"validation_status":  "passed",
			"created_at":         "2026-11-10T00:00:00Z",
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0o644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, OutputTokens: 5}, nil
}

func (r *partialRerunRunner) prompt(stage state.PipelineStage) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.Stage == stage {
			return c.Prompt, true
		}
	}
	return "", false
}

// recordingCommenter records every comment it is asked to post.
type recordingCommenter struct {
	mu     sync.Mutex
	bodies []string
}

func (c *recordingCommenter) AddComment(_ context.Context, _ string, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
	return nil
}

func (c *recordingCommenter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func pendingRemainder(pr int, criteria ...string) func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Pending: true, PR: pr, Criteria: criteria, CommentAt: &at}, nil
	}
}

// partialRerunScheduler is the #202 fixture: a git workspace with committed
// skill fixtures, every stage's real context written by the runner, and a
// pr-create runner that always punts. gate wires feature-dev's real gate.
func partialRerunScheduler(t *testing.T, runner StageRunner, gate bool) (*Scheduler, string) {
	t.Helper()
	stubReconcileGhUnreachable(t)
	root := gitWorkspace(t)
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "fixture")
	s := &Scheduler{
		trustGate:      allWritersGate(),
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(root, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
	if gate {
		s.WithStageGates(map[state.PipelineStage]gates.StageGate{state.StageFeatureDev: gates.FeatureDevGate{}})
	}
	return s, root
}

func ranStage(r *partialRerunRunner, stage state.PipelineStage) bool {
	_, ok := r.prompt(stage)
	return ok
}

// AC 4 scenario 1 and AC 1: the remaining criterion is the run's scope. The
// runtime carries it, the planning context names it, and the planning and
// feature-dev prompts say the rest of the issue is already on main.
func TestPartialRerun_RemainderIsTheRunScope(t *testing.T) {
	runner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}

	var planningCtx map[string]any
	runner.onStage = func(p StageRunParams) {
		// planning-{N}.json as feature-dev reads it: after the splice.
		if p.Stage != state.StageFeatureDev {
			return
		}
		path, err := stagecontext.ContextPath(root, p.IssueNumber, "planning")
		if err != nil {
			t.Errorf("planning context path: %v", err)
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read planning context: %v", err)
			return
		}
		_ = json.Unmarshal(data, &planningCtx)
	}
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	s.runPipeline(context.Background(), types.BoardItem{Number: 3160, Repo: "nightgauge/test", ID: "item-3160", NodeID: "I_3160"})

	if snap == nil || snap.PartialRerun == nil || snap.PartialRerun.PR != 3141 ||
		len(snap.PartialRerun.Criteria) != 1 || snap.PartialRerun.Criteria[0] != partialRerunCriterion {
		t.Fatalf("runtime partial-rerun scope = %+v, want PR #3141 with the one remaining criterion", snap)
	}
	pd, ok := planningCtx["partial_delivery"].(map[string]any)
	if !ok {
		t.Fatalf("planning context has no partial_delivery field: %v", planningCtx)
	}
	if crit, _ := pd["criteria"].([]any); len(crit) != 1 || crit[0] != partialRerunCriterion {
		t.Fatalf("partial_delivery.criteria = %v, want [%q]", pd["criteria"], partialRerunCriterion)
	}
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		prompt, ok := runner.prompt(stage)
		if !ok {
			t.Fatalf("%s never ran", stage)
		}
		if !strings.Contains(prompt, partialRerunPromptHeading) || !strings.Contains(prompt, partialRerunCriterion) {
			t.Errorf("%s prompt does not carry the scope block and its criterion", stage)
		}
		if !strings.Contains(prompt, "already on `main`") {
			t.Errorf("%s prompt does not say the rest of the issue is already on main", stage)
		}
	}
}

// An unscoped run's planning and feature-dev prompts carry no scope block, so
// the cache prefix holds.
func TestPartialRerun_UnscopedPromptHasNoBlock(t *testing.T) {
	if got := partialRerunPromptBlock(state.StageFeatureDev, nil); got != "" {
		t.Fatalf("nil scope rendered %q, want \"\"", got)
	}
	scope := &state.PartialRerun{PR: 1, Criteria: []string{"x"}}
	if got := partialRerunPromptBlock(state.StageFeatureValidate, scope); got != "" {
		t.Fatalf("feature-validate got a scope block: %q", got)
	}

	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Criteria: []string{}}, nil
	}
	s.runPipeline(context.Background(), types.BoardItem{Number: 3161, Repo: "nightgauge/test", ID: "item-3161"})
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		if prompt, _ := runner.prompt(stage); strings.Contains(prompt, partialRerunPromptHeading) {
			t.Errorf("unscoped %s prompt carries the partial-rerun block", stage)
		}
	}
}

// AC 4 scenario 3 and AC 2: a partial re-run whose feature-dev leaves the
// branch level ends not_pipeline_actionable before feature-validate, with the
// remainder commented once and the issue left open.
func TestPartialRerun_NoDiffEndsBeforeValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate bool
	}{{"clean exit", false}, {"gate reports dev_produced_no_changes", true}} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &partialRerunRunner{}
			s, _ := partialRerunScheduler(t, runner, tc.gate)
			s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
			commenter := &recordingCommenter{}
			s.noDiffCommenter = commenter
			var ok bool
			s.OnPipelineComplete(func(_ string, _ int, _ *state.RuntimeState, success bool) { ok = success })

			_, terminalKind := s.runPipeline(context.Background(),
				types.BoardItem{Number: 3162, Repo: "nightgauge/test", ID: "item-3162", NodeID: "I_3162"})

			if !ranStage(runner, state.StageFeatureDev) {
				t.Fatal("feature-dev never ran; the fixture is wrong")
			}
			if ranStage(runner, state.StageFeatureValidate) {
				t.Fatal("feature-validate ran on a no-diff partial re-run")
			}
			if ok {
				t.Fatal("run reported success")
			}
			if terminalKind != TerminalKindNotPipelineActionable {
				t.Fatalf("terminal kind = %q, want %q", terminalKind, TerminalKindNotPipelineActionable)
			}
			if n := commenter.count(); n != 1 {
				t.Fatalf("posted %d comment(s), want 1", n)
			}
			if !strings.Contains(commenter.bodies[0], partialRerunCriterion) {
				t.Errorf("comment does not name the remaining criterion: %q", commenter.bodies[0])
			}
		})
	}
}

// The same level branch on an unscoped run is unchanged: with no gate it goes
// on to feature-validate.
func TestPartialRerun_UnscopedNoDiffStillValidates(t *testing.T) {
	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{}, errors.New("forge unreachable")
	}
	commenter := &recordingCommenter{}
	s.noDiffCommenter = commenter
	s.runPipeline(context.Background(), types.BoardItem{Number: 3163, Repo: "nightgauge/test", ID: "item-3163", NodeID: "I_3163"})
	if !ranStage(runner, state.StageFeatureValidate) {
		t.Fatal("an unscoped run stopped before feature-validate")
	}
	if commenter.count() != 0 {
		t.Fatalf("an unscoped run posted %d partial-rerun comment(s)", commenter.count())
	}
}

// A partial re-run whose feature-dev produced a change continues to
// feature-validate.
func TestPartialRerun_WithDiffValidates(t *testing.T) {
	runner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}
	runner.onStage = func(p StageRunParams) {
		if p.Stage != state.StageFeatureDev {
			return
		}
		wt := runWorktreeFor(t, root, "issue-3164")
		if wt == "" {
			t.Error("no run worktree to change")
			return
		}
		dir := filepath.Join(wt, "internal", "x")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "x_integration_test.go"), []byte("package x\n"), 0o644)
	}
	s.runPipeline(context.Background(), types.BoardItem{Number: 3164, Repo: "nightgauge/test", ID: "item-3164", NodeID: "I_3164"})
	if !ranStage(runner, state.StageFeatureValidate) {
		t.Fatal("a partial re-run with a diff stopped before feature-validate")
	}
}

// runWorktreeFor finds the run worktree whose path contains marker.
func runWorktreeFor(t *testing.T, root, marker string) string {
	t.Helper()
	out, err := gittest.Command(root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimPrefix(line, "worktree "); p != line && strings.Contains(p, marker) {
			return p
		}
	}
	return ""
}

// partialRemainderBody builds an issue body from criteria lines.
func partialRemainderBody(lines ...string) string {
	return "## Acceptance criteria\n\n" + strings.Join(lines, "\n") + "\n"
}

// partialPRBody renders the partial PR's body with the real renderer: met
// criteria under the met section, the rest under the unmet one.
func partialPRBody(met, unmet []string) string {
	var vs []pmstages.ACVerdict
	for i, m := range met {
		vs = append(vs, pmstages.ACVerdict{Index: i, Text: m, Status: pmstages.ACMet, Source: "diff", Evidence: "internal/x.go"})
	}
	for i, u := range unmet {
		vs = append(vs, pmstages.ACVerdict{Index: len(met) + i, Text: u, Status: pmstages.ACNotVerifiable, Source: "judge"})
	}
	return pmstages.RenderBody(pmstages.PRCreateSnapshot{IssueNumber: 3235, ACVerdicts: vs})
}

// partialRerunFixture wires a scheduler whose partial-delivery comment lists
// listed, whose issue body holds bodyLines, whose partial PR (opened at
// opened) has prBody, and whose body was last edited at edited.
func partialRerunFixture(listed, bodyLines []string, prBody string, opened time.Time, edited *time.Time) *Scheduler {
	return &Scheduler{
		trustGate:           allWritersGate(),
		partialDeliveryRead: pendingRemainder(3141, listed...),
		acReconcileBody: func(context.Context, types.BoardItem) (string, error) {
			return partialRemainderBody(bodyLines...), nil
		},
		issueBodyEditedAt: func(context.Context, types.BoardItem) (*time.Time, error) { return edited, nil },
		partialPRRead: func(context.Context, string, int) (partialPR, error) {
			return partialPR{CreatedAt: opened, Body: prBody}, nil
		},
	}
}

func unchecked(cs ...string) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = "- [ ] " + c
	}
	return out
}

func ptrTime(t time.Time) *time.Time { return &t }

var partialOpened = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// scopeOf runs scopePartialRerun and returns the brief.
func scopeOf(t *testing.T, s *Scheduler, ws string) []string {
	t.Helper()
	rt := &state.RuntimeState{}
	s.scopePartialRerun(context.Background(), rt, types.BoardItem{Number: 3235, Repo: "nightgauge/test"}, ws)
	scope := rt.PartialRerunScope()
	if scope == nil {
		return nil
	}
	return scope.Criteria
}

// AC3 case 1 (#3235): the comment lists 4 criteria, the body has 13 — the 4
// listed, 5 the partial PR delivered (prose, so undetectable) and 4 added
// after the PR. The brief is the 4 listed plus the 4 new.
func TestScopePartialRerun_AddsCriteriaNewSincePR(t *testing.T) {
	var listed, delivered, added []string
	for i := 0; i < 4; i++ {
		listed = append(listed, fmt.Sprintf("Listed behaviour %d is handled by the scheduler", i))
		added = append(added, fmt.Sprintf("New behaviour %d is handled by the scheduler", i))
	}
	for i := 0; i < 5; i++ {
		delivered = append(delivered, fmt.Sprintf("Delivered behaviour %d is handled by the scheduler", i))
	}
	body := unchecked(append(append(append([]string{}, listed...), delivered...), added...)...)
	if n := len(preflight.ReconcileACs(t.TempDir(), 1, partialRemainderBody(body...)).AcceptanceCriteria); n != 13 {
		t.Fatalf("parsed %d criteria, want 13", n)
	}
	s := partialRerunFixture(listed, body, partialPRBody(delivered, listed), partialOpened, ptrTime(partialOpened.Add(48*time.Hour)))
	got := scopeOf(t, s, gitWorkspace(t))
	want := append(append([]string{}, listed...), added...)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("brief = %q, want %q", got, want)
	}
}

// (#3235 steer) A criterion the partial PR reported met is not re-added,
// though the rules cannot prove prose met and the body was edited since.
func TestScopePartialRerun_MetInPartialPRNotReadded(t *testing.T) {
	listed := []string{"Remaining behaviour is handled by the scheduler"}
	met := "Delivered behaviour is handled by the scheduler"
	s := partialRerunFixture(listed, unchecked(met, listed[0]), partialPRBody([]string{met}, listed),
		partialOpened, ptrTime(partialOpened.Add(time.Hour)))
	if got := scopeOf(t, s, gitWorkspace(t)); strings.Join(got, "|") != listed[0] {
		t.Fatalf("brief = %q, want only %q", got, listed)
	}
}

// (#3235 steer) A criterion the partial PR listed unmet but the comment did
// not (a trimmed remainder) is not new either.
func TestScopePartialRerun_UnmetInPartialPRNotNew(t *testing.T) {
	listed := []string{"Remaining behaviour is handled by the scheduler"}
	other := "Another unmet behaviour is handled by the scheduler"
	s := partialRerunFixture(listed, unchecked(listed[0], other), partialPRBody(nil, []string{listed[0], other}),
		partialOpened, ptrTime(partialOpened.Add(time.Hour)))
	if got := scopeOf(t, s, gitWorkspace(t)); strings.Join(got, "|") != listed[0] {
		t.Fatalf("brief = %q, want only %q", got, listed)
	}
}

// (#3235 steer) A body unedited since the partial PR opened holds no new
// criteria, whatever the PR body names.
func TestScopePartialRerun_UneditedBodyAddsNothing(t *testing.T) {
	listed := []string{"Remaining behaviour is handled by the scheduler"}
	unnamed := "Unnamed behaviour is handled by the scheduler"
	for _, edited := range []*time.Time{nil, ptrTime(partialOpened.Add(-time.Hour))} {
		s := partialRerunFixture(listed, unchecked(unnamed, listed[0]), "", partialOpened, edited)
		if got := scopeOf(t, s, gitWorkspace(t)); strings.Join(got, "|") != listed[0] {
			t.Fatalf("edited=%v: brief = %q, want only %q", edited, got, listed)
		}
	}
}

// (#3235) A listed criterion removed from the body leaves the brief, is
// recorded as dropped in the context file and logged.
func TestScopePartialRerun_DropsRemovedCriterion(t *testing.T) {
	ws := gitWorkspace(t)
	kept, removed := "Kept behaviour is handled", "Removed behaviour is handled"
	s := partialRerunFixture([]string{kept, removed}, unchecked(kept), partialPRBody(nil, []string{kept, removed}),
		partialOpened, ptrTime(partialOpened.Add(time.Hour)))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if got := scopeOf(t, s, ws); strings.Join(got, "|") != kept {
		t.Fatalf("brief = %q, want only %q", got, kept)
	}
	if !strings.Contains(logs.String(), "dropped: \""+removed+"\"") {
		t.Fatalf("log does not name the dropped criterion: %s", logs.String())
	}
	path, _ := stagecontext.ContextPath(ws, 3235, partialDeliveryContextType)
	var f partialDeliveryFile
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &f) != nil {
		t.Fatalf("read partial-delivery context: %v", err)
	}
	if len(f.Dropped) != 1 || f.Dropped[0] != removed {
		t.Fatalf("context dropped = %q, want [%q]", f.Dropped, removed)
	}
}

// (#3235 review) Every listed criterion removed and none new: the run is
// scoped to no work, never widened to the whole issue.
func TestScopePartialRerun_AllDroppedScopesToNothing(t *testing.T) {
	ws := gitWorkspace(t)
	kept, removed := "Delivered behaviour is handled", "Removed behaviour is handled"
	s := partialRerunFixture([]string{removed}, []string{"- [x] " + kept}, partialPRBody([]string{kept}, []string{removed}),
		partialOpened, ptrTime(partialOpened.Add(time.Hour)))
	rt := &state.RuntimeState{}
	s.scopePartialRerun(context.Background(), rt, types.BoardItem{Number: 3235, Repo: "nightgauge/test"}, ws)
	scope := rt.PartialRerunScope()
	if scope == nil {
		t.Fatal("all-dropped remainder left the run unscoped (whole issue)")
	}
	if len(scope.Criteria) != 0 {
		t.Fatalf("brief = %q, want empty", scope.Criteria)
	}
	dev := partialRerunPromptBlock(state.StageFeatureDev, scope)
	if !strings.Contains(dev, "nothing remains") || !strings.Contains(dev, "write no code") {
		t.Fatalf("feature-dev block does not say nothing remains: %s", dev)
	}
	note := pmstages.PartialRerunNoDiffComment(3235, scope.PR, "b", "main", scope.Criteria)
	if strings.Contains(note, "Still open:") || !strings.Contains(note, "nothing remains open") {
		t.Fatalf("no-diff comment lists an empty remainder: %s", note)
	}
}

// (#3235) An unreadable partial PR adds nothing new: without its met and
// unmet sections, delivered prose criteria cannot be told apart from new ones.
func TestScopePartialRerun_PRReadFailureAddsNothingNew(t *testing.T) {
	listed := []string{"Remaining behaviour is handled by the scheduler"}
	unlisted := "Unlisted behaviour is handled by the scheduler"
	s := partialRerunFixture(listed, unchecked(unlisted, listed[0]), "", partialOpened, nil)
	s.partialPRRead = func(context.Context, string, int) (partialPR, error) { return partialPR{}, errors.New("forge down") }
	want := listed[0]
	if got := scopeOf(t, s, gitWorkspace(t)); strings.Join(got, "|") != want {
		t.Fatalf("brief = %q, want %q", got, want)
	}
}

// (#3235) An unreadable body keeps the comment's list as the brief.
func TestScopePartialRerun_BodyReadFailureKeepsCommentList(t *testing.T) {
	listed := []string{"A is handled", "B is handled"}
	s := partialRerunFixture(listed, nil, "", partialOpened, nil)
	s.acReconcileBody = func(context.Context, types.BoardItem) (string, error) { return "", errors.New("forge down") }
	if got := scopeOf(t, s, t.TempDir()); strings.Join(got, "|") != strings.Join(listed, "|") {
		t.Fatalf("brief = %q, want the comment list %q", got, listed)
	}
}

// (#3235) A listed entry shortened or re-punctuated in the PR body still
// names its body criterion: it is kept, not dropped.
func TestReconcilePartialRemainder_TolerantMatch(t *testing.T) {
	body := "The scheduler logs every dropped criterion with its issue number."
	rec := preflight.ReconcileACs(t.TempDir(), 1, partialRemainderBody("- [ ] "+body))
	listed := []string{"the scheduler logs every dropped criterion", "Short"}
	scope, added, dropped := reconcilePartialRemainder(listed, nil, rec, false)
	if len(scope) != 1 || scope[0] != body || len(added) != 0 {
		t.Fatalf("scope = %q added = %q, want [%q] and none", scope, added, body)
	}
	if len(dropped) != 1 || dropped[0] != "Short" {
		t.Fatalf("dropped = %q, want [Short]", dropped)
	}
}

// (#3235) partialPRCriteria reads both sections of a rendered body, met
// bullets without their status label.
func TestPartialPRCriteria_ReadsMetAndUnmet(t *testing.T) {
	got := partialPRCriteria(partialPRBody([]string{"Met one"}, []string{"Unmet one"}))
	if len(got) != 2 || got[0] != "Unmet one" || !strings.HasPrefix(got[1], "Met one (diff: ") {
		t.Fatalf("criteria = %q", got)
	}
}

// (#3235 steer) The planning prompt fixes focus_acs to the brief; the
// feature-dev prompt keeps the plan's focus over the list.
func TestPartialRerunPromptBlock_FocusIsTheBrief(t *testing.T) {
	scope := &state.PartialRerun{PR: 1, Criteria: []string{"A is handled"}}
	for _, goPath := range []bool{true, false} {
		plan := renderPartialRerunPromptBlock(state.StageFeaturePlanning, scope, goPath)
		if !strings.Contains(plan, "`focus_acs` is exactly these criteria") || strings.Contains(plan, "include it in `focus_acs`") {
			t.Errorf("goPath=%v planning block does not fix focus_acs to the brief: %s", goPath, plan)
		}
		dev := renderPartialRerunPromptBlock(state.StageFeatureDev, scope, goPath)
		if !strings.Contains(dev, "the plan wins") || !strings.Contains(dev, "every focus criterion") {
			t.Errorf("goPath=%v feature-dev block lacks the plan-authority text: %s", goPath, dev)
		}
	}
}

// (#3235) The extension path renders the raw comment list, so its block does
// not claim the list was reconciled.
func TestPartialRerunPromptBlock_ReconciledClaimGoPathOnly(t *testing.T) {
	scope := &state.PartialRerun{PR: 1, Criteria: []string{"A is handled"}}
	if !strings.Contains(renderPartialRerunPromptBlock(state.StageFeatureDev, scope, true), "reconciled against the current issue body") {
		t.Fatal("Go-path block lacks the reconciled claim")
	}
	if strings.Contains(renderPartialRerunPromptBlock(state.StageFeatureDev, scope, false), "reconciled") {
		t.Fatal("extension block claims a reconcile it never ran")
	}
}
