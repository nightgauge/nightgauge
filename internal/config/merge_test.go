package config

import (
	"bytes"
	"errors"
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

// Project-controlled platform values cannot shadow machine credentials.
func TestLoadMergedProjectCannotOverrideMachinePlatform(t *testing.T) {
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
  api_url: "https://project.example.invalid"
  license_key: env:NG_TEST_PROJECT_LICENSE
`)
	t.Setenv("NG_TEST_PROJECT_LICENSE", "lic_project")

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.LicenseKey != "lic_machine" {
		t.Errorf("LicenseKey resolved from project tier; want machine-tier value")
	}
	if cfg.PlatformURL != "" {
		t.Errorf("PlatformURL = %q resolved from project tier; want the block stripped", cfg.PlatformURL)
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
		"opencode",
		"autonomous.enabled_repos",
		"autonomous.allow_self_repo",
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

// TestMachineConfigDirIsTheDirectoryLoadReads: MachineConfigDir names the
// directory whose config.yaml the loader actually reads, the Linux legacy
// ~/.nightgauge included. A child given NIGHTGAUGE_CONFIG_HOME set to it must
// read the same machine tier (ADR-022 § 8), and a pin to the canonical
// directory while only the legacy file exists would lose it, because the
// loader takes the legacy fallback only when neither override is set.
func TestMachineConfigDirIsTheDirectoryLoadReads(t *testing.T) {
	oldGOOS := machineGOOSFn
	oldPathFn := machineConfigPathFn
	machineConfigPathFn = defaultMachineConfigPath
	t.Cleanup(func() { machineGOOSFn = oldGOOS; machineConfigPathFn = oldPathFn })

	for _, tc := range []struct {
		name, goos string
		files      []string // relative to HOME
		configHome string   // NIGHTGAUGE_CONFIG_HOME, relative to HOME
		want       string   // relative to HOME
	}{
		{name: "darwin default", goos: "darwin", want: ".nightgauge"},
		{name: "linux canonical present", goos: "linux", files: []string{".config/nightgauge/config.yaml", ".nightgauge/config.yaml"}, want: ".config/nightgauge"},
		{name: "linux canonical absent, legacy present", goos: "linux", files: []string{".nightgauge/config.yaml"}, want: ".nightgauge"},
		{name: "linux neither present", goos: "linux", want: ".config/nightgauge"},
		{name: "override wins over legacy", goos: "linux", files: []string{".nightgauge/config.yaml"}, configHome: "custom", want: "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
			if tc.configHome != "" {
				t.Setenv("NIGHTGAUGE_CONFIG_HOME", filepath.Join(home, tc.configHome))
			}
			machineGOOSFn = func() string { return tc.goos }
			for _, f := range tc.files {
				writeTierFile(t, filepath.Join(home, f), "github_user: fixture\n")
			}
			got, err := MachineConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(home, tc.want); got != want {
				t.Errorf("MachineConfigDir() = %q, want %q", got, want)
			}
		})
	}
}

// #1049 — the strip ran BEFORE the warning pass, so the one key the loader
// deletes outright could never trigger its own warning.
//
// The asymmetry was the tell: github_user warned in the same run because it is
// merely shadowed, while platform is deleted, and deletion is quieter than
// shadowing. An operator wrote platform.enabled: false, saw the warning system
// working for another key, saw nothing for theirs, and reasonably concluded
// their value was accepted. It was discarded.
func TestLoadMergedWarnsWhenTheProjectTierDefinesPlatform(t *testing.T) {
	resetShadowWarnDedup()
	withMachineConfig(t, `
platform:
  enabled: true
  api_url: https://api.example.com
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
project:
  owner: acme
  number: 1
platform:
  enabled: false
`)

	logged := captureLog(t, func() {
		if _, err := LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})

	if !strings.Contains(logged, "platform is in project YAML") {
		t.Errorf("no warning for a project-tier platform block — the value is discarded silently:\n%s", logged)
	}
	// And it must say what actually happened. "shadows" is factually wrong for
	// a key that is removed before the merge.
	if !strings.Contains(logged, "IGNORED") {
		t.Errorf("warning describes platform as shadowed rather than ignored:\n%s", logged)
	}
}

// The machine tier must still win regardless of the warning — the reorder is
// bookkeeping only and must not change precedence.
func TestLoadMergedPlatformPrecedenceUnchangedByTheWarning(t *testing.T) {
	resetShadowWarnDedup()
	withMachineConfig(t, `
platform:
  enabled: true
`)
	dir := t.TempDir()
	// Nested project: block routes through parseYAMLNested, the real
	// production path (see TestLoadMergedMachineProvidesPlatformSection).
	writeProjectYAML(t, dir, `
schema_version: "2"
project:
  owner: acme
  number: 1
platform:
  enabled: false
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.PlatformEnabled == nil || !*cfg.PlatformEnabled {
		t.Errorf("machine tier must still win: PlatformEnabled = %v, want true", cfg.PlatformEnabled)
	}
}

// TestPlaintextSecretRejected pins #2023: a repository tier (the committed
// project file or the local override) may carry a credential only as an env:
// reference. A literal value fails the load, naming the file and the key and
// never the value.
func TestPlaintextSecretRejected(t *testing.T) {
	const secret = "ghp_example"
	keys := []struct {
		name string
		yaml func(v string) string
	}{
		{"github_auth.token", func(v string) string { return "github_auth:\n  token: " + v + "\n" }},
		{"github_auth.tokens.acme", func(v string) string { return "github_auth:\n  tokens:\n    acme: " + v + "\n" }},
		{"platform.license_key", func(v string) string { return "platform:\n  license_key: " + v + "\n" }},
	}
	const base = "schema_version: \"2\"\nproject:\n  owner: acme\n  number: 1\n"

	for _, tier := range []string{"project", "local"} {
		for _, k := range keys {
			write := func(t *testing.T, dir, body string) string {
				t.Helper()
				if tier == "project" {
					return writeProjectYAML(t, dir, base+body)
				}
				writeProjectYAML(t, dir, base)
				writeLocalYAML(t, dir, body)
				return filepath.Join(dir, ".nightgauge", "config.local.yaml")
			}

			t.Run(tier+"/"+k.name+"/plaintext", func(t *testing.T) {
				withNoMachineConfig(t)
				dir := t.TempDir()
				path := write(t, dir, k.yaml(secret))
				_, err := LoadMerged(dir)
				if err == nil {
					t.Fatalf("LoadMerged accepted a plaintext %s in the %s tier", k.name, tier)
				}
				msg := err.Error()
				if !strings.Contains(msg, path) {
					t.Errorf("error does not name the file %q: %s", path, msg)
				}
				if !strings.Contains(msg, k.name) {
					t.Errorf("error does not name the key %q: %s", k.name, msg)
				}
				if strings.Contains(msg, secret) || strings.Contains(msg, "ghp_") {
					t.Errorf("error leaks the value: %s", msg)
				}
				// Load, the entry point every command uses, refuses it too.
				if _, err := Load(dir); err == nil {
					t.Errorf("Load accepted a plaintext %s in the %s tier", k.name, tier)
				}
			})

			t.Run(tier+"/"+k.name+"/env", func(t *testing.T) {
				withNoMachineConfig(t)
				t.Setenv("TEST_TOKEN", "resolved-from-env")
				dir := t.TempDir()
				write(t, dir, k.yaml("env:TEST_TOKEN"))
				cfg, err := LoadMerged(dir)
				if err != nil {
					t.Fatalf("LoadMerged rejected an env: reference: %v", err)
				}
				switch k.name {
				case "github_auth.token":
					if got, _ := cfg.ResolveToken("acme"); got != "resolved-from-env" {
						t.Errorf("ResolveToken = %q, want the env value", got)
					}
				case "github_auth.tokens.acme":
					if got, _ := cfg.ResolveToken("acme"); got != "resolved-from-env" {
						t.Errorf("ResolveToken(acme) = %q, want the env value", got)
					}
				case "platform.license_key":
					// platform is stripped from repository tiers (#1049): the
					// reference loads, and the machine tier stays the source.
					if cfg.LicenseKey != "" {
						t.Errorf("LicenseKey = %q, want the repository value stripped", cfg.LicenseKey)
					}
				}
			})
		}
	}
}

// TestPlaintextSecretRejectedLists every offending key once and follows YAML
// aliases, so an anchor cannot smuggle a literal past the check.
func TestPlaintextSecretRejectedAliasAndMany(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
owner: acme
x-pat: &pat ghp_anchored
github_auth:
  token: *pat
  tokens:
    acme: literal-one
    other: env:OTHER
`)
	_, err := LoadMerged(dir)
	if err == nil {
		t.Fatal("LoadMerged accepted an aliased plaintext token")
	}
	msg := err.Error()
	for _, want := range []string{"github_auth.token", "github_auth.tokens.acme"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not name %s: %s", want, msg)
		}
	}
	for _, leak := range []string{"ghp_anchored", "literal-one", "github_auth.tokens.other"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error contains %q: %s", leak, msg)
		}
	}
}

// TestMachineTierPlaintextAllowed: the machine tier lives outside every
// repository, so a literal token and license key load from it.
func TestMachineTierPlaintextAllowed(t *testing.T) {
	withMachineConfig(t, `
github_auth:
  token: ghp_machine_literal
  tokens:
    acme: ghp_machine_acme
platform:
  license_key: lic_machine_literal
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, "schema_version: \"2\"\nproject:\n  owner: acme\n  number: 1\n")
	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged rejected a machine-tier plaintext value: %v", err)
	}
	if got, _ := cfg.ResolveToken("acme"); got != "ghp_machine_literal" {
		t.Errorf("ResolveToken = %q, want the machine-tier token", got)
	}
	if cfg.LicenseKey != "lic_machine_literal" {
		t.Errorf("LicenseKey = %q, want the machine-tier key", cfg.LicenseKey)
	}
}

