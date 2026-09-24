package sizeGate

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// gateYAML is the YAML shape of the pipeline.size_gate config section.
type gateYAML struct {
	Pipeline struct {
		SizeGate struct {
			Enabled           *bool `yaml:"enabled"`
			RejectOnOversized *bool `yaml:"reject_on_oversized"`
			Thresholds        struct {
				MaxLocInTitle      *int `yaml:"max_loc_in_title"`
				DecomposedItemsMin *int `yaml:"decomposed_items_min"`
			} `yaml:"thresholds"`
			Heuristics struct {
				LocPatternEnabled         *bool `yaml:"loc_pattern_enabled"`
				DecompositionCheckEnabled *bool `yaml:"decomposition_check_enabled"`
			} `yaml:"heuristics"`
			Routes struct {
				RejectAction           string   `yaml:"reject_action"`
				CapacityFallbackModels []string `yaml:"capacity_fallback_models"`
			} `yaml:"routes"`
		} `yaml:"size_gate"`
	} `yaml:"pipeline"`
}

// LoadGateConfigFromYAML reads pipeline.size_gate from the YAML config file,
// applying defaults for any missing fields. When the file is absent or cannot
// be parsed, all defaults are used — the gate is never disabled by a missing
// config. `nightgauge size-gate` and the scheduler's dispatch-time capacity
// check both read the gate's config through this one function.
func LoadGateConfigFromYAML(configPath string) GateConfig {
	cfg := DefaultGateConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg // config absent — use defaults
	}

	var y gateYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return cfg // parse error — use defaults
	}

	sg := y.Pipeline.SizeGate

	// Respect explicit disable: if enabled is explicitly false, disable all heuristics.
	if sg.Enabled != nil && !*sg.Enabled {
		cfg.LocPatternEnabled = false
		cfg.DecompositionCheckEnabled = false
		cfg.CapacityEnabled = false
		return cfg
	}

	if sg.RejectOnOversized != nil {
		cfg.RejectOnOversized = *sg.RejectOnOversized
	}
	if sg.Thresholds.MaxLocInTitle != nil {
		cfg.MaxLocInTitle = *sg.Thresholds.MaxLocInTitle
	}
	if sg.Thresholds.DecomposedItemsMin != nil {
		cfg.DecomposedItemsMin = *sg.Thresholds.DecomposedItemsMin
	}
	if sg.Heuristics.LocPatternEnabled != nil {
		cfg.LocPatternEnabled = *sg.Heuristics.LocPatternEnabled
	}
	if sg.Heuristics.DecompositionCheckEnabled != nil {
		cfg.DecompositionCheckEnabled = *sg.Heuristics.DecompositionCheckEnabled
	}
	cfg.SoftRoute = strings.TrimSpace(sg.Routes.RejectAction) == "soft-route"
	for _, m := range sg.Routes.CapacityFallbackModels {
		if m = strings.TrimSpace(m); m != "" {
			cfg.CapacityFallbackModels = append(cfg.CapacityFallbackModels, m)
		}
	}

	return cfg
}
