package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestLoadOpenCodeConfigReadsTheMachineTier: the `opencode:` block of the
// machine-tier file is read whole, durations and the § 12 overrides included.
func TestLoadOpenCodeConfigReadsTheMachineTier(t *testing.T) {
	withMachineConfig(t, `
github_user: octocat
opencode:
  binary: /opt/opencode/bin/opencode
  inherit_user_config: true
  model: lmstudio/qwen/qwen3.8-27b
  provider: lm-studio
  base_url: http://127.0.0.1:1234/v1
  limit:
    context: 131072
    output: 8192
  timeouts:
    header: 4m
    chunk: 90s
  snapshot: true
  lsp: false
`)
	cfg, err := LoadOpenCodeConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Binary != "/opt/opencode/bin/opencode" || !cfg.InheritUserConfig || cfg.Model != "lmstudio/qwen/qwen3.8-27b" ||
		cfg.Provider != "lm-studio" || cfg.BaseURL != "http://127.0.0.1:1234/v1" ||
		cfg.Limit != (OpenCodeLimit{Context: 131072, Output: 8192}) {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Timeouts.Header.Duration() != 4*time.Minute || cfg.Timeouts.Chunk.Duration() != 90*time.Second {
		t.Errorf("timeouts = %s, %s", cfg.Timeouts.Header.Duration(), cfg.Timeouts.Chunk.Duration())
	}
	if cfg.Snapshot == nil || !*cfg.Snapshot || cfg.LSP == nil || *cfg.LSP || cfg.Formatter != nil {
		t.Errorf("snapshot, lsp, formatter = %v, %v, %v", cfg.Snapshot, cfg.LSP, cfg.Formatter)
	}
}

// TestLoadOpenCodeConfigAbsent: no machine file, or one without the block,
// is the zero config, not an error.
func TestLoadOpenCodeConfigAbsent(t *testing.T) {
	withNoMachineConfig(t)
	if cfg, err := LoadOpenCodeConfig(""); err != nil || !reflect.DeepEqual(cfg, OpenCodeConfig{}) {
		t.Errorf("no machine file: %+v, %v", cfg, err)
	}
	withMachineConfig(t, "github_user: octocat\nopencode:\n")
	if cfg, err := LoadOpenCodeConfig(""); err != nil || !reflect.DeepEqual(cfg, OpenCodeConfig{}) {
		t.Errorf("an empty block: %+v, %v", cfg, err)
	}
}

// TestLoadOpenCodeConfigRejectsUnknownKeys: a misspelled key is reported, not
// read as a missing one (a `limits:` block would otherwise leave the limits
// at 0 with nothing saying why).
func TestLoadOpenCodeConfigRejectsUnknownKeys(t *testing.T) {
	withMachineConfig(t, "opencode:\n  provider: lm-studio\n  limits:\n    context: 131072\n")
	_, err := LoadOpenCodeConfig("")
	if err == nil || !strings.Contains(err.Error(), "limits") || !strings.Contains(err.Error(), "opencode:") {
		t.Errorf("err = %v, want one naming the unknown key", err)
	}
	withMachineConfig(t, "opencode: lm-studio\n")
	if _, err := LoadOpenCodeConfig(""); err == nil || !strings.Contains(err.Error(), "must be a mapping") {
		t.Errorf("a scalar block: err = %v", err)
	}
}

// TestLoadOpenCodeConfigRefusesAProjectDeclaration: the block decides where a
// stage's code is sent, so a committed project config that declares it is an
// error naming the machine-tier file, even when the machine tier has none.
// The checkout's gitignored local tier is not read at all, because a stage
// can write its own worktree.
func TestLoadOpenCodeConfigRefusesAProjectDeclaration(t *testing.T) {
	withMachineConfig(t, "opencode:\n  provider: lm-studio\n  base_url: http://127.0.0.1:1234/v1\n")
	machinePath, _ := machineConfigPathFn()

	worktree := t.TempDir()
	project := writeProjectYAML(t, worktree, "owner: nightgauge\nopencode:\n  base_url: http://127.0.0.1:9/v1\n")
	_, err := LoadOpenCodeConfig(worktree)
	if err == nil {
		t.Fatal("a committed opencode: block was accepted")
	}
	for _, want := range []string{project, machinePath, "committed repository config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "127.0.0.1:9") {
		t.Errorf("the refusal quotes the committed base URL: %v", err)
	}

	clean := t.TempDir()
	writeProjectYAML(t, clean, "owner: nightgauge\n")
	writeLocalYAML(t, clean, "opencode:\n  base_url: http://127.0.0.1:9/v1\n")
	cfg, err := LoadOpenCodeConfig(clean)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Errorf("base_url = %q, want the machine tier's; the checkout's local tier is never read", cfg.BaseURL)
	}
}

// TestOpenCodeIsMachineTier: `opencode` is classified machine-tier in
// MachineTierKeys and in the canonical classification, so the audit flags a
// project declaration as drift, a runtime write routes to the machine file,
// and a key in both tiers is warned about.
func TestOpenCodeIsMachineTier(t *testing.T) {
	if !slices.Contains(MachineTierKeys, "opencode") {
		t.Fatal("opencode is not in MachineTierKeys")
	}
	classification, err := loadTierClassification()
	if err != nil {
		t.Fatal(err)
	}
	if classification["opencode"] != "machine" {
		t.Errorf("the tier classification gives opencode %q, want machine", classification["opencode"])
	}
	if !IsMachineTierKey("opencode.base_url") {
		t.Error("a write of opencode.base_url would not route to the machine file")
	}

	withMachineConfig(t, "opencode:\n  provider: lm-studio\n")
	dir := t.TempDir()
	writeProjectYAML(t, dir, "owner: nightgauge\nopencode:\n  provider: ollama\n")
	entries, err := BuildAuditReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Key == "opencode.provider" && e.EffectiveTier == "project" {
			found = true
			if e.TargetTier != "machine" || !strings.HasPrefix(e.Status, "DRIFT") {
				t.Errorf("opencode.provider in the project config: target %q, status %q; want machine, DRIFT", e.TargetTier, e.Status)
			}
		}
	}
	if !found {
		t.Error("the audit has no row for opencode.provider in the project config")
	}

	resetShadowWarnDedup()
	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})
	if !strings.Contains(logged, "opencode is in project YAML but is owned by the machine tier") {
		t.Errorf("no machine-tier warning for opencode in both tiers:\n%s", logged)
	}
}
