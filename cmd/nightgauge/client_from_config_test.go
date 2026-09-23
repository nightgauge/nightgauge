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

// TestRootCommandTakesNoTokenOnArgv: a credential on the command line is
// visible through `ps` (ADR-024 § 5, #2031), so the root command offers no
// --token flag; the token comes from the environment, config or gh.
func TestRootCommandTakesNoTokenOnArgv(t *testing.T) {
	root := rootCmd()
	if f := root.PersistentFlags().Lookup("token"); f != nil {
		t.Fatalf("root command still has a persistent --token flag (%q)", f.Usage)
	}
	if f := root.Flags().Lookup("token"); f != nil {
		t.Fatalf("root command still has a --token flag (%q)", f.Usage)
	}
}

// TestResolveReleaseTokenReadsTheEnvironmentOnly: GITHUB_TOKEN wins, GH_TOKEN
// is the fallback, and nothing else is consulted.
func TestResolveReleaseTokenReadsTheEnvironmentOnly(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	if got := resolveReleaseToken(); got != "" {
		t.Fatalf("no env token: got %q, want empty", got)
	}
	t.Setenv("GH_TOKEN", "gh-token")
	if got := resolveReleaseToken(); got != "gh-token" {
		t.Fatalf("GH_TOKEN only: got %q", got)
	}
	t.Setenv("GITHUB_TOKEN", "github-token")
	if got := resolveReleaseToken(); got != "github-token" {
		t.Fatalf("both set: got %q, want GITHUB_TOKEN's value", got)
	}
}
