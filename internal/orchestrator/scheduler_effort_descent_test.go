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

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// scheduler_effort_descent_test.go pins #611: the executing dispatch's provider
// identity where the #606 effort descent is KEYED and where it is CONSUMED.
//
// Two defects, one root cause — the descent machinery could not name the
// provider a dispatch runs on:
//
//  1. downgradeEfforts was keyed by TIER alone, so an xai rung's effort became
//     the wire effort of the next stage that resolved to the substituted tier
//     on ANY adapter. The value is a legal EFFORT_LEVELS member, so the wrong
//     one dispatches with no error and no log.
//  2. Nothing on the IPC path named the provider at all, so IpcStageRunner
//     resolved every extension-side rejection against anthropic and no xai
//     dispatch could ever reach the descent.
//
// The identity comes from evidence, never from inference: execMgr's adapter on
// the Go-direct path, and on the IPC path the concrete model id the adapter's
// own process reported (StageResultParams.ServedModel). Go does not re-derive
// the extension's adapter choice — resolveStageAdapter has an AutoProviderRouter
// rung and a stage-start walkAdapterFallback that config.ResolveStageAdapter
// cannot see, so a mirror is blind in one direction and wrong in the other.

// descentGoldenCell returns one cell of the #581 selection-compat golden's
// downgrade table — the SHIPPED expectation, read from testdata rather than
// restated here, so a test that "passes" because someone edited its literal is
// impossible.
func descentGoldenCell(t *testing.T, rejectedModel string) string {
	t.Helper()
	data, err := os.ReadFile(selectionCompatGoldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", selectionCompatGoldenPath, err)
	}
	var golden selectionCompatGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	cell, ok := golden.Downgrades[rejectedModel]
	if !ok {
		t.Fatalf("golden has no downgrade cell for %q", rejectedModel)
	}
	return cell
}

// downgradeCell renders a decision in the golden's downgrade vocabulary.
func downgradeCell(d DowngradeDecision) string {
	return fmt.Sprintf("%v|%s|%s|%s", d.ShouldDowngrade, d.FromTier, d.NewTier, d.Reason)
}

// TestStickyEffort_XaiDescentDoesNotBleedOntoAnotherAdapter is the #611
// finding-1 regression, built as the exact mixed-adapter sequence from the
// issue.
//
// Stage A dispatches the fable band on a grok adapter and is refused by the
// API; the descent substitutes opus and records grok-4.6's declared rung
// effort. Stage B then resolves to that same substituted tier on a CLAUDE
// adapter. Its wire effort must come from its own envelope resolution — the
// xai rung is a point on xai's ladder and says nothing about what anthropic
// should dispatch.
//
// The two halves are both load-bearing. Without the xai half the test would
// pass on an engine that simply stopped recording sticky efforts at all,
// silently reverting #606; without the claude half it passes on today's
// tier-only key.
func TestStickyEffort_XaiDescentDoesNotBleedOntoAnotherAdapter(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	xai := DowngradeProviderForAdapter("grok")
	anthropic := DowngradeProviderForAdapter("claude")
	if xai != "xai" {
		t.Fatalf("fixture: DowngradeProviderForAdapter(grok) = %q, want xai", xai)
	}

	// ── Stage A: grok dispatch of the fable band, refused by the API. ──
	dg := engine.EvaluateDowngradeForProvider("fable", xai)
	if !dg.ShouldDowngrade || !dg.SameModelDescent {
		t.Fatalf("fixture: xai fable rejection must descend, got %+v", dg)
	}
	if dg.NewTier != "opus" || dg.NewEffort != "high" {
		t.Fatalf("fixture: descent must be grok-4.6@high on opus, got %+v", dg)
	}
	if dg.Provider != "xai" {
		t.Fatalf("decision must name the ladder that produced it, got provider %q", dg.Provider)
	}
	engine.RecordDowngrade("fable", dg)

	// The tier substitution itself is deliberately provider-AGNOSTIC: a band
	// the API refused is refused for the run, whoever dispatches it next. Only
	// the effort rung is scoped. Asserting this keeps a future "fix" from
	// over-scoping the key and quietly disabling #42's sticky reroute.
	if got := engine.ApplyDowngrades("fable"); got != "opus" {
		t.Fatalf("ApplyDowngrades(fable) = %q, want opus on every adapter", got)
	}

	// ── Stage A re-dispatch, still on grok: the rung IS its wire effort. ──
	if got := engine.StickyEffort(xai, "opus"); got != "high" {
		t.Fatalf("StickyEffort(xai, opus) = %q, want high — the #606 descent must still apply on its own provider", got)
	}

	// ── Stage B on claude, resolving to the same substituted tier. ──
	if got := engine.StickyEffort(anthropic, "opus"); got != "" {
		t.Fatalf("StickyEffort(claude, opus) = %q, want \"\" — an xai descent must never become another provider's wire effort", got)
	}

	// The same holds for the other adapters a mixed run can reach; each would
	// otherwise inherit the identical wrong value from the tier-only key.
	for _, adapter := range []string{"codex", "gemini", "copilot", "claude-headless"} {
		if got := engine.StickyEffort(DowngradeProviderForAdapter(adapter), "opus"); got != "" {
			t.Errorf("StickyEffort(%s, opus) = %q, want \"\"", adapter, got)
		}
	}
}

