package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClientFromConfigFailsClosedOnRefusedConfig: a refused config (a
// plaintext token in a repository tier, #2023) fails the command instead of
// falling back to the machine's default gh account.
func TestClientFromConfigFailsClosedOnRefusedConfig(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "owner: acme\ngithub_auth:\n  token: literal-refused-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	client, err := clientFromConfig()
	if err == nil {
		t.Fatalf("clientFromConfig built a client (%v) from a refused config", client != nil)
	}
	if !strings.Contains(err.Error(), "github_auth.token") || strings.Contains(err.Error(), "literal-refused-token") {
		t.Errorf("error should name the key, not the value: %v", err)
	}
}
