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
	gh "github.com/nightgauge/nightgauge/internal/github"
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
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
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
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
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
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
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
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
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
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
//
// Serial: redirects the standard logger.
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
	var f PartialDeliveryFile
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	got := partialPRCriteria(partialPRBody([]string{"Met one"}, []string{"Unmet one"}))
	if len(got) != 2 || got[0] != "Unmet one" || !strings.HasPrefix(got[1], "Met one (this PR — diff: ") {
		t.Fatalf("criteria = %q", got)
	}
}

// (#3235 steer) The planning prompt fixes focus_acs to the brief; the
// feature-dev prompt keeps the plan's focus over the list.
func TestPartialRerunPromptBlock_FocusIsTheBrief(t *testing.T) {
	t.Parallel()

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

// (#3242) Both paths reconcile the list against the issue body, so both
// blocks say so; an empty reconciled scope renders "nothing remains".
func TestPartialRerunScopeBlocks_ReconciledAndEmpty(t *testing.T) {
	t.Parallel()

	for _, goPath := range []bool{true, false} {
		scope := &state.PartialRerun{PR: 1, Criteria: []string{"A is handled"}}
		if !strings.Contains(renderPartialRerunPromptBlock(state.StageFeatureDev, scope, goPath), "reconciled against the current issue body") {
			t.Errorf("goPath=%v block lacks the reconciled claim", goPath)
		}
	}
	blocks := PartialRerunScopeBlocks(7, nil)
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		if !strings.Contains(blocks[string(stage)], "nothing remains") {
			t.Errorf("empty scope %s block = %q, want the nothing-remains block", stage, blocks[string(stage)])
		}
	}
	if PartialRerunScopeBlocks(0, []string{"x"}) != nil {
		t.Error("no PR rendered blocks")
	}
}

// #3269 part E: an unscoped run whose feature-dev and feature-validate leave
// the branch level with its base, with criteria still open in the issue body,
// ends as a no-op before pr-create: not_pipeline_actionable (no lifetime-cap
// increment), with one comment naming the open criteria.
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestNoDiffBeforePRCreate_EndsAsNoOp(t *testing.T) {
	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Criteria: []string{}}, nil
	}
	s.issueSvc.(*mockIssueSvc).addIssue("nightgauge", "test", 3269, &types.Issue{
		NodeID: "I_3269", Number: 3269, State: "OPEN",
		Body: "## Acceptance criteria\n\n- [x] code change\n- [ ] **Supervisor:** run the failure drill\n",
	})
	commenter := &recordingCommenter{}
	s.noDiffCommenter = commenter

	_, terminalKind := s.runPipeline(context.Background(),
		types.BoardItem{Number: 3269, Repo: "nightgauge/test", ID: "item-3269", NodeID: "I_3269"})

	if !ranStage(runner, state.StageFeatureValidate) {
		t.Fatal("feature-validate never ran; the fixture is wrong")
	}
	if ranStage(runner, state.StagePRCreate) {
		t.Fatal("pr-create ran on a branch level with its base")
	}
	if terminalKind != TerminalKindNotPipelineActionable {
		t.Fatalf("terminal kind = %q, want %q (a no-op, not a failure)", terminalKind, TerminalKindNotPipelineActionable)
	}
	if n := commenter.count(); n != 1 {
		t.Fatalf("posted %d comment(s), want 1", n)
	}
	if body := commenter.bodies[0]; !strings.Contains(body, "run the failure drill") || strings.Contains(body, "code change") {
		t.Errorf("comment does not name exactly the open criteria: %q", body)
	}
}

