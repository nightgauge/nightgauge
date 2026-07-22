// Package orchestrator — configvalidate.go performs semantic config-coherence
// checks on the autonomous scheduler configuration. Unlike the structural
// validator in internal/config/config.go (which enforces range and type
// constraints), this file checks cross-field relationships that only make
// sense at runtime — e.g., whether per-repo policy keys match the resolved
// enabled-repos allowlist.
//
// Warnings are advisory-only and never block scheduler startup.
// Issue #3640.
package orchestrator

import "fmt"

// ConfigWarning describes a single config-coherence finding.
type ConfigWarning struct {
	Severity string `json:"severity"` // "warn" | "info"
	Kind     string `json:"kind"`     // "policy-without-allowlist" | "concurrency-cap" | "audit-drift"
	Message  string `json:"message"`
}

// ValidateAutonomousConfig checks cross-field semantic coherence of the
// autonomous scheduler config. Returns zero or more non-fatal warnings.
// Never panics — any internal error is swallowed and an empty slice returned.
//
// enabledRepos is the resolved set of fully-qualified repo names the scheduler
// will scan (e.g. ["nightgauge/nightgauge", "nightgauge/platform"]). When
// nil or empty the scheduler scans all repos — no allowlist to be inconsistent
// with, so Warnings 1 and 2 are no-ops.
//
// auditRepos and ciRepos are reserved for a follow-up (Warning 3, audit-drift).
// Pass nil to skip that check (deferred per ADR-004).
func ValidateAutonomousConfig(cfg AutonomousConfig, enabledRepos []string, auditRepos []string, ciRepos []string) (warnings []ConfigWarning) {
	defer func() {
		if r := recover(); r != nil {
			// Validation must never crash the scheduler.
			warnings = nil
		}
	}()

	// No allowlist → no incoherence possible for Warnings 1 and 2.
	if len(enabledRepos) == 0 {
		return nil
	}

	// Build a short-name set from the enabled repos so we can compare against
	// per-repo policy keys (which may be short or fully-qualified).
	enabledSet := make(map[string]bool, len(enabledRepos))
	for _, r := range enabledRepos {
		enabledSet[shortRepoName(r)] = true
	}

	// Warning 1: Policy-without-allowlist
	// A repo has a per-repo policy (sequential or max_concurrent) but is NOT
	// in enabled_repos. It will be silently excluded from scans.
	seenWarn := make(map[string]bool) // deduplicate per repo name

	for repo, maxC := range cfg.RepositoryMaxConcurrent {
		short := shortRepoName(repo)
		if !enabledSet[short] && !seenWarn[short] {
			seenWarn[short] = true
			warnings = append(warnings, ConfigWarning{
				Severity: "warn",
				Kind:     "policy-without-allowlist",
				Message: fmt.Sprintf(
					"repo %q has per-repo policy (max_concurrent=%d) but is not in "+
						"autonomous.enabled_repos — it will be silently excluded from scans; "+
						"add it to enabled_repos or remove the per-repo policy.",
					short, maxC),
			})
		}
	}

	// Warning 2: Concurrency-vs-sequential cap
	// Count enabled repos that have an effective per-repo cap of 1 (either via
	// RepositorySequential or RepositoryMaxConcurrent == 1). When
	// cfg.MaxConcurrent is below that count, the global cap is the binding
	// constraint and per-repo sequential policy is redundant.
	sequentialCount := 0
	countedSeq := make(map[string]bool)

	for repo, maxC := range cfg.RepositoryMaxConcurrent {
		short := shortRepoName(repo)
		if maxC == 1 && enabledSet[short] && !countedSeq[short] {
			countedSeq[short] = true
			sequentialCount++
		}
	}

	if cfg.MaxConcurrent > 0 && sequentialCount > 0 && cfg.MaxConcurrent < sequentialCount {
		warnings = append(warnings, ConfigWarning{
			Severity: "warn",
			Kind:     "concurrency-cap",
			Message: fmt.Sprintf(
				"max_concurrent=%d but %d enabled repos have sequential=true — "+
					"cross-repo parallelism is limited to max_concurrent; "+
					"consider raising max_concurrent to %d.",
				cfg.MaxConcurrent, sequentialCount, sequentialCount),
		})
	}

	// Warning 3: Audit-drift — deferred (ADR-004).
	// auditRepos and ciRepos parameters are reserved for a follow-up PR.
	_ = auditRepos
	_ = ciRepos

	return warnings
}

// shortRepoName strips the owner prefix from "owner/repo", returning only the
// short name. Names without "/" are returned as-is.
func shortRepoName(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}
