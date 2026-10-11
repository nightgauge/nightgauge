package config

import (
	"strings"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
)

// ResolveDefaultModel resolves the workspace-wide model fallback,
// ui.core.default_model (`resolveModel` Step 3, getDefaultModel in
// modelResolver.ts):
//
//  1. NIGHTGAUGE_UI_CORE_DEFAULT_MODEL  env override
//  2. ui.core.default_model             config
//
// A value that is not one of the registry's tier bands is ignored at either
// layer rather than dispatched. "" means neither layer named one. The stage
// dispatch path (orchestrator.stageBaseModel) and a conversation turn
// (#1569) both read it here, so the two cannot disagree. getenv is injectable
// for tests.
func ResolveDefaultModel(cfg *Config, getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL")); routing.IsTierBand(v) {
		return v
	}
	if cfg != nil && cfg.UI != nil && cfg.UI.Core != nil {
		if v := strings.TrimSpace(cfg.UI.Core.DefaultModel); routing.IsTierBand(v) {
			return v
		}
	}
	return ""
}
