package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// scheduler_effort_envelope_test.go pins #633: the ORDER in which runPipeline's
// dispatch-envelope block (scheduler.go, the wireEffort/effortAttr computation)
// composes the effort axis of a dispatch.
//
// #606 wired the effort half of the envelope through the scheduler, and every
// seam it threads is unit-tested on its own — grokEnvEffortSet suppression,
// routing.ClampEffortToEnvelope, RetryEngine.StickyEffort, resolveWireEffort.
// What no unit test can see is the COMPOSITION: the block applies them in one
// specific order, and a reordering keeps every isolated test green while
// changing what actually dispatches.
//
// The four links, in the order the block applies them:
//
//  1. the operator's NIGHTGAUGE_GROK_EFFORT override SUPPRESSES the Go-resolved
//     wire effort outright on an xai dispatch (the adapter dispatches the env
//     value; resolving a competing one would mis-attribute a value that never
//     went out);
//  2. otherwise resolveWireEffort resolves the explicit chain and CLAMPS it into
//     the mode envelope;
//  3. a same-model descent's sticky rung is substituted LAST — after the clamp,
//     because a mode bound re-raising (or re-lowering) what an API rejection
//     just moved would re-fail identically — and never over the operator
//     override;
//  4. the surviving value lands on StageRunParams.Effort, which is what the
//     executor runs.
//
// The fixture separates the values so no assertion here is reachable from more
// than one of them: the explicit chain resolves `xhigh`, the shipped efficiency
// envelope clamps that to `medium`, xai's same-model descent rung is `high`,
// and the operator override is `max`. A dispatch reading `medium` can only have
// come from the clamp; one reading `high` can only have come from the rung
// applied after it.
const (
	// effortEnvelopeConfigured is what the explicit effort chain resolves
	// (NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT, stageEffortConfig step 3) —
	// deliberately ABOVE the efficiency ceiling so the clamp has work to do.
	effortEnvelopeConfigured = "xhigh"
	// effortEnvelopeClamped is what routing.Envelope(efficiency).EffortCeiling
	// leaves of it. Asserted against the shipped table below, not trusted.
	effortEnvelopeClamped = "medium"
	// effortEnvelopeRung is the effort xai's same-model descent records for the
	// substituted tier — ABOVE the same ceiling, which is the whole point: the
	// substitution runs after the clamp, so it is not bound by it.
	effortEnvelopeRung = "high"
	// effortEnvelopeOperator is the NIGHTGAUGE_GROK_EFFORT value. Distinct from
	// all three above so a dispatch that leaked it would be unmistakable.
	effortEnvelopeOperator = "max"
)

// runEffortEnvelopeFixture drives a real runPipeline through the composition and
// returns every dispatch it captured, in order.
//
// The run is Go-direct on grok for every stage: that is the one dispatch shape
// where all four links are live at once (the xai arm is the only one that reads
// NIGHTGAUGE_GROK_EFFORT, and execMgr's adapter is what names the provider the
// sticky rung is keyed under).
//
// rejectPickup arms the descent the way production does — the run's OWN
// rejection arm evaluates and records it — because runPipeline resets the
// RetryEngine at run start, so a pre-seeded rung would never survive to the
// dispatch under test. That makes this a stage-failing fixture, which arms the
// #3873 reconcile; newDescentTestScheduler stubs the forge for every fixture in
// this package (#660 is why), and the slug below is deliberately not an issue a
// human could ever close.
func runEffortEnvelopeFixture(t *testing.T, grokEnvEffort string, rejectPickup bool) []StageRunParams {
	t.Helper()
	isolateRoutingEnv(t)
	t.Setenv("NIGHTGAUGE_ADAPTER", "")
	t.Setenv("NIGHTGAUGE_GROK_EFFORT", grokEnvEffort)
	// Efficiency is the shipped mode that declares an effort CEILING and pins
	// no stage, so resolveWireEffort reaches its clamp branch rather than
	// short-circuiting on a mode pin.
	t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", string(routing.ModeEfficiency))
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT", effortEnvelopeConfigured)

	// Fixture premises, read from the shipped tables. If #606's envelope table
	// or xai's ladder moves, this fails HERE — naming the fixture — instead of
	// looking like an ordering regression further down.
	env := routing.Envelope(routing.ModeEfficiency)
	if env.EffortFloor != "" || env.EffortCeiling != effortEnvelopeClamped {
		t.Fatalf("fixture: efficiency envelope = %+v, want a bare %q ceiling", env, effortEnvelopeClamped)
	}
	if got := routing.ClampEffortToEnvelope(effortEnvelopeConfigured, env); got != effortEnvelopeClamped {
		t.Fatalf("fixture: clamp(%q) = %q, want %q", effortEnvelopeConfigured, got, effortEnvelopeClamped)
	}
	if dg := NewRetryEngine(DefaultRetryConfig()).
		EvaluateDowngradeForProvider("fable", DowngradeProviderForAdapter("grok")); !dg.SameModelDescent ||
		dg.NewTier != "opus" || dg.NewEffort != effortEnvelopeRung {
		t.Fatalf("fixture: xai fable descent = %+v, want a same-model rung opus@%q", dg, effortEnvelopeRung)
	}

	root := gitWorkspace(t)
	adapterByStage := map[state.PipelineStage]string{}
	for _, stage := range descentFixtureStages {
		adapterByStage[stage] = "grok"
	}
	writeDescentFixtureConfig(t, root, adapterByStage)

	runner := &descentStageRunner{}
	if rejectPickup {
		runner.rejectStage = state.StageIssuePickup
	}
	s := newDescentTestScheduler(t, root, runner, NewRetryEngine(DefaultRetryConfig()), adapters.NewClaudeAdapter())
	if !s.execMgr.HasAdapter() {
		t.Fatal("fixture: this must be the Go-direct path — execMgr must hold an adapter")
	}

	// Not a real slug: this fixture fails a stage on purpose, and a fixture
	// whose verdict can be changed by someone closing an issue is the #660 bug.
	s.runPipeline(context.Background(), types.BoardItem{Number: 990633, Repo: "nightgauge/nightgauge", ID: "item-990633"})

	calls := runner.captured()
	wantPickups := 1
	if rejectPickup {
		wantPickups = 2
	}
	if got := len(dispatchesFor(calls, state.StageIssuePickup)); got != wantPickups {
		t.Fatalf("issue-pickup dispatched %d times, want %d (calls=%d); the fixture is wrong, not the assertion", got, wantPickups, len(calls))
	}
	return calls
}

