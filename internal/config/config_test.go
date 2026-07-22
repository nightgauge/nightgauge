package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Phase 1: JSON config tests ────────────────────────────────────────────────

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want %q", cfg.Owner, "nightgauge")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadMissingConfig(t *testing.T) {
	cfg, err := Load("/nonexistent/path")
	if err != nil {
		t.Fatalf("Load should not error for missing config: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("should return defaults, got Owner = %q", cfg.Owner)
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	configJSON := `{"owner":"TestOrg","projectNumber":42,"defaultRepo":"test-repo","logLevel":"debug"}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Owner != "TestOrg" {
		t.Errorf("Owner = %q, want %q", cfg.Owner, "TestOrg")
	}
	if cfg.ProjectNumber != 42 {
		t.Errorf("ProjectNumber = %d, want 42", cfg.ProjectNumber)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Error("Load should fail on invalid JSON")
	}
}

// ── Phase 2: YAML config parsing tests ───────────────────────────────────────

// fixtureYAML reads a test fixture from tests/fixtures/config/ relative to the
// repo root (two levels above this package).
func fixtureYAML(t *testing.T, name string) string {
	t.Helper()
	// This package is at internal/config/; fixtures are at tests/fixtures/config/
	path := filepath.Join("..", "..", "tests", "fixtures", "config", name)
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("fixtureYAML: resolve path %q: %v", path, err)
	}
	return abs
}

// TestLoadYAML_NestedFormat verifies that the current nested YAML format
// (project.owner / project.number) is parsed correctly.
func TestLoadYAML_NestedFormat(t *testing.T) {
	path := fixtureYAML(t, "nightgauge-nested.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML nested: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want nightgauge", cfg.Owner)
	}
	if cfg.ProjectNumber != 1 {
		t.Errorf("ProjectNumber = %d, want 1", cfg.ProjectNumber)
	}
}

// TestLoadYAML_PlatformNestedFormat verifies that the platform repo's nested
// YAML format is parsed correctly (project.number = 2, project.repo).
func TestLoadYAML_PlatformNestedFormat(t *testing.T) {
	path := fixtureYAML(t, "platform-nested.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML platform-nested: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want nightgauge", cfg.Owner)
	}
	if cfg.ProjectNumber != 2 {
		t.Errorf("ProjectNumber = %d, want 2", cfg.ProjectNumber)
	}
	if cfg.DefaultRepo != "acme-platform" {
		t.Errorf("DefaultRepo = %q, want acme-platform", cfg.DefaultRepo)
	}
}

// TestLoadYAML_PlatformSection verifies that platform.api_url and
// platform.license_key (the fields the VSCode extension actually writes,
// see packages/nightgauge-vscode/src/config/schema.ts PlatformConfigSchema)
// are parsed into Config.PlatformURL / Config.LicenseKey. Before #333 only
// platform.telemetry was wired up, so `nightgauge serve` never saw a
// configured platform even when config.yaml declared one.
func TestLoadYAML_PlatformSection(t *testing.T) {
	yaml := `
owner: nightgauge
project:
  number: 1
platform:
  api_url: "https://api.example.com"
  license_key: "lic_abc123"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(p)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if cfg.PlatformURL != "https://api.example.com" {
		t.Errorf("PlatformURL = %q, want https://api.example.com", cfg.PlatformURL)
	}
	if cfg.LicenseKey != "lic_abc123" {
		t.Errorf("LicenseKey = %q, want lic_abc123", cfg.LicenseKey)
	}
}

// TestLoadYAML_PlatformSection_Absent verifies that omitting platform:
// entirely leaves PlatformURL/LicenseKey empty — the default, fully-local,
// zero-behavior-change case the issue's acceptance criteria requires.
func TestLoadYAML_PlatformSection_Absent(t *testing.T) {
	yaml := `
owner: nightgauge
project:
  number: 1
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(p)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if cfg.PlatformURL != "" {
		t.Errorf("PlatformURL = %q, want empty", cfg.PlatformURL)
	}
	if cfg.LicenseKey != "" {
		t.Errorf("LicenseKey = %q, want empty", cfg.LicenseKey)
	}
}

// TestLoadYAML_LegacyFlatFormat verifies that the legacy flat YAML format
// (bare owner / project as integer) is parsed correctly.
func TestLoadYAML_LegacyFlatFormat(t *testing.T) {
	path := fixtureYAML(t, "legacy-flat.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML legacy-flat: %v", err)
	}
	if cfg.Owner != "TestOrg" {
		t.Errorf("Owner = %q, want TestOrg", cfg.Owner)
	}
	if cfg.ProjectNumber != 42 {
		t.Errorf("ProjectNumber = %d, want 42", cfg.ProjectNumber)
	}
	if cfg.DefaultRepo != "my-repo" {
		t.Errorf("DefaultRepo = %q, want my-repo", cfg.DefaultRepo)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

// TestLoadYAML_MissingOwner_NestedFormat verifies that a nested YAML config
// with a project.number but no project.owner returns a clear error rather than
// silently succeeding with an empty owner.
func TestLoadYAML_MissingOwner_NestedFormat(t *testing.T) {
	path := fixtureYAML(t, "missing-owner.yaml")
	_, err := LoadYAML(path)
	if err == nil {
		t.Fatal("LoadYAML missing owner: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "owner") {
		t.Errorf("error %q should mention 'owner'", err.Error())
	}
}

// TestLoadYAML_MissingOwner_FlatFormat verifies that a flat YAML config with
// no owner field returns a clear error (not silent failure).
func TestLoadYAML_MissingOwner_FlatFormat(t *testing.T) {
	yaml := "project: 99\ndefaultRepo: some-repo\n"
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadYAML(p)
	if err == nil {
		t.Fatal("LoadYAML flat missing owner: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "owner") {
		t.Errorf("error %q should mention 'owner'", err.Error())
	}
}

// TestLoadYAML_MissingProjectNumber verifies that a config with owner but no
// project.number parses successfully with ProjectNumber == 0 (zero is allowed —
// it means "not configured").
func TestLoadYAML_MissingProjectNumber(t *testing.T) {
	path := fixtureYAML(t, "missing-project-number.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML missing project number: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want nightgauge", cfg.Owner)
	}
	if cfg.ProjectNumber != 0 {
		t.Errorf("ProjectNumber = %d, want 0 (unset)", cfg.ProjectNumber)
	}
}

// TestLoadYAML_MalformedYAML verifies that malformed YAML returns an error.
func TestLoadYAML_MalformedYAML(t *testing.T) {
	path := fixtureYAML(t, "malformed.yaml")
	_, err := LoadYAML(path)
	if err == nil {
		t.Fatal("LoadYAML malformed: expected error, got nil")
	}
}

// TestLoad_PrefersYAMLOverJSON verifies that when both config.yaml and
// config.json exist, the YAML file takes precedence.
func TestLoad_PrefersYAMLOverJSON(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := "project:\n  owner: YAMLOrg\n  number: 10\n"
	jsonContent := `{"owner":"JSONOrg","projectNumber":20}`

	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(jsonContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Owner != "YAMLOrg" {
		t.Errorf("Owner = %q, want YAMLOrg (YAML should win over JSON)", cfg.Owner)
	}
	if cfg.ProjectNumber != 10 {
		t.Errorf("ProjectNumber = %d, want 10", cfg.ProjectNumber)
	}
}

// TestLoad_YAMLFallback verifies that Load falls back to config.json when no
// config.yaml is present.
func TestLoad_YAMLFallback(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	jsonContent := `{"owner":"FallbackOrg","projectNumber":99,"logLevel":"warn"}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(jsonContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load fallback to JSON: %v", err)
	}
	if cfg.Owner != "FallbackOrg" {
		t.Errorf("Owner = %q, want FallbackOrg", cfg.Owner)
	}
	if cfg.ProjectNumber != 99 {
		t.Errorf("ProjectNumber = %d, want 99", cfg.ProjectNumber)
	}
}

// TestLoad_YAMLNestedInline verifies nested YAML parsing via Load() without
// relying on fixture files.
func TestLoadYAML_HybridFormat(t *testing.T) {
	// Hybrid: top-level owner/repo + nested project block with just number.
	// This is the actual format used by .nightgauge/config.yaml.
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := "owner: nightgauge\nrepo: nightgauge\nproject:\n  number: 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load hybrid: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want nightgauge", cfg.Owner)
	}
	if cfg.ProjectNumber != 1 {
		t.Errorf("ProjectNumber = %d, want 1", cfg.ProjectNumber)
	}
	if cfg.DefaultRepo != "nightgauge" {
		t.Errorf("DefaultRepo = %q, want nightgauge", cfg.DefaultRepo)
	}
}

