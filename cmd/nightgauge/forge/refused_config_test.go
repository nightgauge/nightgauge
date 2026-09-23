package forgecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// refusedConfigRepo writes a project config the loader refuses (a literal
// GitHub token in a repository tier, #2023) and chdirs into it.
func refusedConfigRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "bogus-ambient-token")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "owner: acme\ngithub_auth:\n  token: literal-refused-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

// TestForgeIssueFailsClosedOnRefusedConfig: a forge command that builds the
// router refuses to run as another identity when the config was refused.
func TestForgeIssueFailsClosedOnRefusedConfig(t *testing.T) {
	refusedConfigRepo(t)
	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"issue", "view", "1", "--repo", "acme/widgets", "--json"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("forge issue view ran on a refused config")
	}
	if !strings.Contains(err.Error(), "github_auth.token") || strings.Contains(err.Error(), "literal-refused-token") {
		t.Errorf("error should name the key and not the value: %v", err)
	}
}

// TestForgeAuthRefreshRunsOnRefusedConfig: the migration command the refusal
// names still runs on that config, and afterwards the config loads.
func TestForgeAuthRefreshRunsOnRefusedConfig(t *testing.T) {
	dir := refusedConfigRepo(t)
	origRead, origStore := readGHToken, storeTokenInKeyring
	readGHToken = func() (string, error) { return "gh-keyring-token-value", nil }
	storeTokenInKeyring = func(string) error { return nil }
	t.Cleanup(func() { readGHToken, storeTokenInKeyring = origRead, origStore })

	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "refresh", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("forge auth refresh on a refused config: %v", err)
	}
	if _, err := config.Load(dir); err != nil {
		t.Fatalf("config still refused after refresh: %v", err)
	}
}
