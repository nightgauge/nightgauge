package baselineGate

import (
	"os"

	"gopkg.in/yaml.v3"
)

// gateYAML is the YAML shape for the pipeline.baseline_ci_gate config section.
type gateYAML struct {
	Pipeline struct {
		BaselineCIGate struct {
			Enabled        *bool `yaml:"enabled"`
			LookbackRuns   *int  `yaml:"lookback_runs"`
			RedThreshold   *int  `yaml:"red_threshold"`
			GreenThreshold *int  `yaml:"green_threshold"`
		} `yaml:"baseline_ci_gate"`
	} `yaml:"pipeline"`
}

// LoadGateConfigFromYAML reads pipeline.baseline_ci_gate from the YAML config
// file, applying defaults for missing fields. When the file is absent,
// defaults are used; the gate is never disabled by a missing config.
//
// Lives here rather than in cmd/nightgauge (#885) because it now has two
// callers: the `baseline-gate` CLI verbs and the autonomous daemon's promote
// sweep. A per-caller copy is how the gate would come to mean one thing on
// the CLI and another in the daemon — the exact split #885 exists to close.
func LoadGateConfigFromYAML(configPath string) GateConfig {
	cfg := DefaultGateConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg
	}
	var y gateYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return cfg
	}
	bg := y.Pipeline.BaselineCIGate
	if bg.Enabled != nil {
		cfg.Enabled = *bg.Enabled
	}
	if bg.LookbackRuns != nil {
		cfg.LookbackRuns = *bg.LookbackRuns
	}
	if bg.RedThreshold != nil {
		cfg.RedThreshold = *bg.RedThreshold
	}
	if bg.GreenThreshold != nil {
		cfg.GreenThreshold = *bg.GreenThreshold
	}
	return cfg
}
