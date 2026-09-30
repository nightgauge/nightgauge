package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
	if !strings.Contains(stderr, "layout v1") {
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
