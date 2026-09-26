package adapters

import (
	"encoding/json"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestOpenCodeToolOutputBounds2178 pins the per-stage tool-output bounds: a
// declared (local) endpoint gets the tighter defaults, a hosted model the
// looser ones, and the machine-wide then per-stage settings override each
// value they set (#2178).
func TestOpenCodeToolOutputBounds2178(t *testing.T) {
	hosted := openCodeToolOutputBounds(config.OpenCodeToolOutput{}, "feature-planning", false)
	if hosted != (config.OpenCodeToolOutputBounds{MaxLines: 1000, MaxBytes: 32 * 1024, ReadMaxLines: 2000}) {
		t.Errorf("hosted defaults = %+v", hosted)
	}
	local := openCodeToolOutputBounds(config.OpenCodeToolOutput{}, "feature-planning", true)
	if local != (config.OpenCodeToolOutputBounds{MaxLines: 400, MaxBytes: 8 * 1024, ReadMaxLines: 400}) {
		t.Errorf("local defaults = %+v", local)
	}
	settings := config.OpenCodeToolOutput{
		OpenCodeToolOutputBounds: config.OpenCodeToolOutputBounds{MaxBytes: 12000, ReadMaxLines: 300},
		Stages: map[string]config.OpenCodeToolOutputBounds{
			"feature-dev": {ReadMaxLines: 600},
		},
	}
	if got := openCodeToolOutputBounds(settings, "feature-planning", true); got != (config.OpenCodeToolOutputBounds{MaxLines: 400, MaxBytes: 12000, ReadMaxLines: 300}) {
		t.Errorf("machine-wide override = %+v", got)
	}
	if got := openCodeToolOutputBounds(settings, "feature-dev", true); got != (config.OpenCodeToolOutputBounds{MaxLines: 400, MaxBytes: 12000, ReadMaxLines: 600}) {
		t.Errorf("stage override = %+v", got)
	}
}

// TestOpenCodeConfigCarriesStageToolOutput2178 proves the built config's
// tool_output block and ReadMaxLines come from the stage's resolved bounds.
func TestOpenCodeConfigCarriesStageToolOutput2178(t *testing.T) {
	settings := lmStudioSettings()
	settings.ToolOutput = config.OpenCodeToolOutput{Stages: map[string]config.OpenCodeToolOutputBounds{
		"feature-planning": {MaxLines: 250, ReadMaxLines: 250},
	}}
	in, err := OpenCodeConfigInputFor(settings, RunOptions{
		Stage:       "feature-planning",
		Model:       "lmstudio/qwen/qwen3.8-27b",
		WorktreeDir: goldenWorktree,
	}, goldenRunRoot, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	built, err := BuildOpenCodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ToolOutput openCodeToolOutputJSON `json:"tool_output"`
	}
	if err := json.Unmarshal([]byte(built.Content), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ToolOutput != (openCodeToolOutputJSON{MaxLines: 250, MaxBytes: 8 * 1024}) {
		t.Errorf("tool_output = %+v", cfg.ToolOutput)
	}
	if built.ReadMaxLines != 250 {
		t.Errorf("ReadMaxLines = %d, want 250", built.ReadMaxLines)
	}
}
