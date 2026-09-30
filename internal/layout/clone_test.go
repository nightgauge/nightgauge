package layout

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

var cloneResolvers = []struct {
	class string
	fn    func(string) (string, error)
}{
	{ClassPipeline, PipelineStateDir},
	{ClassPlans, PlansDir},
	{ClassRetros, RetrosDir},
	{ClassLogs, CloneLogsDir},
}

// evalRoot is root with its symlinks evaluated (macOS /var -> /private/var),
// the spelling GitCommonDir returns.
func evalRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestCloneLayoutResolvers pins every per-clone class to CLONE/<class>, where
// CLONE is <git-common-dir>/nightgauge (ADR-024 § 7): <repo>/.git/nightgauge
// from the main checkout and from a linked worktree alike.
func TestCloneLayoutResolvers(t *testing.T) {
	root := gitInit(t)
	gittest.Run(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, root, "worktree", "add", "-q", worktree)
	sub := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(evalRoot(t, root), ".git", "nightgauge")

	for _, r := range cloneResolvers {
		t.Run(r.class, func(t *testing.T) {
			want := filepath.Join(clone, r.class)
			for _, from := range []string{root, root + string(filepath.Separator) + ".", worktree, sub} {
				got, err := r.fn(from)
				if err != nil {
					t.Fatalf("%s(%q): unexpected error: %v", r.class, from, err)
				}
				if got != want {
					t.Errorf("%s(%q) = %q, want %q", r.class, from, got, want)
				}
			}
			if got, err := ClassDir(root, r.class); err != nil || got != want {
				t.Errorf("ClassDir(%q) = %q, %v; want %q", r.class, got, err, want)
			}

			for _, bad := range []string{"", ".", "relative/repo", filepath.Join("..", "repo")} {
				dir, err := r.fn(bad)
				if !errors.Is(err, ErrRootNotAbsolute) {
					t.Errorf("%s(%q): err = %v, want ErrRootNotAbsolute", r.class, bad, err)
				}
				if dir != "" {
					t.Errorf("%s(%q) = %q on error, want empty", r.class, bad, dir)
				}
			}
		})
	}
	if _, err := ClassDir(root, "worktrees"); err == nil {
		t.Error("ClassDir(unknown class) = nil error")
	}
}

// TestCloneLayoutOutsideGit: outside a git repository every resolver fails
// with ErrNotGitRepository and nothing is written anywhere.
func TestCloneLayoutOutsideGit(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	dir := t.TempDir()
	for _, r := range cloneResolvers {
		got, err := r.fn(dir)
		if !errors.Is(err, ErrNotGitRepository) {
			t.Fatalf("%s(non-repo) = %q, %v; want ErrNotGitRepository", r.class, got, err)
		}
		if !strings.Contains(err.Error(), "not a git repository") || got != "" {
			t.Errorf("%s(non-repo): got %q, err %q", r.class, got, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a failed resolution wrote into %s: %v", dir, entries)
	}
}

// TestCloneLayoutIgnoresInheritedGitEnv: a GIT_DIR exported by a hook names
// another repository and must not redirect CLONE.
func TestCloneLayoutIgnoresInheritedGitEnv(t *testing.T) {
	root := gitInit(t)
	other := gitInit(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	got, err := CloneDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(evalRoot(t, root), ".git", "nightgauge"); got != want {
		t.Errorf("CloneDir = %q, want %q", got, want)
	}
}

// TestCloneDirSecurity: CLONE is created 0700, never inside the working tree,
// and a symlinked CLONE or class directory is refused (ADR-024 § 17).
func TestCloneDirSecurity(t *testing.T) {
	root := gitInit(t)
	dir, err := CloneDir(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("CLONE not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("CLONE mode = %#o, want 0700", info.Mode().Perm())
	}
	if out := gittest.Run(t, root, "status", "--porcelain", "--ignored"); out != "" {
		t.Errorf("git status after resolving CLONE = %q, want clean", out)
	}

	// A class directory that is a symlink out of CLONE is refused.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, ClassPlans)); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if got, err := PlansDir(root); !errors.Is(err, ErrUnsafeCloneDir) {
		t.Errorf("PlansDir(symlinked class) = %q, %v; want ErrUnsafeCloneDir", got, err)
	}

	// A CLONE root that is a symlink is refused.
	root2 := gitInit(t)
	link := filepath.Join(root2, ".git", "nightgauge")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if got, err := PipelineStateDir(root2); !errors.Is(err, ErrUnsafeCloneDir) {
		t.Errorf("PipelineStateDir(symlinked CLONE) = %q, %v; want ErrUnsafeCloneDir", got, err)
	}
}

// TestCloneDirGroupShared: in a group-shared clone CLONE follows git's group
// mode (ADR-024 § 7).
func TestCloneDirGroupShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX group modes")
	}
	root := gittest.InitRepo(t, t.TempDir(), "-q", "--shared=group")
	dir, err := CloneDir(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o070 != 0o070 || perm&0o007 != 0 {
		t.Errorf("group-shared CLONE mode = %#o, want group rwx and no other bits", perm)
	}
}

// TestCloneDirConcurrent: concurrent first resolutions agree and do not race.
func TestCloneDirConcurrent(t *testing.T) {
	root := gitInit(t)
	var wg sync.WaitGroup
	results := make([]string, 16)
	errs := make([]error, 16)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = PipelineStateDir(root)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatalf("resolution %d = %q, %v; want %q", i, results[i], errs[i], results[0])
		}
	}
	if _, err := os.Stat(filepath.Dir(results[0])); errors.Is(err, fs.ErrNotExist) {
		t.Fatal("CLONE not created")
	}
}

// TestCloneLayoutDisplay pins each display form to its resolver's output from
// the main checkout, so help text that names a class directory cannot drift
// from where the class is.
func TestCloneLayoutDisplay(t *testing.T) {
	root := evalRoot(t, gitInit(t))
	for _, d := range []struct {
		class   string
		resolve func(string) (string, error)
		display string
	}{
		{"pipeline", PipelineStateDir, PipelineStateDisplay()},
		{"logs", CloneLogsDir, CloneLogsDisplay()},
	} {
		dir, err := d.resolve(root)
		if err != nil {
			t.Fatalf("%s: %v", d.class, err)
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("%s: %v", d.class, err)
		}
		if want := filepath.ToSlash(rel); d.display != want {
			t.Errorf("%s display = %q, want %q (the resolver's path relative to the root)", d.class, d.display, want)
		}
	}
}
