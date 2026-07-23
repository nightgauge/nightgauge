package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withMachineConfig sets up a temporary machine-config path for the
// duration of a test. The override is restored on cleanup.
func withMachineConfig(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("write machine config: %v", err)
		}
	}
	prev := machineConfigPathFn
	machineConfigPathFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { machineConfigPathFn = prev })
}

// withNoMachineConfig points machineConfigPathFn at a non-existent path
// so machine-tier reads return errConfigNotFound.
func withNoMachineConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "definitely-not-present.yaml")
	prev := machineConfigPathFn
	machineConfigPathFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { machineConfigPathFn = prev })
}

func writeProjectYAML(t *testing.T, dir, contents string) string {
	t.Helper()
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	return path
}

func writeLocalYAML(t *testing.T, dir, contents string) {
	t.Helper()
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(cfgDir, "config.local.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}
}

// captureLog redirects the standard logger to a bytes.Buffer for the
// duration of fn, and returns whatever was logged.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	fn()
	return buf.String()
}

func TestLoadMergedProjectOnly(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  scan_interval: 30s
  enabled_repos:
    - nightgauge
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("owner = %q, want nightgauge", cfg.Owner)
	}
	if cfg.Autonomous == nil || len(cfg.Autonomous.EnabledRepos) != 1 {
		t.Fatalf("expected 1 enabled repo, got %+v", cfg.Autonomous)
	}
	if cfg.Autonomous.EnabledRepos[0] != "nightgauge" {
		t.Errorf("enabled_repos[0] = %q", cfg.Autonomous.EnabledRepos[0])
	}
}

func TestLoadMergedMachineProvidesEnabledRepos(t *testing.T) {
	withMachineConfig(t, `
autonomous:
  enabled_repos:
    - nightgauge
    - acme-mobile
`)
	dir := t.TempDir()
	// Project YAML deliberately does NOT set enabled_repos — this is the
	// post-migration shape where the developer's autonomy policy lives
	// in machine tier.
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  scan_interval: 30s
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Autonomous == nil {
		t.Fatal("autonomous nil")
	}
	if got, want := len(cfg.Autonomous.EnabledRepos), 2; got != want {
		t.Fatalf("enabled_repos len = %d, want %d (%v)", got, want, cfg.Autonomous.EnabledRepos)
	}
	if cfg.Autonomous.EnabledRepos[1] != "acme-mobile" {
		t.Errorf("flutter missing from merged enabled_repos: %v", cfg.Autonomous.EnabledRepos)
	}
}

