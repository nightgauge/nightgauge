package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadReviewConfig(t *testing.T, block string) (*Config, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "owner: nightgauge\nproject:\n  number: 1\n" + block
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(root)
}

func TestReviewMaxRounds(t *testing.T) {
	for _, tc := range []struct {
		block string
		want  int
	}{
		{"", DefaultReviewMaxRounds},
		{"review:\n  max_rounds: 2\n", 2},
		{"review:\n  max_rounds: 4\n", 4},
		{"review:\n  model: opus\n", DefaultReviewMaxRounds},
	} {
		cfg, err := loadReviewConfig(t, tc.block)
		if err != nil {
			t.Fatalf("%q: %v", tc.block, err)
		}
		if got := cfg.ResolveReviewMaxRounds(); got != tc.want {
			t.Errorf("%q: max rounds = %d, want %d", tc.block, got, tc.want)
		}
	}
	if got := (*Config)(nil).ResolveReviewMaxRounds(); got != DefaultReviewMaxRounds {
		t.Errorf("nil config = %d", got)
	}
}

func TestReviewMaxRoundsOutOfRangeIsRejected(t *testing.T) {
	for _, n := range []string{"0", "7", "-1"} {
		_, err := loadReviewConfig(t, "review:\n  max_rounds: "+n+"\n")
		if err == nil || !strings.Contains(err.Error(), "review.max_rounds") {
			t.Errorf("max_rounds %s: err = %v", n, err)
		}
	}
}

func TestResolveReviewModel(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	validate := map[string]string{"NIGHTGAUGE_STAGE": "feature-validate", "NIGHTGAUGE_DISPATCH_MODEL": "sonnet"}
	pinned := &Config{Review: &ReviewConfig{Model: "opus"},
		Pipeline: &PipelineConfig{StageModels: map[string]string{"feature-validate": "haiku"}}}
	staged := &Config{Pipeline: &PipelineConfig{StageModels: map[string]string{"feature-validate": "haiku"}}}
	fallback := &Config{UI: &UIConfig{Core: &UICoreConfig{DefaultModel: "opus"}}}
	for name, tc := range map[string]struct {
		cfg  *Config
		env  map[string]string
		want string
	}{
		"review.model wins":         {pinned, validate, "opus"},
		"dispatch model in stage":   {staged, validate, "sonnet"},
		"dispatch model elsewhere":  {staged, map[string]string{"NIGHTGAUGE_STAGE": "feature-dev", "NIGHTGAUGE_DISPATCH_MODEL": "sonnet"}, "haiku"},
		"stage model":               {staged, nil, "haiku"},
		"workspace default":         {fallback, nil, "opus"},
		"adapter default":           {&Config{}, nil, ""},
		"nil config, dispatch only": {nil, validate, "sonnet"},
	} {
		if got := ResolveReviewModel(tc.cfg, env(tc.env)); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
