package stages

import "context"

// Phase reporting for the deterministic stage runners (#1247).
//
// Phase markers are a SKILL protocol: the model prints
// `<!-- phase:start name="…" index=N total=M stage="…" -->` and the extension
// parses it. A deterministic stage has no model in the loop, so it printed
// nothing — and `phaseInference.STAGE_RULES` covers only `feature-dev` and
// `feature-planning`, so nothing inferred them either. The observable result
// was a `pr-merge` stage that showed 0/14 phases for its entire run and then
// rendered all fourteen `skipped` on success.
//
// The runners do not need a marker protocol to know where they are: they ARE
// the work. They report their waypoints directly through PhaseReporter, which
// the scheduler backs with RuntimeState (the durable run record) and the live
// IPC phase events.

// PhaseReporter receives phase transitions from a deterministic stage runner.
// The scheduler's implementation writes them to the run's RuntimeState and
// fans them out to the live view; tests substitute a recorder.
//
// Every method takes the stage explicitly because a single runner instance is
// shared across concurrent runs — nothing here may be kept on the runner.
type PhaseReporter interface {
	PhaseStart(stage, name string, index, total int)
	PhaseComplete(stage, name string)
	PhaseFail(stage, name string, index, total int)
	PhaseSkip(stage, name string, index, total int)
}

type phaseReporterKey struct{}

// WithPhaseReporter attaches a reporter to ctx.
//
// Carried on the context rather than set on the runner because the scheduler
// holds ONE runner instance for every concurrent run (s.prMergeRunner), so a
// per-run field would be a data race that silently mis-attributes another
// issue's phases. The PRMergeRunner / PRCreateRunner interfaces are unchanged,
// so every existing fake keeps working and simply reports nothing.
func WithPhaseReporter(ctx context.Context, r PhaseReporter) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, phaseReporterKey{}, r)
}

// phaseReporterFrom returns the reporter on ctx, or nil when none was attached
// (the `pr-stage` CLI verb and every unit test that does not care).
func phaseReporterFrom(ctx context.Context) PhaseReporter {
	r, _ := ctx.Value(phaseReporterKey{}).(PhaseReporter)
	return r
}

// phaseRole says who is accountable for a registry phase on the deterministic
// path. Every phase in a stage's registry carries exactly one — asserted by
// TestPhaseSpecsCoverEveryRegistryPhase — because the alternative is inferring
// the leftovers, and inference is what produced "14/14 skipped" for a stage
// that ran.
type phaseRole int

const (
	// phaseRunner: the deterministic runner performs this work and reports it.
	phaseRunner phaseRole = iota
	// phaseCaller: the work exists on the deterministic path but is done by the
	// runner's CALLER, not the runner — pr-merge's branch/worktree teardown
	// lives in the scheduler (cleanupMergedRemoteBranch), so the runner must
	// not claim it and must not skip it either. A caller that does not report
	// it leaves it for the end-of-stage back-fill to record `unreported`, which
	// is the honest reading of "nobody said anything about it".
	phaseCaller
	// phaseOffPath: the phase exists ONLY on the LLM path. Recording it
	// `skipped` on a deterministic success is a true statement — the stage
	// decided not to run it — and is emitted only on the success path, never
	// on a punt (see AC5 / puntPreservesPhases in the tests).
	phaseOffPath
)

// stagePhaseSpec mirrors one stage's entry in PHASE_REGISTRY
// (packages/nightgauge-sdk/src/events/phaseRegistry.ts), which stays the
// single source of truth for names and ordering.
// TestPhaseRegistryParityWithTypeScript reads that file and fails on drift.
type stagePhaseSpec struct {
	stage string
	order []string
	roles map[string]phaseRole
}

func (s stagePhaseSpec) total() int { return len(s.order) }

func (s stagePhaseSpec) index(name string) int {
	for i, n := range s.order {
		if n == name {
			return i
		}
	}
	return -1
}

// prMergePhases mirrors PHASE_REGISTRY["pr-merge"].
//
// The off-path set is written out by hand on purpose. Six of these are the
// LLM's own judgement work — reading reviews, categorising them, editing code
// in response, retrying a failed check, writing a retro note, grading itself —
// and the deterministic path has no analogue for any of them. The other three
// are structural: the runner does no batch detection, validates no
// environment, and returns a struct rather than narrating a summary.
var prMergePhases = stagePhaseSpec{
	stage: "pr-merge",
	order: []string{
		"read-pr-context", "batch-detection", "validate-environment", "ci-gate",
		"auto-fix-retry", "fetch-reviews", "categorize-issues", "address-feedback",
		"freshness-check", "merge", "post-merge-cleanup", "retrospective-feedback",
		"output-summary", "self-assessment",
	},
	roles: map[string]phaseRole{
		"read-pr-context":        phaseRunner,
		"batch-detection":        phaseOffPath,
		"validate-environment":   phaseOffPath,
		"ci-gate":                phaseRunner,
		"auto-fix-retry":         phaseOffPath,
		"fetch-reviews":          phaseOffPath,
		"categorize-issues":      phaseOffPath,
		"address-feedback":       phaseOffPath,
		"freshness-check":        phaseRunner,
		"merge":                  phaseRunner,
		"post-merge-cleanup":     phaseCaller,
		"retrospective-feedback": phaseOffPath,
		"output-summary":         phaseOffPath,
		"self-assessment":        phaseOffPath,
	},
}