// With every criterion ticked the boundary does not stop the run: pr-create's
// own no-diff settlement closes the issue with its evidence (#2941).
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestNoDiffBeforePRCreate_AllTickedContinues(t *testing.T) {
	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Criteria: []string{}}, nil
	}
	s.issueSvc.(*mockIssueSvc).addIssue("nightgauge", "test", 3270, &types.Issue{
		NodeID: "I_3270", Number: 3270, State: "OPEN", Body: "- [x] done\n",
	})
	s.noDiffCommenter = &recordingCommenter{}
	s.runPipeline(context.Background(), types.BoardItem{Number: 3270, Repo: "nightgauge/test", ID: "item-3270", NodeID: "I_3270"})
	if !ranStage(runner, state.StagePRCreate) {
		t.Fatal("the boundary stopped a run whose criteria are all ticked")
	}
}

// The no-op booking: not_pipeline_actionable leaves the failure counter
// unchanged.
func TestNoDiffBeforePRCreate_BookingLeavesFailureCounter(t *testing.T) {
	t.Parallel()

	as := &AutonomousScheduler{
		trustGateOverride: allWritersGate(),
		config:            AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			Running:               []RunningItem{{Repo: "nightgauge/test", Number: 3269, Title: "no diff"}},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]retryPlan{},
	}
	detail := pmstages.NoDiffBeforePRCreateDetail(3269, "feat/3269-x", "main", 1)
	as.onPipelineComplete("nightgauge/test", 3269, false, false, ClassifyTerminalKind(detail), detail, false)
	as.drainBackground()
	if got := as.state.LifetimeIssueFailures["nightgauge/test#3269"]; got != 0 {
		t.Errorf("LifetimeIssueFailures = %d, want 0", got)
	}
	if ClassifyTerminalKind(detail) != TerminalKindNotPipelineActionable {
		t.Errorf("detail classifies as %q, want not_pipeline_actionable", ClassifyTerminalKind(detail))
	}
}

// The record carries the partial PR's merge commit, read once with the
// reconcile's PR read, and reads back as written (#3423).
func TestReconcilePartialRecord_MergeSHA(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	gitInitForPartialRecord(t, ws)
	calls := 0
	reads := PartialScopeReads{
		// A body with criteria, so the reconcile itself reaches reads.PR.
		Body: func(context.Context) (string, error) { return "## Acceptance criteria\n\n- [ ] a\n", nil },
		PR: func(context.Context, int) (gh.PartialPR, error) {
			calls++
			return gh.PartialPR{MergeSHA: strings.Repeat("c", 40)}, nil
		},
		BodyEditedAt: func(context.Context) (*time.Time, error) { return nil, nil },
	}
	rec := ReconcilePartialRecord(context.Background(), reads, ws, 3423, []string{"a"}, 3422, nil)
	if rec.MergeSHA != strings.Repeat("c", 40) || calls != 1 || rec.PR != 3422 || len(rec.Criteria) != 1 {
		t.Fatalf("record %+v after %d PR read(s), want the merge SHA from one read", rec, calls)
	}
	if err := WritePartialDeliveryContext(ws, 3423, rec); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPartialDeliveryContext(ws, 3423)
	if err != nil || got.MergeSHA != rec.MergeSHA || got.PR != 3422 {
		t.Fatalf("read back %+v, %v", got, err)
	}
	reads.PR = func(context.Context, int) (gh.PartialPR, error) { return gh.PartialPR{}, errors.New("forge down") }
	if rec := ReconcilePartialRecord(context.Background(), reads, ws, 3423, []string{"a"}, 3422, nil); rec.MergeSHA != "" {
		t.Fatalf("unreadable PR: merge SHA %q, want empty", rec.MergeSHA)
	}
	reads.PR = nil
	if rec := ReconcilePartialRecord(context.Background(), reads, ws, 3423, []string{"a"}, 3422, nil); rec.MergeSHA != "" {
		t.Fatalf("no PR read configured: merge SHA %q, want empty", rec.MergeSHA)
	}
	if err := RemovePartialDeliveryContext(ws, 3423); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPartialDeliveryContext(ws, 3423); err == nil {
		t.Fatal("missing record: want an error")
	}
}