// TestDowngradeDescent_XaiGoldenCellHoldsForABandDispatch is the #611
// finding-2 regression at the decision layer: the shipped descent behaviour
// must not depend on the dispatch happening to name a CONCRETE model id.
//
// The golden's xai cell was generated from a concrete-id rejection
// ("grok-4.6"), which self-identifies its provider. The wire carries a registry
// BAND (#340), and a band cannot — so an extension-side xai dispatch resolved
// against anthropic and produced a cross-model fallback instead of the descent.
// Both dispatch paths must now reproduce the cell: the Go-direct one keyed on
// execMgr's adapter, and the IPC one keyed on the concrete id the adapter's own
// process reported.
func TestDowngradeDescent_XaiGoldenCellHoldsForABandDispatch(t *testing.T) {
	want := descentGoldenCell(t, "grok-4.6")

	// The Go-direct path already reached this cell via the concrete id.
	byID := NewRetryEngine(DefaultRetryConfig()).EvaluateDowngrade("grok-4.6")
	if got := downgradeCell(byID); got != want {
		t.Fatalf("fixture: concrete-id descent = %q, want the golden cell %q", got, want)
	}

	// Both provider-evidence sources, on the identical band dispatch.
	for _, arm := range []struct {
		path string
		hint string
	}{
		{"go-direct (execMgr adapter)", DowngradeProviderForAdapter("grok")},
		{"ipc (served id the process reported)", DowngradeProviderForServedModel("grok-4.6")},
	} {
		byBand := NewRetryEngine(DefaultRetryConfig()).EvaluateDowngradeForProvider("fable", arm.hint)
		if got := downgradeCell(byBand); got != want {
			t.Errorf("%s band dispatch = %q, want the golden xai cell %q — the descent must not depend on the wire naming a concrete id", arm.path, got, want)
		}
		if !byBand.SameModelDescent || byBand.NewEffort != byID.NewEffort {
			t.Errorf("%s band dispatch envelope = %+v, want the same rung as %+v", arm.path, byBand, byID)
		}
	}
}

