package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// layoutMigrateRepo is a committed fixture repository with a plan at the old
// per-clone location, and the machine state isolated.
func layoutMigrateRepo(t *testing.T) (root, legacyPlan, newPlan string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(gittest.InitRepo(t, t.TempDir(), "-b", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.nightgauge/plans/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", "-A")
	gittest.Run(t, root, "commit", "-q", "-m", "init")
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(layout.EnvStateHome, state)
	legacyPlan = filepath.Join(root, ".nightgauge", "plans", "issue-5.md")
	if err := os.MkdirAll(filepath.Dir(legacyPlan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPlan, []byte("old plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, legacyPlan, filepath.Join(root, ".git", "nightgauge", "plans", "issue-5.md")
}

// runProbe executes a no-op subcommand through the real root command, so its
// PersistentPreRunE (and the migration in it) runs first.
func runProbe(t *testing.T, dir string) (error, bool, string) {
	t.Helper()
	t.Chdir(dir)
	root := rootCmd()
	ran := false
	root.AddCommand(&cobra.Command{Use: "layout-probe", RunE: func(*cobra.Command, []string) error {
		ran = true
		return nil
	}})
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"layout-probe"})
	err := root.Execute()
	return err, ran, stderr.String()
}

// TestAutoMigrateLayoutAtCLIStart (#2040, ADR-024 § 15): the first command on
// an unmigrated clone moves its per-clone data before it runs.
func TestAutoMigrateLayoutAtCLIStart(t *testing.T) {
	root, legacyPlan, newPlan := layoutMigrateRepo(t)
	err, ran, stderr := runProbe(t, root)
	if err != nil || !ran {
		t.Fatalf("command: err %v, ran %v", err, ran)
	}
	if got, rerr := os.ReadFile(newPlan); rerr != nil || string(got) != "old plan\n" {
		t.Errorf("plan not moved to %s: %q, %v", newPlan, got, rerr)
	}
	if _, serr := os.Lstat(legacyPlan); !os.IsNotExist(serr) {
		t.Errorf("legacy plan still present (%v)", serr)
	}
	if !strings.Contains(stderr, fmt.Sprintf("layout v%d", doctor.LayoutVersion)) {
		t.Errorf("stderr %q does not report the migration", stderr)
	}
}

// TestAutoMigrateLayoutNeverFailsTheCommand: a migration that cannot complete
// (a conflict) leaves both files alone, and the command still runs and exits
// cleanly; the notice names `nightgauge doctor --fix`.
func TestAutoMigrateLayoutNeverFailsTheCommand(t *testing.T) {
	root, legacyPlan, newPlan := layoutMigrateRepo(t)
	if err := os.MkdirAll(filepath.Dir(newPlan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPlan, []byte("new plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err, ran, stderr := runProbe(t, root)
	if err != nil || !ran {
		t.Fatalf("a failing migration changed the command's outcome: err %v, ran %v", err, ran)
	}
	for path, want := range map[string]string{legacyPlan: "old plan\n", newPlan: "new plan\n"} {
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Errorf("%s = %q, want %q (untouched)", path, got, want)
		}
	}
	if !strings.Contains(stderr, "nightgauge doctor --fix") {
		t.Errorf("stderr %q does not name `nightgauge doctor --fix`", stderr)
	}
}

// TestAutoMigrateLayoutBlockedNeverFailsTheCommand pins the ADR-024 § 15
// amendment of 2026-09-29: when the migration is blocked (here another process
// holds the migration lock; a run in flight, a busy worktree and a live daemon
// take the same path) the automatic run leaves the data in place and the
// user's command still runs and exits 0, while `nightgauge doctor --fix`
// keeps exit 4 for the same state. The notice names `nightgauge doctor --fix`.
func TestAutoMigrateLayoutBlockedNeverFailsTheCommand(t *testing.T) {
	root, legacyPlan, newPlan := layoutMigrateRepo(t)
	clone, err := layout.CloneDir(root)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(clone, ".migrate.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := flock.Exclusive(lock, 0); err != nil {
		t.Skipf("advisory locks unavailable: %v", err)
	}
	defer func() { _ = flock.Unlock(lock) }()

	err, ran, stderr := runProbe(t, root)
	if err != nil || !ran {
		t.Fatalf("a blocked migration changed the command's outcome: err %v, ran %v", err, ran)
	}
	if got, _ := os.ReadFile(legacyPlan); string(got) != "old plan\n" {
		t.Errorf("legacy plan = %q, want it untouched", got)
	}
	if _, serr := os.Lstat(newPlan); !os.IsNotExist(serr) {
		t.Errorf("the plan moved while the migration was blocked (%v)", serr)
	}
	if !strings.Contains(stderr, "nightgauge doctor --fix") {
		t.Errorf("stderr %q does not name `nightgauge doctor --fix`", stderr)
	}
	// The same state through doctor --fix is exit 4 (blocked).
	rep := fixerFor(t, root, doctor.LayoutCheckID).Run(context.Background(), doctor.FixOptions{Yes: true})
	if rep.ExitCode != 4 {
		t.Errorf("doctor --fix exit = %d with the migration lock held, want 4; results %+v", rep.ExitCode, rep.Results)
	}
}

// TestAutoMigrateLayoutBusyWorktreeNeverFailsTheCommand: a pipeline worktree
// at an old base with a run in flight on it (a live in-flight sidecar an older
// build wrote) is skipped by the automatic run, which moves nothing the run
// holds; the user's command exits 0, while `doctor --fix` exits 4 for the same
// state (ADR-024 § 15 amendment of 2026-09-29).
func TestAutoMigrateLayoutBusyWorktreeNeverFailsTheCommand(t *testing.T) {
	root, legacyPlan, _ := layoutMigrateRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.nightgauge/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", "-A")
	gittest.Run(t, root, "commit", "-q", "-m", "ignore")
	busy := filepath.Join(root, ".nightgauge", "worktrees", "repo-issue-7")
	gittest.Run(t, root, "worktree", "add", "-q", "-b", "issue-7", busy)
	sidecar := fmt.Sprintf(`{"issue_number":7,"run_id":"run-7","pid":%d}`, os.Getpid())
	legacyPipeline := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(legacyPipeline, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyPipeline, "current-run.json"), []byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}

	err, ran, stderr := runProbe(t, root)
	if err != nil || !ran {
		t.Fatalf("a busy worktree changed the command's outcome: err %v, ran %v", err, ran)
	}
	if _, serr := os.Stat(filepath.Join(busy, ".gitignore")); serr != nil {
		t.Errorf("the busy worktree moved: %v", serr)
	}
	if got, _ := os.ReadFile(legacyPlan); string(got) != "old plan\n" {
		t.Errorf("legacy plan = %q, want it held while the run is in flight", got)
	}
	if !strings.Contains(stderr, "nightgauge doctor --fix") {
		t.Errorf("stderr %q does not name `nightgauge doctor --fix`", stderr)
	}
	rep := fixerFor(t, root, doctor.LayoutCheckID).Run(context.Background(), doctor.FixOptions{Yes: true})
	if rep.ExitCode != 4 {
		t.Errorf("doctor --fix exit = %d with a run in flight, want 4; results %+v", rep.ExitCode, rep.Results)
	}
}

// TestLayoutAutoMigrateApplies: doctor, layout, version, help, completion and
// hooks never trigger the migration, and neither does a dry run.
func TestLayoutAutoMigrateApplies(t *testing.T) {
	root := rootCmd()
	find := func(args ...string) *cobra.Command {
		c, _, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		return c
	}
	for _, args := range [][]string{{"doctor"}, {"layout"}, {"version"}, {"hook"}} {
		if layoutAutoMigrateApplies(find(args...)) {
			t.Errorf("%v triggers the migration", args)
		}
	}
	// cobra adds completion and its hidden request commands at Execute.
	for _, name := range []string{"completion", "help", cobra.ShellCompRequestCmd} {
		r := &cobra.Command{Use: "nightgauge"}
		c := &cobra.Command{Use: name}
		r.AddCommand(c)
		if layoutAutoMigrateApplies(c) {
			t.Errorf("%s triggers the migration", name)
		}
	}
	if layoutAutoMigrateApplies(root) {
		t.Error("the bare root command triggers the migration")
	}
	status := find("status")
	if !layoutAutoMigrateApplies(status) {
		t.Error("status does not trigger the migration")
	}
	logsPrune := find("logs", "prune")
	if err := logsPrune.Flags().Set("dry-run", "true"); err != nil {
		t.Fatal(err)
	}
	if layoutAutoMigrateApplies(logsPrune) {
		t.Error("a --dry-run command triggers the migration")
	}
}
