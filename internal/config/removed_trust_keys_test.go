package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A config that still sets the removed key fails to load, naming the removal
// and where to read about it (#2130).
func TestLoad_RejectsRemovedTrustedAuthorAssociations(t *testing.T) {
	for _, value := range []string{`["OWNER", "MEMBER"]`, `[]`} {
		root := writeProjectConfig(t, "project:\n  owner: acme\n  number: 1\nautonomous:\n  trusted_author_associations: "+value+"\n")
		_, err := Load(root)
		if !errors.Is(err, ErrTrustedAuthorAssociationsRemoved) {
			t.Fatalf("value %s: Load error = %v, want the removal error", value, err)
		}
		for _, want := range []string{"was removed", "pipeline:approved", "trusted_bots", "docs/CONFIGURATION.md"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("removal error lacks %q: %v", want, err)
			}
		}
	}
}

func TestLoad_ReadsTrustedBots(t *testing.T) {
	root := writeProjectConfig(t, "project:\n  owner: acme\n  number: 1\nautonomous:\n  trusted_bots: [\"app/my-pipeline\"]\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autonomous == nil || len(cfg.Autonomous.TrustedBots) != 1 || cfg.Autonomous.TrustedBots[0] != "app/my-pipeline" {
		t.Fatalf("TrustedBots = %+v", cfg.Autonomous)
	}
}

// Label approval exists only where the pipeline acts as a GitHub App: an App
// with an id and an installation for the owner (#2130).
func TestAppOwners(t *testing.T) {
	var none *Config
	if got := none.AppOwners(); got != nil {
		t.Errorf("nil config: %v", got)
	}
	cfg := &Config{GitHubAuth: &GitHubAuthConfig{App: &GitHubAppConfig{
		Installations: map[string]int64{"acme": 1}}}}
	if got := cfg.AppOwners(); len(got) != 0 {
		t.Errorf("an App with no id: %v, want none", got)
	}
	cfg.GitHubAuth.App.ID = "123"
	cfg.GitHubAuth.App.Installations = map[string]int64{"acme": 1, "zeta": 2, "broken": 0}
	if got := cfg.AppOwners(); len(got) != 2 || got[0] != "acme" || got[1] != "zeta" {
		t.Errorf("AppOwners = %v, want [acme zeta]", got)
	}
}
