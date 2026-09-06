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
	// And knowledge.enabled unset resolves the same way, because absent means
	// on (ADR-020) — the nil receiver is the "no knowledge: section" case.
	if !(*KnowledgeConfig)(nil).IsTelemetryEnabled() {
		t.Error("nil receiver: IsTelemetryEnabled = false, want true (absent ⇒ enabled)")
	}
	if !(&KnowledgeConfig{}).IsTelemetryEnabled() {
		t.Error("knowledge.enabled unset: telemetry should be on")
	}
}

// --- IsEnabled (#1513, ADR-020) ---

func TestIsEnabled_DefaultsOnWhenAbsent(t *testing.T) {
	// The majority case: a repo with no `knowledge:` section at all. A default
	// of false here is what made the feature invisible in every such repo.
	if !(*KnowledgeConfig)(nil).IsEnabled() {
		t.Error("nil receiver: IsEnabled = false, want true (absent ⇒ enabled)")
	}
	if !(&KnowledgeConfig{}).IsEnabled() {
		t.Error("enabled unset: IsEnabled = false, want true (absent ⇒ enabled)")
	}
}

func TestIsEnabled_ExplicitOptOut(t *testing.T) {
	fa := false
	tr := true
	if (&KnowledgeConfig{Enabled: &fa}).IsEnabled() {
		t.Error("explicit enabled=false not respected — the opt-out must still work")
	}
	if !(&KnowledgeConfig{Enabled: &tr}).IsEnabled() {
		t.Error("explicit enabled=true not respected")
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
	// A nil receiver and an empty config now resolve enabled ⇒ true
	// (ADR-020), so auto_scaffold defaults on with them.
	if !(*KnowledgeConfig)(nil).IsAutoScaffold() {
		t.Error("nil receiver: IsAutoScaffold = false, want true (absent ⇒ enabled)")
	}
	if !(&KnowledgeConfig{}).IsAutoScaffold() {
		t.Error("empty config (enabled unset ⇒ true): IsAutoScaffold = false, want true")
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

func TestKnowledgeRecallWeightsDefaults(t *testing.T) {
	var absent *KnowledgeConfig
	w := absent.ResolveRecallWeights()

	cases := map[string]struct {
		got  *float64
		want float64
	}{
		"human_reviewed":    {w.HumanReviewed, 1.25},
		"machine_confirmed": {w.MachineConfirmed, 1.0},
		"unverified":        {w.Unverified, 0.85},
		"status_draft":      {w.StatusDraft, 0.9},
		"status_deprecated": {w.StatusDeprecated, 0.25},
		"expired":           {w.Expired, 0.5},
	}
	for name, tc := range cases {
		if tc.got == nil {
			t.Fatalf("%s resolved to nil — downstream code must never re-check", name)
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %v, want %v", name, *tc.got, tc.want)
		}
	}
}

func TestKnowledgeRecallWeights_PartialOverrideKeepsOtherDefaults(t *testing.T) {
	custom := 2.0
	cfg := &KnowledgeConfig{Recall: &RecallConfig{
		Weights: &RecallWeights{HumanReviewed: &custom},
	}}

	w := cfg.ResolveRecallWeights()
	if *w.HumanReviewed != 2.0 {
		t.Errorf("human_reviewed = %v, want the override", *w.HumanReviewed)
	}
	if *w.StatusDeprecated != 0.25 {
		t.Errorf("status_deprecated = %v, want the default alongside an override", *w.StatusDeprecated)
	}
}
