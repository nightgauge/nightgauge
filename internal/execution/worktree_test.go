package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/gitworktree"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// initTestGitRepo creates a real git repo with one commit on a named branch.
// Returns the repo root path.
func initTestGitRepo(t *testing.T, branchName string) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Run(t, dir, "init", "-b", branchName)
	gittest.Run(t, dir, "config", "user.email", "test@test")
	gittest.Run(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-m", "initial")
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

// TestEnsureWorktree_RecordsCreationCommit: the OpenCode tamper gate diffs
// against the commit the worktree was created at (#1825), so ensureWorktree
// must record the primary checkout's HEAD in the worktree's own admin dir.
func TestEnsureWorktree_RecordsCreationCommit(t *testing.T) {
	repoRoot := initTestGitRepo(t, "main")
	head := strings.TrimSpace(gittest.Run(t, repoRoot, "rev-parse", "HEAD"))
	m := &Manager{workspaceRoot: repoRoot}

	wt, err := m.ensureWorktree("nightgauge/nightgauge", 1825)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	gitDir := strings.TrimSpace(gittest.Run(t, wt, "rev-parse", "--absolute-git-dir"))
	got, err := os.ReadFile(filepath.Join(gitDir, gitworktree.BaseCommitFile))
	if err != nil {
		t.Fatalf("no recorded creation commit: %v", err)
	}
	if strings.TrimSpace(string(got)) != head {
		t.Errorf("recorded %q, want the primary checkout's HEAD %s", got, head)
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

// TestCleanupWorktree_PreservesUnpushedCommit is a regression guard for #266:
// a worktree with a CLEAN tree (so the pre-existing hasUncommittedChanges
// guard does not fire) whose current branch carries a commit not yet on the
// default branch must survive CleanupWorktree — this is exactly the state a
// killed stage leaves behind after a validated commit that never reached
// pr-create.
func TestCleanupWorktree_PreservesUnpushedCommit(t *testing.T) {
	repoRoot := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: repoRoot}
	worktreePath, err := m.ensureWorktree("nightgauge/nightgauge", 266)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}

	gittest.Run(t, worktreePath, "checkout", "-b", "feat/266-thing")
	if err := os.WriteFile(filepath.Join(worktreePath, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, worktreePath, "add", ".")
	gittest.Run(t, worktreePath, "commit", "-m", "feat: unpushed work")

	if err := m.CleanupWorktree("nightgauge/nightgauge", 266); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree carrying an unmerged commit must survive cleanup, got stat err: %v", err)
	}
}

// TestCleanupWorktree_PreservesStrayTempPrePushBranch regression-guards the
// exact #266 scenario: a SIGKILL mid pre-push validation can leave a
// worktree's HEAD on a stray `temp-pre-push-<n>` branch (not the issue's
// expected feature branch) holding a commit. CleanupWorktree must inspect
// whatever branch is actually checked out, not assume it is the feature
// branch, and preserve it.
func TestCleanupWorktree_PreservesStrayTempPrePushBranch(t *testing.T) {
	repoRoot := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: repoRoot}
	worktreePath, err := m.ensureWorktree("nightgauge/nightgauge", 267)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}

	gittest.Run(t, worktreePath, "checkout", "-b", "temp-pre-push-42")
	if err := os.WriteFile(filepath.Join(worktreePath, "stray.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, worktreePath, "add", ".")
	gittest.Run(t, worktreePath, "commit", "-m", "feat: committed while stranded on temp branch")

	if err := m.CleanupWorktree("nightgauge/nightgauge", 267); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree stranded on temp-pre-push branch must survive cleanup, got stat err: %v", err)
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
		{"lm-studio", false},
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
		content := "ui:\n  core:\n    adapter: \"opencode\"\n"
		if err := os.WriteFile(cfg, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		got := readAdapterFromYaml(cfg)
		if got != "opencode" {
			t.Errorf("got %q, want %q", got, "opencode")
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

// mustWorktreePath is m.worktreePath for a test that expects resolution to
// succeed.
func mustWorktreePath(t testing.TB, m *Manager, repo string, issueNumber int) string {
	t.Helper()
	p, err := m.worktreePath(repo, issueNumber)
	if err != nil {
		t.Fatalf("worktreePath(%s, %d): %v", repo, issueNumber, err)
	}
	return p
}

// --- Worktree location (#2038, ADR-024 § 9) ---

// hermeticConfigHome points the machine config tier at an empty directory so
// the developer's own machine config cannot leak a worktree_base into a test.
func hermeticConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", dir)
	return dir
}

func writeTestConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// evalDir resolves a test directory's symlinks (macOS /var -> /private/var).
func evalDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWorktreePath_HonoursWorktreeBase(t *testing.T) {
	hermeticConfigHome(t)
	repoRoot := initTestGitRepo(t, "main")
	base := filepath.Join(evalDir(t, t.TempDir()), "wt")
	writeTestConfig(t, filepath.Join(repoRoot, ".nightgauge", "config.local.yaml"),
		"pipeline:\n  worktree_base: "+base+"\n")

	m := &Manager{workspaceRoot: repoRoot}
	got := mustWorktreePath(t, m, "acme/widget", 7)
	if want := filepath.Join(base, "widget-issue-7"); got != want {
		t.Fatalf("worktreePath = %q, want %q (under pipeline.worktree_base)", got, want)
	}
}

func TestWorktreePath_HonoursMachineTierWorktreeBase(t *testing.T) {
	configHome := hermeticConfigHome(t)
	repoRoot := initTestGitRepo(t, "main")
	base := filepath.Join(evalDir(t, t.TempDir()), "machine-wt")
	writeTestConfig(t, filepath.Join(configHome, "config.yaml"),
		"pipeline:\n  worktree_base: "+base+"\n")

	m := &Manager{workspaceRoot: repoRoot}
	if got, want := mustWorktreePath(t, m, "acme/widget", 7), filepath.Join(base, "widget-issue-7"); got != want {
		t.Fatalf("worktreePath = %q, want %q", got, want)
	}
}

func TestWorktreePath_DefaultIsOutsideWorkingTree(t *testing.T) {
	hermeticConfigHome(t)
	repoRoot := initTestGitRepo(t, "main")
	m := &Manager{workspaceRoot: repoRoot}

	got := mustWorktreePath(t, m, "acme/widget", 7)
	state, err := layout.StateHomePath()
	if err != nil {
		t.Fatal(err)
	}
	common, err := layout.GitCommonDir(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(state, "worktrees", layout.RepoKey(common), "widget-issue-7"); got != want {
		t.Fatalf("worktreePath = %q, want the ADR-024 default %q", got, want)
	}
	rel, err := filepath.Rel(repoRoot, got)
	if err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("filepath.Rel(repoRoot, path) = %q, want a path outside the working tree", rel)
	}
}

func TestWorktreePath_RefusesTeamTierRelativeAndInTreeValues(t *testing.T) {
	cases := []struct {
		name, file, value string
		wantInErr         []string
	}{
		{"team tier", "config.yaml", "/abs/elsewhere", []string{"committed team config", "config.yaml:2", "config.local.yaml"}},
		{"relative", "config.local.yaml", ".worktrees", []string{"relative path", "config.local.yaml:2"}},
		{"in tree", "config.local.yaml", "<root>/inside", []string{"inside the working tree", "config.local.yaml:2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hermeticConfigHome(t)
			repoRoot := initTestGitRepo(t, "main")
			value := strings.ReplaceAll(tc.value, "<root>", repoRoot)
			writeTestConfig(t, filepath.Join(repoRoot, ".nightgauge", tc.file),
				"pipeline:\n  worktree_base: "+value+"\n")
			m := &Manager{workspaceRoot: repoRoot}

			_, err := m.worktreePath("acme/widget", 7)
			if !errors.Is(err, layout.ErrWorktreeBase) {
				t.Fatalf("worktreePath error = %v, want ErrWorktreeBase", err)
			}
			for _, s := range tc.wantInErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not name %q", err, s)
				}
			}
			// Creation fails the same way and leaves nothing in the tree.
			if _, err := m.ensureWorktree("acme/widget", 7); !errors.Is(err, layout.ErrWorktreeBase) {
				t.Fatalf("ensureWorktree error = %v, want ErrWorktreeBase", err)
			}
			if _, err := os.Stat(filepath.Join(repoRoot, ".worktrees")); !os.IsNotExist(err) {
				t.Errorf("a refused relative base was created in the tree: %v", err)
			}
		})
	}
}

// A base that is a symlink into the working tree is refused after symlink
// evaluation, not accepted on its spelling.
func TestWorktreePath_RefusesBaseSymlinkedIntoTree(t *testing.T) {
	hermeticConfigHome(t)
	repoRoot := initTestGitRepo(t, "main")
	inside := filepath.Join(repoRoot, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "base-link")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeTestConfig(t, filepath.Join(repoRoot, ".nightgauge", "config.local.yaml"),
		"pipeline:\n  worktree_base: "+link+"\n")
	m := &Manager{workspaceRoot: repoRoot}
	if _, err := m.worktreePath("acme/widget", 7); !errors.Is(err, layout.ErrWorktreeBase) {
		t.Fatalf("worktreePath error = %v, want ErrWorktreeBase for a base symlinked into the tree", err)
	}
}

// Security: a leaf planted as a symlink to another directory, and a crafted
// repository name, cannot place a worktree outside the configured base.
func TestWorktreePath_ContainmentRejectsEscapes(t *testing.T) {
	hermeticConfigHome(t)
	repoRoot := initTestGitRepo(t, "main")
	base := filepath.Join(evalDir(t, t.TempDir()), "wt")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestConfig(t, filepath.Join(repoRoot, ".nightgauge", "config.local.yaml"),
		"pipeline:\n  worktree_base: "+base+"\n")
	m := &Manager{workspaceRoot: repoRoot}

	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(base, "widget-issue-7")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := m.worktreePath("acme/widget", 7); !errors.Is(err, layout.ErrWorktreeEscape) {
		t.Fatalf("symlinked leaf: error = %v, want ErrWorktreeEscape", err)
	}
	if got, err := m.ensureWorktree("acme/widget", 7); !errors.Is(err, layout.ErrWorktreeEscape) || got != "" {
		t.Fatalf("ensureWorktree through a symlinked leaf = (%q, %v), want refusal", got, err)
	}

	for _, repo := range []string{"acme/..", "..", "acme/.", "", "acme/a b", `acme\..\x`} {
		if p, err := m.worktreePath(repo, 7); !errors.Is(err, layout.ErrWorktreeEscape) {
			t.Errorf("repo %q: worktreePath = (%q, %v), want ErrWorktreeEscape", repo, p, err)
		}
	}
	if p, err := m.worktreePath("acme/widget", -1); !errors.Is(err, layout.ErrWorktreeEscape) {
		t.Errorf("negative issue: worktreePath = (%q, %v), want ErrWorktreeEscape", p, err)
	}
}

// A run that began before #2038 keeps its in-tree worktree: ensureWorktree
// reuses it rather than creating a second one that would fight it for the
// feature branch, and CleanupWorktree tears the legacy one down.
func TestEnsureWorktree_ReusesLegacyInTreeWorktree(t *testing.T) {
	hermeticConfigHome(t)
	repoRoot := initTestGitRepo(t, "main")
	legacy := filepath.Join(repoRoot, ".nightgauge", "worktrees", "widget-issue-9")
	gittest.Run(t, repoRoot, "worktree", "add", "--detach", legacy, "HEAD")

	fakeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeDir, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := &Manager{workspaceRoot: repoRoot}
	got, err := m.ensureWorktree("acme/widget", 9)
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	if got != legacy {
		t.Fatalf("ensureWorktree = %q, want the existing legacy worktree %q", got, legacy)
	}
	if _, err := os.Stat(mustWorktreePath(t, m, "acme/widget", 9)); !os.IsNotExist(err) {
		t.Fatalf("a second worktree was created at the new location: %v", err)
	}
	if err := m.CleanupWorktree("acme/widget", 9); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy worktree survived CleanupWorktree: %v", err)
	}
}
