package config

import "testing"

func TestResolveAdversarialReviewEnabled(t *testing.T) {
	tru, fls := true, false

	cases := []struct {
		name string
		pc   *PipelineConfig
		want bool
	}{
		{"nil pipeline → default on", nil, true},
		{"nil block → default on", &PipelineConfig{}, true},
		{"nil enabled → default on", &PipelineConfig{AdversarialReview: &AdversarialReviewConfig{}}, true},
		{"explicit true", &PipelineConfig{AdversarialReview: &AdversarialReviewConfig{Enabled: &tru}}, true},
		{"explicit false honored", &PipelineConfig{AdversarialReview: &AdversarialReviewConfig{Enabled: &fls}}, false},
	}
	for _, c := range cases {
		if got := c.pc.ResolveAdversarialReviewEnabled(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
