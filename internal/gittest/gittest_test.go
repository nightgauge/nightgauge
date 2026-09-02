package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- deterministic: the disarming/isolation this package promises is active ---

// TestEnvDisarmsBackgroundMaintenanceTriggers is the direct, non-timing-
// dependent proof for #680: every knob Env() sets is actually in force for a
// git invocation, and none of it was ever written to the repository's own
// .git/config — it is layered in at the environment level, so it applies to
// the very first command run against a brand-new repo (the `git init` that
// creates it included), not just to commands issued after some setup step a
// test could forget to call.
func TestEnvDisarmsBackgroundMaintenanceTriggers(t *testing.T) {
	dir := InitRepo(t, t.TempDir(), "-q", "-b", "main")

	for _, tc := range []struct {
		key  string
		want string
	}{
		{"gc.auto", "0"},
		{"gc.autoDetach", "false"},
		{"maintenance.auto", "false"},
		{"core.fsmonitor", "false"},
	} {
		if got := Run(t, dir, "config", "--get", tc.key); got != tc.want {
			t.Errorf("git config --get %s = %q, want %q", tc.key, got, tc.want)
		}
		// --local must see nothing: proves the value came from the
		// environment, not from a line this package (or the test) wrote into
		// .git/config.
		if out, err := Command(dir, "config", "--local", "--get", tc.key).CombinedOutput(); err == nil {
			t.Errorf("git config --local --get %s unexpectedly succeeded (%q) — the override leaked into .git/config instead of staying environment-only", tc.key, out)
		}
	}
}

// TestEnvIsolatesAmbientConfig is the direct regression test for #542: a
// hostile GLOBAL git config (signing on, a hook that refuses everything)
// must not affect a repo built through this package. This reproduces #542's
// own repro technique (a scrubbed HOME with a poisoned global .gitconfig)
// rather than merely asserting the two settings it happened to name, so a
// third ambient setting with the same shape is covered too.
func TestEnvIsolatesAmbientConfig(t *testing.T) {
	fakeHome := t.TempDir()
	globalConfig := filepath.Join(fakeHome, ".gitconfig")
	refusingHook := filepath.Join(fakeHome, "refuse-everything")
	if err := os.WriteFile(refusingHook, []byte("#!/bin/sh\necho ambient pre-commit hook refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake hook: %v", err)
	}
	globalCfg := "[commit]\n\tgpgsign = true\n[core]\n\thooksPath = " + fakeHome + "\n[gpg]\n\tprogram = /path/does/not/exist-so-signing-would-explode\n"
	if err := os.WriteFile(globalConfig, []byte(globalCfg), 0o644); err != nil {
		t.Fatalf("write fake global config: %v", err)
	}
	if err := os.Rename(refusingHook, filepath.Join(fakeHome, "pre-commit")); err != nil {
		t.Fatalf("install fake pre-commit hook: %v", err)
	}
	if err := os.Chmod(filepath.Join(fakeHome, "pre-commit"), 0o755); err != nil {
		t.Fatalf("chmod fake pre-commit hook: %v", err)
	}
	// Mirrors #542's own reproduction: point HOME at a directory carrying a
	// hostile ~/.gitconfig. Env() must still win regardless, since it points
	// GIT_CONFIG_GLOBAL at /dev/null unconditionally rather than depending on
	// HOME not resolving to something dangerous.
	t.Setenv("HOME", fakeHome)

	dir := InitRepo(t, t.TempDir(), "-q", "-b", "main")
	Run(t, dir, "config", "user.email", "isolated@example.invalid")
	Run(t, dir, "config", "user.name", "Isolated")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	Run(t, dir, "add", "f.txt")

	// If ambient isolation fails, this either shells out to the nonexistent
	// gpg program (gpgsign=true) or is refused by the hostile hook — the
	// exact two failure modes #542 captured. Env() overrides GIT_CONFIG_GLOBAL
	// unconditionally (see Env's doc comment), so this must succeed
	// regardless of what t.Setenv("HOME", ...) above pointed at.
	Run(t, dir, "commit", "-m", "should succeed despite a hostile ambient global config")
}

// --- best-effort: the underlying OS mechanism the fix exists to avoid ---

