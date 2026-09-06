package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// teamConfigBody mirrors the shape of the repository's committed
// .nightgauge/config.yaml: a header comment block, blank lines between
// sections, and the autonomous safety default.
const teamConfigBody = `# Safe public-core dogfood defaults.
#
# Autonomous execution and issue discovery are disabled by default so cloning
# the repository never authorizes work.
owner: nightgauge
repo: nightgauge

pipeline:
  budget_preset: conservative
  worktree_base: .worktrees

autonomous:
  dry_run: true
  scan_interval: 30s
`

func newWorkspace(t *testing.T) (workspaceRoot, teamPath, localPath, machinePath string) {
	t.Helper()
	workspaceRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	teamPath = ProjectConfigPath(workspaceRoot)
	if err := os.WriteFile(teamPath, []byte(teamConfigBody), 0o644); err != nil {
		t.Fatalf("write team config: %v", err)
	}
	localPath = LocalConfigPath(workspaceRoot)

	machineDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	machinePath = filepath.Join(machineDir, "config.yaml")
	return
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// #1516: a runtime toggle must leave the committed team file byte-identical.
func TestWriteRuntimeValue_LeavesTeamConfigByteIdentical(t *testing.T) {
	root, teamPath, localPath, _ := newWorkspace(t)

	path, err := WriteRuntimeValue(root, "autonomous.dry_run", false)
	if err != nil {
		t.Fatalf("WriteRuntimeValue: %v", err)
	}

	if got := mustRead(t, teamPath); got != teamConfigBody {
		t.Errorf("team config must be byte-identical.\nwant:\n%s\ngot:\n%s", teamConfigBody, got)
	}
	if path != localPath {
		t.Errorf("expected the write to target %s, got %s", localPath, path)
	}
}

// The value lands in the local tier, and only that key does.
func TestWriteRuntimeValue_LandsInLocalTier(t *testing.T) {
	root, _, localPath, _ := newWorkspace(t)

	if _, err := WriteRuntimeValue(root, "autonomous.dry_run", false); err != nil {
		t.Fatalf("WriteRuntimeValue: %v", err)
	}

	out := mustRead(t, localPath)
	if !strings.Contains(out, "dry_run: false") {
		t.Errorf("expected dry_run: false in the local tier, got:\n%s", out)
	}
	if strings.Contains(out, "scan_interval") || strings.Contains(out, "owner:") {
		t.Errorf("only the addressed key may be written to the local tier, got:\n%s", out)
	}
}

// A machine-tier key routes to ~/.nightgauge/config.yaml, not to the workspace.
func TestWriteRuntimeValue_MachineTierKeyLandsInMachineFile(t *testing.T) {
	root, teamPath, localPath, machinePath := newWorkspace(t)

	path, err := WriteRuntimeValue(root, "github_user", "someone")
	if err != nil {
		t.Fatalf("WriteRuntimeValue: %v", err)
	}
	if path != machinePath {
		t.Errorf("expected machine tier %s, got %s", machinePath, path)
	}
	if !strings.Contains(mustRead(t, machinePath), "github_user: someone") {
		t.Errorf("expected github_user in the machine file:\n%s", mustRead(t, machinePath))
	}
	if got := mustRead(t, teamPath); got != teamConfigBody {
		t.Errorf("team config must be untouched by a machine-tier write:\n%s", got)
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Errorf("machine-tier write must not create the local tier")
	}
}

// A key nested under a machine-tier key routes to the machine file too.
func TestIsMachineTierKey_MatchesNestedPaths(t *testing.T) {
	cases := map[string]bool{
		"github_auth":               true,
		"github_auth.token":         true,
		"platform.license_key":      true,
		"ui.core.default_model":     true,
		"autonomous.dry_run":        false,
		"autonomous.max_concurrent": false,
		"pipeline.worktree_base":    false,
	}
	for path, want := range cases {
		if got := IsMachineTierKey(path); got != want {
			t.Errorf("IsMachineTierKey(%q) = %v, want %v", path, got, want)
		}
	}
}

// The merged view must read the value the runtime write persisted, so the
// toggle actually takes effect even though the team file still says `true`.
func TestWriteRuntimeValue_MergedViewReadsTheNewValue(t *testing.T) {
	root, _, _, _ := newWorkspace(t)

	merged, err := LoadMerged(root)
	if err != nil {
		t.Fatalf("LoadMerged before: %v", err)
	}
	if merged.Autonomous == nil || merged.Autonomous.DryRun == nil || !*merged.Autonomous.DryRun {
		t.Fatalf("expected the team default dry_run: true before the write")
	}

	if _, err := WriteRuntimeValue(root, "autonomous.dry_run", false); err != nil {
		t.Fatalf("WriteRuntimeValue: %v", err)
	}

	merged, err = LoadMerged(root)
	if err != nil {
		t.Fatalf("LoadMerged after: %v", err)
	}
	if merged.Autonomous == nil || merged.Autonomous.DryRun == nil || *merged.Autonomous.DryRun {
		t.Errorf("merged view must read dry_run: false from the local tier")
	}
}

// An existing local tier keeps its own comments and untouched keys.
func TestWriteRuntimeValue_PreservesLocalTierComments(t *testing.T) {
	root, _, localPath, _ := newWorkspace(t)

	seed := `# Local dogfood board.
project:
  number: 3

# Autonomous dispatch is enabled for this checkout only.
autonomous:
  dry_run: false
`
	if err := os.WriteFile(localPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed local config: %v", err)
	}

	if _, err := WriteRuntimeValue(root, "autonomous.scan_interval", "45s"); err != nil {
		t.Fatalf("WriteRuntimeValue: %v", err)
	}

	out := mustRead(t, localPath)
	for _, want := range []string{
		"# Local dogfood board.",
		"number: 3",
		"# Autonomous dispatch is enabled for this checkout only.",
		"dry_run: false",
		"scan_interval: 45s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// Passing nil deletes the key rather than writing a null.
func TestWriteRuntimeValue_NilDeletesKey(t *testing.T) {
	root, _, localPath, _ := newWorkspace(t)

	if _, err := WriteRuntimeValue(root, "autonomous.dry_run", false); err != nil {
		t.Fatalf("WriteRuntimeValue: %v", err)
	}
	if _, err := WriteRuntimeValue(root, "autonomous.dry_run", nil); err != nil {
		t.Fatalf("WriteRuntimeValue delete: %v", err)
	}

	out := mustRead(t, localPath)
	if strings.Contains(out, "dry_run") {
		t.Errorf("expected dry_run to be removed, got:\n%s", out)
	}
}

// RuntimeWriteTarget never returns the committed team file.
func TestRuntimeWriteTarget_NeverTargetsTheTeamFile(t *testing.T) {
	root, teamPath, _, _ := newWorkspace(t)

	for _, key := range []string{
		"autonomous.dry_run",
		"pipeline.worktree_base",
		"github_auth.token",
		"platform.license_key",
		"owner",
	} {
		path, err := RuntimeWriteTarget(root, key)
		if err != nil {
			t.Fatalf("RuntimeWriteTarget(%q): %v", key, err)
		}
		if path == teamPath {
			t.Errorf("RuntimeWriteTarget(%q) must not target the committed team file", key)
		}
	}
}
