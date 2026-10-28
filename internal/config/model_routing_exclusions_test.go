package config

import (
	"strings"
	"testing"
)

func exclusionsCfg(mr *ModelRoutingConfig, stageModels map[string]string) *Config {
	cfg := &Config{ModelRouting: mr}
	if stageModels != nil {
		cfg.Pipeline = &PipelineConfig{StageModels: stageModels}
	}
	return cfg
}

func TestValidateModelRoutingExclusions(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *Config
		environ []string
		want    []string // substrings; nil = valid
	}{
		{name: "empty lists pass", cfg: exclusionsCfg(&ModelRoutingConfig{}, map[string]string{"feature-dev": "fable"})},
		{name: "nil model_routing passes", cfg: &Config{}},
		{name: "valid lists pass", cfg: exclusionsCfg(&ModelRoutingConfig{
			ExcludedModels: []string{"claude-fable-5-1"}, ExcludedEfforts: []string{"max"}}, map[string]string{"feature-dev": "opus"})},
		{name: "typo id lists valid ids", cfg: exclusionsCfg(&ModelRoutingConfig{ExcludedModels: []string{"claude-fabel-5-1"}}, nil),
			want: []string{`unknown model id "claude-fabel-5-1"`, "claude-fable-5-1", "claude-opus-5-5"}},
		{name: "unknown effort lists valid efforts", cfg: exclusionsCfg(&ModelRoutingConfig{ExcludedEfforts: []string{"extreme"}}, nil),
			want: []string{`unknown effort "extreme"`, "low, medium, high, xhigh, max"}},
		{name: "stage_models band pin conflicts", cfg: exclusionsCfg(&ModelRoutingConfig{ExcludedModels: []string{"claude-fable-5-1"}},
			map[string]string{"feature-dev": "fable"}),
			want: []string{"pipeline.stage_models.feature-dev", `"fable"`, "claude-fable-5-1"}},
		{name: "env override pin conflicts", cfg: exclusionsCfg(&ModelRoutingConfig{ExcludedModels: []string{"claude-fable-5-1"}}, nil),
			environ: []string{"NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV=fable", "PATH=/bin"},
			want:    []string{"NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "claude-fable-5-1"}},
		{name: "stage_efforts pin conflicts", cfg: exclusionsCfg(&ModelRoutingConfig{ExcludedEfforts: []string{"max"},
			StageEfforts: map[string]string{"feature-dev": "max"}}, nil),
			want: []string{"model_routing.stage_efforts.feature-dev", `"max"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelRoutingExclusions(tc.cfg, tc.environ)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %v, got nil", tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("error %q missing %q", err, w)
				}
			}
		})
	}
}
