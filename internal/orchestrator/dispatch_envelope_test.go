package orchestrator

import "testing"

// TestResolveDispatchEffort pins the honest, narrow evidence
// resolveDispatchEffort has: the grok-family adapters' NIGHTGAUGE_GROK_EFFORT
// dispatch env var, validated against the canonical EFFORT_LEVELS ladder, and
// absolutely nothing else (Issue #580).
func TestResolveDispatchEffort(t *testing.T) {
	tests := []struct {
		name      string
		adapter   string
		envEffort string
		want      string
	}{
		{"grok, unset env, no evidence", "grok", "", ""},
		{"grok, valid low rung", "grok", "low", "low"},
		{"grok, valid high rung", "grok", "high", "high"},
		{"grok, valid top rung", "grok", "max", "max"},
		{
			"grok, grok-native extra rung not in EFFORT_LEVELS",
			"grok", "minimal", "",
		},
		{"grok, another grok-native extra rung", "grok", "none", ""},
		{"grok, garbage value", "grok", "extreme", ""},
		{"grok-headless prefix also counts as xai", "grok-headless", "high", "high"},
		{"claude adapter never reads the grok env var", "claude", "high", ""},
		{"codex adapter never reads the grok env var", "codex", "high", ""},
		{"empty adapter has no provider evidence", "", "high", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NIGHTGAUGE_GROK_EFFORT", tc.envEffort)
			if got := resolveDispatchEffort(tc.adapter); got != tc.want {
				t.Errorf("resolveDispatchEffort(%q) with NIGHTGAUGE_GROK_EFFORT=%q = %q, want %q",
					tc.adapter, tc.envEffort, got, tc.want)
			}
		})
	}
}

// TestResolveDispatchThinking pins the registry-derived thinking axis and the
// anthropic-only interlock override (Issue #580).
func TestResolveDispatchThinking(t *testing.T) {
	tests := []struct {
		name            string
		adapter         string
		model           string
		disableThinking string
		want            string
	}{
		{"claude sonnet band defaults on", "claude", "sonnet", "", "on"},
		{"claude haiku band defaults off", "claude", "haiku", "", "off"},
		{
			"claude sonnet with the disable escape hatch set",
			"claude", "sonnet", "1", "off",
		},
		{
			"claude haiku with the disable escape hatch set stays off",
			"claude", "haiku", "true", "off",
		},
		{
			"grok defaults on and the claude escape hatch does not touch it",
			"grok", "grok-4.6", "1", "on",
		},
		{"codex model declares no thinking_default", "codex", "gpt-5.6-sol", "", ""},
		{"unresolvable model", "claude", "not-a-real-model-xyz", "", ""},
		{"empty model", "claude", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableThinking != "" {
				t.Setenv("CLAUDE_CODE_DISABLE_THINKING", tc.disableThinking)
			}
			if got := resolveDispatchThinking(tc.adapter, tc.model); got != tc.want {
				t.Errorf("resolveDispatchThinking(%q, %q) = %q, want %q",
					tc.adapter, tc.model, got, tc.want)
			}
		})
	}
}

// TestResolveDispatchSelectionMode pins that the mode this issue records is
// the SAME value dispatch_routing.go's modelRoutingMode already resolves for
// model selection itself (Issue #580, resolves #462) — not a second,
// independently-drifting notion of "mode".
func TestResolveDispatchSelectionMode(t *testing.T) {
	t.Run("no config: automatic default", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		if got := resolveDispatchSelectionMode(dir); got != "automatic" {
			t.Errorf("resolveDispatchSelectionMode = %q, want %q", got, "automatic")
		}
	})

	t.Run("model_routing.mode: manual in config", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: manual\n")
		if got := resolveDispatchSelectionMode(dir); got != "manual" {
			t.Errorf("resolveDispatchSelectionMode = %q, want %q", got, "manual")
		}
	})

	t.Run("model_routing.mode: hybrid in config", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: hybrid\n")
		if got := resolveDispatchSelectionMode(dir); got != "hybrid" {
			t.Errorf("resolveDispatchSelectionMode = %q, want %q", got, "hybrid")
		}
	})

	t.Run("env override wins over config, matching modelRoutingMode", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: manual\n")
		t.Setenv("NIGHTGAUGE_MODEL_ROUTING_MODE", "hybrid")
		if got := resolveDispatchSelectionMode(dir); got != "hybrid" {
			t.Errorf("resolveDispatchSelectionMode = %q, want %q — the env override must win", got, "hybrid")
		}
	})
}
