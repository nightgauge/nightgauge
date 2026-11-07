package adapters

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/opencodeallow"
)

// TestIncludeCacheIsGrantedOnceARenderWroteCopies2789: the render's rewritten
// include copies live in the user cache dir, outside every other granted
// root, so the Claude adapter must add it (read) and deny edits to it, and
// the OpenCode allow-list must list it. With no copies written, nothing is
// granted.
func TestIncludeCacheIsGrantedOnceARenderWroteCopies2789(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	worktree := t.TempDir()
	opts := RunOptions{WorktreeDir: worktree}

	if got := claudeSkillsDirArgs(opts); got != nil {
		t.Fatalf("no copies written: args = %v, want none", got)
	}
	if slices.Contains(opencodeallow.Roots(openCodeAllowOptions(opts)), opencodeallow.IncludeCacheRoot()) {
		t.Fatal("no copies written: OpenCode roots list the include cache")
	}

	cache := opencodeallow.IncludeCacheRoot()
	if cache == "" {
		t.Fatal("IncludeCacheRoot is empty with HOME set")
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	got := claudeSkillsDirArgs(opts)
	if len(got) < 4 || got[0] != "--add-dir" || got[1] != cache {
		t.Fatalf("args = %v, want --add-dir %s", got, cache)
	}
	if !strings.Contains(got[len(got)-1], "Edit(//"+strings.TrimLeft(filepath.ToSlash(cache), "/")+"/**)") {
		t.Errorf("--disallowedTools = %s, want an Edit deny on %s", got[len(got)-1], cache)
	}
	if !slices.Contains(opencodeallow.Roots(openCodeAllowOptions(opts)), cache) {
		t.Errorf("OpenCode roots omit the include cache %s", cache)
	}
	edit := openCodePermissionMap(opts, "").Edit
	denied := false
	for _, p := range openCodeWorktreeRelativeDirPatterns(worktree, cache) {
		if edit.values[p] == openCodeDeny {
			denied = true
		}
	}
	if !denied {
		t.Errorf("OpenCode edit map does not deny the include cache %s", cache)
	}
}

// noIncludeCache gives a test a HOME with no include cache, so an exact
// --add-dir assertion does not depend on whether another test in the
// package rendered copies into the shared fake HOME.
func noIncludeCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// TestOpenCodeHookSeesTheDispatchersIncludeCache2789: the OpenCode hook runs
// under the child's isolated XDG_CACHE_HOME, so it must take the cache root
// from the env the adapter exported, not recompute it.
func TestOpenCodeHookSeesTheDispatchersIncludeCache2789(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "skill-includes")
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the child's isolated cache
	t.Setenv(opencodeallow.EnvIncludeCacheDir, cache)
	if !slices.Contains(opencodeallow.RootsFromEnv(t.TempDir()), cache) {
		t.Errorf("RootsFromEnv omits the exported include cache %s", cache)
	}
	t.Setenv(opencodeallow.EnvIncludeCacheDir, "relative/cache")
	for _, r := range opencodeallow.RootsFromEnv(t.TempDir()) {
		if strings.Contains(r, "relative/cache") {
			t.Errorf("a relative exported cache dir widened the roots: %s", r)
		}
	}
}
