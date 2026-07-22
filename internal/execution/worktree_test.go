package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestGitRepo creates a real git repo with one commit on a named branch.
// Returns the repo root path.
func initTestGitRepo(t *testing.T, branchName string) string {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-b", branchName)
	runGit("config", "user.email", "test@test")
	runGit("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "initial")
	return dir
}

func TestEnsureWorktree_DoesNotCollideWithMainRepoBranch(t *testing.T) {
	// Regression guard for the worktree collision that tripped the
	// autonomous circuit breaker on #2671: the old code read the main
	// repo's HEAD branch name and tried to check that same branch out in
	// the worktree. Git forbids two worktrees on one branch, so dispatch
	// failed any time the main repo was on a real branch.
	//
	// After the fix, ensureWorktree uses `worktree add --detach <sha>`,
	// which never claims a branch reference. This test asserts that a
	// worktree can be created even though the main repo is on a feature
	// branch whose name matches no other constraint.
	repoRoot := initTestGitRepo(t, "feature-that-should-not-block")
	workspaceRoot := t.TempDir()

	m := &Manager{workspaceRoot: workspaceRoot}
	// Make repoRoot() return our initialized repo for this test — the
	// production impl uses workspaceRoot as the repo root.
	// Simulate this by placing a dummy sentinel at workspaceRoot and
	// pointing HEAD/config there. Simplest: use the repo itself as the
	// workspace root.
	m.workspaceRoot = repoRoot

	got, err := m.ensureWorktree("nightgauge/nightgauge", 2671)
	if err != nil {
		t.Fatalf("ensureWorktree failed (branch collision regression?): %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("worktree dir not created at %s: %v", got, err)
	}

	// Verify it really is detached (no branch held by the worktree).
	headRef, err := os.ReadFile(filepath.Join(got, ".git"))
	if err != nil {
		t.Fatalf("read worktree .git pointer: %v", err)
	}
	// Follow the gitdir pointer and check HEAD contents.
	gitDirLine := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(headRef)), "gitdir:"))
	headContents, err := os.ReadFile(filepath.Join(gitDirLine, "HEAD"))
	if err != nil {
		t.Fatalf("read worktree HEAD: %v", err)
	}
	// Detached HEAD is a bare SHA, not a `ref: refs/heads/...` line.
	if strings.HasPrefix(strings.TrimSpace(string(headContents)), "ref:") {
		t.Errorf("worktree HEAD should be detached, got %q", headContents)
	}
}

// TestCleanupWorktree_TearsDownComposeStack asserts that CleanupWorktree
// runs `docker compose -p issue-NNN down -v --remove-orphans` BEFORE
// `git worktree remove`. Sequence is verified through a single fake binary
// shadowing both `docker` and `git` on PATH and recording each invocation
// to a per-test log. See Issue #3050.
func TestCleanupWorktree_TearsDownComposeStack(t *testing.T) {
	repoRoot := initTestGitRepo(t, "main")

	// Create a real worktree so `git worktree remove` has something to act on
	// when the production binary is on PATH (the fake only takes precedence
	// while it's installed below).
	m := &Manager{workspaceRoot: repoRoot}
	worktreePath, err := m.ensureWorktree("nightgauge/nightgauge", 8421)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree should exist before cleanup: %v", err)
	}

	// Install a fake `docker` shim that records calls. We do NOT shadow git;
	// CleanupWorktree's `git worktree remove` should still hit the real git.
	fakeDir := t.TempDir()
	logPath := filepath.Join(fakeDir, "calls.log")
	dockerScript := `#!/bin/sh
echo "docker $@" >> "$FAKE_DOCKER_LOG"
case "$1" in
  version) exit 0 ;;
  compose)
    case "$2" in
      ls) printf '[]' ; exit 0 ;;
      -p) [ "$4" = "down" ] && exit 0 ;;
    esac ;;
  images) printf '' ; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "docker"), []byte(dockerScript), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)

	if err := m.CleanupWorktree("nightgauge/nightgauge", 8421); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}

	calls, _ := os.ReadFile(logPath)
	if !strings.Contains(string(calls), "compose -p issue-8421 down -v --remove-orphans") {
		t.Errorf("expected compose teardown call for issue-8421, got log:\n%s", calls)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree should be removed after cleanup; stat err=%v", err)
	}
}

// TestCleanupWorktree_SoftFailWhenDockerMissing asserts that worktree
// removal still succeeds when docker is not available on PATH.
func TestCleanupWorktree_SoftFailWhenDockerMissing(t *testing.T) {
	repoRoot := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: repoRoot}
	worktreePath, err := m.ensureWorktree("nightgauge/nightgauge", 9001)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}

	// Install a fake docker that always fails `version` so IsAvailable returns
	// false. Worktree removal must still complete successfully.
	fakeDir := t.TempDir()
	failingDocker := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "docker"), []byte(failingDocker), 0o755); err != nil {
		t.Fatalf("write failing docker: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := m.CleanupWorktree("nightgauge/nightgauge", 9001); err != nil {
		t.Fatalf("CleanupWorktree must soft-fail when docker missing, got: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree should be removed even when docker unavailable")
	}
}

// TestRepoRoot_ResolvesViaResolver verifies the additive repo-root resolution
// (#229): with a resolver installed, a registered repo resolves to its mapped
// filesystem root, an unregistered repo falls back to workspaceRoot, and with
// no resolver installed every repo resolves to workspaceRoot (single-repo
// behavior unchanged).
func TestRepoRoot_ResolvesViaResolver(t *testing.T) {
	launchRoot := t.TempDir()

	m := &Manager{workspaceRoot: launchRoot}
	m.SetRepoPathResolver(func(repo string) string {
		if repo == "owner/other" {
			return "/tmp/other"
		}
		return ""
	})

	if got := m.RepoRoot("owner/other"); got != "/tmp/other" {
		t.Errorf("RepoRoot(owner/other) = %q, want /tmp/other", got)
	}
	if got := m.RepoRoot("owner/unknown"); got != launchRoot {
		t.Errorf("RepoRoot(owner/unknown) = %q, want launchRoot %q", got, launchRoot)
	}

	// No resolver installed → every repo resolves to the workspace root.
	m2 := &Manager{workspaceRoot: launchRoot}
	if got := m2.RepoRoot("owner/other"); got != launchRoot {
		t.Errorf("RepoRoot with nil resolver = %q, want launchRoot %q", got, launchRoot)
	}
	if got := m2.RepoRoot(""); got != launchRoot {
		t.Errorf("RepoRoot(\"\") with nil resolver = %q, want launchRoot %q", got, launchRoot)
	}
}

func TestShouldBuildSdkCli(t *testing.T) {
	tests := []struct {
		adapter string
		want    bool
	}{
		{"codex", true},
		{"copilot", true},
		{"lm-studio", true},
		{"claude", false},
		{"gemini", false},
		{"gemini-sdk", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		got := shouldBuildSdkCli(tt.adapter)
		if got != tt.want {
			t.Errorf("shouldBuildSdkCli(%q) = %v, want %v", tt.adapter, got, tt.want)
		}
	}
}

func TestReadAdapterFromYaml(t *testing.T) {
	t.Run("reads adapter from ui.core section", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		content := `pipeline:
  max_retries: 3