// TestDowngradeDescent_NonXaiAdaptersKeepEveryGoldenCell is the AC-3 guard:
// widening the key by the executing adapter must perturb nothing outside xai.
//
// Every rejected model in the shipped golden is re-evaluated with each non-xai
// adapter's provider hint. All of them must reproduce their golden cell byte
// for byte — that is what makes "single-adapter behaviour unchanged" an
// assertion rather than a claim in a PR body.
func TestDowngradeDescent_NonXaiAdaptersKeepEveryGoldenCell(t *testing.T) {
	data, err := os.ReadFile(selectionCompatGoldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden selectionCompatGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(golden.Downgrades) == 0 {
		t.Fatal("golden downgrade table is empty; the fixture is wrong, not the assertion")
	}

	for _, adapter := range []string{"", "claude", "claude-headless", "codex", "gemini", "copilot", "ollama"} {
		// The scoping contract itself, and then the consequence of breaking
		// it. Reported rather than fatal so the cell comparison below still
		// runs: threading google's provider, for instance, turns the sonnet
		// row into an exhausted ladder (flash serves both sonnet and haiku),
		// and seeing that row change is the evidence, not the guard.
		hint := DowngradeProviderForAdapter(adapter)
		if hint != "" {
			t.Errorf("DowngradeProviderForAdapter(%q) = %q, want \"\" — only xai may be threaded (#606 judgment call)", adapter, hint)
		}
		for rejected, want := range golden.Downgrades {
			// A fresh engine per row: sticky downgrades must not leak between
			// rows, exactly as the golden generator requires.
			got := downgradeCell(NewRetryEngine(DefaultRetryConfig()).EvaluateDowngradeForProvider(rejected, hint))
			if got != want {
				t.Errorf("adapter %q, rejected %q: %q, want the golden %q", adapter, rejected, got, want)
			}
		}
	}
}

// TestStickyEffort_SingleAdapterXaiRunIsUnchanged pins the other side of AC 3:
// a run that never mixes adapters must still descend and still apply the rung,
// including down the chained ladder #606 shipped.
func TestStickyEffort_SingleAdapterXaiRunIsUnchanged(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	xai := DowngradeProviderForAdapter("grok-headless")

	first := engine.EvaluateDowngradeForProvider("fable", xai)
	engine.RecordDowngrade("fable", first)
	second := engine.EvaluateDowngradeForProvider(first.NewTier, xai)
	engine.RecordDowngrade(first.NewTier, second)

	if got := engine.ApplyDowngrades("fable"); got != "sonnet" {
		t.Fatalf("ApplyDowngrades(fable) = %q, want sonnet (chained descent)", got)
	}
	if got := engine.StickyEffort(xai, "sonnet"); got != "medium" {
		t.Fatalf("StickyEffort(xai, sonnet) = %q, want medium", got)
	}
}

// ─── The production wiring, driven through runPipeline ───────────────────────
//
// The engine tests above assert behaviour GIVEN a provider. The two lines that
// actually SUPPLY that provider in production live inside runPipeline's stage
// loop — the sticky-effort lookup that becomes the dispatched wire effort, and
// the downgrade evaluation an API rejection walks. Both are invisible to any
// unit test of the engine: replace either argument with a literal and the whole
// engine suite still passes. The two tests below dispatch real pipelines so
// that substitution goes red.

// descentRejection is a model-unavailability failure text ClassifyTerminalKind
// resolves to TerminalKindModelUnavailable (pinned in model_fallback_test.go).
const descentRejection = `API Error: 404 {"type":"error","error":{"type":"not_found_error","message":"model: claude-fable-5"}}`

// descentStageRunner dispatches like a real runner: it rejects the FIRST
// attempt at rejectStage with an API model-unavailability error and succeeds
// afterwards, reporting `servedModel` as the concrete id its process ran.
//
// engine mirrors IpcStageRunner when set: the extension-side path evaluates and
// records the downgrade itself and hands the scheduler FallbackRecorded, which
// is what makes the scheduler's own rejection arm Go-direct-only. Left nil for
// a Go-direct fixture, where the scheduler evaluates the rejection.
type descentStageRunner struct {
	mu          sync.Mutex
	calls       []StageRunParams
	rejectStage state.PipelineStage
	rejected    bool
	servedModel string
	engine      *RetryEngine
}

func (r *descentStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	reject := params.Stage == r.rejectStage && !r.rejected
	if reject {
		r.rejected = true
	}
	r.mu.Unlock()

	if reject {
		out := &StageRunResult{ExitCode: 1, ErrorText: descentRejection, ServedModel: r.servedModel}
		if r.engine != nil {
			// The IpcStageRunner shape, verbatim: classify, evaluate against
			// the provider the ADAPTER reported, record, and tell the
			// scheduler a fallback is already booked.
			if ClassifyTerminalKind(out.ErrorText) == TerminalKindModelUnavailable {
				provider := DowngradeProviderForServedModel(out.ServedModel)
				if dg := r.engine.EvaluateDowngradeForProvider(params.Model, provider); dg.ShouldDowngrade {
					r.engine.RecordDowngrade(params.Model, dg)
					out.FallbackRecorded = true
					out.FallbackFromModel, out.FallbackToModel = params.Model, dg.NewTier
				}
			}
			return out, nil
		}
		return out, fmt.Errorf("%s", descentRejection)
	}

	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0o755)
		payload := map[string]any{
			"schema_version":     "1.0",
			"issue_number":       params.IssueNumber,
			"ok":                 true,
			"validation_status":  "passed",
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0o644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, OutputTokens: 5, ServedModel: r.servedModel}, nil
}

func (r *descentStageRunner) captured() []StageRunParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StageRunParams(nil), r.calls...)
}

// dispatchesFor returns every dispatch of one stage, in order.
func dispatchesFor(calls []StageRunParams, stage state.PipelineStage) []StageRunParams {
	var out []StageRunParams
	for _, c := range calls {
		if c.Stage == stage {
			out = append(out, c)
		}
	}
	return out
}

