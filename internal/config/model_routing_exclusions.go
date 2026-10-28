package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// exclusionPinProvider is the provider a band-named pin is resolved against
// when checking it for conflicts — the Go dispatch band wire's provider
// (routing.BandWireProvider; config cannot import routing).
const exclusionPinProvider = "anthropic"

const stageModelEnvPrefix = "NIGHTGAUGE_PIPELINE_STAGE_MODEL_"

// ValidateModelRoutingExclusions checks `model_routing.excluded_models` and
// `excluded_efforts` (#2063) at load, so a typo or a contradiction fails where
// the operator sees it instead of silently excluding nothing:
//
//   - every excluded model must be a registry id (the error lists them);
//   - every excluded effort must be on the effort ladder;
//   - an explicit pin — `pipeline.stage_models.<stage>`, its
//     NIGHTGAUGE_PIPELINE_STAGE_MODEL_<STAGE> override, or
//     `model_routing.stage_efforts.<stage>` — naming an excluded value is an
//     error naming the key. A pin is the operator choosing; exclusions only
//     bound what AUTOMATIC routing may choose, so neither silently wins.
//
// environ is os.Environ()-shaped. All findings are joined into one error.
func ValidateModelRoutingExclusions(cfg *Config, environ []string) error {
	if cfg == nil || cfg.ModelRouting == nil {
		return nil
	}
	mr := cfg.ModelRouting
	if len(mr.ExcludedModels) == 0 && len(mr.ExcludedEfforts) == 0 {
		return nil
	}
	var errs []error
	excludedModels := map[string]bool{}
	for _, id := range mr.ExcludedModels {
		id = strings.TrimSpace(id)
		if !isRegistryModelID(id) {
			errs = append(errs, fmt.Errorf("model_routing.excluded_models: unknown model id %q (valid: %s)",
				id, strings.Join(registryModelIDs(), ", ")))
			continue
		}
		excludedModels[id] = true
	}
	excludedEfforts := map[string]bool{}
	for _, e := range mr.ExcludedEfforts {
		e = strings.TrimSpace(e)
		if !isEffortLevel(e) {
			errs = append(errs, fmt.Errorf("model_routing.excluded_efforts: unknown effort %q (valid: %s)",
				e, strings.Join(models.EffortOrder, ", ")))
			continue
		}
		excludedEfforts[e] = true
	}

	pinExcluded := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if id := pinnedModelID(value); id != "" && excludedModels[id] {
			errs = append(errs, fmt.Errorf("%s: %q resolves to %s, which model_routing.excluded_models excludes",
				key, value, id))
		}
	}
	if cfg.Pipeline != nil {
		for _, stage := range sortedKeys(cfg.Pipeline.StageModels) {
			pinExcluded("pipeline.stage_models."+stage, cfg.Pipeline.StageModels[stage])
		}
	}
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, stageModelEnvPrefix) {
			pinExcluded(key, value)
		}
	}
	for _, stage := range sortedKeys(mr.StageEfforts) {
		if e := strings.TrimSpace(mr.StageEfforts[stage]); excludedEfforts[e] {
			errs = append(errs, fmt.Errorf("model_routing.stage_efforts.%s: %q is excluded by model_routing.excluded_efforts",
				stage, e))
		}
	}
	return errors.Join(errs...)
}

// validateModelRoutingExclusionsFromEnv is the Load-time entry point.
func validateModelRoutingExclusionsFromEnv(cfg *Config) error {
	return ValidateModelRoutingExclusions(cfg, os.Environ())
}

// pinnedModelID is the registry id a pin dispatches: the id itself, or the
// model serving a band name on the band wire's provider. "" when unknown.
func pinnedModelID(value string) string {
	desc, ok := models.Resolve(exclusionPinProvider, value)
	if !ok {
		return ""
	}
	return desc.ID
}

func isRegistryModelID(id string) bool {
	for _, m := range models.All() {
		if m.ID == id {
			return true
		}
	}
	return false
}

func registryModelIDs() []string {
	ids := make([]string, 0, len(models.All()))
	for _, m := range models.All() {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}

func isEffortLevel(e string) bool {
	for _, l := range models.EffortOrder {
		if l == e {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
