package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

func execConfigInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"config", "init"}, args...))
	err := cmd.Execute()
	return stdout.String(), err
}

func repoWithOrigin(t *testing.T, url string) string {
	t.Helper()
	root := gittest.InitRepo(t, t.TempDir(), "-q")
	if url != "" {
		out, err := gittest.Command(root, "remote", "add", "origin", url).CombinedOutput()
		if err != nil {
			t.Fatalf("git remote add: %v\n%s", err, out)
		}
	}
	t.Chdir(root)
	return root
}

// #2760: plain init writes `number: <PROJECT_NUMBER>`, which the loader refuses.
// Onboarding depends on --detect output loading through the real loader.
func TestConfigInitDetect_OutputLoads(t *testing.T) {
	root := repoWithOrigin(t, "git@github.com:acme-co/widget.git")

	if _, err := execConfigInit(t, "--detect", "--json"); err != nil {
		t.Fatalf("config init --detect: %v", err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	if cfg.Owner != "acme-co" {
		t.Fatalf("owner = %q, want acme-co", cfg.Owner)
	}
	if cfg.ProjectNumber != 0 {
		t.Fatalf("project number = %d, want 0", cfg.ProjectNumber)
	}
	body, err := os.ReadFile(filepath.Join(root, ".nightgauge", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "<") && strings.Contains(text, "_ID>") {
		t.Fatalf("placeholder token left in config:\n%s", text)
	}
	if !strings.Contains(text, "repo: widget\n") {
		t.Fatalf("repo not derived from the remote:\n%s", text)
	}
}

// A machine-wide owner is back-filled into --owner before RunE. It must neither
// trip the --owner/--detect refusal nor replace the remote's owner.
func TestConfigInitDetect_IgnoresConfiguredOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".nightgauge", "config.yaml")
	if err := os.WriteFile(global, []byte("owner: someone-else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := repoWithOrigin(t, "https://github.com/acme-co/widget.git")

	if _, err := execConfigInit(t, "--detect"); err != nil {
		t.Fatalf("config init --detect: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".nightgauge", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "owner: acme-co\n") {
		t.Fatalf("owner not taken from the remote:\n%s", body)
	}
}

func TestConfigInitDetect_RefusesConflictingFlags(t *testing.T) {
	repoWithOrigin(t, "https://github.com/acme-co/widget")
	for name, args := range map[string][]string{
		"owner":   {"--detect", "--owner", "x"},
		"repo":    {"--detect", "--repo", "x"},
		"project": {"--detect", "--project", "3"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := execConfigInit(t, args...); err == nil {
				t.Fatal("want an error")
			}
			if _, err := os.Stat(".nightgauge/config.yaml"); !os.IsNotExist(err) {
				t.Fatalf("config written despite refusal: %v", err)
			}
		})
	}
}

func TestCheckInitIdentityFlags_RequiresOwnerOrDetect(t *testing.T) {
	if err := checkInitIdentityFlags("", "", 0, false); err == nil {
		t.Fatal("want an error with neither --owner nor --detect")
	}
	if err := checkInitIdentityFlags("acme", "", 0, false); err != nil {
		t.Fatalf("--owner alone: %v", err)
	}
	if err := checkInitIdentityFlags("", "", 0, true); err != nil {
		t.Fatalf("--detect alone: %v", err)
	}
}

func TestConfigInitDetect_UnusableRemoteWritesNothing(t *testing.T) {
	for name, url := range map[string]string{
		"no remote":      "",
		"not GitHub":     "https://gitlab.com/acme/widget.git",
		"yaml in a name": "https://github.com/acme/widget: true",
		"nested path":    "https://github.com/acme/widget/extra",
	} {
		t.Run(name, func(t *testing.T) {
			repoWithOrigin(t, url)
			_, err := execConfigInit(t, "--detect")
			if err == nil || !strings.Contains(err.Error(), "--detect needs") {
				t.Fatalf("err = %v, want one naming what --detect needs", err)
			}
			if _, statErr := os.Stat(".nightgauge/config.yaml"); !os.IsNotExist(statErr) {
				t.Fatalf("config written: %v", statErr)
			}
		})
	}
}
