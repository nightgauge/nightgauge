package doctor

import (
	"context"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// Learning and automation checks (#2090), native ADR-025 checks with explicit
// causes and declared remedies.

func init() {
	native := func(id, title, code string, timeout time.Duration, run func(ctx context.Context, env *Env) ([]Finding, string)) {
		builtinChecks = append(builtinChecks, Check{
			ID: id, Title: title, Group: "learning", Code: code, Timeout: timeout,
			Run: func(ctx context.Context, env *Env) []Finding {
				fs, detail := run(ctx, env)
				env.SetDetail(id, detail)
				return fs
			},
		})
	}
	// Outcome recording bootstraps this file; `outcome init` writes it now.
	native("complexity_model", "Complexity model", "NGD014", 0,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return complexityModelFindings(env.Cwd)
		})
	// Absence detectors (#992, #1019, #994, #996): work that should have been
	// observed by now and was not.
	native("survival_backlog", "Survival backlog", "NGD026", 0,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			window := survival.DefaultWindowDays
			if env.Cfg != nil {
				window = env.Cfg.Pipeline.ResolveSurvivalWindowDays()
			}
			return survivalBacklogFindings(env.Cwd, env.Now, window)
		})
	native("survival_coverage", "Survival coverage", "NGD027", 0,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return survivalCoverageFindings(env.Cwd)
		})
	native("corpus_calibration", "Corpus calibration", "NGD028", 0,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return corpusCalibrationFindings(env.Cwd)
		})
	native("scheduled_automations", "Scheduled automations", "NGD029", 30*time.Second,
		func(ctx context.Context, env *Env) ([]Finding, string) {
			var declared []cadence.ConfigAutomation
			if env.Cfg != nil {
				declared = env.Cfg.Cadence
			}
			pauses, err := LoadAutomationPauses(env.Cwd)
			if err != nil {
				// An unreadable pause record pauses nothing: every stopped
				// automation reports at full severity.
				pauses = map[string]AutomationPause{}
			}
			return scheduledAutomationFindings(ctx, map[cadence.EvidenceKind]cadenceProbe{
				cadence.EvidenceAutonomousState: autonomousStateEvidence(env.Cwd),
				cadence.EvidenceWorkflowRun:     workflowRunEvidence(env.Client, doctorOwner(env.Cfg), doctorRepo(env.Cfg)),
			}, cadenceScope(env.Cfg, env.Cwd), declared, pauses,
				func() error { return autonomousRestartable(ctx, env.Cwd) }, env.Now)
		})
}
