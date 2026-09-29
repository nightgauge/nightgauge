package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// #2093. `doctor --dry-run`, `--fix` and `--history` against a real
// repository holding a leaked (merged) pipeline worktree.

// fixerFor is the production Fixer narrowed to the named checks, over the
// real built-in verbs, logging to the default fix-log path.
func fixerFor(t *testing.T, root string, ids ...string) *doctor.Fixer {
	t.Helper()
	all := map[string]doctor.Check{}
	for _, c := range doctor.DefaultRegistry().Checks() {
		c.DependsOn = nil
		all[c.ID] = c
	}
	reg := doctor.NewRegistry()
	for _, id := range ids {
		reg.MustRegister(all[id])
	}
	env := &doctor.Env{Cwd: root, Now: time.Now()}
	path, err := doctor.DefaultFixLogPath()
	if err != nil {
		t.Fatalf("fix log path: %v", err)
	}
	return &doctor.Fixer{Registry: reg, Verbs: doctor.BuiltinVerbs(env), Env: env, Log: &doctor.FixLog{Path: path}}
}

func isolateDoctorState(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_STATE_HOME", filepath.Join(home, "state"))
}

func doctorFix(t *testing.T, fx *doctor.Fixer, fl doctorFixFlags, jsonOut bool) (string, int) {
	t.Helper()
	opts, err := fl.options()
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	var out bytes.Buffer
	code := runDoctorFix(context.Background(), &out, fx, opts, jsonOut)
	return out.String(), code
}

func TestDoctorFix_DryRunFixAndIdempotentRerun(t *testing.T) {
	root, wt := sweepRepo(t, 2093)
	isolateDoctorState(t)

	out, code := doctorFix(t, fixerFor(t, root, "worktree_leaks"), doctorFixFlags{dryRun: true}, false)
	if code != 0 || !strings.Contains(out, "[DRY RUN] NGD017") || !strings.Contains(out, "preview: remove worktree "+wt) {
		t.Fatalf("--dry-run exit %d, output:\n%s", code, out)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("--dry-run removed the worktree: %v", err)
	}

	out, code = doctorFix(t, fixerFor(t, root, "worktree_leaks"), doctorFixFlags{fix: true}, false)
	if code != 0 || !strings.Contains(out, "[FIXED] NGD017") || !strings.Contains(out, "verified: worktree_leaks no longer reports it") {
		t.Fatalf("--fix exit %d, output:\n%s", code, out)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("--fix left the worktree in place (stat err %v)", err)
	}

	out, code = doctorFix(t, fixerFor(t, root, "worktree_leaks"), doctorFixFlags{fix: true}, true)
	var rep doctor.FixReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("second --fix --json did not decode: %v\n%s", err, out)
	}
	if code != 0 || len(rep.Results) != 0 || rep.ExitCode != 0 {
		t.Fatalf("second --fix: exit %d, %d result(s): %s", code, len(rep.Results), out)
	}

	// --history shows the applied entry, through the real command.
	cmd := doctorCmd()
	var hist bytes.Buffer
	cmd.SetOut(&hist)
	cmd.SetArgs([]string{"--history"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor --history: %v", err)
	}
	if !strings.Contains(hist.String(), "FIXED") || !strings.Contains(hist.String(), "NGD017") ||
		!strings.Contains(hist.String(), "worktree.sweep") {
		t.Fatalf("--history output:\n%s", hist.String())
	}
}

func TestDoctorFix_ConfirmRemedyNeedsYes(t *testing.T) {
	root, _ := sweepRepo(t, 2094)
	isolateDoctorState(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := gittest.Command(root, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// A merged branch no worktree holds: its remedy is confirm branch.delete.
	git("checkout", "-q", "-b", "fix/77-landed", "main")
	if err := os.WriteFile(filepath.Join(root, "landed.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "landed.txt")
	git("commit", "-q", "-m", "work")
	git("checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(root, "landed.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "landed.txt")
	git("commit", "-q", "-m", "squash: fix/77-landed")
	git("push", "-q", "origin", "main")
	git("fetch", "-q", "origin")

	out, code := doctorFix(t, fixerFor(t, root, "stranded_branches"), doctorFixFlags{fix: true, only: []string{"stranded_branches"}}, false)
	if code != 0 || !strings.Contains(out, "[NEEDS --yes] NGD018") || !strings.Contains(out, "fix/77-landed") {
		t.Fatalf("--fix without --yes: exit %d, output:\n%s", code, out)
	}
	if b, err := gittest.Command(root, "branch", "--list", "fix/77-landed").Output(); err != nil || !strings.Contains(string(b), "fix/77-landed") {
		t.Fatalf("a confirm remedy was applied without --yes (branch list %q, err %v)", b, err)
	}

	out, code = doctorFix(t, fixerFor(t, root, "stranded_branches"), doctorFixFlags{fix: true, yes: true}, false)
	if code != 0 || !strings.Contains(out, "[FIXED] NGD018") {
		t.Fatalf("--fix --yes: exit %d, output:\n%s", code, out)
	}
}

func TestDoctorFix_FlagValidation(t *testing.T) {
	for _, fl := range []doctorFixFlags{
		{yes: true},
		{only: []string{"NGD017"}},
		{severity: []string{"warning"}},
		{history: true, fix: true},
		{fix: true, severity: []string{"urgent"}},
	} {
		if _, err := fl.options(); err == nil {
			t.Errorf("flags %+v were accepted", fl)
		}
	}
	opts, err := doctorFixFlags{dryRun: true, yes: true, severity: []string{"Housekeeping"}}.options()
	if err != nil || !opts.DryRun || opts.Yes || len(opts.Severities) != 1 {
		t.Errorf("dry-run options = %+v, err %v; --dry-run must never carry consent", opts, err)
	}
}
