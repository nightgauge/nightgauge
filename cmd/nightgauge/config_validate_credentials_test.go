package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigValidateRefusesPlaintextRepoToken: `config validate` holds a
// repository file to the loader's rule, so it never passes a file every other
// command refuses (#2023). The machine-tier file may hold a literal value.
func TestConfigValidateRefusesPlaintextRepoToken(t *testing.T) {
	machineHome := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineHome)
	body := "owner: acme\ngithub_auth:\n  token: literal-validate-token\n"

	repoFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(repoFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := configValidateCmd()
	cmd.SetArgs([]string{"--config", repoFile})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "github_auth.token") || strings.Contains(err.Error(), "literal-validate-token") {
		t.Fatalf("validate of a repository file = %v, want a redacted refusal naming the key", err)
	}

	machineFile := filepath.Join(machineHome, "config.yaml")
	if err := os.WriteFile(machineFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = configValidateCmd()
	cmd.SetArgs([]string{"--config", machineFile})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err != nil && strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("validate refused the machine-tier file: %v", err)
	}
}

// TestConfigValidateAcceptsMachineFileThroughSymlink: the machine-tier file is
// recognised after resolving symlinks, not by string comparison.
func TestConfigValidateAcceptsMachineFileThroughSymlink(t *testing.T) {
	machineHome := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineHome)
	machineFile := filepath.Join(machineHome, "config.yaml")
	if err := os.WriteFile(machineFile, []byte("owner: acme\ngithub_auth:\n  token: literal-validate-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.Symlink(machineFile, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	cmd := configValidateCmd()
	cmd.SetArgs([]string{"--config", link})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err != nil && strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("validate refused the machine-tier file reached through a symlink: %v", err)
	}
}
