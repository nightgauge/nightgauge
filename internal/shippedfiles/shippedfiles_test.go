package shippedfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withExe(t *testing.T, exe string) {
	t.Helper()
	old := executable
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = old })
}

func TestLocateOrder(t *testing.T) {
	cases := []struct {
		name  string
		setup func(root string) (exe, env, want string)
	}{
		{"env shipped layout", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "env", "git-hooks", "pre-push"))
			touch(t, filepath.Join(r, "bin", "git-hooks", "pre-push"))
			return filepath.Join(r, "bin", "nightgauge"), filepath.Join(r, "env"),
				filepath.Join(r, "env", "git-hooks", "pre-push")
		}},
		{"env shipped name over env checkout", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "env", "git-hooks", "pre-push"))
			touch(t, filepath.Join(r, "env", "scripts", "git-hooks", "pre-push"))
			return filepath.Join(r, "bin", "nightgauge"), filepath.Join(r, "env"),
				filepath.Join(r, "env", "git-hooks", "pre-push")
		}},
		{"vsix over checkout", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "git-hooks", "pre-push"))
			touch(t, filepath.Join(r, "scripts", "git-hooks", "pre-push"))
			return filepath.Join(r, "bin", "nightgauge"), "",
				filepath.Join(r, "bin", "..", "git-hooks", "pre-push")
		}},
		{"env checkout", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "src", "scripts", "git-hooks", "pre-push"))
			return filepath.Join(r, "bin", "nightgauge"), filepath.Join(r, "src"),
				filepath.Join(r, "src", "scripts", "git-hooks", "pre-push")
		}},
		{"beside exe (archive)", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "a", "git-hooks", "pre-push"))
			touch(t, filepath.Join(r, "git-hooks", "pre-push"))
			return filepath.Join(r, "a", "nightgauge"), "", filepath.Join(r, "a", "git-hooks", "pre-push")
		}},
		{"parent of exe (vsix)", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "dist", "git-hooks", "pre-push"))
			return filepath.Join(r, "dist", "bin", "nightgauge"), "",
				filepath.Join(r, "dist", "bin", "..", "git-hooks", "pre-push")
		}},
		{"dev checkout", func(r string) (string, string, string) {
			touch(t, filepath.Join(r, "scripts", "git-hooks", "pre-push"))
			return filepath.Join(r, "bin", "nightgauge"), "",
				filepath.Join(r, "bin", "..", "scripts", "git-hooks", "pre-push")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, env, want := tc.setup(t.TempDir())
			t.Setenv(EnvDir, env)
			withExe(t, exe)
			got, err := Locate(PrePushHook, PrePushHookSource)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Clean(got) != filepath.Clean(want) {
				t.Fatalf("Locate = %s, want %s", got, want)
			}
		})
	}
}

