package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// scheduler_effort_descent_test.go pins #611: the executing adapter's identity
// where the #606 effort descent is KEYED and where it is CONSUMED.
//
// Two defects, one root cause — the descent machinery could not name the
// provider a dispatch runs on:
//
//  1. downgradeEfforts was keyed by TIER alone, so an xai rung's effort became
//     the wire effort of the next stage that resolved to the substituted tier
//     on ANY adapter. The value is a legal EFFORT_LEVELS member, so the wrong
//     one dispatches with no error and no log.
//  2. StageRunParams did not carry the adapter, so IpcStageRunner resolved
//     every extension-side rejection against anthropic and no xai dispatch
//     could ever reach the descent.

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
// With the executing adapter on the envelope, the band dispatch reproduces the
// golden cell exactly.
func TestDowngradeDescent_XaiGoldenCellHoldsForABandDispatch(t *testing.T) {
	want := descentGoldenCell(t, "grok-4.6")

	// The Go-direct path already reached this cell via the concrete id.
	byID := NewRetryEngine(DefaultRetryConfig()).EvaluateDowngrade("grok-4.6")
	if got := downgradeCell(byID); got != want {
		t.Fatalf("fixture: concrete-id descent = %q, want the golden cell %q", got, want)
	}

	// The wire-band dispatch, keyed by the adapter that executes it, must land
	// on the identical cell.
	byBand := NewRetryEngine(DefaultRetryConfig()).
		EvaluateDowngradeForProvider("fable", DowngradeProviderForAdapter("grok"))
	if got := downgradeCell(byBand); got != want {
		t.Fatalf("band dispatch on a grok adapter = %q, want the golden xai cell %q — the descent must not depend on the wire naming a concrete id", got, want)
	}
	if !byBand.SameModelDescent || byBand.NewEffort != byID.NewEffort {
		t.Fatalf("band dispatch envelope = %+v, want the same rung as %+v", byBand, byID)
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

// TestRunPipeline_DispatchCarriesTheExecutingAdapter pins the single
// production line that puts the executing adapter on the dispatch envelope
// (`Adapter: dispatchAdapter` at runPipeline's StageRunParams construction).
//
// This fixture is IPC-mode — execMgr holds no adapter, exactly as in VSCode —
// which is the case the value could not previously be derived for at all. The
// scheduler resolves it from the canonical #54 chain, the same one the VSCode
// per-stage resolver reads, so the wire names who actually executes rather than
// a second guess.
//
// Without this, the field is deletable with every suite green: the engine tests
// assert behaviour GIVEN a provider, and the IpcStageRunner test asserts the
// consumption given an Adapter. Nothing else observes where the value comes
// from.
func TestRunPipeline_DispatchCarriesTheExecutingAdapter(t *testing.T) {
	root := gitWorkspace(t)
	isolateRoutingEnv(t)
	t.Setenv("NIGHTGAUGE_ADAPTER", "")
	writeStageAdapterConfig(t, root, "grok")

	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	if s.execMgr.HasAdapter() {
		t.Fatal("fixture: this must be the IPC path — execMgr must hold no adapter")
	}

	item := types.BoardItem{Number: 611, Repo: "nightgauge/nightgauge", ID: "item-611"}
	s.runPipeline(context.Background(), item)

	calls := runner.captured()
	if len(calls) == 0 {
		t.Fatal("no stage was dispatched; the fixture is wrong, not the assertion")
	}
	for _, params := range calls {
		if params.Adapter != "grok" {
			t.Errorf("stage %s dispatched with Adapter = %q, want grok — the executing adapter must ride the envelope", params.Stage, params.Adapter)
		}
	}

	// And an unresolvable adapter stays honestly empty rather than defaulting
	// to something the extension never spawns: "" is what every consumer reads
	// as the historical anthropic inference.
	bare := gitWorkspace(t)
	bareRunner := &runIDCapturingRunner{}
	bs := newRunIdentityTestScheduler(t, bare, bareRunner)
	bs.runPipeline(context.Background(), types.BoardItem{Number: 612, Repo: "nightgauge/nightgauge", ID: "item-612"})
	bareCalls := bareRunner.captured()
	if len(bareCalls) == 0 {
		t.Fatal("no stage was dispatched from the config-less workspace")
	}
	for _, params := range bareCalls {
		if params.Adapter != "" {
			t.Errorf("config-less stage %s carried Adapter = %q, want \"\"", params.Stage, params.Adapter)
		}
	}
}

// writeStageAdapterConfig installs a project-tier config whose ui.core.adapter
// names the execution adapter for every stage — the canonical #54 global rung.
func writeStageAdapterConfig(t *testing.T, root, adapter string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	body := "owner: nightgauge\nrepo: nightgauge\nproject: 1\nui:\n  core:\n    adapter: " + adapter + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}
