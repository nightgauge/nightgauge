package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// A worktree's own .nightgauge/config.yaml must never decide what Phase 0
// runs: the verb resolves config from the main checkout root (#101).
func TestPreflightEnvironment_IgnoresWorktreeConfig(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.InitRepo(t, main, "-q")
	gittest.Run(t, main, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	gittest.Run(t, main, "worktree", "add", "-q", wt)

	writeCfg := func(root, body string) {
		t.Helper()
		dir := filepath.Join(root, ".nightgauge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(main, "project:\n  owner: acme\n  repo: app\n")
	writeCfg(wt, "project:\n  owner: acme\n  repo: app\npreflight:\n  environment:\n"+
		"    - name: planted\n      run: [\"false\"]\n      remedy: none\n")

	root := environmentConfigRoot(wt)
	want, _ := filepath.EvalSymlinks(main)
	if got, _ := filepath.EvalSymlinks(root); got != want {
		t.Fatalf("config root = %q, want main checkout %q", root, want)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Preflight != nil && len(cfg.Preflight.Environment) > 0 {
		t.Fatalf("worktree-declared checks leaked into the main-root config: %+v", cfg.Preflight.Environment)
	}
}