// TestBackgroundGitProcessCanOutliveItsParent is a demonstration, not a
// correctness assertion about this package. It exists to make the #680
// mechanism concrete and executable rather than only asserted in a PR
// description: a `git gc --detach` genuinely forks a child that keeps
// mutating .git (and can hold the directory "not empty" against a concurrent
// RemoveAll) after the command that spawned it has already returned control
// to the caller — which is exactly what races t.TempDir()'s cleanup.
//
// `--detach` is used directly (bypassing the `--auto` heuristic Env()
// disarms) because that heuristic's "does the repo need housekeeping"
// estimate samples the object store rather than counting it exactly, so
// whether it decides to run is hash-dependent and was not reliably
// triggerable on demand in this environment (see the issue's investigation
// notes) — `--detach` reproduces the same detach-and-keep-writing shape
// deterministically, which is what this test needs to be a reliable,
// permanent, non-flaky part of the suite.
//
// Because it is still fundamentally a race against OS process scheduling,
// this test only ever demonstrates or skips — it must never fail the build,
// or it would reintroduce exactly the kind of environment-dependent red
// check #680 is about.
func TestBackgroundGitProcessCanOutliveItsParent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	Run(t, dir, "init", "-q", "-b", "main")
	Run(t, dir, "config", "user.email", "race@example.invalid")
	Run(t, dir, "config", "user.name", "Race")

	// Give gc actual work to do so the background child stays alive long
	// enough to be caught in the act: one commit adding many small files
	// (fast — a single add/commit pair, not one per file) rather than many
	// commits (slow — one git process per commit).
	const fileCount = 500
	for i := 0; i < fileCount; i++ {
		name := filepath.Join(dir, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("loose object filler\n"), 0o644); err != nil {
			t.Fatalf("write filler file: %v", err)
		}
	}
	Run(t, dir, "add", "-A")
	Run(t, dir, "commit", "-q", "-m", "bulk add for gc filler")

	// Explicit --detach, deliberately NOT through Env() / gittest.Command:
	// this test is proving the underlying git/OS mechanism, not exercising
	// this package's disarming (--detach ignores gc.auto entirely, so
	// disarming gc.auto would not change this test's outcome either way).
	gc := exec.Command("git", "-C", dir, "gc", "--detach", "--quiet")
	if err := gc.Start(); err != nil {
		t.Skipf("could not start background git gc: %v", err)
	}
	t.Cleanup(func() { _ = gc.Wait() })

	pidFile := filepath.Join(dir, ".git", "gc.pid")
	deadline := time.Now().Add(2 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); err == nil {
			found = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Skip("could not observe .git/gc.pid within the poll window on this machine/git version — " +
			"the background-gc mechanism was not caught in the act here; see the issue's investigation " +
			"notes for a manual reproduction that did catch it")
	}

	// Caught it alive: race a removal against it right now, the same way
	// t.TempDir()'s deferred cleanup would.
	err := os.RemoveAll(dir)
	if err != nil {
		t.Logf("reproduced #680's failure mode: RemoveAll raced the still-running background gc and lost: %v", err)
		return
	}
	t.Log("RemoveAll happened to win the race this run (gc finished between the poll and the removal) — " +
		"the mechanism was still observed (gc.pid was present), this is just timing, not a failure")
}

// TestIsolateProcessDisarmsBackgroundWritersForInheritedGit is the #1293 proof:
// git spawned by the CODE UNDER TEST — a plain exec.Command with no Env() —
// must inherit the same gc/maintenance disarm as the commands this package
// spawns, or a checkpoint commit forks a detached `gc --auto` that races
// t.TempDir cleanup. It goes red if IsolateProcess stops applying the block.
func TestIsolateProcessDisarmsBackgroundWritersForInheritedGit(t *testing.T) {
	keys := []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1", "GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2",
		"GIT_CONFIG_KEY_3", "GIT_CONFIG_VALUE_3"}
	saved := map[string]string{}
	for _, k := range keys {
		saved[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	})

	IsolateProcess()

	dir := t.TempDir()
	for key, want := range map[string]string{
		"gc.auto":          "0",
		"gc.autoDetach":    "false",
		"maintenance.auto": "false",
		"core.fsmonitor":   "false",
	} {
		// Deliberately NOT gittest.Command: this is the inherited-environment
		// path the code under test uses.
		cmd := exec.Command("git", "config", "--get", key)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git config --get %s after IsolateProcess: %v (the disarm did not reach an inherited-env git)", key, err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("git config --get %s after IsolateProcess = %q, want %q", key, got, want)
		}
	}
}
