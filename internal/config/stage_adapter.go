package config

import "strings"

// AdapterResolution names where a resolved stage adapter came from —
// mirroring the VSCode resolver's AdapterSource vocabulary (#54).
type AdapterResolution struct {
	Adapter string // collapsed adapter name ("" when nothing resolved)
	Source  string // "stage-env" | "adapter-env" | "stage-config" | "global-config" | ""
}

// ResolveStageAdapter resolves the execution adapter for a pipeline stage
// through the canonical precedence chain shared by all three layers (#54):
//
//  1. NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE>  (per-stage env — most specific)
//  2. NIGHTGAUGE_ADAPTER                          (per-invocation override)
//  3. pipeline.stage_adapters.<stage>             (config)
//  4. ui.core.adapter                             (config global default)
//
// Returns an empty Adapter when nothing resolves — the caller applies its
// layer default (Go: claude-headless via the adapter registry). An explicit
// --adapter flag outranks all of these and never reaches this function.
// getenv is injectable for tests (pass os.Getenv in production).
func ResolveStageAdapter(cfg *Config, stage string, getenv func(string) string) AdapterResolution {
	envKey := "NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_" +
		strings.ToUpper(strings.ReplaceAll(stage, "-", "_"))
	if v := strings.TrimSpace(getenv(envKey)); v != "" {
		return AdapterResolution{Adapter: v, Source: "stage-env"}
	}
	if v := strings.TrimSpace(getenv("NIGHTGAUGE_ADAPTER")); v != "" {
		return AdapterResolution{Adapter: v, Source: "adapter-env"}
	}
	if cfg != nil && cfg.Pipeline != nil {
		if v := strings.TrimSpace(cfg.Pipeline.StageAdapters[stage]); v != "" {
			return AdapterResolution{Adapter: v, Source: "stage-config"}
		}
	}
	if cfg != nil && cfg.UI != nil && cfg.UI.Core != nil {
		if v := strings.TrimSpace(cfg.UI.Core.Adapter); v != "" {
			return AdapterResolution{Adapter: v, Source: "global-config"}
		}
	}
	return AdapterResolution{}
}
