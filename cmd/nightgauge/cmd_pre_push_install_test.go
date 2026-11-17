package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/shippedfiles"
)

// TestPrePushInstallWritesTheShippedHook (#3383): the installed hook is the
// shipped file byte for byte. testdata/pre-push.golden is a deliberate change
// guard: the hook the binary carried before #3383, so a hook edit is a
// visible two-file change.
func TestPrePushInstallWritesTheShippedHook(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "pre-push.golden"))
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(t.TempDir(), "pre-push")
	// A reinstall over a non-executable hook must leave it 0755.
	if err := os.WriteFile(hook, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installPrePushHook(hook); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, golden) {
		t.Fatalf("installed hook differs from testdata/pre-push.golden")
	}
	if fi, _ := os.Stat(hook); fi.Mode().Perm() != 0o755 {
		t.Errorf("hook mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestPrePushInstallNamesTheMissingHook(t *testing.T) {
	t.Setenv(shippedfiles.EnvDir, t.TempDir())
	err := installPrePushHook(filepath.Join(t.TempDir(), "pre-push"))
	if err == nil || !strings.Contains(err.Error(), shippedfiles.PrePushHook) {
		t.Fatalf("err = %v, want a miss naming %s", err, shippedfiles.PrePushHook)
	}
}

func TestPrePushHookSHA256MatchesTheSource(t *testing.T) {
	got, err := shippedfiles.Digest(filepath.Join("..", "..", filepath.FromSlash(shippedfiles.PrePushHookSource)))
	if err != nil {
		t.Fatal(err)
	}
	if got != prePushHookSHA256 {
		t.Fatalf("prePushHookSHA256 = %s; scripts/git-hooks/pre-push is %s", prePushHookSHA256, got)
	}
}

func TestPrePushInstallRefusesAnEditedHook(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, filepath.FromSlash(shippedfiles.PrePushHook))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(shippedfiles.EnvDir, dir)
	hook := filepath.Join(t.TempDir(), "pre-push")
	if err := installPrePushHook(hook); err == nil || !strings.Contains(err.Error(), prePushHookSHA256) {
		t.Fatalf("err = %v, want a digest refusal", err)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("an edited hook was installed")
	}
}