// writeDescentFixtureConfig installs a project-tier config that makes every
// dispatch of this fixture deterministic: manual routing pinning the fable band
// on every stage, an explicit per-stage adapter (so no stage inherits the
// previous one's), and a wire effort of "low" so a descended "high" cannot be
// confused with the value the envelope chain would have resolved anyway.
func writeDescentFixtureConfig(t *testing.T, root string, stageAdapters map[state.PipelineStage]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("owner: nightgauge\nrepo: nightgauge\nproject: 1\n")
	b.WriteString("model_routing:\n  mode: manual\n  default_effort: low\n")
	b.WriteString("pipeline:\n  stage_models:\n")
	for _, stage := range descentFixtureStages {
		b.WriteString("    " + string(stage) + ": fable\n")
	}
	if len(stageAdapters) > 0 {
		b.WriteString("  stage_adapters:\n")
		for _, stage := range descentFixtureStages {
			if adapter := stageAdapters[stage]; adapter != "" {
				b.WriteString("    " + string(stage) + ": " + adapter + "\n")
			}
		}
	}
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

var descentFixtureStages = []state.PipelineStage{
	state.StageIssuePickup,
	state.StageFeaturePlanning,
	state.StageFeatureDev,
	state.StageFeatureValidate,
	state.StagePRCreate,
	state.StagePRMerge,
}

// newDescentTestScheduler mirrors newRunIdentityTestScheduler but lets the test
// own the RetryEngine (so it can read the descent state the run recorded) and
// choose whether Go holds an adapter — which is the ONLY difference between the
// Go-direct and IPC dispatch paths as far as this keying is concerned.
func newDescentTestScheduler(t *testing.T, root string, runner StageRunner, engine *RetryEngine, adapter adapters.SkillRunner) *Scheduler {
	t.Helper()
	commitPipelineSkillFixtures(t, root)
	return &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   engine,
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, adapter),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
	}
}

// TestRunPipeline_GoDirectXaiRungRidesTheRetryAndNoOtherAdapter pins BOTH
// scheduler lines that turn the provider-keyed descent into real dispatch
// behaviour, on the path where Go holds the adapter and therefore knows
// first-hand who executes.
//
// The run is mixed-adapter, exactly the shape of the issue: issue-pickup
// dispatches the fable band on GROK and the API refuses it; every later stage
// runs on CLAUDE and resolves to the same substituted tier.
//
//   - The rejection arm must key the ladder on the executing adapter, or no
//     same-model descent is recorded at all and the retry re-dispatches at the
//     effort the envelope chain resolved.
//   - The sticky lookup must key on it too, in BOTH directions: the grok retry
//     gets the descended rung, and the claude stages — resolving to the very
//     same tier — must not. Pinning only the first direction would pass on a
//     hardcoded "xai"; pinning only the second would pass on a hardcoded "".
func TestRunPipeline_GoDirectXaiRungRidesTheRetryAndNoOtherAdapter(t *testing.T) {
	isolateRoutingEnv(t)
	t.Setenv("NIGHTGAUGE_ADAPTER", "")
	t.Setenv("NIGHTGAUGE_GROK_EFFORT", "")
	root := gitWorkspace(t)

	adapterByStage := map[state.PipelineStage]string{}
	for _, stage := range descentFixtureStages {
		adapterByStage[stage] = "claude"
	}
	adapterByStage[state.StageIssuePickup] = "grok"
	writeDescentFixtureConfig(t, root, adapterByStage)

	engine := NewRetryEngine(DefaultRetryConfig())
	runner := &descentStageRunner{rejectStage: state.StageIssuePickup}
	s := newDescentTestScheduler(t, root, runner, engine, adapters.NewClaudeAdapter())
	if !s.execMgr.HasAdapter() {
		t.Fatal("fixture: this must be the Go-direct path — execMgr must hold an adapter")
	}

	s.runPipeline(context.Background(), types.BoardItem{Number: 611, Repo: "nightgauge/nightgauge", ID: "item-611"})

	calls := runner.captured()
	pickup := dispatchesFor(calls, state.StageIssuePickup)
	if len(pickup) != 2 {
		t.Fatalf("issue-pickup dispatched %d times, want 2 (rejected, then retried on the substituted tier); calls=%d", len(pickup), len(calls))
	}
	if pickup[0].Model != "fable" || pickup[0].Effort != "low" {
		t.Fatalf("first grok dispatch = %s@%s, want fable@low — the fixture must start off the descended rung", pickup[0].Model, pickup[0].Effort)
	}

	// The rejection walked xai's ladder: grok-4.6 serves both bands, so the
	// substitution is the SAME model one declared effort rung lower.
	if got := engine.StickyEffort("xai", pickup[1].Model); got != "high" {
		t.Fatalf("StickyEffort(xai, %s) = %q, want high — the rejection must key the ladder on the executing adapter", pickup[1].Model, got)
	}
	if pickup[1].Model != "opus" || pickup[1].Effort != "high" {
		t.Fatalf("grok retry = %s@%s, want opus@high — the descended rung must reach the dispatch", pickup[1].Model, pickup[1].Effort)
	}

	// Every later stage resolves to the same substituted tier on claude. The
	// xai rung is a point on xai's ladder and must say nothing about what
	// anthropic dispatches.
	later := 0
	for _, params := range calls {
		if params.Stage == state.StageIssuePickup {
			continue
		}
		later++
		if params.Model != "opus" {
			t.Errorf("stage %s model = %q, want opus — the tier substitution is provider-agnostic", params.Stage, params.Model)
		}
		if params.Effort == "high" {
			t.Errorf("stage %s dispatched on claude with Effort = high — an xai descent must never become another provider's wire effort", params.Stage)
		}
	}
	if later == 0 {
		t.Fatal("no post-pickup stage dispatched; the fixture is wrong, not the assertion")
	}
}

