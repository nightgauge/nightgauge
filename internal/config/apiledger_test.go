package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeWorkspaceConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return root
}

// An absent `github.api_ledger` must stay nil, not decode to a false Enabled.
// Every config in the workspace predates this key, and a plain bool here would
// read all of them as an explicit opt-out — switching the instrument off
// everywhere while appearing to add it.
func TestAPILedgerAbsentFromConfigLeavesDefault(t *testing.T) {
	root := writeWorkspaceConfig(t, "project:\n  owner: acme\n  number: 1\n  repo: widget\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHubAPILedger != nil {
		t.Errorf("GitHubAPILedger = %+v for a config that never mentions it, want nil", cfg.GitHubAPILedger)
	}
}

func TestAPILedgerParsedFromGitHubBlock(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want bool
	}{
		{"disabled", "github:\n  api_ledger:\n    enabled: false\n", false},
		{"enabled", "github:\n  api_ledger:\n    enabled: true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeWorkspaceConfig(t,
				"project:\n  owner: acme\n  number: 1\n  repo: widget\n"+tc.yaml)
			cfg, err := Load(root)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.GitHubAPILedger == nil || cfg.GitHubAPILedger.Enabled == nil {
				t.Fatalf("GitHubAPILedger = %+v, want the parsed setting", cfg.GitHubAPILedger)
			}
			if got := *cfg.GitHubAPILedger.Enabled; got != tc.want {
				t.Errorf("Enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// ApplyAPILedgerSetting must tolerate every "nothing was said" shape rather
// than dereferencing its way into a panic on a partially-populated config.
func TestApplyAPILedgerSettingIgnoresUnsetShapes(t *testing.T) {
	on := true
	for _, cfg := range []*Config{
		nil,
		{},
		{GitHubAPILedger: &APILedgerConfig{}},
		{GitHubAPILedger: &APILedgerConfig{Enabled: &on}},
	} {
		ApplyAPILedgerSetting(cfg) // must not panic
	}
}
