package orchestrator

import (
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

func TestOpenCodeConfiguredModel(t *testing.T) {
	prev := openCodeReadinessLoadSettings
	t.Cleanup(func() { openCodeReadinessLoadSettings = prev })

	cases := []struct {
		name    string
		model   string
		setting string
		loadErr error
		want    string
	}{
		{"tier runs opencode.model", "sonnet", "mtplx/qwen-27b", nil, "mtplx/qwen-27b"},
		{"bare registry id runs opencode.model", "claude-sonnet-5", "mtplx/qwen-27b", nil, "mtplx/qwen-27b"},
		{"qualified model is kept", "mtplx-remote/qwen-27b", "mtplx/qwen-27b", nil, ""},
		{"unset opencode.model leaves the refusal", "haiku", "", nil, ""},
		{"unqualified opencode.model leaves the refusal", "haiku", "qwen-27b", nil, ""},
		{"settings that do not load leave the refusal", "haiku", "", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			openCodeReadinessLoadSettings = func(string) (config.OpenCodeConfig, error) {
				return config.OpenCodeConfig{Model: tc.setting}, tc.loadErr
			}
			if got := openCodeConfiguredModel(t.TempDir(), tc.model); got != tc.want {
				t.Fatalf("openCodeConfiguredModel(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
