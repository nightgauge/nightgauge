package config

import "testing"

func TestIsWorkspaceScoped_DefaultsTrue(t *testing.T) {
	if !(*KnowledgeConfig)(nil).IsWorkspaceScoped() {
		t.Error("nil receiver: IsWorkspaceScoped = false, want true")
	}
	empty := &KnowledgeConfig{}
	if !empty.IsWorkspaceScoped() {
		t.Error("empty config: IsWorkspaceScoped = false, want true")
	}
}

func TestIsWorkspaceScoped_ExplicitOverride(t *testing.T) {
	tr := true
	fa := false
	cfg := &KnowledgeConfig{WorkspaceScoped: &tr}
	if !cfg.IsWorkspaceScoped() {
		t.Error("WorkspaceScoped=true not respected")
	}
	cfg = &KnowledgeConfig{WorkspaceScoped: &fa}
	if cfg.IsWorkspaceScoped() {
		t.Error("WorkspaceScoped=false not respected")
	}
}

func TestIsTelemetryEnabled_OffWhenKnowledgeDisabled(t *testing.T) {
	if (*KnowledgeConfig)(nil).IsTelemetryEnabled() {
		t.Error("nil receiver: IsTelemetryEnabled = true, want false")
	}
	empty := &KnowledgeConfig{}
	if empty.IsTelemetryEnabled() {
		t.Error("knowledge.enabled unset: telemetry should be off")
	}
	tr := true
	disabled := false
	cfg := &KnowledgeConfig{Enabled: &disabled, Telemetry: &KnowledgeTelemetryConfig{Enabled: &tr}}
	if cfg.IsTelemetryEnabled() {
		t.Error("knowledge.enabled=false must force telemetry off regardless of nested flag")
	}
}

func TestIsTelemetryEnabled_DefaultsOnWhenKnowledgeOn(t *testing.T) {
	tr := true
	cfg := &KnowledgeConfig{Enabled: &tr}
	if !cfg.IsTelemetryEnabled() {
		t.Error("knowledge.enabled=true with telemetry unset must default to true")
	}
}

func TestIsTelemetryEnabled_ExplicitOptOut(t *testing.T) {
	tr := true
	fa := false
	cfg := &KnowledgeConfig{Enabled: &tr, Telemetry: &KnowledgeTelemetryConfig{Enabled: &fa}}
	if cfg.IsTelemetryEnabled() {
		t.Error("explicit telemetry.enabled=false not respected")
	}
}

func TestIsTelemetryEnabled_ExplicitOptIn(t *testing.T) {
	tr := true
	cfg := &KnowledgeConfig{Enabled: &tr, Telemetry: &KnowledgeTelemetryConfig{Enabled: &tr}}
	if !cfg.IsTelemetryEnabled() {
		t.Error("explicit telemetry.enabled=true not respected")
	}
}