// assertEveryDispatchEffort pins the composed value on the field the executor
// actually reads — link 4 — for every dispatch the run made.
func assertEveryDispatchEffort(t *testing.T, calls []StageRunParams, want string) {
	t.Helper()
	for i, params := range calls {
		if params.Effort != want {
			t.Errorf("dispatch %d (stage %s) = %s@%q, want Effort %q", i, params.Stage, params.Model, params.Effort, want)
		}
	}
}

// TestRunPipeline_EffortEnvelopeClampsBeforeSubstitutingTheDescentRung is the
// #633 composition guard. Each sub-test isolates one link of the order, and
// each expected value is reachable from exactly one of them:
//
//   - no descent → the CLAMP's output (`medium`), a value no input to the run
//     carries;
//   - descent recorded mid-run → the RUNG (`high`) from the retry onward, which
//     is above the clamp's ceiling and therefore observable only if the
//     substitution runs after it;
//   - operator override set → NOTHING, on both sides of the descent, because
//     the override suppresses the resolution and the rung alike.
//
// Swapping the clamp and the sticky substitution in scheduler.go turns the
// second red; dropping either grokEnvEffortSet guard turns the third red;
// dropping the clamp turns the first red.
func TestRunPipeline_EffortEnvelopeClampsBeforeSubstitutingTheDescentRung(t *testing.T) {
	t.Run("the mode ceiling clamps the resolved wire effort onto every dispatch", func(t *testing.T) {
		assertEveryDispatchEffort(t, runEffortEnvelopeFixture(t, "", false), effortEnvelopeClamped)
	})

	t.Run("the descent rung is substituted after the clamp, not into it", func(t *testing.T) {
		calls := runEffortEnvelopeFixture(t, "", true)
		pickup := dispatchesFor(calls, state.StageIssuePickup)

		// Before the descent: the clamp's own answer.
		if pickup[0].Model != "fable" || pickup[0].Effort != effortEnvelopeClamped {
			t.Fatalf("first dispatch = %s@%q, want fable@%q — the clamped explicit chain", pickup[0].Model, pickup[0].Effort, effortEnvelopeClamped)
		}
		// After it: the rung, ABOVE the ceiling that had just bound the same
		// stage. A clamp applied after the substitution would read `medium`
		// here — the same value the pre-descent dispatch carries — which is
		// exactly the reordering this assertion exists to catch.
		if pickup[1].Model != "opus" || pickup[1].Effort != effortEnvelopeRung {
			t.Fatalf("retry = %s@%q, want opus@%q — the rung must survive the mode ceiling", pickup[1].Model, pickup[1].Effort, effortEnvelopeRung)
		}

		// Every later stage resolves to the same substituted tier and reads the
		// same rung, so the ordering is pinned run-wide rather than on one
		// retry.
		later := 0
		for _, params := range calls {
			if params.Stage == state.StageIssuePickup {
				continue
			}
			later++
			if params.Model != "opus" || params.Effort != effortEnvelopeRung {
				t.Errorf("stage %s = %s@%q, want opus@%q", params.Stage, params.Model, params.Effort, effortEnvelopeRung)
			}
		}
		if later == 0 {
			t.Fatal("no post-pickup stage dispatched; the fixture is wrong, not the assertion")
		}
	})

	t.Run("the grok operator override suppresses both the resolution and the rung", func(t *testing.T) {
		calls := runEffortEnvelopeFixture(t, effortEnvelopeOperator, true)

		// The descent is recorded here too — the rejection arm does not consult
		// the env override — so this arm is not vacuous: there IS a rung, and
		// the tier substitution reaching the retry is the proof of it.
		pickup := dispatchesFor(calls, state.StageIssuePickup)
		if pickup[1].Model != "opus" {
			t.Fatalf("retry model = %q, want opus — without a recorded descent this arm would not test the rung suppression at all", pickup[1].Model)
		}
		// Nothing on the wire, on either side of the descent: the adapter
		// dispatches the operator's value, and the envelope must neither
		// compete with it nor mis-attribute one that never went out.
		assertEveryDispatchEffort(t, calls, "")
	})
}
