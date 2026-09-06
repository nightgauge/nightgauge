package orchestrator

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/models"
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
//     registry bands (haiku|sonnet|opus|fable) and nothing else. A pair written
//     in two vocabularies reports a *measured* 0% forever — strictly worse than
//     no data, because the reader stops saying "bootstrapping" and starts
//     asserting a number that can never move.
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
// comparable. It is the PREDICTION half's normalizer; the measured half goes
// through OutcomeActualBand, which needs the prediction to invert a many-to-one
// adapter mapping.
//
// A model the registry has no band for records "" — absent, excluded from every
// consumer's denominator. This is deliberately NOT the verbatim pass-through it
// was before #340: the pair is compared for EQUALITY against a band, so a
// verbatim id ("gemini-2.0-flash", a user-configured local model) is not
// "attribution the corpus keeps", it is a guaranteed MISS the router never made
// — a fabricated measurement, which rule 2 exists to prevent. Attribution of
// what actually ran lives in the run record's per-stage model_selection, which
// keeps the concrete id.
func OutcomeModelBand(model string) string {
	if model == "" {
		return ""
	}
	return NormalizeModelTier(model)
}

// OutcomeActualBand expresses the model a run actually SERVED in the band
// vocabulary its prediction is written in.
//
// The naive normalization is wrong for every non-Claude adapter, and wrong in
// the direction that manufactures misses. Go dispatches a BAND ("opus"); the
// extension translates it at the last mile — `resolveCodexPipelineModel` for
// codex and the registry-backed `getAdapterModelForBand` for gemini/
// gemini-sdk/copilot in every performance mode — and reports the concrete id
// back, which the scheduler re-records as the stage's model. Those ids are
// MULTI-BAND — gpt-5.6-sol serves [opus, fable], gemini-2.5-pro serves
// [opus, fable] — so
// a strongest-band collapse reads "fable" for a run the router predicted "opus"
// and the adapter served exactly as asked. Every such run would book a routing
// MISS, feeding the calibration loop with systematic garbage.
//
// So the mapping is inverted through the registry instead of collapsed: when
// the served model serves the predicted band, THAT is the band it was launched
// for, and the run is a HIT. A model that serves neither the predicted band nor
// any other (unregistered) records "" — an honest unknown, excluded — rather
// than a value guaranteed to compare unequal.
//
// This function does NOT assume translation succeeded. Both the Go executor
// and extension dispatch paths translate recognized bands for codex/gemini/
// gemini-sdk/copilot in every mode, so a gemini run asked for opus normally
// serves gemini-2.5-pro and books a HIT. A provider fallback can still serve a
// weaker registered model and correctly book a MISS. Local adapters are the
// deliberate exception: lm-studio has no registry tier hierarchy and launches
// its configured model, which records an honest band or unknown on that basis.
//
// The comparison and the recording therefore happen in ONE space each, on
// purpose: divergence is judged in the concrete-id space where the adapter
// actually launched a process (utils/skillRunner.ts reports the id that ran),
// and the corpus records the band, because the prediction it is scored against
// is a band. See docs/OUTCOME_RECORDING.md.
func OutcomeActualBand(served, predicted string) string {
	band := OutcomeModelBand(served)
	if band == "" {
		return ""
	}
	predictedBand := OutcomeModelBand(predicted)
	if predictedBand == "" || predictedBand == band {
		return band
	}
	if desc, ok := models.Get(served); ok && desc.HasTier(predictedBand) {
		// One concrete model serves both bands — the adapter maps the predicted
		// band onto exactly this id, so this IS the predicted band served.
		return predictedBand
	}
	return band
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

// OutcomePredictedModelDiagnostic and OutcomeActualModelDiagnostic are the
// operator-facing explanations for an empty corpus model band — the ONE place
// each sentence is written, because the corpus has two writers
// (Scheduler.recordOutcome and the internal/ipc notifyComplete handler) and a
// diagnostic that disagrees between them is a second, quieter drift.
//
// They exist because an empty band has THREE causes, not one, and the log text
// used to assert the first:
//
//  1. nothing was predicted / the stage reported nothing — genuinely absent;
//  2. (predicted half) the router recorded a recommendation the registry has no
//     band for;
//  3. (actual half) the stage DID serve a model, and its id has no registry
//     band — every codex `gpt-5.5`, gemini, lm-studio and ollama workspace.
//
// Since OutcomeModelBand stopped passing unregistered ids through verbatim
// (they book a guaranteed miss, which rule 2 above forbids), cause 3 is the
// COMMON one on non-Claude workspaces — and "the feature-dev stage reported no
// served model" told exactly those operators that the stage never ran. That is
// the misdiagnosis this pair removes: an id is named, and the band-only rule is
// stated, so "excluded" is distinguishable from "missing".
//
// Callers prefix their own context ("#12: " / "notifyComplete: #12 ") and log
// the returned sentence verbatim.
func OutcomePredictedModelDiagnostic(issueNumber int, predicted string) string {
	if strings.TrimSpace(predicted) == "" {
		return fmt.Sprintf(
			"learning outcome has no PREDICTED model — issue-%d.json carried no routing.pickup_recommendation.dev_model, so model routing cannot be calibrated for this run (#304)",
			issueNumber)
	}
	return fmt.Sprintf(
		"learning outcome has no PREDICTED model — issue-%d.json recommended %q, which the model registry has no band for; the corpus pair is compared band to band (%s), so an unregistered id is EXCLUDED from the accuracy denominator rather than booked as a miss (#340)",
		issueNumber, predicted, models.BandAlternation())
}

// OutcomeActualModelDiagnostic — see OutcomePredictedModelDiagnostic.
func OutcomeActualModelDiagnostic(served string) string {
	if strings.TrimSpace(served) == "" {
		return fmt.Sprintf(
			"learning outcome has no ACTUAL model — the %s stage reported no served model, so this run measures nothing about model routing (#304)",
			OutcomeModelStage)
	}
	return fmt.Sprintf(
		"learning outcome has no ACTUAL model — the %s stage served %q, which the model registry has no band for; the corpus pair is compared band to band (%s), so an unregistered id (a local model, or a provider id the registry does not carry) is EXCLUDED from the accuracy denominator rather than booked as a miss (#340)",
		OutcomeModelStage, served, models.BandAlternation())
}

// OutcomeSizeInput resolves the size term the router itself scored the issue
// on, in the router's OWN order: the project board Size field, then a `size:*`
// label, then "" — nothing recognized, so `complexity_score`'s size term was
// the M default (routing.resolveSize, internal/intelligence/routing/derive.go).
//
// It exists because a shared helper whose ARGUMENT differs per caller is not
// shared. Round 3's writers both called OutcomePredictedSize and still keyed
// absence on two routinely-disagreeing sources: the scheduler passed the board
// Size field, the extension passed the size:* label. For one issue with board
// Size=L and no label that is "medium" on one writer and "" on the other — one
// corpus field, two presence rules, no discriminator to tell the rows apart.
//
// INPUT AVAILABILITY IS NOT THE SAME AS THE RULE, and only the rule is shared.
// The autonomous scheduler has both terms (the board item it dispatched). The
// extension path has only the labels: issue-{N}.json carries `labels` and
// `routing`, never the board Size field, so its board term is always "" and a
// board-sized, unlabelled issue records no size prediction there. That gap is
// an input the extension does not receive, not a second definition — see
// docs/SELF_IMPROVEMENT_LOOP.md § Outcome Recording for the follow-up.
func OutcomeSizeInput(boardSize string, labels []string) string {
	if routing.SizeBaseScore(boardSize) > 0 {
		return boardSize
	}
	for _, l := range labels {
		lower := strings.ToLower(strings.TrimSpace(l))
		if !strings.HasPrefix(lower, "size:") {
			continue
		}
		if size := strings.TrimPrefix(lower, "size:"); routing.SizeBaseScore(size) > 0 {
			return size
		}
	}
	return ""
}

// Size sources, in the precedence order ResolveRunSize applies (#1515). The
// resolved source is recorded on the run record (`size_source`) and on the
// learning outcome (`sizeSource`), because a corpus field whose provenance is
// unrecorded is a field with several meanings and no discriminator — the exact
// defect rule 1 above exists to prevent.
const (
	// SizeSourceLabel: the issue's own board Size field or `size:*` label —
	// the term the ROUTER scored, and the only source whose complexity score
	// is a function of the size.
	SizeSourceLabel = "label"
	// SizeSourcePlanner: `complexity_assessment` in the run's own
	// planning-{N}.json. An assessment made by an agent that had read the
	// issue and the code, which is strictly more evidence than the label
	// carries — but the router did not see it, so the run's complexity score
	// is NOT derived from it.
	SizeSourcePlanner = "planner"
	// SizeSourceEstimator: complexity.Estimator over the issue's own metadata.
	// The weakest source, and the last resort.
	SizeSourceEstimator = "estimator"
)

// SizeResolution is the outcome of the three-source size precedence (#1515).
//
// Size/Source are the resolved answer; the three per-source fields are kept
// because a DISAGREEMENT is itself learnable. An issue labelled size:S whose
// planner assessed L is the most interesting row in the corpus — it is the
// router's input being wrong — and collapsing it to one winner throws away the
// only evidence that the two disagreed.
type SizeResolution struct {
	// Size is the winning XS|S|M|L|XL bucket, or "" when no source had one.
	Size string
	// Source is one of SizeSourceLabel/Planner/Estimator, or "" when Size is.
	Source string
	// LabelSize / PlannerSize / EstimatorSize are the per-source values, each
	// "" when that source produced nothing recognized.
	LabelSize     string
	PlannerSize   string
	EstimatorSize string
}

// Disagrees reports a label and a planner assessment that name different
// buckets. Nothing relabels on a disagreement — the label is the human's, and
// an agent silently overwriting it is a worse failure than the disagreement —
// but both halves are recorded so the disagreement can be measured.
func (r SizeResolution) Disagrees() bool {
	return r.LabelSize != "" && r.PlannerSize != "" && r.LabelSize != r.PlannerSize
}

// ResolveRunSize applies the three-source precedence: the issue's own size term
// (board Size field, then `size:*` label — OutcomeSizeInput's order), then the
// planner's assessment, then the estimator's score-derived bucket.
//
// Each candidate is validated against the recognized bucket set
// (routing.SizeBaseScore > 0) before it can win, so a plan that wrote
// "medium" or "Large" contributes nothing rather than a bucket no reader
// recognizes.
//
// Both terminal record writers call this — Scheduler.recordV2History on the
// autonomous path and the notifyComplete handler in internal/ipc on the
// extension path — for the same reason every other helper in this file is
// shared: two writers of one field, each with its own precedence, is one field
// with two meanings.
func ResolveRunSize(boardSize string, labels []string, plannerSize, estimatorSize string) SizeResolution {
	r := SizeResolution{
		LabelSize:     recognizedSize(OutcomeSizeInput(boardSize, labels)),
		PlannerSize:   recognizedSize(plannerSize),
		EstimatorSize: recognizedSize(estimatorSize),
	}
	switch {
	case r.LabelSize != "":
		r.Size, r.Source = r.LabelSize, SizeSourceLabel
	case r.PlannerSize != "":
		r.Size, r.Source = r.PlannerSize, SizeSourcePlanner
	case r.EstimatorSize != "":
		r.Size, r.Source = r.EstimatorSize, SizeSourceEstimator
	}
	return r
}

// RunSizeResolution resolves one run's size from all three sources, reading the
// planner's assessment off the run's own planning-{N}.json.
//
// This is the function BOTH terminal record writers call. It exists rather than
// each writer assembling its own arguments because "a shared helper whose
// ARGUMENT differs per caller is not shared" — the lesson OutcomeSizeInput's
// doc comment records from round 3, where two writers called one helper and
// still keyed absence on two different sources.
//
// boardSize is the project board's Size field, empty on the extension path
// (issue-{N}.json never carries it). title/body feed the estimator; body is
// empty on the extension path too, which is exactly why the estimator's own
// confidence gates it below.
func RunSizeResolution(
	runRoot, worktreeDir, repo string,
	issueNumber int,
	boardSize string,
	labels []string,
	title, body string,
) SizeResolution {
	assessment := execution.LoadPlannerAssessment(runRoot, worktreeDir, repo, issueNumber)
	return ResolveRunSize(
		boardSize,
		labels,
		PlannerSizeFromAssessment(assessment.SizeLabel, assessment.Score),
		EstimatorSize(title, body, labels),
	)
}

// EstimatorSize is the third and weakest size source: complexity.Estimator run
// over the issue's own metadata.
//
// GATED ON THE ESTIMATOR'S OWN CONFIDENCE, and that gate is the point. Estimate
// clamps its score to [1,10] and always names a bucket, so an ungated estimator
// source is available for literally every run — which would make the resolved
// size never absent, retire the #112 "no size at all" warning into dead code,
// and fill the record's join key with a bucket derived from a title's word
// count. A "low" confidence is the estimator saying it had fewer than two
// signals to work with; that is not a measurement, and rule 2 says an unknown
// is spelled "", never a plausible-looking default.
func EstimatorSize(title, body string, labels []string) string {
	score := complexity.NewEstimator().Estimate(complexity.Input{
		Title:  title,
		Body:   body,
		Labels: labels,
	})
	if score.Confidence == "low" {
		return ""
	}
	return score.SizeLabel
}

// PlannerSizeFromAssessment names the bucket a planning context assessed:
// its `size_label` when it wrote one, otherwise the bucket its Fibonacci
// `computed_score` maps to exactly.
//
// The score path is an EXACT inverse (routing.SizeForBaseScore), never a
// nearest match: a score off the 1/2/3/5/8 scale names no bucket, because an
// invented size reaches the corpus indistinguishable from a real one.
func PlannerSizeFromAssessment(sizeLabel string, score int) string {
	if s := recognizedSize(sizeLabel); s != "" {
		return s
	}
	return routing.SizeForBaseScore(score)
}

// recognizedSize normalizes a bucket and returns it only if it is one of the
// five the router scores; everything else is "" — absent, per rule 2.
func recognizedSize(size string) string {
	normalized := strings.ToUpper(strings.TrimSpace(size))
	if routing.SizeBaseScore(normalized) > 0 {
		return normalized
	}
	return ""
}

// OutcomeSizeSource names the source behind OutcomePredictedSize, and is empty
// exactly when that value is.
//
// Provenance for a value that was never recorded is worse than no provenance:
// a row carrying sizeSource "estimator" with predictedSize "" reads, to a
// consumer scanning for populated fields, as a row that has a prediction. The
// two fields are written together or not at all.
func OutcomeSizeSource(res SizeResolution, complexityScore int) string {
	if OutcomePredictedSize(res, complexityScore) == "" {
		return ""
	}
	return res.Source
}

// OutcomePredictedSize expresses the router's pre-run size prediction in the
// corpus's small|medium|large vocabulary, or "" when the run carried no size
// input to predict from.
//
// Takes the RAW inputs, not a pre-resolved size, so neither writer can feed it
// a different quantity than the other (see OutcomeSizeInput).
//
// Absence is derived from the INPUTS, not from a score sentinel. complexity
// scores are clamped to [1,8] and default to 3 (the M base score) for an issue
// with no size input — both in routing.CoerceRouting and in the extension's
// changeAnalyzer — so score==0 essentially never occurs in the field and a
// guard keyed on it is dead code that lets ~95% of real runs record a
// fabricated "small". A run whose size term was the router's default has no
// size prediction to score, so it records none.
func OutcomePredictedSize(res SizeResolution, complexityScore int) string {
	switch res.Source {
	case SizeSourceLabel:
		// The router scored THIS term, so the score is the prediction.
		if complexityScore <= 0 {
			return "" // unscored
		}
		return SizeBucketForScore(complexityScore)
	case SizeSourcePlanner:
		// The router did NOT see the planner's assessment: for a label-less
		// issue its complexity score used the M default, so bucketing the
		// score here would record the default under a new name. The
		// assessment's own base score is the prediction instead — a real size
		// term, expressed in the corpus's vocabulary through the same
		// bucketing the label path uses.
		return SizeBucketForScore(routing.SizeBaseScore(res.Size))
	default:
		// Absent, or estimator-derived. The estimator's bucket is a second
		// reading of the same title/body/labels the prediction would be
		// scored against, and it is available for very nearly every run — so
		// admitting it here would fill the accuracy denominator with rows that
		// measure the arithmetic rather than the router (rules 2 and 3). It is
		// still recorded as the run record's `size` join key, where nothing
		// compares it to a measurement; it just never becomes a PREDICTION.
		return ""
	}
}

// NOTE ON THE ACTUAL SIZE — its shared implementation lives in
// internal/intelligence/actualsize, below the orchestrator import boundary.
// Both dispatch paths capture insertions+deletions against the PR base at
// pr-create exit, while the branch still exists, and persist the raw count on
// RuntimeState. Terminal writers bucket that measurement through the same
// XS/S/M/L/XL thresholds as github.OutcomeService and then through the
// SizeBucketForScore-compatible small/medium/large vocabulary. Runs that never
// reach pr-create leave the measurement EMPTY.
//
// What must never come back is the label-derived
// substitute the round-2 review caught: bucketing the issue's own size:* label
// makes actual a second reading of the SAME pre-run inputs the prediction came
// from (complexity_score = fib_round(SIZE_MAP[size] × PRIORITY_MULT[priority])),
// so the comparison measures the arithmetic and yields permanent structural
// misses — size:M + priority:critical scores 5 → predicted "medium" against an
// "actual" of "small", for a run the router sized exactly right. See
// docs/SELF_IMPROVEMENT_LOOP.md § Outcome Recording.
