package main

import (
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// autonomousConfigOverrides carries the handful of values an entry point
// resolves for itself before config.yaml is consulted. `nightgauge autonomous
// run` sets every field from its CLI flags (which have already fallen back to
// config.yaml where the flag was not passed), so an explicit `--budget 0`
// stays 0 instead of being re-filled from the file. `serve` has no flags and
// passes the zero struct, leaving config.yaml and the package defaults to
// decide.
type autonomousConfigOverrides struct {
	ScanInterval  *time.Duration
	MaxConcurrent *int
	BudgetCeiling *int64
	DryRun        *bool
	AllowSelfRepo *bool
}

// buildAutonomousConfig is the single construction site for
// orchestrator.AutonomousConfig.
//
// Both entry points that attach an autonomous scheduler call it: the `serve`
// daemon (behind the scheduler lease) and `nightgauge autonomous run`. They
// used to assemble the struct by hand, independently, and the two literals
// drifted — `auto_actionable`, `trusted_author_associations`,
// `disable_epic_blockedby_cascade` and the three `refinement_*` keys were read
// only on the `run` path and silently ignored under `serve`, which is the
// primary long-running daemon (#1445). One builder means a new config.yaml key
// cannot be honoured by one entry point and dropped by the other.
func buildAutonomousConfig(cfg *config.Config, over autonomousConfigOverrides) orchestrator.AutonomousConfig {
	autoCfg := orchestrator.DefaultAutonomousConfig()
	autoCfg.MaxConcurrent = config.ResolvedMaxConcurrent(cfg)
	if cfg != nil && cfg.Autonomous != nil {
		autoCfg.ExcludeLabels = cfg.Autonomous.ResolvedExcludeLabels()
		if d := cfg.Autonomous.ScanInterval.Duration(); d > 0 {
			autoCfg.ScanInterval = d
		}
		if cfg.Autonomous.BudgetCeiling > 0 {
			autoCfg.BudgetCeiling = cfg.Autonomous.BudgetCeiling
		}
		if cfg.Autonomous.DryRun != nil {
			autoCfg.DryRun = *cfg.Autonomous.DryRun
		}
		if cfg.Autonomous.PickupBacklog != nil {
			autoCfg.PickupBacklog = *cfg.Autonomous.PickupBacklog
		}
		if cfg.Autonomous.AllowSelfRepo {
			autoCfg.AllowSelfRepo = true
		}
		if cfg.Autonomous.RefinementEnabled != nil {
			autoCfg.RefinementEnabled = *cfg.Autonomous.RefinementEnabled
		}
		// 30s is the floor, not a default: a shorter interval abuses the
		// GitHub rate limit, so it is ignored rather than clamped.
		if d := cfg.Autonomous.RefinementInterval.Duration(); d >= 30*time.Second {
			autoCfg.RefinementInterval = d
		}
		if cfg.Autonomous.RefinementMaxConcurrent > 0 {
			autoCfg.RefinementMaxConcurrent = cfg.Autonomous.RefinementMaxConcurrent
		}
		if cfg.Autonomous.AutoActionable != nil {
			autoCfg.AutoActionable = *cfg.Autonomous.AutoActionable
		}
		// Unset/empty means "use the built-in trusted set", which the
		// orchestrator resolves from a nil slice.
		autoCfg.TrustedAuthorAssociations = cfg.Autonomous.TrustedAuthorAssociations
		if cfg.Autonomous.DisableEpicBlockedByCascade {
			autoCfg.DisableEpicBlockedByCascade = true
		}
		if cfg.Autonomous.SafetyRails != nil {
			src := cfg.Autonomous.SafetyRails
			autoCfg.SafetyRails = &orchestrator.SafetyConfig{
				BudgetCeiling:     src.BudgetCeiling,
				CircuitBreakerMax: src.CircuitBreakerMax,
				RateLimitPerHour:  src.RateLimitPerHour,
				// Resolved, not copied: an omitted key must keep the default.
				EpicCheckpoint: config.ResolveEpicCheckpoint(cfg),
				HealthGateMin:  src.HealthGateMin,
			}
		}
	}
	if cfg != nil {
		rc := config.ResolveConcurrency(cfg)
		autoCfg.PerRepoMax = rc.PerRepoMax
		if cfg.Concurrency != nil && len(cfg.Concurrency.RepositoryOverrides) > 0 {
			autoCfg.RepositoryMaxConcurrent = cfg.Concurrency.RepositoryOverrides
		}
	}
	applyStuckEpicConfig(&autoCfg, cfg)

	if over.ScanInterval != nil {
		autoCfg.ScanInterval = *over.ScanInterval
	}
	if over.MaxConcurrent != nil {
		autoCfg.MaxConcurrent = *over.MaxConcurrent
	}
	if over.BudgetCeiling != nil {
		autoCfg.BudgetCeiling = *over.BudgetCeiling
	}
	if over.DryRun != nil {
		autoCfg.DryRun = *over.DryRun
	}
	if over.AllowSelfRepo != nil {
		autoCfg.AllowSelfRepo = *over.AllowSelfRepo
	}
	return autoCfg
}
