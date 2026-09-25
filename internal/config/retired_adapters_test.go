package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadRejectsRetiredAdapters: a config naming the removed lm-studio or
// ollama adapter (#2128) fails to load with the migration, wherever it names
// it.
func TestLoadRejectsRetiredAdapters(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	for _, tc := range []struct{ name, yaml, where string }{
		{"global lm-studio", "project:\n  owner: o\n  number: 1\nui:\n  core:\n    adapter: lm-studio\n", "ui.core.adapter"},
		{"stage ollama", "project:\n  owner: o\n  number: 1\npipeline:\n  stage_adapters:\n    feature-dev: ollama\n", "pipeline.stage_adapters.feature-dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(root)
			if err == nil {
				t.Fatal("Load succeeded, want the retired-adapter error")
			}
			for _, want := range []string{tc.where, "removed", "opencode", "openai-compatible", "OpenAI-compatible server"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestRetiredAdapterErrorIgnoresLiveAdapters(t *testing.T) {
	for _, name := range []string{"", "opencode", "claude", "codex", "openai-compatible"} {
		if err := RetiredAdapterError(name, "x"); err != nil {
			t.Errorf("RetiredAdapterError(%q) = %v, want nil", name, err)
		}
	}
}
