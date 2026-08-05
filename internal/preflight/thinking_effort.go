// thinking_effort.go gates the Opus 5 thinking/effort interlock (#76).
//
// Opus 5 made two previously independent settings interdependent: disabling
// thinking is accepted only at effort `high` or below, and the pairing returns
// a 400 at `xhigh`/`max`. Nothing in this repo used to couple them, and the
// failure surfaces mid-stage as an opaque provider error the pipeline then
// classifies as a generic stage failure and retries into the same wall.
//
// The reachable path is real, not theoretical: internal/execution/adapters/claude.go
// documents `CLAUDE_CODE_DISABLE_THINKING=1` as an operator escape hatch for
// the #3801 replay bug, and the spawn env is built on os.Environ() — so an
// operator who set it once, plus a stage configured at xhigh, produces the 400.
//
// The rule is registry data (behavior.thinking_disable_max_effort), not a
// model-name check: a future model with different limits is a JSON edit.
package preflight

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// ThinkingEffortSchemaVersion is the JSON envelope version for this gate.
const ThinkingEffortSchemaVersion = 1

// DisableThinkingEnvVar is the Claude Code escape hatch that turns thinking off.
const DisableThinkingEnvVar = "CLAUDE_CODE_DISABLE_THINKING"

// ThinkingEffortFinding is one invalid (model, effort, thinking) combination.
type ThinkingEffortFinding struct {
	// Source describes where the effort came from, e.g. "stage_efforts.feature-dev".
	Source string `json:"source"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// MaxAllowed is the highest effort at which thinking may be disabled.
	MaxAllowed string `json:"max_allowed"`
	Message    string `json:"message"`
}

// ThinkingEffortResult is the gate's stable output shape.
type ThinkingEffortResult struct {
	V int `json:"v"`
	// ThinkingDisabled reports whether the disable escape hatch is active in
	// this environment. When false the gate is a no-op by construction.
	ThinkingDisabled bool                    `json:"thinking_disabled"`
	Checked          []string                `json:"checked"`
	Findings         []ThinkingEffortFinding `json:"findings"`
	Warnings         []string                `json:"warnings"`
}

// ThinkingEffortOptions parameterizes the check. Efforts maps a source label
// (e.g. "stage_efforts.feature-dev") to the resolved (model, effort) pair the
// pipeline would actually use.
type ThinkingEffortOptions struct {
	// Efforts is the set of resolved pairs to validate.
	Efforts map[string]ModelEffort
	// ThinkingDisabledOverride forces the env answer (tests; empty = read env).
	ThinkingDisabledOverride *bool
}

// ModelEffort pairs a resolved model id or tier with a resolved effort level.
type ModelEffort struct {
	Model  string
	Effort string
}

// ThinkingDisabledInEnv reports whether the disable escape hatch is set to a
// truthy value in the current environment.
func ThinkingDisabledInEnv() bool {
	return isTruthy(os.Getenv(DisableThinkingEnvVar))
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// RunThinkingEffortCheck validates every supplied (model, effort) pair against
// the registry's thinking/effort constraint.
//
// Fail-open by construction: with thinking enabled (the default) there is
// nothing to conflict with, and a model with no declared constraint — including
// every local model, which has no registry entry — never produces a finding.
func RunThinkingEffortCheck(opts ThinkingEffortOptions) *ThinkingEffortResult {
	disabled := ThinkingDisabledInEnv()
	if opts.ThinkingDisabledOverride != nil {
		disabled = *opts.ThinkingDisabledOverride
	}

	result := &ThinkingEffortResult{
		V:                ThinkingEffortSchemaVersion,
		ThinkingDisabled: disabled,
		Checked:          []string{},
		Findings:         []ThinkingEffortFinding{},
		Warnings:         []string{},
	}
	if !disabled {
		return result
	}

	for source, pair := range opts.Efforts {
		result.Checked = append(result.Checked, source)
		if pair.Effort == "" || pair.Model == "" {
			continue
		}
		m, ok := models.Get(pair.Model)
		if !ok {
			// Unknown/local model: no constraint to check against. Silence is
			// correct — rejecting would break local runs.
			continue
		}
		conflict, maxAllowed := m.ThinkingDisableConflict(pair.Effort)
		if !conflict {
			continue
		}
		// A "never" ceiling has no effort low enough to satisfy it, so the
		// usual "lower the effort" remedy is not available and must not be
		// offered — unsetting the escape hatch is the only fix.
		message := fmt.Sprintf(
			"%s=%s with %s set: %s rejects disabled thinking above effort %q (HTTP 400). "+
				"Either unset %s, or lower the effort to %q or below.",
			source, pair.Effort, DisableThinkingEnvVar, m.ID, maxAllowed,
			DisableThinkingEnvVar, maxAllowed)
		if maxAllowed == models.ThinkingDisableNever {
			message = fmt.Sprintf(
				"%s=%s with %s set: %s rejects disabled thinking at every effort (HTTP 400). "+
					"Unset %s — no effort level makes this pairing valid.",
				source, pair.Effort, DisableThinkingEnvVar, m.ID, DisableThinkingEnvVar)
		}
		result.Findings = append(result.Findings, ThinkingEffortFinding{
			Source:     source,
			Model:      m.ID,
			Effort:     pair.Effort,
			MaxAllowed: maxAllowed,
			Message:    message,
		})
	}
	return result
}