func TestLocateFollowsExeSymlink(t *testing.T) {
	r := t.TempDir()
	touch(t, filepath.Join(r, "cask", "git-hooks", "pre-push"))
	touch(t, filepath.Join(r, "cask", "nightgauge"))
	if err := os.MkdirAll(filepath.Join(r, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r, "bin", "nightgauge")
	if err := os.Symlink(filepath.Join(r, "cask", "nightgauge"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDir, "")
	withExe(t, link)
	got, err := Locate(PrePushHook, PrePushHookSource)
	if err != nil {
		t.Fatalf("symlinked exe: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(r, "cask", "git-hooks", "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	if gotReal, _ := filepath.EvalSymlinks(got); gotReal != want || strings.Contains(got, filepath.Join("bin", "git-hooks")) {
		t.Fatalf("Locate = %s, want the hook beside the symlink's target, %s", got, want)
	}
}

func TestLocateMissNamesEveryPath(t *testing.T) {
	r := t.TempDir()
	t.Setenv(EnvDir, filepath.Join(r, "env"))
	withExe(t, filepath.Join(r, "bin", "nightgauge"))
	_, err := Locate(PrePushHook, PrePushHookSource)
	if err == nil {
		t.Fatal("Locate found a file in an empty tree")
	}
	for _, p := range Candidates(PrePushHook, PrePushHookSource) {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not name %s", err, p)
		}
	}
	if len(Candidates(PrePushHook, PrePushHookSource)) != 5 {
		t.Errorf("want 5 candidates")
	}
}

func TestCandidatesWithoutAnExecutable(t *testing.T) {
	old := executable
	executable = func() (string, error) { return "", errors.New("no executable") }
	t.Cleanup(func() { executable = old })
	t.Setenv(EnvDir, "/env")
	got := Candidates(PrePushHook, PrePushHookSource)
	want := []string{filepath.Join("/env", PrePushHook), filepath.Join("/env", PrePushHookSource)}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Candidates = %q, want only the env paths %q", got, want)
	}
}

// TestLocateReportsAnUnreadableCandidate: a candidate that exists but cannot
// be stat'ed is reported as itself, not as "not found".
func TestLocateReportsAnUnreadableCandidate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	r := t.TempDir()
	env := filepath.Join(r, "env")
	touch(t, filepath.Join(env, "git-hooks", "pre-push"))
	if err := os.Chmod(env, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(env, 0o755) })
	t.Setenv(EnvDir, env)
	withExe(t, filepath.Join(r, "bin", "nightgauge"))
	_, err := Locate(PrePushHook, PrePushHookSource)
	if err == nil || strings.Contains(err.Error(), "not found") || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want the permission error on %s", err, env)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDigest: a file's digest is the sha256 of its bytes; a tree's digest
// changes with any file's content, name, location or presence.
func TestDigest(t *testing.T) {
	r := t.TempDir()
	f := filepath.Join(r, "hook")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("#!/bin/sh\n"))
	if got, err := Digest(f); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("Digest(file) = %s, %v; want %x", got, err, sum)
	}

	base := map[string]string{"index.js": "a", "nightgauge/gates.js": "b"}
	digest := func(files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		writeTree(t, root, files)
		d, err := Digest(root)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	want := digest(base)
	if again := digest(map[string]string{"nightgauge/gates.js": "b", "index.js": "a"}); again != want {
		t.Fatalf("the same tree digests differently: %s vs %s", again, want)
	}
	variants := map[string]map[string]string{
		"content changed": {"index.js": "A", "nightgauge/gates.js": "b"},
		"file renamed":    {"main.js": "a", "nightgauge/gates.js": "b"},
		"file added":      {"index.js": "a", "nightgauge/gates.js": "b", "extra.js": ""},
		"file moved":      {"index.js": "a", "gates.js": "b"},
		"file removed":    {"index.js": "a"},
	}
	for name, files := range variants {
		if got := digest(files); got == want {
			t.Errorf("%s: the digest did not change", name)
		}
	}
}

// TestLoadVerified returns the bytes it checked, and refuses a mismatch.
func TestLoadVerified(t *testing.T) {
	r := t.TempDir()
	touch(t, filepath.Join(r, PrePushHook))
	t.Setenv(EnvDir, r)
	sum := sha256.Sum256([]byte("x"))
	p, err := LoadVerified(PrePushHook, PrePushHookSource, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if string(p.File()) != "x" || p.Path != filepath.Join(r, PrePushHook) {
		t.Fatalf("LoadVerified = %q at %s", p.File(), p.Path)
	}
	if _, err := LoadVerified(PrePushHook, PrePushHookSource, strings.Repeat("0", 64)); err == nil ||
		!strings.Contains(err.Error(), "this binary pins") {
		t.Fatalf("err = %v, want a digest refusal", err)
	}

	tree := filepath.Join(r, OpenCodePlugin)
	writeTree(t, tree, map[string]string{"index.js": "a", "nightgauge/gates.js": "b"})
	want, err := Digest(tree)
	if err != nil {
		t.Fatal(err)
	}
	p, err = LoadVerified(OpenCodePlugin, OpenCodePluginSource, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 || string(p.Files["nightgauge/gates.js"]) != "b" {
		t.Fatalf("LoadVerified tree = %v", p.Files)
	}
}