// TestLoadMergedMachineProvidesPlatformSection reproduces the exact scenario
// in #333: an extension-spawned `nightgauge serve` runs with a workspace
// config.yaml that has no platform: section at all (the common case — most
// project repos don't commit license keys), while the developer's global
// ~/.nightgauge/config.yaml carries platform.api_url + platform.license_key.
// Because LoadMerged deep-merges the raw YAML tiers (machine -> project ->
// local) before handing the merged document to the single YAML parser, no
// tier-specific merge code is needed for a new leaf key — adding api_url and
// license_key to yamlConfigNested.Platform in config.go is sufficient for the
// existing generic merge to carry them through. This test guards that.
func TestLoadMergedMachineProvidesPlatformSection(t *testing.T) {
	withMachineConfig(t, `
platform:
  api_url: "https://api.nightgauge.dev"
  license_key: "lic_from_machine_tier"
`)
	dir := t.TempDir()
	// Project YAML deliberately omits platform: entirely. It declares a
	// nested project: block, matching the schema the VSCode extension
	// actually writes (schema.ts RootConfigSchema requires project.owner /
	// project.number) — this is what routes the merged document through
	// parseYAMLNested, the real production path.
	writeProjectYAML(t, dir, `
schema_version: "2"
project:
  owner: nightgauge
  number: 1
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.PlatformURL != "https://api.nightgauge.dev" {
		t.Errorf("PlatformURL = %q, want machine-tier api_url", cfg.PlatformURL)
	}
	if cfg.LicenseKey != "lic_from_machine_tier" {
		t.Errorf("LicenseKey = %q, want machine-tier license_key", cfg.LicenseKey)
	}
}

// TestLoadMergedProjectOverridesMachinePlatform confirms platform.* follows
// the same "later tier wins" precedence as every other scalar key — a
// project-tier license_key (e.g. a shared team/CI license) shadows the
// developer's personal machine-tier key.
func TestLoadMergedProjectOverridesMachinePlatform(t *testing.T) {
	withMachineConfig(t, `
platform:
  license_key: "lic_machine"
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
project:
  owner: nightgauge
  number: 1
platform:
  license_key: "lic_project"
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.LicenseKey != "lic_project" {
		t.Errorf("LicenseKey = %q, want project-tier override lic_project", cfg.LicenseKey)
	}
}

func TestLoadMergedProjectOverridesMachine(t *testing.T) {
	// When both tiers set the same scalar key, project wins. This is the
	// standard precedence rule.
	withMachineConfig(t, `
autonomous:
  budget_ceiling: 500
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  budget_ceiling: 999
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Autonomous == nil || cfg.Autonomous.BudgetCeiling != 999 {
		t.Errorf("budget_ceiling = %v, want 999 (project should win)", cfg.Autonomous)
	}
}

func TestLoadMergedLocalOverridesProject(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  budget_ceiling: 999
`)
	writeLocalYAML(t, dir, `
autonomous:
  budget_ceiling: 12345
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Autonomous == nil || cfg.Autonomous.BudgetCeiling != 12345 {
		t.Errorf("budget_ceiling = %v, want 12345 (local should win)", cfg.Autonomous)
	}
}

func TestLoadMergedNestedMappingMerge(t *testing.T) {
	// Nested mappings deep-merge — machine and project each set
	// different sub-keys under the same parent, and both survive.
	withMachineConfig(t, `
autonomous:
  scan_interval: 45s
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  budget_ceiling: 1000000
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Autonomous == nil {
		t.Fatalf("autonomous missing")
	}
	if cfg.Autonomous.BudgetCeiling != 1000000 {
		t.Errorf("budget_ceiling = %d, want 1000000 (project)", cfg.Autonomous.BudgetCeiling)
	}
	if want := 45 * 1_000_000_000; int64(cfg.Autonomous.ScanInterval) != int64(want) {
		t.Errorf("scan_interval = %d ns, want %d ns (machine)", int64(cfg.Autonomous.ScanInterval), want)
	}
}

func TestLoadMergedSequenceReplaces(t *testing.T) {
	// Sequences (lists) intentionally REPLACE rather than concatenate.
	// Project's enabled_repos wins entirely over machine's.
	withMachineConfig(t, `
autonomous:
  enabled_repos:
    - machine-a
    - machine-b
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  enabled_repos:
    - project-only
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Autonomous == nil {
		t.Fatal("autonomous nil")
	}
	if got, want := len(cfg.Autonomous.EnabledRepos), 1; got != want {
		t.Fatalf("len = %d, want %d (%v)", got, want, cfg.Autonomous.EnabledRepos)
	}
	if cfg.Autonomous.EnabledRepos[0] != "project-only" {
		t.Errorf("project should win for sequences, got %v", cfg.Autonomous.EnabledRepos)
	}
}

// #360 — the machine-tier shadow warning is emitted on every config merge
// (each IPC call resolves config), so it must dedupe to once per process to
// avoid the observed ~3s "WARN config: github_user ..." stderr flood.
func TestShadowWarningDedupesToOncePerProcess(t *testing.T) {
	resetShadowWarnDedup()

	project := []byte("github_user: alice\n")
	machine := []byte("github_user: bob\n")

	logged := captureLog(t, func() {
		// Simulate many config merges (as many IPC board.list calls would).
		for i := 0; i < 5; i++ {
			warnMachineKeysInProjectYAML(project, machine)
		}
	})

	got := strings.Count(logged, "github_user is in project YAML but is owned by the machine tier")
	if got != 1 {
		t.Errorf("expected github_user shadow warning exactly once per process, got %d:\n%s", got, logged)
	}

	// After a reset the warning is eligible to fire again (covers the
	// test-isolation helper the other shadow tests rely on).
	resetShadowWarnDedup()
	loggedAgain := captureLog(t, func() {
		warnMachineKeysInProjectYAML(project, machine)
	})
	if strings.Count(loggedAgain, "github_user is in project YAML") != 1 {
		t.Errorf("expected warning to fire again after reset, got:\n%s", loggedAgain)
	}
}

func TestLoadMergedShadowWarningFiresForMachineKeyInProject(t *testing.T) {
	resetShadowWarnDedup()
	withMachineConfig(t, `
autonomous:
  enabled_repos:
    - nightgauge
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  enabled_repos:
    - shadow-in-project
`)

	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})

	if !strings.Contains(logged, "autonomous.enabled_repos") || !strings.Contains(logged, "machine tier") {
		t.Errorf("expected shadow warning for autonomous.enabled_repos, got:\n%s", logged)
	}
}

func TestLoadMergedShadowWarningFiresForPerRepoEntry(t *testing.T) {
	// Warning fires only when both machine and project define the same slug.
	resetShadowWarnDedup()
	withMachineConfig(t, `
autonomous:
  repositories:
    nightgauge:
      sequential: true
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  repositories:
    nightgauge:
      sequential: false
`)

	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})

	if !strings.Contains(logged, "autonomous.repositories.nightgauge") {
		t.Errorf("expected per-repo shadow warning, got:\n%s", logged)
	}
}

func TestLoadMergedNoShadowWarningWhenPerRepoSlugDiffers(t *testing.T) {
	// No warning when machine and project define different repo slugs —
	// there is no actual conflict.
	withMachineConfig(t, `
autonomous:
  repositories:
    nightgauge:
      sequential: true
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  repositories:
    acme-mobile:
      sequential: true
`)

	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})

	if strings.Contains(logged, "machine tier") {
		t.Errorf("did not expect shadow warning for non-conflicting repo slugs:\n%s", logged)
	}
}

func TestLoadMergedNoShadowWarningWhenMachineKeyAbsent(t *testing.T) {
	// No warning when machine config exists but does NOT define the key
	// that the project config sets (false-positive scenario from #3761).
	withMachineConfig(t, `
schema_version: "2"
owner: nightgauge
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
pipeline:
  max_concurrent: 3
`)

	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})

	if strings.Contains(logged, "machine tier") {
		t.Errorf("did not expect shadow warning when machine config does not set the key:\n%s", logged)
	}
}

func TestLoadMergedNoShadowWarningWhenNoMachineConfig(t *testing.T) {
	// When the machine config doesn't exist, having the keys in project
	// YAML is the only valid configuration today — don't pester users
	// who haven't yet migrated.
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
autonomous:
  enabled_repos:
    - nightgauge
`)

	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})

	if strings.Contains(logged, "machine tier") {
		t.Errorf("did not expect shadow warning when machine config absent:\n%s", logged)
	}
}