// (#3487) A close is offered over the record's fingerprint, so the record has
// one only when its criteria were reconciled from that very body.
func TestReconcilePartialRecord_CriteriaFingerprint(t *testing.T) {
	t.Parallel()
	listed, added := "Listed behaviour is handled", "Added behaviour is handled"
	body := "## Acceptance criteria\n\n- [ ] " + listed + "\n- [ ] " + added + "\n"
	down := errors.New("forge down")
	for _, tc := range []struct {
		name, body string
		bodyErr    error
		prErr      error
		want       string
	}{
		{name: "reconciled", body: body, want: preflight.ACFingerprint(body)},
		{name: "criterion in the body, absent from the scope", body: body, prErr: down},
		{name: "body unreadable", body: body, bodyErr: down},
		{name: "body without criteria", body: "prose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bodyReads := 0
			edited := partialMerged.Add(time.Hour)
			reads := PartialScopeReads{
				Body: func(context.Context) (string, error) { bodyReads++; return tc.body, tc.bodyErr },
				PR: func(context.Context, int) (gh.PartialPR, error) {
					return gh.PartialPR{CreatedAt: partialOpened, MergedAt: partialMerged}, tc.prErr
				},
				BodyEditedAt: editedAt(edited),
			}
			rec := ReconcilePartialRecord(context.Background(), reads, gitWorkspace(t), 3487, []string{listed}, 7, nil)
			if rec.CriteriaFingerprint != tc.want || bodyReads != 1 {
				t.Fatalf("fingerprint %q after %d body read(s), want %q from one", rec.CriteriaFingerprint, bodyReads, tc.want)
			}
			if tc.want != "" && strings.Join(rec.Criteria, "|") != listed+"|"+added {
				t.Fatalf("scope %q does not cover the fingerprinted body", rec.Criteria)
			}
			if tc.prErr != nil && strings.Join(rec.Criteria, "|") != listed {
				t.Fatalf("scope %q, want the added criterion absent", rec.Criteria)
			}
		})
	}
}

func gitInitForPartialRecord(t *testing.T, dir string) {
	t.Helper()
	gittest.Run(t, dir, "init", "-q")
}

