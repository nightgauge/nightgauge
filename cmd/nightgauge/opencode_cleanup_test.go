package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// TestOpenCodeConfigVerbPrintsItsRunID: run_id names the root run_dir is,
// whether --run-id named it or the verb minted it, so an SDK caller can
// delete it (#1648).
func TestOpenCodeConfigVerbPrintsItsRunID(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	for _, args := range [][]string{{"--run-id", openCodeVerbRunID}, nil} {
		out, err := runOpenCodeVerb(t, append([]string{"--stage", "feature-dev", "--worktree", worktree, "--json"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		var run adapters.OpenCodeRun
		if err := json.Unmarshal([]byte(out), &run); err != nil {
			t.Fatal(err)
		}
		if run.RunID == "" || filepath.Base(run.RunDir) != run.RunID {
			t.Errorf("run_id = %q, run_dir = %q: run_id must name run_dir", run.RunID, run.RunDir)
		}
		if args != nil && run.RunID != openCodeVerbRunID {
			t.Errorf("run_id = %q, want --run-id's %q", run.RunID, openCodeVerbRunID)
		}
	}
}

// TestOpenCodeConfigVerbRefusesAnUnresolvableSkillsRoot: with --skills-root
// named, a stage whose SKILL.md is not found fails the verb, with nothing on
// stdout, instead of printing an all-deny permission map (#1648).
func TestOpenCodeConfigVerbRefusesAnUnresolvableSkillsRoot(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--skills-root", t.TempDir(), "--json")
	if err == nil || !strings.Contains(err.Error(), "--skills-root") {
		t.Fatalf("the verb accepted an unresolvable --skills-root: err=%v", err)
	}
	if out != "" {
		t.Errorf("a refusal printed on stdout:\n%s", out)
	}
}

func runOpenCodeCleanup(t *testing.T, args ...string) error {
	t.Helper()
	cmd := opencodeCmd()
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	cmd.SetArgs(append([]string{"cleanup"}, args...))
	return cmd.Execute()
}

// TestOpenCodeCleanupVerb deletes the root the config verb created for a run
// id, accepts one that is already gone, and refuses an id that is not a run
// identity (#1648, ADR-022 § 22).
func TestOpenCodeCleanupVerb(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var run adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.RunDir); err != nil {
		t.Fatalf("the config verb created no root: %v", err)
	}
	if err := runOpenCodeCleanup(t, "--run-id", run.RunID); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(run.RunDir); !os.IsNotExist(err) {
		t.Errorf("the root %s is still there: %v", run.RunDir, err)
	}
	if err := runOpenCodeCleanup(t, "--run-id", run.RunID); err != nil {
		t.Errorf("cleaning up a root that is gone failed: %v", err)
	}
	for _, bad := range []string{"../../etc", "wf-12-feature-dev", ""} {
		if err := runOpenCodeCleanup(t, "--run-id", bad); err == nil {
			t.Errorf("cleanup accepted --run-id %q", bad)
		}
	}
}
