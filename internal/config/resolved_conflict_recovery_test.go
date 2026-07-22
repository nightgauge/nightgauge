// Tests for the conflict-recovery resolver (#4072). The resolver surfaces a
// single defaults-applied value for pipeline.recovery.conflict_recovery so the
// default semantics live in exactly one place.
package config

import "testing"

func boolPtr(b bool) *bool { return &b }

func TestResolveConflictRecovery_DefaultsWhenUnset(t *testing.T) {
	got := ResolveConflictRecovery(&Config{})
	if !got.Enabled {
		t.Errorf("expected enabled by default")
	}
	if got.MaxDevRedispatch != DefaultConflictMaxDevRedispatch {
		t.Errorf("max_dev_redispatch = %d, want default %d", got.MaxDevRedispatch, DefaultConflictMaxDevRedispatch)
	}
}

func TestResolveConflictRecovery_NilConfig(t *testing.T) {
	got := ResolveConflictRecovery(nil)
	if !got.Enabled || got.MaxDevRedispatch != DefaultConflictMaxDevRedispatch {
		t.Errorf("nil config must resolve to defaults, got %+v", got)
	}
}

func TestResolveConflictRecovery_ExplicitValues(t *testing.T) {
	cfg := &Config{Pipeline: &PipelineConfig{Recovery: &PipelineRecoveryConfig{
		ConflictRecovery: &ConflictRecoveryConfig{Enabled: boolPtr(false), MaxDevRedispatch: 4},
	}}}
	got := ResolveConflictRecovery(cfg)
	if got.Enabled {
		t.Errorf("expected enabled=false from explicit config")
	}
	if got.MaxDevRedispatch != 4 {
		t.Errorf("max_dev_redispatch = %d, want 4", got.MaxDevRedispatch)
	}
}

func TestResolveConflictRecovery_ZeroRedispatchFallsBack(t *testing.T) {
	cfg := &Config{Pipeline: &PipelineConfig{Recovery: &PipelineRecoveryConfig{
		ConflictRecovery: &ConflictRecoveryConfig{MaxDevRedispatch: 0},
	}}}
	got := ResolveConflictRecovery(cfg)
	if got.MaxDevRedispatch != DefaultConflictMaxDevRedispatch {
		t.Errorf("zero max_dev_redispatch must fall back to default, got %d", got.MaxDevRedispatch)
	}
	// Enabled is a pointer — unset means default true.
	if !got.Enabled {
		t.Errorf("enabled unset must default to true")
	}
}
