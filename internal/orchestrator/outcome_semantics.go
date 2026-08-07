package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Corpus semantics for the learning/calibration outcome corpus
// (`<targetRepoRoot>/.nightgauge/pipeline/history/outcomes.jsonl`), shared by
// its TWO writers — Scheduler.recordOutcome on the autonomous path and the
// `pipeline.notifyComplete` handler in internal/ipc on the extension path.
//
// The corpus is schema-less JSONL with no producer discriminator, and every
// consumer (learning.Recorder.Calibrate, the calibration loop verdict,
// `nightgauge learn tune`) compares the two halves of a pair for EQUALITY. So
// the two writers must agree on what each field means, or one field carries two
// meanings and the resulting "accuracy" is arithmetic over a category error.
//
// Three rules hold everywhere, and the helpers below are the only place they
// are expressed:
//
//  1. ONE VOCABULARY per pair. Sizes are small|medium|large; models are
//     registry bands (haiku|sonnet|opus|fable) with unregistered models passed
//     through verbatim. A pair written in two vocabularies reports a *measured*
//     0% forever — strictly worse than no data, because the reader stops saying
//     "bootstrapping" and starts asserting a number that can never move.
//
//  2. ABSENT MEANS EMPTY. An unknown value is "", never a plausible-looking
//     default. Consumers exclude a pair with an empty half from their
//     denominators; a fabricated value is counted as a measurement. This is not
//     hypothetical: every pre-#304 row spells an unscored run's predicted size
//     "small" (SizeBucketForScore(0)) and both model fields "", so those rows
//     scored a routing HIT ("" == "") on evidence of nothing.
//
//  3. AN ACTUAL IS A MEASUREMENT. The `actual*` half must be something the run
//     produced, never a second reading of the same pre-run inputs the
//     prediction came from. A comparison between two functions of (size label,
//     priority, change type) measures the arithmetic, not the router.

// OutcomeModelStage is the stage the corpus's model pair is about.
//
// The prediction half is routing.pickup_recommendation.dev_model — the router's
// recommendation for the IMPLEMENTATION stage — so the measured half must be
// the model that stage actually served. Attributing the run to some other stage
// (the terminal one, the alphabetically first one, or the one that dominated
// cost) compares two different quantities: on this machine's real history the
// dominant-cost stage is feature-dev in well under half of runs, so a run that
// dies in issue-pickup on opus would book a routing MISS for a stage that never
// ran, and no routing improvement could move it.
const OutcomeModelStage = state.StageFeatureDev

// OutcomeModelBand normalizes a model reference onto its registry band so the
// router's alias ("sonnet") and a concrete served id ("claude-sonnet-5") are
// comparable.
//
// Three outcomes, deliberately distinct: a registry-known reference collapses to
// its band; an unregistered model (a user-defined local model) passes through
// VERBATIM, because an unrecognized name is still attribution and collapsing it
// to "" would relabel real data as missing; only an absent model yields "".
func OutcomeModelBand(model string) string {
	if model == "" {
		return ""
	}
	if tier := NormalizeModelTier(model); tier != "" {
		return tier
	}
	return model
}

// OutcomeServedDevModel returns the model the run's implementation stage
// ACTUALLY served, or "" when that stage never ran or reported no model.
//
// #91: when the CLI silently retried a safety-refused turn on a fallback model,
// the fallback is what produced the output. The scheduler re-records StageModels
// with the served model, so the map already carries it on that path; the
// explicit scan makes the same measurement available from a snapshot that
// captured the swap without the re-record, and — critically — scopes the
// override to the implementation stage. A run-level "last refusal anywhere"
// override would attribute a pr-merge swap to a feature-dev prediction.
func OutcomeServedDevModel(snap *state.RuntimeState) string {
	if snap == nil {
		return ""
	}
	for i := len(snap.ModelRefusalFallbacks) - 1; i >= 0; i-- {
		fb := snap.ModelRefusalFallbacks[i]
		if fb.Stage == string(OutcomeModelStage) && fb.FallbackModel != "" {
			return fb.FallbackModel
		}
	}
	return snap.StageModel(OutcomeModelStage)
}

// OutcomePredictedSize expresses the router's pre-run size prediction in the
// corpus's small|medium|large vocabulary, or "" when the run carried no size
// input to predict from.
//
// Absence is derived from the INPUTS, not from a score sentinel. complexity
// scores are clamped to [1,8] and default to 3 (the M base score) for an issue
// with no size:* label — both in routing.CoerceRouting and in the extension's
// changeAnalyzer — so score==0 essentially never occurs in the field and a
// guard keyed on it is dead code that lets ~95% of real runs record a
// fabricated "small". A run whose size term was the router's default has no
// size prediction to score, so it records none.
func OutcomePredictedSize(sizeLabel string, complexityScore int) string {
	if routing.SizeBaseScore(sizeLabel) <= 0 {
		return "" // no recognized size:* input — the score's size term is a default
	}
	if complexityScore <= 0 {
		return "" // unscored
	}
	return SizeBucketForScore(complexityScore)
}

// NOTE ON THE ACTUAL SIZE — there is deliberately no helper here, because
// neither writer can honestly produce one.
//
// The corpus's actualSize means "how big the change the run produced turned out
// to be": the codebase's own non-circular definition is
// OutcomeService.getActualSizeBucket (internal/github/outcome.go), which buckets
// by lines ACTUALLY changed and is fed from the merged PR's line count by
// `nightgauge outcome record`.
//
// Nothing at either terminal recording boundary carries that measurement. The
// V2 run record has file COUNTS (files.read_count / files.written_count), never
// line counts, and computing a diff at terminal time is not a substitute: on the
// success path pr-merge has already landed the branch, so `git diff <base>`
// reports ~0 and would book every merged run as "small" — a confidently wrong
// measurement, which is worse than an absent one.
//
// So both writers leave actualSize EMPTY and every consumer excludes the pair
// from its denominator. What must never come back is the label-derived
// substitute the round-2 review caught: bucketing the issue's own size:* label
// makes actual a second reading of the SAME pre-run inputs the prediction came
// from (complexity_score = fib_round(SIZE_MAP[size] × PRIORITY_MULT[priority])),
// so the comparison measures the arithmetic and yields permanent structural
// misses — size:M + priority:critical scores 5 → predicted "medium" against an
// "actual" of "small", for a run the router sized exactly right. See
// docs/SELF_IMPROVEMENT_LOOP.md § Outcome Recording.
