package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// optIn writes the committed project config that opts root in (#2765).
func optIn(t *testing.T, root string) {
	t.Helper()
	path := config.ProjectConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gittestRepo returns a fresh git repository with no project config.
func gittestRepo(t *testing.T) string {
	t.Helper()
	return gittest.InitRepo(t, t.TempDir(), "-q")
}

func TestRepoOptedIn(t *testing.T) {
	t.Run("repository without project config is not opted in", func(t *testing.T) {
		repo := gittest.InitRepo(t, t.TempDir(), "-q")
		if RepoOptedIn(repo) {
			t.Fatal("a repository with no .nightgauge/config.yaml must not be opted in")
		}
	})

	t.Run("project config at the toplevel opts in from a subdirectory", func(t *testing.T) {
		repo := gittest.InitRepo(t, t.TempDir(), "-q")
		optIn(t, repo)
		sub := filepath.Join(repo, "pkg", "deep")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if !RepoOptedIn(sub) {
			t.Fatal("a subdirectory of an opted-in repository must be opted in")
		}
	})

	t.Run("local-tier config alone does not opt in", func(t *testing.T) {
		repo := gittest.InitRepo(t, t.TempDir(), "-q")
		local := config.LocalConfigPath(repo)
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if RepoOptedIn(repo) {
			t.Fatal("config.local.yaml alone must not opt a repository in")
		}
	})

	t.Run("a directory at the config path does not opt in", func(t *testing.T) {
		repo := gittest.InitRepo(t, t.TempDir(), "-q")
		if err := os.MkdirAll(config.ProjectConfigPath(repo), 0o755); err != nil {
			t.Fatal(err)
		}
		if RepoOptedIn(repo) {
			t.Fatal("only a regular file opts a repository in")
		}
	})

	t.Run("linked worktree resolves the canonical checkout", func(t *testing.T) {
		repo := gittest.InitRepo(t, t.TempDir(), "-q")
		gittest.Run(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-qm", "init")
		wt := filepath.Join(t.TempDir(), "wt")
		gittest.Run(t, repo, "worktree", "add", "-q", wt, "HEAD")
		if RepoOptedIn(wt) {
			t.Fatal("premise: neither checkout has a project config yet")
		}
		// The config exists only in the canonical checkout (uncommitted there),
		// so the worktree is opted in through the git common dir.
		optIn(t, repo)
		if !RepoOptedIn(wt) {
			t.Fatal("a worktree of an opted-in canonical checkout must be opted in")
		}
	})

	t.Run("non-repository directory uses itself", func(t *testing.T) {
		dir := t.TempDir()
		if RepoOptedIn(dir) {
			t.Fatal("an empty directory must not be opted in")
		}
		optIn(t, dir)
		if !RepoOptedIn(dir) {
			t.Fatal("outside git, the directory's own project config opts it in")
		}
	})
}