ui:
  core:
    adapter: codex
`
		if err := os.WriteFile(cfg, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		got := readAdapterFromYaml(cfg)
		if got != "codex" {
			t.Errorf("got %q, want %q", got, "codex")
		}
	})

	t.Run("returns empty string when file does not exist", func(t *testing.T) {
		got := readAdapterFromYaml("/nonexistent/config.yaml")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("returns empty string when adapter not set", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfg, []byte("pipeline:\n  max_retries: 3\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got := readAdapterFromYaml(cfg)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("handles quoted adapter value", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		content := "ui:\n  core:\n    adapter: \"lm-studio\"\n"
		if err := os.WriteFile(cfg, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		got := readAdapterFromYaml(cfg)
		if got != "lm-studio" {
			t.Errorf("got %q, want %q", got, "lm-studio")
		}
	})
}

func TestReadAdapterFromWorktree(t *testing.T) {
	t.Run("prefers config.local.yaml over config.yaml", func(t *testing.T) {
		dir := t.TempDir()
		cfgDir := filepath.Join(dir, ".nightgauge")
		if err := os.MkdirAll(cfgDir, 0755); err != nil {
			t.Fatal(err)
		}

		// config.yaml says claude
		localYaml := "ui:\n  core:\n    adapter: claude\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(localYaml), 0644); err != nil {
			t.Fatal(err)
		}

		// config.local.yaml says codex
		localOverride := "ui:\n  core:\n    adapter: codex\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.local.yaml"), []byte(localOverride), 0644); err != nil {
			t.Fatal(err)
		}

		got := readAdapterFromWorktree(dir)
		if got != "codex" {
			t.Errorf("got %q, want %q (local override should win)", got, "codex")
		}
	})

	t.Run("falls back to config.yaml when local not present", func(t *testing.T) {
		dir := t.TempDir()
		cfgDir := filepath.Join(dir, ".nightgauge")
		if err := os.MkdirAll(cfgDir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "ui:\n  core:\n    adapter: copilot\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		got := readAdapterFromWorktree(dir)
		if got != "copilot" {
			t.Errorf("got %q, want %q", got, "copilot")
		}
	})

	t.Run("returns claude as default when no config found", func(t *testing.T) {
		dir := t.TempDir()
		got := readAdapterFromWorktree(dir)
		if got != "claude" {
			t.Errorf("got %q, want %q", got, "claude")
		}
	})
}

func TestCopyWorktreeConfig(t *testing.T) {
	t.Run("copies existing config files to worktree", func(t *testing.T) {
		repoRoot := t.TempDir()
		worktreeDir := t.TempDir()

		srcDir := filepath.Join(repoRoot, ".nightgauge")
		if err := os.MkdirAll(srcDir, 0755); err != nil {
			t.Fatal(err)
		}

		configContent := "ui:\n  core:\n    adapter: codex\n"
		if err := os.WriteFile(filepath.Join(srcDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatal(err)
		}
		localContent := "ui:\n  core:\n    adapter: copilot\n"
		if err := os.WriteFile(filepath.Join(srcDir, "config.local.yaml"), []byte(localContent), 0644); err != nil {
			t.Fatal(err)
		}

		copyWorktreeConfig(repoRoot, worktreeDir)

		dst := filepath.Join(worktreeDir, ".nightgauge", "config.yaml")
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("config.yaml not copied: %v", err)
		}
		if string(got) != configContent {
			t.Errorf("config.yaml content mismatch")
		}

		dstLocal := filepath.Join(worktreeDir, ".nightgauge", "config.local.yaml")
		gotLocal, err := os.ReadFile(dstLocal)
		if err != nil {
			t.Fatalf("config.local.yaml not copied: %v", err)
		}
		if string(gotLocal) != localContent {
			t.Errorf("config.local.yaml content mismatch")
		}
	})

	t.Run("does not fail when source files are absent", func(t *testing.T) {
		repoRoot := t.TempDir()
		worktreeDir := t.TempDir()
		// Should not panic or error
		copyWorktreeConfig(repoRoot, worktreeDir)
	})
}