// TestPlaintextSecretErrorNamesMachinePath: the error tells the operator where
// a literal value is accepted, by the path the loader actually reads.
func TestPlaintextSecretErrorNamesMachinePath(t *testing.T) {
	withMachineConfig(t, "")
	machine, _ := machineConfigPathFn()
	err := ValidateRepoTierSecrets([]byte("github_auth:\n  token: literal\n"), "/repo/.nightgauge/config.yaml")
	if err == nil || !strings.Contains(err.Error(), machine) {
		t.Fatalf("error does not name the machine-tier path %q: %v", machine, err)
	}
}

// TestRepoTierProtectedKeysShareOneList pins the single list behind both
// protections: every hard-stripped block is recognised by
// isHardStrippedMachineKey, and every protected block names a credential path
// that ValidateRepoTierSecrets enforces.
func TestRepoTierProtectedKeysShareOneList(t *testing.T) {
	stripped := 0
	for _, k := range repoTierProtectedKeys {
		if k.hardStrip != isHardStrippedMachineKey(k.root) {
			t.Errorf("%s: hardStrip=%v but isHardStrippedMachineKey=%v", k.root, k.hardStrip, isHardStrippedMachineKey(k.root))
		}
		if k.hardStrip {
			stripped++
		}
		for _, secret := range k.secrets {
			path := k.root + "." + strings.ReplaceAll(secret, "*", "someowner")
			segs := strings.Split(path, ".")
			body := ""
			for i, seg := range segs {
				body += strings.Repeat("  ", i) + seg + ":"
				if i < len(segs)-1 {
					body += "\n"
				}
			}
			body += " literal\n"
			if err := ValidateRepoTierSecrets([]byte(body), "f.yaml"); err == nil || !strings.Contains(err.Error(), path) {
				t.Errorf("%s is listed but not enforced: %v", path, err)
			}
		}
	}
	if stripped == 0 || !isHardStrippedMachineKey("platform") {
		t.Error("platform must stay hard-stripped from repository tiers (#1049)")
	}
	if isHardStrippedMachineKey("github_user") {
		t.Error("isHardStrippedMachineKey reports a key the list does not strip")
	}
}

