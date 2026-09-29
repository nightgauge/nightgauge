package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
)

func writeWorktreeBaseFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadWorktreeBaseSetting_TierPrecedence(t *testing.T) {
	root := t.TempDir()
	machine := filepath.Join(t.TempDir(), "config.yaml")
	withMachineConfigPath(t, machine)

	if s, err := ReadWorktreeBaseSetting(root); err != nil || s.Value != "" {
		t.Fatalf("unset: (%+v, %v), want the zero setting", s, err)
	}

	writeWorktreeBaseFile(t, machine, "pipeline:\n  worktree_base: /m/wt\n")
	s, err := ReadWorktreeBaseSetting(root)
	if err != nil || s.Value != "/m/wt" || s.Source != machine || s.Line != 2 {
		t.Fatalf("machine tier: (%+v, %v)", s, err)
	}

	local := LocalConfigPath(root)
	writeWorktreeBaseFile(t, local, "# personal\npipeline:\n  max_concurrent: 2\n  worktree_base: /l/wt\n")
	s, err = ReadWorktreeBaseSetting(root)
	if err != nil || s.Value != "/l/wt" || s.Line != 4 || !strings.HasSuffix(s.Source, "config.local.yaml") {
		t.Fatalf("local tier must win over machine: (%+v, %v)", s, err)
	}

	// A null value is unset, not an empty path.
	writeWorktreeBaseFile(t, local, "pipeline:\n  worktree_base:\n")
	if s, err := ReadWorktreeBaseSetting(root); err != nil || s.Value != "/m/wt" {
		t.Fatalf("null local value must fall through to machine: (%+v, %v)", s, err)
	}
}

func TestReadWorktreeBaseSetting_TeamTierIsAnError(t *testing.T) {
	root := t.TempDir()
	withMachineConfigPath(t, filepath.Join(t.TempDir(), "config.yaml"))
	writeWorktreeBaseFile(t, ProjectConfigPath(root), "owner: acme\npipeline:\n  worktree_base: .worktrees\n")

	_, err := ReadWorktreeBaseSetting(root)
	if !errors.Is(err, layout.ErrWorktreeBase) {
		t.Fatalf("error = %v, want ErrWorktreeBase", err)
	}
	for _, want := range []string{"config.yaml:3", "committed team config", "Delete it", "config.local.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// withMachineConfigPath points the machine tier at path for one test.
func withMachineConfigPath(t *testing.T, path string) {
	t.Helper()
	prev := machineConfigPathFn
	machineConfigPathFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { machineConfigPathFn = prev })
}
