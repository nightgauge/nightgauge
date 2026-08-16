package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

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

// isolateEffortEnv clears the ambient effort inputs the wire chain reads, so
// a developer's exported overrides never leak into an assertion.
func isolateEffortEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV", "")
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT", "")
	t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "")
}

// TestResolveWireEffort pins the Go mirror of the TS resolveStageEffort chain
// (#581): the wire effort must be exactly what the extension resolved for
// itself on the IPC path before the wire carried the value — that identity is
// the whole behavioral-compatibility argument for flipping effort precedence
// to the wire. TS twin: resolveModel.modeKnobAgreement.test.ts (mode knobs)
// and the resolveStageEffort suite.
func TestResolveWireEffort(t *testing.T) {
	stage := state.StageFeatureDev

	t.Run("elevated, no config: no explicit effort — omit the flag", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		isolateEffortEnv(t)
		if got := resolveWireEffort(dir, stage); got != "" {
			t.Errorf("resolveWireEffort = %q, want \"\"", got)
		}
	})

	t.Run("maximum pins {opus, high} per stage", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		isolateEffortEnv(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
		if got := resolveWireEffort(dir, stage); got != "high" {
			t.Errorf("resolveWireEffort(maximum) = %q, want \"high\"", got)
		}
	})

	t.Run("maximum + stage model env override: pin suppressed, effort floor still raises", func(t *testing.T) {
		// Mirrors resolveStageEffort exactly: the NIGHTGAUGE_PIPELINE_STAGE_MODEL_*
		// override suppresses the mode's Step-0 pin, and the [effortFloor,
		// effortCeiling] clamp introduces the floor over the unresolved effort.
		dir := isolatedWorkspace(t)
		isolateEffortEnv(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "sonnet")
		if got := resolveWireEffort(dir, stage); got != "high" {
			t.Errorf("resolveWireEffort(maximum, model env override) = %q, want \"high\"", got)
		}
	})

	t.Run("efficiency caps a configured effort at medium", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  stage_efforts:\n    feature-dev: xhigh\n")
		isolateEffortEnv(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		if got := resolveWireEffort(dir, stage); got != "medium" {
			t.Errorf("resolveWireEffort(efficiency, xhigh config) = %q, want \"medium\"", got)
		}
	})

	t.Run("env stage effort wins over config default_effort", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  default_effort: high\n")
		isolateEffortEnv(t)
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV", "low")
		if got := resolveWireEffort(dir, stage); got != "low" {
			t.Errorf("resolveWireEffort(env low over config high) = %q, want \"low\"", got)
		}
	})

	t.Run("config stage_efforts, then default_effort", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  default_effort: high\n  stage_efforts:\n    feature-dev: medium\n")
		isolateEffortEnv(t)
		if got := resolveWireEffort(dir, stage); got != "medium" {
			t.Errorf("resolveWireEffort(stage_efforts over default_effort) = %q, want \"medium\"", got)
		}
		dir2 := routedWorkspace(t, "model_routing:\n  default_effort: high\n")
		if got := resolveWireEffort(dir2, stage); got != "high" {
			t.Errorf("resolveWireEffort(default_effort) = %q, want \"high\"", got)
		}
	})

	t.Run("manual mode falls back to the DEFAULT_STAGE_EFFORTS table", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: manual\n")
		isolateEffortEnv(t)
		if got := resolveWireEffort(dir, stage); got != "medium" {
			t.Errorf("resolveWireEffort(manual, feature-dev) = %q, want \"medium\"", got)
		}
		if got := resolveWireEffort(dir, state.StagePRCreate); got != "" {
			t.Errorf("resolveWireEffort(manual, pr-create) = %q, want \"\" — table has no entry", got)
		}
	})

	t.Run("off-ladder values are ignored, not dispatched", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  stage_efforts:\n    feature-dev: turbo\n")
		isolateEffortEnv(t)
		if got := resolveWireEffort(dir, stage); got != "" {
			t.Errorf("resolveWireEffort(invalid config value) = %q, want \"\"", got)
		}
	})
}

// TestResolveWireThinking pins the wire thinking to the selection query's
// declared rung under the band contract the wire Model already speaks (#581),
// with the CLAUDE_CODE_DISABLE_THINKING interlock as the one override. Every
// mode passes the same table because no mode declares a thinking policy
// (#606): the policy axis exists and is consumed, but its column is empty —
// so the mode parameter must be behaviorally inert today.
func TestResolveWireThinking(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		disableThinking string
		want            string
	}{
		{"sonnet band: declared on", "sonnet", "", "on"},
		{"haiku band: declared off", "haiku", "", "off"},
		{"concrete id collapses onto its band", "claude-sonnet-5", "", "on"},
		{"disable interlock forces off", "sonnet", "1", "off"},
		{"local model has no rung: absent", "my-local-model", "", ""},
		{"empty model: absent", "", "", ""},
	}
	modes := []routing.PerformanceMode{
		routing.ModeEfficiency, routing.ModeElevated, routing.ModeMaximum, routing.ModeFrontier,
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableThinking != "" {
				t.Setenv("CLAUDE_CODE_DISABLE_THINKING", tc.disableThinking)
			}
			for _, mode := range modes {
				if got := resolveWireThinking(tc.model, mode); got != tc.want {
					t.Errorf("resolveWireThinking(%q, %s) = %q, want %q", tc.model, mode, got, tc.want)
				}
			}
		})
	}
}

// TestWireThinkingUnderPolicy exercises the policy branch the wire thinking
// grew in #606 (spike #568 §4.1.3) at its explicit seam, since no shipped
// mode declares a policy value yet: a policy overrides the rung's declared
// default, supplies a value where the rung declares none, an off-vocabulary
// policy is ignored, and the disable interlock still wins over a policy.
func TestWireThinkingUnderPolicy(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		policy          string
		disableThinking string
		want            string
	}{
		{"policy overrides the declared default", "sonnet", "off", "", "off"},
		{"policy raises where the rung declares off", "haiku", "on", "", "on"},
		{"policy supplies a value where no rung exists", "my-local-model", "on", "", "on"},
		{"off-vocabulary policy is ignored, not dispatched", "sonnet", "hard", "", "on"},
		{"disable interlock outranks the policy", "haiku", "on", "1", "off"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableThinking != "" {
				t.Setenv("CLAUDE_CODE_DISABLE_THINKING", tc.disableThinking)
			}
			if got := wireThinkingUnderPolicy(tc.model, tc.policy); got != tc.want {
				t.Errorf("wireThinkingUnderPolicy(%q, %q) = %q, want %q", tc.model, tc.policy, got, tc.want)
			}
		})
	}
}
