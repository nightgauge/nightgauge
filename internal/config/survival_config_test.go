package config

import "testing"

func TestResolveSurvivalWindowDays(t *testing.T) {
	var nilP *PipelineConfig
	if got := nilP.ResolveSurvivalWindowDays(); got != DefaultSurvivalWindowDays {
		t.Errorf("nil pipeline = %d, want default %d", got, DefaultSurvivalWindowDays)
	}

	cases := []struct {
		name string
		cfg  *PipelineConfig
		want int
	}{
		{"nil survival block", &PipelineConfig{}, DefaultSurvivalWindowDays},
		{"zero window → default", &PipelineConfig{Survival: &SurvivalConfig{WindowDays: 0}}, DefaultSurvivalWindowDays},
		{"negative window → default", &PipelineConfig{Survival: &SurvivalConfig{WindowDays: -3}}, DefaultSurvivalWindowDays},
		{"explicit window honored", &PipelineConfig{Survival: &SurvivalConfig{WindowDays: 14}}, 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolveSurvivalWindowDays(); got != tc.want {
				t.Errorf("ResolveSurvivalWindowDays() = %d, want %d", got, tc.want)
			}
		})
	}
}