// TestPlaintextSecretRejectedThroughMergeKeys: yaml.v3 resolves `<<` merge
// keys when the loader decodes, so the check must too — at the leaf, at the
// block, and at the root (review finding on #2023).
func TestPlaintextSecretRejectedThroughMergeKeys(t *testing.T) {
	cases := map[string]string{
		"block merge": `
x-b: &b {token: literal-merge-token}
github_auth:
  <<: *b
`,
		"tokens merge": `
x-b: &b {acme: literal-merge-token}
github_auth:
  tokens:
    <<: *b
`,
		"root merge": `
x-b: &b
  github_auth:
    token: literal-merge-token
<<: *b
`,
		"sequence merge": `
x-a: &a {token: literal-merge-token}
x-c: &c {suppress_gh_warning: true}
github_auth:
  <<: [*c, *a]
`,
		"null tag": `
github_auth:
  token: !!null literal-merge-token
`,
		"mapping value": `
github_auth:
  token: {inner: literal-merge-token}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			withNoMachineConfig(t)
			dir := t.TempDir()
			writeProjectYAML(t, dir, "owner: acme\n"+body)
			cfg, err := LoadMerged(dir)
			if err == nil {
				tok, _ := cfg.ResolveToken("acme")
				t.Fatalf("loaded; ResolveToken returned %d bytes", len(tok))
			}
			if !errors.Is(err, ErrRepoTierCredential) {
				t.Errorf("error is not ErrRepoTierCredential: %v", err)
			}
			if strings.Contains(err.Error(), "literal-merge-token") {
				t.Errorf("error leaks the value: %v", err)
			}
		})
	}
}

// TestEnvRefShapedLikeTokenRejected: `env:` followed by a token is a pasted
// token, not a variable name. The loader refuses it and resolveEnvRef never
// echoes it.
func TestEnvRefShapedLikeTokenRejected(t *testing.T) {
	tok := "ghp_" + strings.Repeat("A1", 18)
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, "owner: acme\ngithub_auth:\n  token: env:"+tok+"\n")
	_, err := LoadMerged(dir)
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("LoadMerged = %v, want a redacted refusal", err)
	}
	if _, err := resolveEnvRef("env:" + tok); err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("resolveEnvRef = %v, want a redacted error", err)
	}
	// An owner name that is a pasted token is redacted too.
	writeProjectYAML(t, dir, "owner: acme\ngithub_auth:\n  tokens:\n    "+tok+": literal\n")
	if _, err := LoadMerged(dir); err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("LoadMerged = %v, want a refusal that does not echo the key", err)
	}
}

// TestPlaintextSecretErrorNamesRefresh: the refusal names the migration
// command that removes literal GitHub tokens from these files.
func TestPlaintextSecretErrorNamesRefresh(t *testing.T) {
	withNoMachineConfig(t)
	err := ValidateRepoTierSecrets([]byte("github_auth:\n  token: literal\n"), "/repo/.nightgauge/config.yaml")
	if err == nil || !strings.Contains(err.Error(), "nightgauge forge auth refresh") {
		t.Fatalf("error does not name the migration command: %v", err)
	}
}

// TestLegacyJSONCredentialsRejected: the legacy config.json is a repository
// tier too; encoding/json matches keys case-insensitively, so the typed
// decode is what is checked, and the license key never survives.
func TestLegacyJSONCredentialsRejected(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	withNoMachineConfig(t)
	for name, body := range map[string]string{
		"token":        `{"owner":"acme","githubAuth":{"token":"literal-json-token"}}`,
		"case variant": `{"owner":"acme","GITHUBAUTH":{"TOKEN":"literal-json-token"}}`,
		"tokens":       `{"owner":"acme","githubAuth":{"tokens":{"acme":"literal-json-token"}}}`,
		"license":      `{"owner":"acme","licenseKey":"literal-json-token"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := write(t, body)
			_, err := Load(dir)
			if err == nil || !errors.Is(err, ErrRepoTierCredential) || strings.Contains(err.Error(), "literal-json-token") {
				t.Fatalf("Load = %v, want a redacted refusal", err)
			}
			if !strings.Contains(err.Error(), "config.json") {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
	t.Run("env reference loads and platform is stripped", func(t *testing.T) {
		t.Setenv("NG_TEST_JSON_TOKEN", "from-env")
		dir := write(t, `{"owner":"acme","githubAuth":{"token":"env:NG_TEST_JSON_TOKEN"},"platformUrl":"https://x.invalid","licenseKey":"env:NG_TEST_JSON_TOKEN"}`)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got, _ := cfg.ResolveToken("acme"); got != "from-env" {
			t.Errorf("ResolveToken = %q", got)
		}
		if cfg.LicenseKey != "" || cfg.PlatformURL != "" {
			t.Errorf("platform survived the legacy JSON tier: %q %q", cfg.LicenseKey, cfg.PlatformURL)
		}
	})
}

// TestHomeDirProjectIsMachineTier: run from $HOME, .nightgauge/config.yaml is
// the machine file. It is not a repository tier, so a literal loads.
func TestHomeDirProjectIsMachineTier(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		home := t.TempDir()
		machine := filepath.Join(home, ".nightgauge", "config.yaml")
		prev := machineConfigPathFn
		machineConfigPathFn = func() (string, error) { return machine, nil }
		t.Cleanup(func() { machineConfigPathFn = prev })
		writeProjectYAML(t, home, "owner: acme\ngithub_auth:\n  token: literal-home-token\n")
		cfg, err := LoadMerged(home)
		if err != nil {
			t.Fatalf("LoadMerged from home: %v", err)
		}
		if got, _ := cfg.ResolveToken("acme"); got != "literal-home-token" {
			t.Errorf("ResolveToken = %q", got)
		}
	})
	t.Run("linux legacy through a symlink", func(t *testing.T) {
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "home")
		if err := os.Symlink(real, link); err != nil {
			t.Skip("symlinks unavailable")
		}
		t.Setenv("HOME", real)
		t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		prevGOOS := machineGOOSFn
		machineGOOSFn = func() string { return "linux" }
		prev := machineConfigPathFn
		machineConfigPathFn = defaultMachineConfigPath
		t.Cleanup(func() { machineGOOSFn = prevGOOS; machineConfigPathFn = prev })
		writeProjectYAML(t, real, "owner: acme\ngithub_auth:\n  token: literal-home-token\n")
		if _, err := LoadMerged(link); err != nil {
			t.Fatalf("LoadMerged from the legacy machine directory: %v", err)
		}
	})
}