// ── #3859: github: block resolution (legacy member configs) ──────────────────

// loadYAMLFromString writes the given YAML to a temp config.yaml and loads it.
func loadYAMLFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(p)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	return cfg
}

// A nested-format member config that carries owner/repo ONLY inside the legacy
// github: block (no top-level repo:, no project.repo) must still resolve a
// non-empty Owner/DefaultRepo so workspace sync-payload includes it (AC #2/#4).
func TestLoadYAML_GitHubBlock_Nested(t *testing.T) {
	cfg := loadYAMLFromString(t, "github:\n  owner: Acme-Community\n  repo: acmesvc-tracker\nproject:\n  number: 1\n")
	if cfg.Owner != "Acme-Community" {
		t.Errorf("Owner = %q, want Acme-Community (from github.owner)", cfg.Owner)
	}
	if cfg.DefaultRepo != "acmesvc-tracker" {
		t.Errorf("DefaultRepo = %q, want acmesvc-tracker (from github.repo)", cfg.DefaultRepo)
	}
	if cfg.ProjectNumber != 1 {
		t.Errorf("ProjectNumber = %d, want 1", cfg.ProjectNumber)
	}
}

// Flat-format member config with owner/repo only in the github: block.
func TestLoadYAML_GitHubBlock_Flat(t *testing.T) {
	cfg := loadYAMLFromString(t, "github:\n  owner: FlatGitHubOrg\n  repo: flat-gh-repo\nproject: 3\n")
	if cfg.Owner != "FlatGitHubOrg" {
		t.Errorf("Owner = %q, want FlatGitHubOrg (from github.owner)", cfg.Owner)
	}
	if cfg.DefaultRepo != "flat-gh-repo" {
		t.Errorf("DefaultRepo = %q, want flat-gh-repo (from github.repo)", cfg.DefaultRepo)
	}
	if cfg.ProjectNumber != 3 {
		t.Errorf("ProjectNumber = %d, want 3", cfg.ProjectNumber)
	}
}

// Precedence (nested): project.owner/project.repo must win over the github:
// block — the block is the LAST fallback only. (R2 mitigation.)
func TestLoadYAML_GitHubBlock_PrecedenceNested(t *testing.T) {
	cfg := loadYAMLFromString(t, "owner: TopLevelOrg\nrepo: top-level-repo\ngithub:\n  owner: ShouldNotWinOrg\n  repo: should-not-win-repo\nproject:\n  owner: ProjectOrg\n  repo: project-repo\n  number: 5\n")
	if cfg.Owner != "ProjectOrg" {
		t.Errorf("Owner = %q, want ProjectOrg (project.owner wins over github.owner)", cfg.Owner)
	}
	if cfg.DefaultRepo != "project-repo" {
		t.Errorf("DefaultRepo = %q, want project-repo (project.repo wins over github.repo)", cfg.DefaultRepo)
	}
}

// Precedence (flat): top-level owner/defaultRepo must win over the github: block.
func TestLoadYAML_GitHubBlock_PrecedenceFlat(t *testing.T) {
	cfg := loadYAMLFromString(t, "owner: FlatTopOrg\ndefaultRepo: flat-top-repo\ngithub:\n  owner: ShouldNotWinOrg\n  repo: should-not-win-repo\nproject: 2\n")
	if cfg.Owner != "FlatTopOrg" {
		t.Errorf("Owner = %q, want FlatTopOrg (top-level owner wins over github.owner)", cfg.Owner)
	}
	if cfg.DefaultRepo != "flat-top-repo" {
		t.Errorf("DefaultRepo = %q, want flat-top-repo (top-level defaultRepo wins over github.repo)", cfg.DefaultRepo)
	}
}

// ── Phase 3: Autonomous config tests (Issue #2536) ────────────────────────────