// mergeSiblingInto commits, in root, a partial PR as a real merge commit that
// changes only a sibling of `ExcludePattern`, and returns the merge SHA.
func mergeSiblingInto(t *testing.T, root string) string {
	t.Helper()
	write := func(content string) {
		if err := os.MkdirAll(filepath.Join(root, "scan"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "scan", "scan.go"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package scan\n\nconst ExcludePattern = `^---`\n\nfunc match() bool { return true }\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "scan")
	gitIn(t, root, "checkout", "-b", "partial")
	write("package scan\n\nconst ExcludePattern = `^---`\n\nfunc match() bool { return false }\n")
	gitIn(t, root, "commit", "-am", "partial")
	gitIn(t, root, "checkout", "main")
	gitIn(t, root, "merge", "--no-ff", "-m", "Merge PR #3422", "partial")
	return gittest.Run(t, root, "rev-parse", "HEAD")
}

// #3423: a zero-change partial re-run whose remainder is met on the earlier
// PR's merged diff closes the issue and ends issue_closed, posting no
// remainder comment; a failed close books not_pipeline_actionable; an unmet
// remainder keeps the #3160 booking and comment.
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestPartialRerun_MetRemainderCloses(t *testing.T) {
	const preserved = "`ExcludePattern` keeps its current value"
	for _, tc := range []struct {
		name      string
		gate      bool
		criterion string
		closeErr  error
		wantKind  string
		wantClose bool
	}{
		{"clean exit, met", false, preserved, nil, TerminalKindIssueClosed, true},
		{"gate no-op, met", true, preserved, nil, TerminalKindIssueClosed, true},
		{"met, close fails", true, preserved, errors.New("forge down"), TerminalKindNotPipelineActionable, true},
		{"presence remainder", true, partialRerunCriterion, nil, TerminalKindNotPipelineActionable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &partialRerunRunner{}
			s, root := partialRerunScheduler(t, runner, tc.gate)
			merge := mergeSiblingInto(t, root)
			s.partialDeliveryRead = pendingRemainder(3422, tc.criterion)
			s.partialPRRead = func(context.Context, string, int) (partialPR, error) {
				return partialPR{CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), MergeSHA: merge}, nil
			}
			commenter := &recordingCommenter{}
			s.noDiffCommenter = commenter
			var closes []RemainderVerdict
			s.partialRemainderClose = func(_ context.Context, _ types.BoardItem, pr int, v RemainderVerdict) (PartialCloseResult, error) {
				closes = append(closes, v)
				if tc.closeErr != nil {
					return PartialCloseResult{}, tc.closeErr
				}
				return PartialCloseResult{Closed: true}, nil
			}

			_, kind := s.runPipeline(context.Background(),
				types.BoardItem{Number: 3423, Repo: "nightgauge/test", ID: "item-3423", NodeID: "I_3423"})

			if ranStage(runner, state.StageFeatureValidate) {
				t.Fatal("feature-validate ran on a zero-change partial re-run")
			}
			if kind != tc.wantKind {
				t.Fatalf("terminal kind = %q, want %q", kind, tc.wantKind)
			}
			if got := len(closes) > 0; got != tc.wantClose {
				t.Fatalf("close attempted = %v, want %v", got, tc.wantClose)
			}
			if tc.wantClose && (!closes[0].AllMet || closes[0].MergeSHA != merge) {
				t.Fatalf("closed on verdict %+v, want all met on %s", closes[0], merge)
			}
			if wantComments := map[bool]int{true: 0, false: 1}[tc.wantClose]; commenter.count() != wantComments {
				t.Fatalf("posted %d remainder comment(s), want %d", commenter.count(), wantComments)
			}
		})
	}
}

// partialMerged is when the fixtures' partial PR merged (#3460).
var partialMerged = partialOpened.Add(2 * time.Hour)

// revisedReads are reconcile reads for a body of bodyLines last edited at
// edited, after a partial PR opened at partialOpened and merged at
// partialMerged whose body is prBody.
func revisedReads(bodyLines []string, prBody string, edited func(context.Context) (*time.Time, error)) PartialScopeReads {
	return PartialScopeReads{
		Body: func(context.Context) (string, error) { return partialRemainderBody(bodyLines...), nil },
		PR: func(context.Context, int) (gh.PartialPR, error) {
			return gh.PartialPR{CreatedAt: partialOpened, MergedAt: partialMerged, Body: prBody, MergeSHA: "x"}, nil
		},
		BodyEditedAt: edited,
	}
}

func editedAt(t time.Time) func(context.Context) (*time.Time, error) {
	return func(context.Context) (*time.Time, error) { return &t, nil }
}

// AC 3 (#3460): the supervisor ticks the one listed criterion and adds a new
// one after the partial PR merged. The scope is exactly the new criterion, and
// the no-op verdict names it, not the ticked one.
func TestReconcilePartialRecord_RevisedAfterMergeUsesUntickedBoxes(t *testing.T) {
	t.Parallel()
	ws := gitWorkspace(t)
	ticked := "Ticked behaviour is handled by the scheduler"
	added := "internal/orchestrator/partial_rerun.go:42 handles the revised remainder"
	// The PR body names the new criterion, so the #3235 filter would drop it.
	reads := revisedReads([]string{"- [x] " + ticked, "- [ ] " + added},
		partialPRBody([]string{added}, []string{ticked}), editedAt(partialMerged.Add(time.Hour)))
	rec := ReconcilePartialRecord(context.Background(), reads, ws, 3460, []string{ticked}, 7, nil)
	if strings.Join(rec.Criteria, "|") != added {
		t.Fatalf("scope = %q, want only %q", rec.Criteria, added)
	}
	if len(rec.Dropped) != 1 || rec.Dropped[0] != ticked {
		t.Fatalf("dropped = %q, want [%q]", rec.Dropped, ticked)
	}
	summary := unmetSummary(JudgeRemainder(context.Background(), ws, rec.MergeSHA, rec.Criteria))
	if !strings.Contains(summary, added) || strings.Contains(summary, ticked) {
		t.Fatalf("no-op verdict = %q, want it to name %q and not %q", summary, added, ticked)
	}
}

