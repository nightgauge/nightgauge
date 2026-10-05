package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// machineYAML is a machine-tier file as an operator writes it: machine-owned
// keys and no owner, which is a project-tier key.
const machineYAML = "github_user: someone\nplatform:\n  enabled: false\n"

// tempHome points $HOME at a fresh directory, clears the overrides that would
// move the machine tier elsewhere, and writes machineYAML to
// ~/.nightgauge/config.yaml. It returns the home directory.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(machineYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	resetShadowWarnDedup()
	t.Cleanup(resetShadowWarnDedup)
	return home
}

// TestLoad_HomeIsNotAProject is #2423: run from $HOME, the loader read
// ~/.nightgauge/config.yaml as the project tier, warned that every
// machine-owned key shadowed itself, and failed on the missing owner.
func TestLoad_HomeIsNotAProject(t *testing.T) {
	home := tempHome(t)
	var cfg *Config
	var err error
	logged := captureLog(t, func() { cfg, err = Load(home) })
	if err != nil {
		t.Fatalf("Load($HOME): %v", err)
	}
	if want := DefaultConfig().Owner; cfg.Owner != want {
		t.Errorf("Owner = %q, want the no-project default %q", cfg.Owner, want)
	}
	if strings.Contains(logged, "project YAML") {
		t.Errorf("machine file reported as project YAML:\n%s", logged)
	}
	if !IsMachineConfigRoot(home) {
		t.Error("IsMachineConfigRoot($HOME) = false")
	}
	if _, err := readProjectConfigBytes(home); err != errConfigNotFound {
		t.Errorf("readProjectConfigBytes($HOME) err = %v, want errConfigNotFound", err)
	}
}

// TestLoad_ConfigHomeRootIsNotAProject covers a machine tier moved by
// NIGHTGAUGE_CONFIG_HOME: a root whose .nightgauge directory is that
// directory is not a project either.
func TestLoad_ConfigHomeRootIsNotAProject(t *testing.T) {
	tempHome(t)
	root := t.TempDir()
	machineDir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(machineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(machineYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	logged := captureLog(t, func() {
		if _, err := Load(root); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if strings.Contains(logged, "project YAML") {
		t.Errorf("machine file reported as project YAML:\n%s", logged)
	}
}

// TestLoad_ProjectUnderHomeStillMerges guards the other side: a checkout
// below $HOME keeps its project tier and the machine tier under it.
func TestLoad_ProjectUnderHomeStillMerges(t *testing.T) {
	home := tempHome(t)
	// Pin the machine tier to the file tempHome wrote: the platform default
	// is ~/.config/nightgauge on Linux, where it would be absent.
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", filepath.Join(home, ".nightgauge"))
	repo := filepath.Join(home, "src", "widget")
	if err := os.MkdirAll(filepath.Join(repo, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectConfigPath(repo), []byte("owner: acme\ndefault_repo: widget\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsMachineConfigRoot(repo) {
		t.Fatal("IsMachineConfigRoot(checkout) = true")
	}
	cfg, err := Load(repo)
	if err != nil {
		t.Fatalf("Load(checkout): %v", err)
	}
	if cfg.Owner != "acme" {
		t.Errorf("Owner = %q, want acme", cfg.Owner)
	}
	if cfg.GitHubUser != "someone" {
		t.Errorf("GitHubUser = %q, want the machine tier's someone", cfg.GitHubUser)
	}
}