// TestRunPipeline_IpcDescentKeysOffTheAdapterTheExtensionActuallyRan is the
// #611 finding-2 regression at the scheduler.
//
// In IPC mode Go holds no adapter and MUST NOT re-derive one: the extension's
// resolveStageAdapter has an AutoProviderRouter rung and a stage-start
// walkAdapterFallback that config.ResolveStageAdapter cannot see, so a Go-side
// mirror is blind for an auto-router-selected grok and wrong whenever the
// walker hops. The evidence used instead is what the adapter process itself
// reported — the concrete served id — which is why this fixture configures NO
// adapter at all and still reaches the descent.
//
// The control arm is the same fixture reporting an anthropic served id: the
// rung must not be applied, or the implementation is keying everything to xai.
func TestRunPipeline_IpcDescentKeysOffTheAdapterTheExtensionActuallyRan(t *testing.T) {
	run := func(t *testing.T, served string) []StageRunParams {
		t.Helper()
		isolateRoutingEnv(t)
		t.Setenv("NIGHTGAUGE_ADAPTER", "")
		root := gitWorkspace(t)
		writeDescentFixtureConfig(t, root, nil)

		engine := NewRetryEngine(DefaultRetryConfig())
		runner := &descentStageRunner{
			rejectStage: state.StageIssuePickup,
			servedModel: served,
			engine:      engine,
		}
		s := newDescentTestScheduler(t, root, runner, engine, nil)
		if s.execMgr.HasAdapter() {
			t.Fatal("fixture: this must be the IPC path — execMgr must hold no adapter")
		}
		s.runPipeline(context.Background(), types.BoardItem{Number: 611, Repo: "nightgauge/nightgauge", ID: "item-611"})
		return runner.captured()
	}

	t.Run("grok served id reaches the descent", func(t *testing.T) {
		pickup := dispatchesFor(run(t, "grok-4.6"), state.StageIssuePickup)
		if len(pickup) != 2 {
			t.Fatalf("issue-pickup dispatched %d times, want 2", len(pickup))
		}
		if pickup[0].Effort != "low" {
			t.Fatalf("first dispatch effort = %q, want low", pickup[0].Effort)
		}
		if pickup[1].Model != "opus" || pickup[1].Effort != "high" {
			t.Fatalf("retry = %s@%s, want opus@high — an extension-side xai dispatch must reach the #606 rung", pickup[1].Model, pickup[1].Effort)
		}
	})

	t.Run("anthropic served id keeps its own envelope effort", func(t *testing.T) {
		pickup := dispatchesFor(run(t, "claude-opus-5"), state.StageIssuePickup)
		if len(pickup) != 2 {
			t.Fatalf("issue-pickup dispatched %d times, want 2", len(pickup))
		}
		if pickup[1].Effort == "high" {
			t.Fatalf("retry effort = high — a cross-model anthropic fallback records no rung, so nothing may be substituted")
		}
	})
}
