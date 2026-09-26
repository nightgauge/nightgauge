package config

import "testing"

// TestLoadOpenCodeConfigToolOutput2178 reads the machine-wide tool-output
// bounds and a per-stage override through the strict loader (#2178).
func TestLoadOpenCodeConfigToolOutput2178(t *testing.T) {
	withMachineConfig(t, `
opencode:
  tool_output:
    max_bytes: 6144
    read_max_lines: 300
    stages:
      feature-dev:
        max_lines: 200
        read_max_lines: 500
`)
	cfg, err := LoadOpenCodeConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolOutput.OpenCodeToolOutputBounds != (OpenCodeToolOutputBounds{MaxBytes: 6144, ReadMaxLines: 300}) {
		t.Errorf("machine-wide = %+v", cfg.ToolOutput.OpenCodeToolOutputBounds)
	}
	if got := cfg.ToolOutput.Stages["feature-dev"]; got != (OpenCodeToolOutputBounds{MaxLines: 200, ReadMaxLines: 500}) {
		t.Errorf("feature-dev = %+v", got)
	}

	withMachineConfig(t, `
opencode:
  tool_output:
    max_byte: 6144
`)
	if _, err := LoadOpenCodeConfig(t.TempDir()); err == nil {
		t.Error("a misspelled tool_output key was accepted")
	}
}
