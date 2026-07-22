package config

import "testing"

func TestResolveGroundingGateEnabled(t *testing.T) {
	tru, fls := true, false
	cases := []struct {
		name string
		pc   *PipelineConfig
		want bool
	}{
		{"nil pipeline → default on", nil, true},
		{"nil block → default on", &PipelineConfig{}, true},
		{"nil enabled → default on", &PipelineConfig{GroundingGate: &GroundingGateConfig{}}, true},
		{"explicit true", &PipelineConfig{GroundingGate: &GroundingGateConfig{Enabled: &tru}}, true},
		{"explicit false honored", &PipelineConfig{GroundingGate: &GroundingGateConfig{Enabled: &fls}}, false},
	}
	for _, c := range cases {
		if got := c.pc.ResolveGroundingGateEnabled(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
