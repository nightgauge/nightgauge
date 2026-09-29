package recall_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/knowledge/recall"
)

// committedFixture scaffolds the knowledge fixtures into a fresh git
// repository and commits them, so `git status --porcelain` is empty unless
// something writes into the working tree afterwards.
func committedFixture(t *testing.T) string {
	t.Helper()
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)
	gittest.InitRepo(t, root, "-q")
	gittest.Run(t, root, "add", "-A")
	gittest.Run(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "fixture")
	return root
}

func assertCleanTree(t *testing.T, root string) {
	t.Helper()
	if out := gittest.Run(t, root, "status", "--porcelain", "--untracked-files=all", "--ignored"); out != "" {
		t.Fatalf("building the index wrote into the working tree:\n%s", out)
	}
}

func assertHits(t *testing.T, idx *recall.Index) {
	t.Helper()
	res, err := recall.Query(idx, "cache", 10, nil)
	if err != nil || res.TotalHits == 0 {
		t.Fatalf("index serves no hits (%d, %v)", res.TotalHits, err)
	}
}

// #2028: the recall index is written under <cache home>/recall/<root-key>/,
// never into the working tree.
func TestCacheDirUnderCacheHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NIGHTGAUGE_CACHE_HOME", home)
	root := committedFixture(t)

	idx, err := recall.BuildIndex(root, nil, &config.KnowledgeConfig{})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	assertHits(t, idx)

	want := filepath.Join(home, "recall", recall.RootKey(root), "index.jsonl")
	got, err := recall.CachePath(root)
	if err != nil || got != want {
		t.Fatalf("CachePath = %q, %v; want %q", got, err, want)
	}
	if info, err := os.Lstat(want); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("no cache file at %s: %v", want, err)
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{filepath.Join(home, "recall"), filepath.Dir(want)} {
			info, err := os.Stat(d)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("%s mode = %v, %v; want 0700", d, info.Mode().Perm(), err)
			}
		}
	}
	assertCleanTree(t, root)

	// --update-cache deletes it; the next build writes it again, still outside
	// the tree.
	if err := recall.InvalidateCache(root); err != nil {
		t.Fatalf("InvalidateCache: %v", err)
	}
	if _, err := os.Lstat(want); !os.IsNotExist(err) {
		t.Fatalf("cache survived InvalidateCache: %v", err)
	}
	if _, err := recall.BuildIndex(root, nil, &config.KnowledgeConfig{}); err != nil {
		t.Fatalf("BuildIndex (rebuild): %v", err)
	}
	if _, err := os.Lstat(want); err != nil {
		t.Fatalf("rebuild wrote no cache: %v", err)
	}
	assertCleanTree(t, root)
}

// The key is per checkout: two roots never share a cache, and one root spelled
// through a symlink resolves to the same key.
func TestRootKeyIsCanonicalAndDistinct(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if recall.RootKey(a) == recall.RootKey(b) {
		t.Fatal("two repositories share a recall cache key")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if recall.RootKey(link) != recall.RootKey(a) {
		t.Fatal("a symlinked spelling of the root changed its key")
	}
}

// With no usable cache home the index is built in memory for the call and
// nothing is written anywhere in the working tree.
func TestNoCacheHomeBuildsInMemory(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CACHE_HOME", "relative/cache")
	root := committedFixture(t)

	idx, err := recall.BuildIndex(root, nil, &config.KnowledgeConfig{})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	assertHits(t, idx)
	if _, err := recall.CachePath(root); err == nil {
		t.Fatal("a relative NIGHTGAUGE_CACHE_HOME resolved to a cache path")
	}
	assertCleanTree(t, root)
}

// The writer never follows a symlink out of the cache home: a planted
// recall -> elsewhere link makes the cache unusable (memory only), and
// nothing lands at the link's target.
func TestCacheRefusesSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "recall")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("NIGHTGAUGE_CACHE_HOME", home)
	root := committedFixture(t)

	idx, err := recall.BuildIndex(root, nil, &config.KnowledgeConfig{})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	assertHits(t, idx)
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("the cache writer followed the symlink out of the cache home: %v", entries)
	}
	assertCleanTree(t, root)
}

// A symlink planted at the cache FILE's path is replaced, not written through.
func TestCacheFileSymlinkIsReplacedNotFollowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NIGHTGAUGE_CACHE_HOME", home)
	root := committedFixture(t)
	path, err := recall.CachePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := recall.BuildIndex(root, nil, &config.KnowledgeConfig{}); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("the cache writer wrote through a symlink: victim now %q", b)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("cache path is not a regular file after the build: %v", err)
	}
}