// prCreatePhases mirrors PHASE_REGISTRY["pr-create"].
//
// `build-knowledge-section` and `build-what-to-test-section` are off-path even
// though RenderBody emits a Knowledge block: the block arrives PRE-RENDERED in
// PRCreateSnapshot.KnowledgeSection, so the runner copies it rather than
// building it, and claiming otherwise would credit the deterministic path with
// work it never did.
var prCreatePhases = stagePhaseSpec{
	stage: "pr-create",
	order: []string{
		"auto-merge-guard", "load-context", "batch-detection", "build-knowledge-section",
		"build-what-to-test-section", "preflight-checks", "proactive-main-merge",
		"security-rescan", "scope-drift-gate", "create-pr", "verify-pr-created",
		"monitor-ci-status", "write-context", "self-assessment",
	},
	roles: map[string]phaseRole{
		"auto-merge-guard":           phaseOffPath,
		"load-context":               phaseRunner,
		"batch-detection":            phaseOffPath,
		"build-knowledge-section":    phaseOffPath,
		"build-what-to-test-section": phaseOffPath,
		"preflight-checks":           phaseRunner,
		"proactive-main-merge":       phaseOffPath,
		"security-rescan":            phaseOffPath,
		"scope-drift-gate":           phaseOffPath,
		"create-pr":                  phaseRunner,
		"verify-pr-created":          phaseRunner,
		"monitor-ci-status":          phaseOffPath,
		"write-context":              phaseRunner,
		"self-assessment":            phaseOffPath,
	},
}

// phaseSpecs indexes the specs by stage name for the parity test.
var phaseSpecs = []stagePhaseSpec{prMergePhases, prCreatePhases}

// phaseEmitter is the runner-facing handle. Nil-reporter safe so every call
// site can be unconditional — the `pr-stage` CLI verb and the existing unit
// tests run with no reporter attached and must behave exactly as before.
type phaseEmitter struct {
	r    PhaseReporter
	spec stagePhaseSpec
	// inFlight is the phase currently started and not yet settled. A runner
	// that returns while one is open would leave a `running` record that
	// CloseRunningPhases later rewrites to `abandoned` (#1009) — the exact
	// "where did it get stuck?" lie that bug was about — so every exit path
	// goes through settle/failInFlight.
	inFlight string
}

func newPhaseEmitter(ctx context.Context, spec stagePhaseSpec) *phaseEmitter {
	return &phaseEmitter{r: phaseReporterFrom(ctx), spec: spec}
}

func (e *phaseEmitter) start(name string) {
	if e.r == nil {
		return
	}
	e.failInFlight()
	e.inFlight = name
	e.r.PhaseStart(e.spec.stage, name, e.spec.index(name), e.spec.total())
}

func (e *phaseEmitter) complete(name string) {
	if e.r == nil {
		return
	}
	if e.inFlight == name {
		e.inFlight = ""
	}
	e.r.PhaseComplete(e.spec.stage, name)
}

// failInFlight closes whatever phase is open as `failed`.
//
// Called on every punt. "Failed" is the truthful record of a deterministic
// ATTEMPT that did not get through — it does not claim the phase cannot be
// done, and it does not block the skill from starting the same phase fresh and
// completing it. What it must never do here is write `skipped`: the LLM path
// is about to run that phase for real, and a skip would say the stage decided
// not to.
func (e *phaseEmitter) failInFlight() {
	if e.r == nil || e.inFlight == "" {
		return
	}
	name := e.inFlight
	e.inFlight = ""
	e.r.PhaseFail(e.spec.stage, name, e.spec.index(name), e.spec.total())
}

// skipOffPath records every LLM-only phase as `skipped`.
//
// ONLY legal on a terminal deterministic success. On a punt the skill runs and
// genuinely executes these phases; SkipPhase is first-writer-wins and
// append-only, so a premature skip would permanently outrank the skill's real
// record — the run would end asserting the stage declined work it demonstrably
// did (AC5).
func (e *phaseEmitter) skipOffPath() {
	if e.r == nil {
		return
	}
	e.failInFlight()
	for i, name := range e.spec.order {
		if e.spec.roles[name] == phaseOffPath {
			e.r.PhaseSkip(e.spec.stage, name, i, e.spec.total())
		}
	}
}

// skip records one named phase as skipped — used for a waypoint the runner
// deliberately did not need to perform (pr-merge's `merge` when the PR was
// already MERGED before the runner looked).
func (e *phaseEmitter) skip(name string) {
	if e.r == nil {
		return
	}
	if e.inFlight == name {
		e.inFlight = ""
	}
	e.r.PhaseSkip(e.spec.stage, name, e.spec.index(name), e.spec.total())
}

// PhasePosition returns the 0-based registry index and the stage's phase total
// for a named phase, or (-1, 0) when the stage or phase is unknown.
//
// Exported for the ONE phase whose work lives outside the runner:
// pr-merge's post-merge-cleanup, done by the scheduler after the runner
// returns. It exists so that caller reads the position from this table rather
// than hard-coding `10, 14` at the call site, where a registry change would
// silently mis-index it.
func PhasePosition(stage, name string) (int, int) {
	for _, spec := range phaseSpecs {
		if spec.stage != stage {
			continue
		}
		i := spec.index(name)
		if i < 0 {
			return -1, 0
		}
		return i, spec.total()
	}
	return -1, 0
}