func TestLoadMergedNoTiersReturnsDefaults(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir() // no project YAML, no local YAML

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	def := DefaultConfig()
	if cfg.Owner != def.Owner || cfg.LogLevel != def.LogLevel {
		t.Errorf("expected defaults, got %+v", cfg)
	}
}

func TestMachineTierKeysSnapshot(t *testing.T) {
	// Pin the exact contents so future edits trigger review of the
	// classification + shadow-warning behavior.
	want := []string{
		"github_user",
		"github_auth",
		"notifications.discord.enabled",
		"lm_studio",
		"autonomous.enabled_repos",
		"ui.core.adapter",
		"ui.core.default_model",
		"ui.core.fallback_model",
		"ui.core.auth_provider",
		"platform",
		"pipeline.max_concurrent",
	}
	if len(MachineTierKeys) != len(want) {
		t.Fatalf("MachineTierKeys length = %d, want %d (%v)", len(MachineTierKeys), len(want), MachineTierKeys)
	}
	for i, w := range want {
		if MachineTierKeys[i] != w {
			t.Errorf("MachineTierKeys[%d] = %q, want %q", i, MachineTierKeys[i], w)
		}
	}
}

func TestLoadFallsBackToLegacyJSON(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	// No YAML, but legacy JSON exists.
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	jsonPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(jsonPath, []byte(`{"Owner":"LegacyOrg","ProjectNumber":42}`), 0o644); err != nil {
		t.Fatalf("write json: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Owner != "LegacyOrg" || cfg.ProjectNumber != 42 {
		t.Errorf("expected legacy JSON to be loaded, got %+v", cfg)
	}
}

func TestLoadDoesNotMergeMachineWhenProjectAbsent(t *testing.T) {
	// Machine config is by itself meaningless — Load needs a workspace.
	withMachineConfig(t, `
log_level: debug
github_user: octocat
`)
	dir := t.TempDir() // no project YAML, no local YAML, no JSON

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := DefaultConfig()
	if cfg.Owner != def.Owner || cfg.LogLevel != def.LogLevel {
		t.Errorf("expected defaults (no project), got %+v", cfg)
	}
}

// TestMachineConfigPathEnvOverrides pins the machine-tier path resolution
// parity with the TS globalConfigResolver: NIGHTGAUGE_CONFIG_HOME wins,
// then XDG_CONFIG_HOME/nightgauge, then the ~/.nightgauge default.
func TestMachineConfigPathEnvOverrides(t *testing.T) {
	oldGOOS := machineGOOSFn
	machineGOOSFn = func() string { return "darwin" }
	t.Cleanup(func() { machineGOOSFn = oldGOOS })
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "/tmp/ib-config-home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-home")
	got, err := defaultMachineConfigPath()
	if err != nil {
		t.Fatalf("defaultMachineConfigPath: %v", err)
	}
	if want := filepath.Join("/tmp/ib-config-home", "config.yaml"); got != want {
		t.Errorf("NIGHTGAUGE_CONFIG_HOME path = %q, want %q", got, want)
	}

	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	got, err = defaultMachineConfigPath()
	if err != nil {
		t.Fatalf("defaultMachineConfigPath: %v", err)
	}
	if want := filepath.Join("/tmp/xdg-home", "nightgauge", "config.yaml"); got != want {
		t.Errorf("XDG_CONFIG_HOME path = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	got, err = defaultMachineConfigPath()
	if err != nil {
		t.Fatalf("defaultMachineConfigPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if want := filepath.Join(home, ".nightgauge", "config.yaml"); got != want {
		t.Errorf("default path = %q, want %q", got, want)
	}
}

func TestMachineConfigPathPlatformDefaults(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "/windows/appdata")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	oldGOOS := machineGOOSFn
	t.Cleanup(func() { machineGOOSFn = oldGOOS })

	tests := []struct {
		goos string
		want string
	}{
		{"darwin", filepath.Join(home, ".nightgauge", "config.yaml")},
		{"linux", filepath.Join(home, ".config", "nightgauge", "config.yaml")},
		{"windows", filepath.Join("/windows/appdata", "nightgauge", "config.yaml")},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			machineGOOSFn = func() string { return tc.goos }
			got, pathErr := defaultMachineConfigPath()
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if got != tc.want {
				t.Fatalf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadMachineConfigBytesFallsBackToLegacyLinuxPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	oldGOOS := machineGOOSFn
	machineGOOSFn = func() string { return "linux" }
	t.Cleanup(func() { machineGOOSFn = oldGOOS })
	oldPathFn := machineConfigPathFn
	machineConfigPathFn = defaultMachineConfigPath
	t.Cleanup(func() { machineConfigPathFn = oldPathFn })

	legacy := filepath.Join(home, ".nightgauge", "config.yaml")
	writeTierFile(t, legacy, "github_user: legacy-user\n")
	got, err := readMachineConfigBytes()
	if err != nil {
		t.Fatalf("readMachineConfigBytes: %v", err)
	}
	if !strings.Contains(string(got), "legacy-user") {
		t.Fatalf("legacy config not loaded: %s", got)
	}
	canonical, err := defaultMachineConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if canonical != filepath.Join(home, ".config", "nightgauge", "config.yaml") {
		t.Fatalf("canonical path = %q", canonical)
	}
}
