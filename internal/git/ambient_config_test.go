package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestServiceGitIsIsolatedFromAmbientConfig is the regression test for #542.
//
// It reproduces the issue's own technique — a HOME carrying a hostile
// ~/.gitconfig — and then drives a service path that SHELLS OUT
// (commitAll → `git add -A` && `git commit`). That is the half Env() cannot
// reach: Service.gitExec and commitAll build a bare exec.Command and inherit
// os.Environ() on purpose, so in production a real run honours the operator's
// hooks and signing. Only TestMain's gittest.IsolateProcess() keeps that
// inheritance from dragging the operator's config into the test binary.
//
// MUTATION: delete the gittest.IsolateProcess() call from TestMain and this
// test fails with "ambient pre-commit hook refused". It compiles either way,
// which is the point — a mutation that only breaks the build would prove
// nothing about the guard.
//
// Note this test does NOT set GIT_CONFIG_GLOBAL itself. Doing so would make it
// pass on its own and stop testing TestMain at all — the assertion is that
// process-wide isolation is already in force before the test runs.
func TestServiceGitIsIsolatedFromAmbientConfig(t *testing.T) {
	hostileHome := t.TempDir()
	hook := filepath.Join(hostileHome, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho ambient pre-commit hook refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hostile hook: %v", err)
	}
	cfg := "[commit]\n\tgpgsign = true\n" +
		"[core]\n\thooksPath = " + hostileHome + "\n" +
		"[gpg]\n\tprogram = /nonexistent-so-signing-would-explode\n"
	if err := os.WriteFile(filepath.Join(hostileHome, ".gitconfig"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write hostile global config: %v", err)
	}
	t.Setenv("HOME", hostileHome)

	svc, dir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "ambient.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.commitAll("chore: commit under a hostile ambient config"); err != nil {
		t.Fatalf("commitAll failed under a hostile ambient git config — "+
			"the service's own child git is not isolated (#542): %v", err)
	}
}

// TestAmbientConfigKeysAreNeutralisedForChildren pins the mechanism rather than
// one symptom, so a future change that drops a key (or that isolates only the
// fixture helper again) is caught by name.
func TestAmbientConfigKeysAreNeutralisedForChildren(t *testing.T) {
	for _, k := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if got := os.Getenv(k); got != os.DevNull {
			t.Errorf("%s = %q, want %q — ambient git config reaches every child "+
				"process this package spawns", k, got, os.DevNull)
		}
	}
	if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
		t.Errorf("GIT_CONFIG_NOSYSTEM = %q, want \"1\"", got)
	}
}