// (#3460) A listed criterion ticked in a body edited before the merge leaves
// the scope without being reported dropped.
func TestReconcilePartialRecord_TickedListedCriterionExcluded(t *testing.T) {
	t.Parallel()
	ticked, open := "Ticked behaviour is handled", "Open behaviour is handled"
	for i, edited := range []func(context.Context) (*time.Time, error){
		editedAt(partialOpened.Add(time.Hour)),
		func(context.Context) (*time.Time, error) { return nil, nil },
	} {
		reads := revisedReads([]string{"- [x] " + ticked, "- [ ] " + open},
			partialPRBody(nil, []string{ticked, open}), edited)
		rec := ReconcilePartialRecord(context.Background(), reads, gitWorkspace(t), 3460, []string{ticked, open}, 7, nil)
		if strings.Join(rec.Criteria, "|") != open || len(rec.Dropped) != 0 {
			t.Fatalf("case %d: scope = %q dropped = %q, want [%q] and none", i, rec.Criteria, rec.Dropped, open)
		}
	}
}

// (#3460) An unreadable edit time keeps the #3235 reconcile: the listed
// remainder stands and a criterion the PR named is not added.
func TestReconcilePartialRecord_UnreadableEditTimeKeepsReconcile(t *testing.T) {
	t.Parallel()
	listed, named := "Listed behaviour is handled", "Named behaviour is handled"
	fresh := "Fresh behaviour is handled"
	reads := revisedReads(unchecked(listed, named, fresh), partialPRBody([]string{named}, []string{listed}),
		func(context.Context) (*time.Time, error) { return nil, errors.New("boom") })
	rec := ReconcilePartialRecord(context.Background(), reads, gitWorkspace(t), 3460, []string{listed}, 7, nil)
	if strings.Join(rec.Criteria, "|") != listed+"|"+fresh {
		t.Fatalf("scope = %q, want [%q %q]: unreadable counts as edited, PR-named stays out", rec.Criteria, listed, fresh)
	}
}

// (#3460) On the revised path a listed criterion still unticked stays in
// scope, neither added nor dropped, in body order with a new one.
func TestReconcilePartialRecord_RevisedKeepsUntickedListed(t *testing.T) {
	t.Parallel()
	added, listed := "Added behaviour is handled", "Listed behaviour is handled"
	reads := revisedReads(unchecked(added, listed), "", editedAt(partialMerged.Add(time.Minute)))
	rec := ReconcilePartialRecord(context.Background(), reads, gitWorkspace(t), 3460, []string{listed}, 7, nil)
	if strings.Join(rec.Criteria, "|") != added+"|"+listed || len(rec.Dropped) != 0 {
		t.Fatalf("scope = %q dropped = %q", rec.Criteria, rec.Dropped)
	}
	// Edited exactly at the merge is not a revision: the #3235 rule applies,
	// and it keeps out a criterion the PR body named.
	reads = revisedReads(unchecked(added, listed), partialPRBody([]string{added}, []string{listed}), editedAt(partialMerged))
	rec = ReconcilePartialRecord(context.Background(), reads, gitWorkspace(t), 3460, []string{listed}, 7, nil)
	if strings.Join(rec.Criteria, "|") != listed {
		t.Fatalf("edited at merge: scope = %q, want only %q", rec.Criteria, listed)
	}
}