// TestDefaultAutonomousConfig_NewFields verifies that DefaultAutonomousConfig
// returns sensible defaults for the four new refinement fields.
func TestDefaultAutonomousConfig_NewFields(t *testing.T) {
	cfg := DefaultAutonomousConfig()
	if cfg.AutoActionable == nil {
		t.Fatal("AutoActionable should not be nil")
	}
	if *cfg.AutoActionable != false {
		t.Errorf("AutoActionable default = %v, want false", *cfg.AutoActionable)
	}
	if cfg.RefinementEnabled == nil {
		t.Fatal("RefinementEnabled should not be nil")
	}
	if *cfg.RefinementEnabled != true {
		t.Errorf("RefinementEnabled default = %v, want true", *cfg.RefinementEnabled)
	}
	if cfg.RefinementInterval.Duration() != 60*time.Second {
		t.Errorf("RefinementInterval default = %s, want 60s", cfg.RefinementInterval.Duration())
	}
	if cfg.RefinementMaxConcurrent != 1 {
		t.Errorf("RefinementMaxConcurrent default = %d, want 1", cfg.RefinementMaxConcurrent)
	}
}

// TestLoadYAML_AutonomousNewFields verifies that all four new autonomous fields
// parse correctly from inline YAML.
func TestLoadYAML_AutonomousNewFields(t *testing.T) {
	yaml := `
owner: TestOrg
project:
  number: 1
autonomous:
  auto_actionable: true
  refinement_enabled: false
  refinement_interval: "2m"
  refinement_max_concurrent: 2
  stall_detection_minutes: 75
  auto_redispatch_stalled: true
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(p)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	a := cfg.Autonomous
	if a == nil {
		t.Fatal("Autonomous should not be nil")
	}
	if a.AutoActionable == nil || *a.AutoActionable != true {
		t.Errorf("AutoActionable = %v, want true", a.AutoActionable)
	}
	if a.RefinementEnabled == nil || *a.RefinementEnabled != false {
		t.Errorf("RefinementEnabled = %v, want false", a.RefinementEnabled)
	}
	if a.RefinementInterval.Duration() != 2*time.Minute {
		t.Errorf("RefinementInterval = %s, want 2m", a.RefinementInterval.Duration())
	}
	if a.RefinementMaxConcurrent != 2 {
		t.Errorf("RefinementMaxConcurrent = %d, want 2", a.RefinementMaxConcurrent)
	}
	if a.StallDetectionMinutes != 75 {
		t.Errorf("StallDetectionMinutes = %d, want 75", a.StallDetectionMinutes)
	}
	if a.AutoRedispatchStalled == nil || *a.AutoRedispatchStalled != true {
		t.Errorf("AutoRedispatchStalled = %v, want true", a.AutoRedispatchStalled)
	}
}

// TestLoadYAML_AutonomousNewFields_Defaults verifies that omitting new fields
// from YAML results in nil/zero values (defaults applied by DefaultAutonomousConfig).
func TestLoadYAML_AutonomousNewFields_Defaults(t *testing.T) {
	yaml := `
owner: TestOrg
project:
  number: 1
autonomous:
  scan_interval: "45s"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(p)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	a := cfg.Autonomous
	if a == nil {
		t.Fatal("Autonomous should not be nil")
	}
	// New optional fields should be nil when omitted — callers apply defaults.
	if a.AutoActionable != nil {
		t.Errorf("AutoActionable = %v, want nil (omitted)", a.AutoActionable)
	}
	if a.RefinementEnabled != nil {
		t.Errorf("RefinementEnabled = %v, want nil (omitted)", a.RefinementEnabled)
	}
	if a.RefinementInterval != 0 {
		t.Errorf("RefinementInterval = %s, want zero (omitted)", a.RefinementInterval.Duration())
	}
	if a.RefinementMaxConcurrent != 0 {
		t.Errorf("RefinementMaxConcurrent = %d, want 0 (omitted)", a.RefinementMaxConcurrent)
	}
	if a.StallDetectionMinutes != 0 {
		t.Errorf("StallDetectionMinutes = %d, want 0 (omitted)", a.StallDetectionMinutes)
	}
	if a.AutoRedispatchStalled != nil {
		t.Errorf("AutoRedispatchStalled = %v, want nil (omitted)", a.AutoRedispatchStalled)
	}
}

func TestDefaultAutonomousConfig_StallWatchdogDefaults(t *testing.T) {
	a := DefaultAutonomousConfig()
	if got := a.ResolvedStallDetectionMinutes(); got != 60 {
		t.Fatalf("ResolvedStallDetectionMinutes = %d, want 60", got)
	}
	if a.IsAutoRedispatchStalled() {
		t.Fatal("IsAutoRedispatchStalled should default to false")
	}
}

// TestValidateAutonomousConfig_RefinementIntervalTooShort verifies that
// refinement_interval values below 30s return a descriptive error.
func TestValidateAutonomousConfig_RefinementIntervalTooShort(t *testing.T) {
	a := &AutonomousConfig{
		RefinementInterval: YAMLDuration(10 * time.Second),
	}
	err := ValidateAutonomousConfig(a)
	if err == nil {
		t.Fatal("expected error for refinement_interval < 30s, got nil")
	}
	if !strings.Contains(err.Error(), "30s") {
		t.Errorf("error %q should mention '30s'", err.Error())
	}
}

// TestValidateAutonomousConfig_RefinementIntervalAtMinimum verifies that
// refinement_interval == 30s is accepted.
func TestValidateAutonomousConfig_RefinementIntervalAtMinimum(t *testing.T) {
	a := &AutonomousConfig{
		RefinementInterval: YAMLDuration(30 * time.Second),
	}
	if err := ValidateAutonomousConfig(a); err != nil {
		t.Errorf("expected no error for refinement_interval = 30s, got: %v", err)
	}
}

// TestValidateAutonomousConfig_MaxConcurrentOutOfRange verifies that
// refinement_max_concurrent outside [1, 3] returns a descriptive error.
// Note: 0 is treated as "not set" (zero value) and is always valid.
func TestValidateAutonomousConfig_MaxConcurrentOutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		value int
	}{
		{"above maximum", 4},
		{"way above maximum", 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &AutonomousConfig{RefinementMaxConcurrent: tc.value}
			err := ValidateAutonomousConfig(a)
			if err == nil {
				t.Fatalf("expected error for refinement_max_concurrent=%d, got nil", tc.value)
			}
			if !strings.Contains(err.Error(), "[1, 3]") {
				t.Errorf("error %q should mention '[1, 3]'", err.Error())
			}
		})
	}
}

