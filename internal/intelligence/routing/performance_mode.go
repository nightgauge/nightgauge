package routing

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PerformanceMode represents the three named pipeline performance modes.
type PerformanceMode string

const (
	ModeEfficiency PerformanceMode = "efficiency"
	ModeElevated   PerformanceMode = "elevated"
	ModeMaximum    PerformanceMode = "maximum"
	// ModeFrontier is the premium opt-in tier above Maximum. It routes the
	// intelligence-critical reasoning stages (feature-planning, feature-dev,
	// feature-validate) to Fable 5 — the frontier model at ~2× Opus cost —
	// while keeping the mechanical stages (issue-pickup, pr-create, pr-merge)
	// on Haiku to avoid paying frontier rates for git plumbing. Unlike
	// Maximum, Frontier is never reached by automatic routing; it must be
	// selected deliberately because Fable is the most expensive tier.
	ModeFrontier PerformanceMode = "frontier"
)

// performanceModeState is the persisted YAML shape of performance-mode.yaml.
type performanceModeState struct {
	Mode string `yaml:"mode"`
}

// ResolvePerformanceMode reads the active performance mode.
//
// Precedence (matches TypeScript getPerformanceMode):
//  1. NIGHTGAUGE_PERFORMANCE_MODE env var
//  2. .nightgauge/performance-mode.yaml in workspaceRoot
//  3. ModeElevated (default — no overrides)
//
// Exported (Issue #3215) so the scheduler can capture per-stage mode at
// stage-start for the V2/V3 history record's per-stage performance_mode
// field. The router continues to call this through the package-private
// alias below to avoid disturbing existing call sites.
func ResolvePerformanceMode(workspaceRoot string) PerformanceMode {
	return resolvePerformanceMode(workspaceRoot)
}

func resolvePerformanceMode(workspaceRoot string) PerformanceMode {
	if env := strings.TrimSpace(strings.ToLower(os.Getenv("NIGHTGAUGE_PERFORMANCE_MODE"))); env != "" {
		if m := parseMode(env); m != "" {
			return m
		}
	}

	if workspaceRoot != "" {
		if m := readPerformanceModeFile(filepath.Join(workspaceRoot, ".nightgauge", "performance-mode.yaml")); m != "" {
			return m
		}
	}

	return ModeElevated
}

// DashboardPerformanceMode maps a resolved PerformanceMode to the web
// dashboard's PerformanceMode vocabulary ('efficiency' | 'elevated' |
// 'maximum'). The three named modes pass through verbatim; the premium
// 'frontier' tier has NO dashboard representation, so it (and any unrecognised
// value) maps to "" — telling the emit site to omit `mode` from the wire rather
// than send a value the dashboard can't render (it would surface a misleading
// "Unknown mode" badge). Keep this in sync with the dashboard's PerformanceMode
// type in acme-dashboard/src/app/features/pipelines/pipeline.model.ts.
func DashboardPerformanceMode(m PerformanceMode) string {
	switch m {
	case ModeEfficiency, ModeElevated, ModeMaximum:
		return string(m)
	default:
		// ModeFrontier and any unresolved/unknown value: not representable.
		return ""
	}
}

func readPerformanceModeFile(path string) PerformanceMode {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return ""
	}
	var s performanceModeState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return ""
	}
	return parseMode(s.Mode)
}

func parseMode(s string) PerformanceMode {
	switch PerformanceMode(s) {
	case ModeEfficiency, ModeElevated, ModeMaximum, ModeFrontier:
		return PerformanceMode(s)
	}
	return ""
}

// applyModeOverride applies per-stage model overrides for the given mode.
// Returns the overridden model, or baseModel when the mode imposes no override.
func applyModeOverride(mode PerformanceMode, stage, baseModel string) string {
	switch mode {
	case ModeEfficiency:
		switch stage {
		case "issue-pickup", "pr-create", "pr-merge":
			return ModelHaiku
		case "feature-planning", "feature-dev", "feature-validate":
			return ModelSonnet
		}
	case ModeMaximum:
		return ModelOpus
	case ModeFrontier:
		switch stage {
		case "issue-pickup", "pr-create", "pr-merge":
			return ModelHaiku
		case "feature-planning", "feature-dev", "feature-validate":
			return ModelFable
		}
	case ModeElevated:
		// No overrides — use complexity-based selection
	}
	return baseModel
}
