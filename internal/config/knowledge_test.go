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

// --- IsAutoScaffold (#1205) ---
//
// The field existed since the knowledge base shipped and NOTHING in Go read it.
// Its struct comment said "defaults to false" while docs/KNOWLEDGE_BASE.md, the
// docs/CONFIGURATION.md table and the SDK's own reader all treat unset as on —
// a contradiction that could never surface, because no resolver existed to be
// wrong. The truth table below is the one the docs publish.

func TestIsAutoScaffold_DefaultsOnWhenKnowledgeOn(t *testing.T) {
	tr := true
	cfg := &KnowledgeConfig{Enabled: &tr}
	if !cfg.IsAutoScaffold() {
		t.Error("enabled=true, auto_scaffold unset: IsAutoScaffold = false, want true")
	}
}

func TestIsAutoScaffold_OffWhenKnowledgeDisabled(t *testing.T) {
	fa := false
	tr := true
	// Gated by Enabled for the same reason telemetry is (ADR-005): a project
	// that opted out of the KB must not get directories scaffolded into its
	// tree by a nested flag it never looked at.
	cfg := &KnowledgeConfig{Enabled: &fa, AutoScaffold: &tr}
	if cfg.IsAutoScaffold() {
		t.Error("enabled=false, auto_scaffold=true: IsAutoScaffold = true, want false")
	}
	if (*KnowledgeConfig)(nil).IsAutoScaffold() {
		t.Error("nil receiver: IsAutoScaffold = true, want false")
	}
	if (&KnowledgeConfig{}).IsAutoScaffold() {
		t.Error("empty config (enabled unset ⇒ false): IsAutoScaffold = true, want false")
	}
}

func TestIsAutoScaffold_ExplicitOptOut(t *testing.T) {
	tr := true
	fa := false
	cfg := &KnowledgeConfig{Enabled: &tr, AutoScaffold: &fa}
	if cfg.IsAutoScaffold() {
		t.Error("auto_scaffold=false not respected — enabled but not automatic")
	}
}
