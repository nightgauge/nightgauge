package config

import "testing"

func TestPipelineConfig_RelaxClassesFor(t *testing.T) {
	t.Run("nil receiver and nil gates are safe", func(t *testing.T) {
		var p *PipelineConfig
		if got := p.RelaxClassesFor("pr-merge"); got != nil {
			t.Errorf("nil PipelineConfig = %v, want nil", got)
		}
		if got := (&PipelineConfig{}).RelaxClassesFor("pr-merge"); got != nil {
			t.Errorf("nil Gates = %v, want nil", got)
		}
	})

	p := &PipelineConfig{Gates: &PipelineGatesConfig{
		PrMerge:  &GateRelaxConfig{RelaxOnChangeClass: []string{"docs_only", "config_only"}},
		PrCreate: &GateRelaxConfig{RelaxOnChangeClass: []string{"docs_only"}},
	}}
	if got := p.RelaxClassesFor("pr-merge"); len(got) != 2 {
		t.Errorf("pr-merge = %v, want 2 classes", got)
	}
	if got := p.RelaxClassesFor("pr-create"); len(got) != 1 || got[0] != "docs_only" {
		t.Errorf("pr-create = %v, want [docs_only]", got)
	}
	if got := p.RelaxClassesFor("feature-validate"); got != nil {
		t.Errorf("unknown gate = %v, want nil", got)
	}
}

// TestLoad_PipelineGates parses a pipeline.gates.* block end-to-end.
func TestLoad_PipelineGates(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	yaml := `owner: nightgauge
project:
  number: 1
  repo: nightgauge
pipeline:
  gates:
    pr_merge:
      relax_on_change_class: [docs_only, config_only]
    pr_create:
      relax_on_change_class: [docs_only]
`
	writeProjectYAML(t, dir, yaml)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pipeline == nil || cfg.Pipeline.Gates == nil {
		t.Fatal("pipeline.gates not parsed")
	}
	if got := cfg.Pipeline.RelaxClassesFor("pr-merge"); len(got) != 2 {
		t.Errorf("pr-merge relax classes = %v, want 2", got)
	}
	if got := cfg.Pipeline.RelaxClassesFor("pr-create"); len(got) != 1 {
		t.Errorf("pr-create relax classes = %v, want 1", got)
	}
}
