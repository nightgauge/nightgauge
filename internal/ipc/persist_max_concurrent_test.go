// Tests for persistMaxConcurrent's write target and pipeline-block targeting.
//
// Issue #3195 pinned that the value lands on `pipeline.max_concurrent` and not
// on `autonomous.max_concurrent`, whichever order they appear in.
//
// Issue #1516 pins the tier: moving the concurrency slider is a runtime
// preference, so it must never rewrite the committed `.nightgauge/config.yaml`.
// `pipeline.max_concurrent` is a machine-tier key (config.MachineTierKeys), so
// it lands in `~/.nightgauge/config.yaml`; the team file must come out
// byte-identical.
package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupConfig provisions a Server pointing at a fresh temp workspace whose
// .nightgauge/config.yaml has the supplied body, and points the machine tier
// at a temp directory so the developer's real ~/.nightgauge is never touched.
// Returns the server, the team-file path and the machine-file path.
func setupConfig(t *testing.T, body string) (*Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ".nightgauge", "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	machineDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	return &Server{workspaceRoot: dir}, path, filepath.Join(machineDir, "config.yaml")
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(b)
}

// The committed team file is the public-core safety default. A runtime toggle
// must leave it byte-identical — comments, blank lines and values (#1516).
func TestPersistMaxConcurrent_LeavesTeamConfigByteIdentical(t *testing.T) {
	body := `# Safe public-core dogfood defaults.
#
# Autonomous execution is disabled so cloning never authorizes work.
owner: nightgauge

pipeline:
  max_concurrent: 3
  worktree_base: .worktrees

autonomous:
  dry_run: true
`
	srv, teamPath, machinePath := setupConfig(t, body)

	if err := srv.persistMaxConcurrent(7); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	if got := readConfig(t, teamPath); got != body {
		t.Errorf("team config must be byte-identical after a runtime toggle.\nwant:\n%s\ngot:\n%s", body, got)
	}
	out := readConfig(t, machinePath)
	if !strings.Contains(out, "max_concurrent: 7") {
		t.Errorf("expected pipeline.max_concurrent: 7 in the machine tier, got:\n%s", out)
	}
}

// Regression (#3195): with autonomous.max_concurrent declared above
// pipeline.max_concurrent, the original line-splice implementation clobbered
// the autonomous value and left pipeline at its old value.
func TestPersistMaxConcurrent_UpdatesPipelineEvenWhenAutonomousAppearsFirst(t *testing.T) {
	srv, teamPath, machinePath := setupConfig(t, `autonomous:
  max_concurrent: 1
  scan_interval: 30s
pipeline:
  max_concurrent: 3
  worktree_base: .worktrees
`)

	if err := srv.persistMaxConcurrent(5); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, machinePath)
	if !strings.Contains(out, "pipeline:\n    max_concurrent: 5") &&
		!strings.Contains(out, "pipeline:\n  max_concurrent: 5") {
		t.Errorf("expected pipeline.max_concurrent: 5 in the machine tier:\n%s", out)
	}
	if strings.Contains(out, "autonomous:") {
		t.Errorf("only the addressed key may be written to the machine tier:\n%s", out)
	}
	// The team file keeps both blocks untouched.
	team := readConfig(t, teamPath)
	if !strings.Contains(team, "autonomous:\n  max_concurrent: 1") {
		t.Errorf("autonomous.max_concurrent must be preserved unchanged:\n%s", team)
	}
	if !strings.Contains(team, "pipeline:\n  max_concurrent: 3") {
		t.Errorf("team pipeline.max_concurrent must not be rewritten:\n%s", team)
	}
}

// Repeated writes update the same leaf rather than appending duplicates.
func TestPersistMaxConcurrent_UpdatesExistingMachineKey(t *testing.T) {
	srv, _, machinePath := setupConfig(t, "pipeline:\n  worktree_base: .worktrees\n")

	if err := srv.persistMaxConcurrent(2); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}
	if err := srv.persistMaxConcurrent(7); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, machinePath)
	if strings.Count(out, "max_concurrent:") != 1 {
		t.Errorf("expected exactly one max_concurrent key, got:\n%s", out)
	}
	if !strings.Contains(out, "max_concurrent: 7") {
		t.Errorf("expected max_concurrent: 7, got:\n%s", out)
	}
}

// The machine file need not exist yet — the writer creates it.
func TestPersistMaxConcurrent_CreatesMachineFileWhenAbsent(t *testing.T) {
	srv, _, machinePath := setupConfig(t, "project:\n  number: 1\n")

	if _, err := os.Stat(machinePath); !os.IsNotExist(err) {
		t.Fatalf("machine config should not exist yet")
	}
	if err := srv.persistMaxConcurrent(4); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, machinePath)
	if !strings.Contains(out, "max_concurrent: 4") {
		t.Errorf("expected max_concurrent: 4, got:\n%s", out)
	}
}

// Existing machine-tier keys and their comments survive the write.
func TestPersistMaxConcurrent_PreservesMachineFileComments(t *testing.T) {
	srv, _, machinePath := setupConfig(t, "project:\n  number: 1\n")

	seed := `# Personal Nightgauge settings.
github_user: someone

pipeline:
  # How many issues run at once.
  max_concurrent: 3
`
	if err := os.WriteFile(machinePath, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed machine config: %v", err)
	}

	if err := srv.persistMaxConcurrent(9); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, machinePath)
	for _, want := range []string{
		"# Personal Nightgauge settings.",
		"github_user: someone",
		"# How many issues run at once.",
		"max_concurrent: 9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}
