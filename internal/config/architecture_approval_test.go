package config

import "testing"

func TestResolveArchitectureApproval(t *testing.T) {
	tru, fls := true, false

	if !(&PipelineConfig{}).ResolveArchitectureApprovalEnabled() {
		t.Error("nil block should default enabled=true")
	}
	if (&PipelineConfig{ArchitectureApproval: &ArchitectureApprovalConfig{Enabled: &fls}}).ResolveArchitectureApprovalEnabled() {
		t.Error("explicit false must be honored")
	}
	if !(&PipelineConfig{ArchitectureApproval: &ArchitectureApprovalConfig{Enabled: &tru}}).ResolveArchitectureApprovalEnabled() {
		t.Error("explicit true must be honored")
	}

	if got := (&PipelineConfig{}).ResolveArchitectureApprovalLabel(); got != DefaultArchitectureApprovalLabel {
		t.Errorf("default label = %q, want %q", got, DefaultArchitectureApprovalLabel)
	}
	if got := (&PipelineConfig{ArchitectureApproval: &ArchitectureApprovalConfig{ApprovalLabel: "ok:arch"}}).ResolveArchitectureApprovalLabel(); got != "ok:arch" {
		t.Errorf("custom label = %q, want ok:arch", got)
	}
}
