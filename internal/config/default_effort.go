package config

import (
	"strings"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
)

// Provenance of a resolved workspace default effort (#1567).
const (
	DefaultEffortSourceEnv    = "env"
	DefaultEffortSourceConfig = "config"
	DefaultEffortSourceNone   = ""
)

// ResolveDefaultEffort resolves the workspace-wide default effort, the two
// layers of the stage effort chain that name no stage (getModelDefaultEffort,
// stageResolver.ts):
//
//  1. NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT  env override
//  2. model_routing.default_effort             config
//
// A value off the EFFORT_LEVELS ladder is ignored at either layer, as the TS
// guard ignores it. "" with an empty source means neither layer named one.
// The dispatch path's stage chain (orchestrator.stageEffortConfig) calls this
// for its steps 3 and 4, so the advertised default and the dispatched one
// cannot disagree. getenv is injectable for tests.
func ResolveDefaultEffort(cfg *Config, getenv func(string) string) (effort, source string) {
	if v := strings.TrimSpace(getenv("NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT")); routing.IsEffortLevel(v) {
		return v, DefaultEffortSourceEnv
	}
	if cfg != nil && cfg.ModelRouting != nil {
		if v := strings.TrimSpace(cfg.ModelRouting.DefaultEffort); routing.IsEffortLevel(v) {
			return v, DefaultEffortSourceConfig
		}
	}
	return "", DefaultEffortSourceNone
}
