package config

import (
	"strings"

	"github.com/nightgauge/nightgauge/internal/state"
)

// Shipped defaults that had no Go home before #1517.
//
// A default that lives in four places — the Go structs, the extension's
// DEFAULT_CONFIG, the `nightgauge config init` template and the
// docs/CONFIGURATION.md tables — is four defaults. The audit of 2026-09-06
// found nineteen keys where those four disagreed, and the pattern behind most
// of them was the same: the value existed as a comment ("Default: true") or as
// a number hard-coded at the one call site that needed it, with nothing for a
// second reader to point at.
//
// Everything below is a named constant plus the single resolver that applies
// it, so a caller can never re-derive a default of its own. TestDefaultsAgree
// in defaults_agreement_test.go pins each of them against the other three
// sources.

// DefaultSafetyBudgetCeiling is the global token budget across all autonomous
// pipeline runs. It matches orchestrator.DefaultSafetyConfig().BudgetCeiling.
//
// The struct comment used to read "0 = unlimited", which was true of the field
// and false of the system: the orchestrator substitutes 500_000 for an
// unconfigured rail, so nothing was ever unlimited and an operator reading the
// comment could not tell what they were actually running under.
const DefaultSafetyBudgetCeiling int64 = 500_000

// DefaultSafetyHealthGateMin is the minimum health score (0–100) an autonomous
// workspace must hold to keep dispatching. Matches
// orchestrator.DefaultSafetyConfig().HealthGateMin.
const DefaultSafetyHealthGateMin = 30

// ResolveSafetyBudgetCeiling returns the effective global token ceiling.
//
// Reading SafetyRails.BudgetCeiling directly is the #991 bug: writing a
// safety_rails: block to tune any other rail leaves this one at its zero value,
// and a bare read then reports "no ceiling" for a workspace that never asked to
// remove it.
func ResolveSafetyBudgetCeiling(cfg *Config) int64 {
	if cfg == nil || cfg.Autonomous == nil || cfg.Autonomous.SafetyRails == nil {
		return DefaultSafetyBudgetCeiling
	}
	if v := cfg.Autonomous.SafetyRails.BudgetCeiling; v > 0 {
		return v
	}
	return DefaultSafetyBudgetCeiling
}

// ResolveHealthGateMin returns the effective health-gate floor. Same zero-value
// hazard as ResolveSafetyBudgetCeiling.
func ResolveHealthGateMin(cfg *Config) int {
	if cfg == nil || cfg.Autonomous == nil || cfg.Autonomous.SafetyRails == nil {
		return DefaultSafetyHealthGateMin
	}
	if v := cfg.Autonomous.SafetyRails.HealthGateMin; v > 0 {
		return v
	}
	return DefaultSafetyHealthGateMin
}

// DefaultDebounceRepos is the shipped default for autonomous.debounce_repos:
// only re-query repositories with recent completions between scans.
const DefaultDebounceRepos = true

// ResolveDebounceRepos returns the effective autonomous.debounce_repos setting.
// The field's comment claimed "Default: true" and no resolver existed, so the
// key answered false everywhere it was actually read.
func (a *AutonomousConfig) ResolveDebounceRepos() bool {
	if a == nil || a.DebounceRepos == nil {
		return DefaultDebounceRepos
	}
	return *a.DebounceRepos
}

// DefaultModelRoutingMode is the shipped model_routing.mode: defer every
// stage's base model to the complexity router.
//
// It existed only on the TypeScript side (modelResolver.getModelRoutingMode),
// so a CLI-only workspace with no model_routing block took a different routing
// path from the same repository opened in the extension.
const DefaultModelRoutingMode = "automatic"

// ResolveMode returns the effective model_routing.mode. An empty or
// unrecognised value resolves to DefaultModelRoutingMode rather than being
// passed through — an unknown mode is a typo, and silently routing on it is
// how a workspace ends up on a path nobody chose.
//
// Validity is state.IsModelRoutingMode, the one Go declaration of the
// vocabulary, pinned against the extension's ModelRoutingModeSchema. This
// resolver does NOT consult NIGHTGAUGE_MODEL_ROUTING_MODE; the env var is the
// dispatch path's override and is applied by
// orchestrator.modelRoutingMode ahead of this call.
func (m *ModelRoutingConfig) ResolveMode() string {
	if m == nil {
		return DefaultModelRoutingMode
	}
	if mode := strings.TrimSpace(m.Mode); state.IsModelRoutingMode(mode) {
		return mode
	}
	return DefaultModelRoutingMode
}

// Top-level feedback_loop.* defaults. These were literals inside the IPC
// handler that answers config.getHealthThresholds, which made the extension's
// HealthActionService the de-facto owner of numbers the Go side also enforces.
const (
	// DefaultHealthWarningThreshold is the score below which the health
	// monitor warns.
	DefaultHealthWarningThreshold = 70.0
	// DefaultHealthCriticalThreshold is the score below which it escalates.
	DefaultHealthCriticalThreshold = 50.0
	// DefaultHealthEmergencyThreshold is the score below which it stops work.
	DefaultHealthEmergencyThreshold = 30.0
	// DefaultHealthActionsEnabled lets the monitor take its remediation
	// actions rather than only reporting.
	DefaultHealthActionsEnabled = true
	// DefaultHealthPoliciesEnabled applies the policy table to those actions.
	DefaultHealthPoliciesEnabled = true
	// DefaultFeedbackLoopAutoRetroactive runs the retro pass over completed
	// runs. Read-only analysis; it files nothing on its own.
	DefaultFeedbackLoopAutoRetroactive = true
)

// ResolveWarningThreshold returns the effective feedback_loop
// health_warning_threshold. Safe on a nil receiver.
func (f *FeedbackLoopConfig) ResolveWarningThreshold() float64 {
	if f == nil || f.WarningThreshold == 0 {
		return DefaultHealthWarningThreshold
	}
	return f.WarningThreshold
}

// ResolveCriticalThreshold returns the effective health_critical_threshold.
func (f *FeedbackLoopConfig) ResolveCriticalThreshold() float64 {
	if f == nil || f.CriticalThreshold == 0 {
		return DefaultHealthCriticalThreshold
	}
	return f.CriticalThreshold
}

// ResolveEmergencyThreshold returns the effective health_emergency_threshold.
func (f *FeedbackLoopConfig) ResolveEmergencyThreshold() float64 {
	if f == nil || f.EmergencyThreshold == 0 {
		return DefaultHealthEmergencyThreshold
	}
	return f.EmergencyThreshold
}

// ResolveActionsEnabled returns the effective health_actions_enabled.
func (f *FeedbackLoopConfig) ResolveActionsEnabled() bool {
	if f == nil || f.ActionsEnabled == nil {
		return DefaultHealthActionsEnabled
	}
	return *f.ActionsEnabled
}

// ResolvePoliciesEnabled returns the effective health_policies_enabled.
func (f *FeedbackLoopConfig) ResolvePoliciesEnabled() bool {
	if f == nil || f.PoliciesEnabled == nil {
		return DefaultHealthPoliciesEnabled
	}
	return *f.PoliciesEnabled
}

// ResolveAutoRetroactive returns the effective feedback_loop.auto_retroactive.
func (f *FeedbackLoopConfig) ResolveAutoRetroactive() bool {
	if f == nil || f.AutoRetroactive == nil {
		return DefaultFeedbackLoopAutoRetroactive
	}
	return *f.AutoRetroactive
}