// TestValidateAutonomousConfig_MaxConcurrentValidRange verifies that
// refinement_max_concurrent values 1, 2, 3 are all accepted.
func TestValidateAutonomousConfig_MaxConcurrentValidRange(t *testing.T) {
	for _, v := range []int{1, 2, 3} {
		a := &AutonomousConfig{RefinementMaxConcurrent: v}
		if err := ValidateAutonomousConfig(a); err != nil {
			t.Errorf("refinement_max_concurrent=%d should be valid, got: %v", v, err)
		}
	}
}

// TestValidateAutonomousConfig_Nil verifies that nil config passes validation.
func TestValidateAutonomousConfig_Nil(t *testing.T) {
	if err := ValidateAutonomousConfig(nil); err != nil {
		t.Errorf("nil config should pass validation, got: %v", err)
	}
}

// TestLoadYAML_AutonomousRefinementFixture loads the autonomous-refinement.yaml
// fixture and verifies all four new fields parse correctly.
func TestLoadYAML_AutonomousRefinementFixture(t *testing.T) {
	path := fixtureYAML(t, "autonomous-refinement.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML autonomous-refinement fixture: %v", err)
	}
	a := cfg.Autonomous
	if a == nil {
		t.Fatal("Autonomous should not be nil")
	}
	if a.AutoActionable == nil || *a.AutoActionable != false {
		t.Errorf("AutoActionable = %v, want false", a.AutoActionable)
	}
	if a.RefinementEnabled == nil || *a.RefinementEnabled != true {
		t.Errorf("RefinementEnabled = %v, want true", a.RefinementEnabled)
	}
	if a.RefinementInterval.Duration() != 60*time.Second {
		t.Errorf("RefinementInterval = %s, want 60s", a.RefinementInterval.Duration())
	}
	if a.RefinementMaxConcurrent != 1 {
		t.Errorf("RefinementMaxConcurrent = %d, want 1", a.RefinementMaxConcurrent)
	}
	// Validate passes for the fixture values
	if err := ValidateAutonomousConfig(a); err != nil {
		t.Errorf("ValidateAutonomousConfig on fixture: %v", err)
	}
}

func TestLoad_YAMLNestedInline(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := "project:\n  owner: InlineOrg\n  number: 7\nlogLevel: warn\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load nested inline: %v", err)
	}
	if cfg.Owner != "InlineOrg" {
		t.Errorf("Owner = %q, want InlineOrg", cfg.Owner)
	}
	if cfg.ProjectNumber != 7 {
		t.Errorf("ProjectNumber = %d, want 7", cfg.ProjectNumber)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn", cfg.LogLevel)
	}
}

// TestValidateAutonomousConfig_OnFailureStatusValid verifies that valid
// on_failure_status values pass validation.
func TestValidateAutonomousConfig_OnFailureStatusValid(t *testing.T) {
	for _, v := range []string{"", "ready", "backlog", "unchanged"} {
		a := &AutonomousConfig{OnFailureStatus: v}
		if err := ValidateAutonomousConfig(a); err != nil {
			t.Errorf("on_failure_status=%q should be valid, got: %v", v, err)
		}
	}
}

// TestValidateAutonomousConfig_OnFailureStatusInvalid verifies that an invalid
// on_failure_status value returns a descriptive error.
func TestValidateAutonomousConfig_OnFailureStatusInvalid(t *testing.T) {
	a := &AutonomousConfig{OnFailureStatus: "done"}
	err := ValidateAutonomousConfig(a)
	if err == nil {
		t.Fatal("expected error for on_failure_status='done', got nil")
	}
	if !strings.Contains(err.Error(), "on_failure_status") {
		t.Errorf("error %q should mention 'on_failure_status'", err.Error())
	}
}

// TestResolvedOnFailureStatus verifies the default and explicit behavior.
func TestResolvedOnFailureStatus(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *AutonomousConfig
		wantVal string
	}{
		{"nil config", nil, "ready"},
		{"empty value", &AutonomousConfig{}, "ready"},
		{"explicit ready", &AutonomousConfig{OnFailureStatus: "ready"}, "ready"},
		{"explicit backlog", &AutonomousConfig{OnFailureStatus: "backlog"}, "backlog"},
		{"explicit unchanged", &AutonomousConfig{OnFailureStatus: "unchanged"}, "unchanged"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.ResolvedOnFailureStatus()
			if got != tc.wantVal {
				t.Errorf("ResolvedOnFailureStatus() = %q, want %q", got, tc.wantVal)
			}
		})
	}
}

