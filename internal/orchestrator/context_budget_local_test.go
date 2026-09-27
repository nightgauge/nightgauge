package orchestrator

import "testing"

// TestIsLocalDispatch (#2186): only an opencode model on a local provider key
// prefers the compact profile.
func TestIsLocalDispatch(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		adapter, model string
		want           bool
	}{
		{"opencode", "lmstudio/qwen3-27b", true},
		{"opencode", "ollama/qwen3:8b", true},
		{"opencode", "ollama/gpt-oss:120b-cloud", false},
		{"opencode", "anthropic/claude-sonnet-4-5", false},
		{"claude", "sonnet", false},
	} {
		if got := isLocalDispatch(tc.adapter, tc.model, dir); got != tc.want {
			t.Errorf("isLocalDispatch(%q, %q) = %v, want %v", tc.adapter, tc.model, got, tc.want)
		}
	}
	if got := profileReasonSuffix("local_endpoint"); got != ", reason local_endpoint" {
		t.Errorf("profileReasonSuffix = %q", got)
	}
}