// TestResolvedEnabledRepos verifies normalization of EnabledRepos entries.
func TestResolvedEnabledRepos(t *testing.T) {
	tests := []struct {
		name  string
		cfg   *AutonomousConfig
		owner string
		want  []string
	}{
		{"nil config", nil, "nightgauge", nil},
		{"empty list", &AutonomousConfig{}, "nightgauge", nil},
		{
			"short names expand with owner",
			&AutonomousConfig{EnabledRepos: []string{"acme-platform", "acme-mobile"}},
			"nightgauge",
			[]string{"nightgauge/acme-platform", "nightgauge/acme-mobile"},
		},
		{
			"fully-qualified passes through",
			&AutonomousConfig{EnabledRepos: []string{"acme/platform"}},
			"nightgauge",
			[]string{"acme/platform"},
		},
		{
			"mixed short and full",
			&AutonomousConfig{EnabledRepos: []string{"acme/platform", "acme-mobile"}},
			"nightgauge",
			[]string{"acme/platform", "nightgauge/acme-mobile"},
		},
		{
			"whitespace trimmed, empties dropped",
			&AutonomousConfig{EnabledRepos: []string{" acme-platform ", "", "  "}},
			"nightgauge",
			[]string{"nightgauge/acme-platform"},
		},
		{
			"short name without owner kept as-is",
			&AutonomousConfig{EnabledRepos: []string{"acme-platform"}},
			"",
			[]string{"acme-platform"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.ResolvedEnabledRepos(tc.owner)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestResolvedExcludeLabels verifies the autonomous.exclude_labels resolution
// (#317): unset/empty falls back to the single default ["owner-action"], a
// configured list overrides it entirely (not additively), and whitespace/
// empty entries are trimmed and dropped like ResolvedEnabledRepos.
func TestResolvedExcludeLabels(t *testing.T) {
	tests := []struct {
		name string
		cfg  *AutonomousConfig
		want []string
	}{
		{"nil config", nil, []string{"owner-action"}},
		{"empty value", &AutonomousConfig{}, []string{"owner-action"}},
		{"explicit empty list", &AutonomousConfig{ExcludeLabels: []string{}}, []string{"owner-action"}},
		{
			"custom single label overrides default",
			&AutonomousConfig{ExcludeLabels: []string{"needs-human"}},
			[]string{"needs-human"},
		},
		{
			"custom multi-label list",
			&AutonomousConfig{ExcludeLabels: []string{"owner-action", "needs-human"}},
			[]string{"owner-action", "needs-human"},
		},
		{
			"whitespace trimmed, empties dropped",
			&AutonomousConfig{ExcludeLabels: []string{" needs-human ", "", "  "}},
			[]string{"needs-human"},
		},
		{
			"all entries blank falls back to default",
			&AutonomousConfig{ExcludeLabels: []string{"", "  "}},
			[]string{"owner-action"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.ResolvedExcludeLabels()
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ── Phase N: GitHub token resolution tests (#2663) ───────────────────────────

func TestResolveEnvRef_DirectValue(t *testing.T) {
	got, err := resolveEnvRef("ghp_directtoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ghp_directtoken" {
		t.Errorf("got %q, want %q", got, "ghp_directtoken")
	}
}

func TestResolveEnvRef_Valid(t *testing.T) {
	t.Setenv("TEST_GH_TOKEN_VALID", "ghp_fromenv")
	got, err := resolveEnvRef("env:TEST_GH_TOKEN_VALID")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ghp_fromenv" {
		t.Errorf("got %q, want %q", got, "ghp_fromenv")
	}
}

func TestResolveEnvRef_Missing(t *testing.T) {
	t.Setenv("TEST_GH_TOKEN_MISSING", "")
	_, err := resolveEnvRef("env:TEST_GH_TOKEN_MISSING")
	if err == nil {
		t.Error("expected error for empty env var, got nil")
	}
}

func TestResolveEnvRef_EmptyVarName(t *testing.T) {
	_, err := resolveEnvRef("env:")
	if err == nil {
		t.Error("expected error for empty variable name, got nil")
	}
	if !strings.Contains(err.Error(), "variable name is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveToken_ProjectLevel(t *testing.T) {
	t.Setenv("TEST_PROJECT_TOKEN", "ghp_project")
	cfg := &Config{
		Owner: "nightgauge",
		GitHubAuth: &GitHubAuthConfig{
			Token: "env:TEST_PROJECT_TOKEN",
			Tokens: map[string]string{
				"nightgauge": "env:TEST_ORG_TOKEN",
			},
		},
	}
	tok, err := cfg.ResolveToken("nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_project" {
		t.Errorf("expected project-level token, got %q", tok)
	}
}

func TestResolveToken_OrgMapping(t *testing.T) {
	t.Setenv("TEST_ORG_TOKEN_2663", "ghp_org")
	cfg := &Config{
		Owner: "nightgauge",
		GitHubAuth: &GitHubAuthConfig{
			Tokens: map[string]string{
				"nightgauge": "env:TEST_ORG_TOKEN_2663",
			},
		},
	}
	tok, err := cfg.ResolveToken("nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_org" {
		t.Errorf("expected org token, got %q", tok)
	}
}

func TestResolveToken_OrgMapping_UnknownOwner(t *testing.T) {
	cfg := &Config{
		Owner: "nightgauge",
		GitHubAuth: &GitHubAuthConfig{
			Tokens: map[string]string{
				"nightgauge": "env:SOME_TOKEN",
			},
		},
	}
	tok, err := cfg.ResolveToken("OtherOrg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "" {
		t.Errorf("expected empty token for unknown owner, got %q", tok)
	}
}

func TestResolveToken_NoAuth(t *testing.T) {
	cfg := &Config{Owner: "nightgauge"}
	tok, err := cfg.ResolveToken("nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "" {
		t.Errorf("expected empty token when GitHubAuth is nil, got %q", tok)
	}
}

func TestResolveToken_EnvRefMissing_ReturnsError(t *testing.T) {
	t.Setenv("TEST_MISSING_2663", "")
	cfg := &Config{
		Owner: "nightgauge",
		GitHubAuth: &GitHubAuthConfig{
			Token: "env:TEST_MISSING_2663",
		},
	}
	_, err := cfg.ResolveToken("nightgauge")
	if err == nil {
		t.Error("expected error for missing env var in token ref, got nil")
	}
}

// TestLoadYAML_GitHubAuthToken verifies that the per-project token fixture
// is parsed into GitHubAuth.Token.
func TestLoadYAML_GitHubAuthToken(t *testing.T) {
	path := fixtureYAML(t, "nightgauge-github-auth-token.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML github-auth-token: %v", err)
	}
	if cfg.GitHubAuth == nil {
		t.Fatal("GitHubAuth is nil")
	}
	if cfg.GitHubAuth.Token != "env:GITHUB_TOKEN_NIGHTGAUGE" {
		t.Errorf("Token = %q, want %q", cfg.GitHubAuth.Token, "env:GITHUB_TOKEN_NIGHTGAUGE")
	}
}

// TestLoadYAML_GitHubAuthTokens verifies that the per-org tokens fixture
// is parsed into GitHubAuth.Tokens.
func TestLoadYAML_GitHubAuthTokens(t *testing.T) {
	path := fixtureYAML(t, "nightgauge-github-auth-tokens.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML github-auth-tokens: %v", err)
	}
	if cfg.GitHubAuth == nil {
		t.Fatal("GitHubAuth is nil")
	}
	wantAcme := "env:GITHUB_TOKEN_NIGHTGAUGE"
	wantAcmesvc := "env:GITHUB_TOKEN_ACME"
	if cfg.GitHubAuth.Tokens["nightgauge"] != wantAcme {
		t.Errorf("Tokens[nightgauge] = %q, want %q", cfg.GitHubAuth.Tokens["nightgauge"], wantAcme)
	}
	if cfg.GitHubAuth.Tokens["Acme-Community"] != wantAcmesvc {
		t.Errorf("Tokens[Acme-Community] = %q, want %q", cfg.GitHubAuth.Tokens["Acme-Community"], wantAcmesvc)
	}
}

// ── SuppressGHWarning tests (#2671) ───────────────────────────────────────────

// TestSuppressGHWarning_NilReceiver verifies that a nil *Config returns false.
func TestSuppressGHWarning_NilReceiver(t *testing.T) {
	var c *Config
	if c.SuppressGHWarning() {
		t.Error("nil Config.SuppressGHWarning() should return false")
	}
}

// TestSuppressGHWarning_NilGitHubAuth verifies that missing GitHubAuth returns false.
func TestSuppressGHWarning_NilGitHubAuth(t *testing.T) {
	c := &Config{}
	if c.SuppressGHWarning() {
		t.Error("Config with nil GitHubAuth.SuppressGHWarning() should return false")
	}
}

// TestSuppressGHWarning_False verifies the default (false) value.
func TestSuppressGHWarning_False(t *testing.T) {
	c := &Config{GitHubAuth: &GitHubAuthConfig{SuppressGHWarning: false}}
	if c.SuppressGHWarning() {
		t.Error("SuppressGHWarning should return false when field is false")
	}
}

// TestSuppressGHWarning_True verifies the field is respected when set to true.
func TestSuppressGHWarning_True(t *testing.T) {
	c := &Config{GitHubAuth: &GitHubAuthConfig{SuppressGHWarning: true}}
	if !c.SuppressGHWarning() {
		t.Error("SuppressGHWarning should return true when field is true")
	}
}

// TestLoadYAML_SuppressGHWarning verifies YAML round-trip for suppress_gh_warning.
func TestLoadYAML_SuppressGHWarning(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := "project:\n  owner: TestOrg\n  number: 1\ngithub_auth:\n  suppress_gh_warning: true\n"
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg, err := LoadYAML(p)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if cfg.GitHubAuth == nil {
		t.Fatal("GitHubAuth is nil")
	}
	if !cfg.GitHubAuth.SuppressGHWarning {
		t.Error("GitHubAuth.SuppressGHWarning should be true after YAML parse")
	}
	if !cfg.SuppressGHWarning() {
		t.Error("Config.SuppressGHWarning() should return true")
	}
}

// ── Unified concurrency model tests (#3781) ──────────────────────────────────

func TestResolveConcurrency(t *testing.T) {
	t.Run("defaults when concurrency block absent", func(t *testing.T) {
		rc := ResolveConcurrency(&Config{})
		if rc.WorkspaceMax != DefaultWorkspaceMax {
			t.Errorf("WorkspaceMax = %d, want %d", rc.WorkspaceMax, DefaultWorkspaceMax)
		}
		if rc.PerRepoMax != DefaultPerRepoMax {
			t.Errorf("PerRepoMax = %d, want %d", rc.PerRepoMax, DefaultPerRepoMax)
		}
	})

	t.Run("nil config defaults", func(t *testing.T) {
		rc := ResolveConcurrency(nil)
		if rc.WorkspaceMax != DefaultWorkspaceMax || rc.PerRepoMax != DefaultPerRepoMax {
			t.Errorf("nil -> %+v, want defaults", rc)
		}
	})

	t.Run("explicit values win", func(t *testing.T) {
		rc := ResolveConcurrency(&Config{Concurrency: &ConcurrencyConfig{WorkspaceMax: 5, PerRepoMax: 2}})
		if rc.WorkspaceMax != 5 || rc.PerRepoMax != 2 {
			t.Errorf("got %+v, want workspace=5 per_repo=2", rc)
		}
	})

	t.Run("CapForRepo override by short name else per_repo_max", func(t *testing.T) {
		rc := ResolveConcurrency(&Config{Concurrency: &ConcurrencyConfig{
			PerRepoMax:          1,
			RepositoryOverrides: map[string]int{"flutter": 3},
		}})
		if got := rc.CapForRepo("nightgauge/flutter"); got != 3 {
			t.Errorf("CapForRepo(owner/flutter) = %d, want 3 (short-name override)", got)
		}
		if got := rc.CapForRepo("flutter"); got != 3 {
			t.Errorf("CapForRepo(flutter) = %d, want 3", got)
		}
		if got := rc.CapForRepo("other"); got != 1 {
			t.Errorf("CapForRepo(other) = %d, want 1 (per_repo_max default)", got)
		}
	})

	t.Run("ResolvedMaxConcurrent delegates to WorkspaceMax", func(t *testing.T) {
		if got := ResolvedMaxConcurrent(&Config{Concurrency: &ConcurrencyConfig{WorkspaceMax: 7}}); got != 7 {
			t.Errorf("ResolvedMaxConcurrent = %d, want 7", got)
		}
	})
}

// ── Phase 3: Forge config schema tests (#3351) ────────────────────────────────

// TestLoadYAML_V1Default verifies that a v1 config (no schema_version) is
// loaded and automatically migrated in-memory: forges.github is inserted and
// SchemaVersion is set to "2".
func TestLoadYAML_V1Default(t *testing.T) {
	path := fixtureYAML(t, "v1-default.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML v1-default: %v", err)
	}
	if cfg.SchemaVersion != "2" {
		t.Errorf("SchemaVersion = %q, want %q (migration should set it)", cfg.SchemaVersion, "2")
	}
	if cfg.Forges == nil {
		t.Fatal("Forges should not be nil after v1→v2 migration")
	}
	gh, ok := cfg.Forges["github"]
	if !ok {
		t.Fatal("migration should insert forges[\"github\"]")
	}
	if gh.Kind != "github" {
		t.Errorf("migrated forges.github.Kind = %q, want %q", gh.Kind, "github")
	}
	if gh.BaseURL != "https://github.com" {
		t.Errorf("migrated forges.github.BaseURL = %q, want %q", gh.BaseURL, "https://github.com")
	}
}

// TestLoadYAML_V2Migrated verifies that a v2 config is parsed without
// re-migration: the existing forges.github block is preserved, SchemaVersion stays "2".
func TestLoadYAML_V2Migrated(t *testing.T) {
	path := fixtureYAML(t, "v2-migrated.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML v2-migrated: %v", err)
	}
	if cfg.SchemaVersion != "2" {
		t.Errorf("SchemaVersion = %q, want %q", cfg.SchemaVersion, "2")
	}
	if cfg.Forges == nil || cfg.Forges["github"] == nil {
		t.Fatal("forges.github should be present in v2-migrated.yaml")
	}
	if cfg.Forges["github"].BaseURL != "https://github.com" {
		t.Errorf("forges.github.BaseURL = %q, want https://github.com", cfg.Forges["github"].BaseURL)
	}
}

// TestLoadYAML_V2SingleGitLab verifies that a single GitLab forge entry is
// parsed correctly, including all v2 fields.
func TestLoadYAML_V2SingleGitLab(t *testing.T) {
	path := fixtureYAML(t, "v2-single-gitlab.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML v2-single-gitlab: %v", err)
	}
	entry, ok := cfg.Forges["corp-gitlab"]
	if !ok {
		t.Fatal("forges[\"corp-gitlab\"] should be present")
	}
	if entry.Kind != "gitlab" {
		t.Errorf("Kind = %q, want gitlab", entry.Kind)
	}
	if entry.BaseURL != "https://gitlab.example.com" {
		t.Errorf("BaseURL = %q, want https://gitlab.example.com", entry.BaseURL)
	}
	if entry.GraphQLURL != "https://gitlab.example.com/api/graphql" {
		t.Errorf("GraphQLURL = %q, want https://gitlab.example.com/api/graphql", entry.GraphQLURL)
	}
	if entry.AuthMethod != "token" {
		t.Errorf("AuthMethod = %q, want token", entry.AuthMethod)
	}
	if entry.TokenEnv != "GITLAB_TOKEN" {
		t.Errorf("TokenEnv = %q, want GITLAB_TOKEN", entry.TokenEnv)
	}
}

// TestLoadYAML_V2Mixed verifies that a config with both GitHub and GitLab
// forges is parsed, and per-repo forge references are loaded correctly.
func TestLoadYAML_V2Mixed(t *testing.T) {
	path := fixtureYAML(t, "v2-mixed.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML v2-mixed: %v", err)
	}
	if _, ok := cfg.Forges["github"]; !ok {
		t.Error("forges[\"github\"] should be present")
	}
	if _, ok := cfg.Forges["corp-gitlab"]; !ok {
		t.Error("forges[\"corp-gitlab\"] should be present")
	}
	if cfg.Autonomous == nil {
		t.Fatal("Autonomous should not be nil")
	}
	repoConfig, ok := cfg.Autonomous.Repositories["corp-service"]
	if !ok {
		t.Fatal("autonomous.repositories[\"corp-service\"] should be present")
	}
	if repoConfig.Forge != "corp-gitlab" {
		t.Errorf("corp-service Forge = %q, want corp-gitlab", repoConfig.Forge)
	}
}

// TestLoadYAML_V2FullPopulated verifies all fields in a fully-populated v2 config.
func TestLoadYAML_V2FullPopulated(t *testing.T) {
	path := fixtureYAML(t, "v2-full-populated.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML v2-full-populated: %v", err)
	}
	gl, ok := cfg.Forges["corp-gitlab"]
	if !ok {
		t.Fatal("forges[\"corp-gitlab\"] should be present")
	}
	if gl.CABundle != "certs/corp-ca.pem" {
		t.Errorf("CABundle = %q, want certs/corp-ca.pem", gl.CABundle)
	}
	if gl.DefaultProjectID != 42 {
		t.Errorf("DefaultProjectID = %d, want 42", gl.DefaultProjectID)
	}
	if gl.Proxy != "http://proxy.corp.example.com:3128" {
		t.Errorf("Proxy = %q, want http://proxy.corp.example.com:3128", gl.Proxy)
	}
	if gl.GraphQLURL != "https://gitlab.corp.example.com/api/graphql" {
		t.Errorf("GraphQLURL = %q, want https://gitlab.corp.example.com/api/graphql", gl.GraphQLURL)
	}
	if !gl.InsecureSkipTLS {
		t.Error("InsecureSkipTLS = false, want true")
	}
}

// TestForgeConfigEntry_InsecureSkipTLS_Parsed verifies insecure_skip_tls round-trips
// through YAML parsing correctly.
func TestForgeConfigEntry_InsecureSkipTLS_Parsed(t *testing.T) {
	path := fixtureYAML(t, "v2-full-populated.yaml")
	cfg, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	gl, ok := cfg.Forges["corp-gitlab"]
	if !ok {
		t.Fatal("forges[\"corp-gitlab\"] not found")
	}
	if !gl.InsecureSkipTLS {
		t.Error("InsecureSkipTLS = false, want true from fixture")
	}
}

// TestValidateForgeConfig_InsecureSkipTLS_Warns verifies that ValidateForgeConfig
// emits a warning to stderr (not an error) when InsecureSkipTLS=true.
func TestValidateForgeConfig_InsecureSkipTLS_Warns(t *testing.T) {
	forges := map[string]*ForgeConfigEntry{
		"mygl": {
			Kind:            "gitlab",
			BaseURL:         "https://gitlab.example.com",
			InsecureSkipTLS: true,
			AuthMethod:      "token",
			TokenEnv:        "SOME_TOKEN",
		},
	}
	// ValidateForgeConfig should succeed (warning, not error).
	if err := ValidateForgeConfig(forges, nil); err != nil {
		t.Errorf("ValidateForgeConfig InsecureSkipTLS: expected no error, got %v", err)
	}
}

// TestValidateForgeConfig_InvalidKind verifies that an unknown forge kind
// returns a validation error.
func TestValidateForgeConfig_InvalidKind(t *testing.T) {
	forges := map[string]*ForgeConfigEntry{
		"myforge": {Kind: "bitbucket", BaseURL: "https://bitbucket.org"},
	}
	err := ValidateForgeConfig(forges, nil)
	if err == nil {
		t.Fatal("expected validation error for unknown forge kind, got nil")
	}
	if !strings.Contains(err.Error(), "bitbucket") {
		t.Errorf("error should mention the invalid kind, got: %v", err)
	}
}

// TestValidateForgeConfig_DanglingRef verifies that a repo referencing a
// non-existent forge key returns a validation error.
func TestValidateForgeConfig_DanglingRef(t *testing.T) {
	forges := map[string]*ForgeConfigEntry{
		"github": {Kind: "github", BaseURL: "https://github.com"},
	}
	repos := map[string]*RepositoryConfig{
		"some-repo": {Forge: "nonexistent-forge"},
	}
	err := ValidateForgeConfig(forges, repos)
	if err == nil {
		t.Fatal("expected validation error for dangling forge reference, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent-forge") {
		t.Errorf("error should mention the missing forge key, got: %v", err)
	}
}

// TestValidateForgeConfig_MissingBaseURL verifies that a gitlab forge without
// base_url returns a validation error.
func TestValidateForgeConfig_MissingBaseURL(t *testing.T) {
	forges := map[string]*ForgeConfigEntry{
		"my-gitlab": {Kind: "gitlab", AuthMethod: "token", TokenEnv: "GITLAB_TOKEN"},
	}
	err := ValidateForgeConfig(forges, nil)
	if err == nil {
		t.Fatal("expected validation error for missing base_url on gitlab forge, got nil")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error should mention base_url, got: %v", err)
	}
}

// TestValidateForgeConfig_ValidEntries verifies that a fully-valid forge config
// (github + gitlab with all required fields) passes validation.
func TestValidateForgeConfig_ValidEntries(t *testing.T) {
	forges := map[string]*ForgeConfigEntry{
		"github": {Kind: "github", BaseURL: "https://github.com"},
		"corp-gitlab": {
			Kind:       "gitlab",
			BaseURL:    "https://gitlab.example.com",
			AuthMethod: "token",
			TokenEnv:   "GITLAB_TOKEN",
		},
	}
	repos := map[string]*RepositoryConfig{
		"corp-service": {Forge: "corp-gitlab"},
	}
	err := ValidateForgeConfig(forges, repos)
	if err != nil {
		t.Errorf("expected no validation errors, got: %v", err)
	}
}

// TestValidateForgeConfig_TokenAuthMissingEnv verifies that auth_method=token
// without a token_env returns a validation error.
func TestValidateForgeConfig_TokenAuthMissingEnv(t *testing.T) {
	forges := map[string]*ForgeConfigEntry{
		"gh": {Kind: "github", BaseURL: "https://github.com", AuthMethod: "token"},
	}
	err := ValidateForgeConfig(forges, nil)
	if err == nil {
		t.Fatal("expected validation error for auth_method=token without token_env, got nil")
	}
	if !strings.Contains(err.Error(), "token_env") {
		t.Errorf("error should mention token_env, got: %v", err)
	}
}

// TestMigrateV1ToV2_Idempotent verifies that calling migrateV1ToV2 twice
// produces the same result and doesn't duplicate forges.
func TestMigrateV1ToV2_Idempotent(t *testing.T) {
	cfg := &Config{Owner: "nightgauge"}

	migrateV1ToV2(cfg)
	if cfg.SchemaVersion != "2" {
		t.Errorf("after first migration, SchemaVersion = %q, want 2", cfg.SchemaVersion)
	}
	forgesAfterFirst := len(cfg.Forges)

	migrateV1ToV2(cfg)
	if len(cfg.Forges) != forgesAfterFirst {
		t.Errorf("second migration changed forge count from %d to %d; must be idempotent", forgesAfterFirst, len(cfg.Forges))
	}
}

// ── #4068: per-owner github_user resolution ──────────────────────────────────

// TestResolveGitHubUserForOwner_CrossOrg verifies the resolver maps the PASSED
// owner (not just the workspace root owner) through github_auth.users, so a
// cross-org target resolves the right identity (Acme-Community →
// acmebot) even when the workspace owner differs.
func TestResolveGitHubUserForOwner_CrossOrg(t *testing.T) {
	cfg := &Config{
		Owner: "nightgauge",
		GitHubAuth: &GitHubAuthConfig{
			Users: map[string]string{
				"nightgauge":     "octocat",
				"Acme-Community": "acmebot",
			},
		},
	}
	// Cross-org owner resolves the org-mapped identity.
	if got := cfg.ResolveGitHubUserForOwner("Acme-Community"); got != "acmebot" {
		t.Errorf("ResolveGitHubUserForOwner(Acme-Community) = %q, want acmebot", got)
	}
	// The workspace owner resolves its own mapping.
	if got := cfg.ResolveGitHubUserForOwner("nightgauge"); got != "octocat" {
		t.Errorf("ResolveGitHubUserForOwner(nightgauge) = %q, want octocat", got)
	}
	// ResolveGitHubUser() still resolves against c.Owner (the zero-arg convenience).
	if got := cfg.ResolveGitHubUser(); got != "octocat" {
		t.Errorf("ResolveGitHubUser() = %q, want octocat (c.Owner=nightgauge)", got)
	}
	// An owner with no mapping resolves to empty (single-identity / skip).
	if got := cfg.ResolveGitHubUserForOwner("UnknownOrg"); got != "" {
		t.Errorf("ResolveGitHubUserForOwner(UnknownOrg) = %q, want \"\"", got)
	}
}

// TestResolveGitHubUserForOwner_ExplicitWins verifies an explicit per-repo
// github_user takes priority over the github_auth.users map for ANY owner.
func TestResolveGitHubUserForOwner_ExplicitWins(t *testing.T) {
	cfg := &Config{
		Owner:      "Acme-Community",
		GitHubUser: "acmebot",
		GitHubAuth: &GitHubAuthConfig{
			Users: map[string]string{"Acme-Community": "should-not-win"},
		},
	}
	if got := cfg.ResolveGitHubUserForOwner("Acme-Community"); got != "acmebot" {
		t.Errorf("explicit github_user should win: got %q, want acmebot", got)
	}
	if got := cfg.ResolveGitHubUserForOwner("AnyOtherOrg"); got != "acmebot" {
		t.Errorf("explicit github_user is owner-independent: got %q, want acmebot", got)
	}
}

// TestResolveGitHubUserForOwner_NilSafe verifies a nil receiver resolves to "".
func TestResolveGitHubUserForOwner_NilSafe(t *testing.T) {
	var cfg *Config
	if got := cfg.ResolveGitHubUserForOwner("nightgauge"); got != "" {
		t.Errorf("nil config = %q, want \"\"", got)
	}
}
