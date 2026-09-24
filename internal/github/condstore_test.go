package github

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConditionalStoreDir_HonoursTheOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cacheHomeEnv, root)
	dir, err := ConditionalStoreDir()
	if err != nil || dir != filepath.Join(root, "github-conditional") {
		t.Fatalf("dir = %q, %v", dir, err)
	}
}

// The store holds forge answers read with a private token; it must never land
// inside the repository a daemon happens to run from.
func TestConditionalStoreDir_IsNeverInsideTheRepository(t *testing.T) {
	t.Setenv(cacheHomeEnv, "")
	dir, err := ConditionalStoreDir()
	if err != nil {
		t.Skipf("no user cache directory on this machine: %v", err)
	}
	_, self, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(self), "..", ".."))
	if strings.HasPrefix(filepath.Clean(dir)+string(filepath.Separator), repoRoot+string(filepath.Separator)) {
		t.Fatalf("store dir %q is inside the repository %q", dir, repoRoot)
	}
	if base, _ := os.UserCacheDir(); !strings.HasPrefix(dir, base) {
		t.Fatalf("store dir %q is not under the user cache directory %q", dir, base)
	}
}

func TestConditionalStore_TightensAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewConditionalStore(dir)
	s.put("tok:a", "https://api.github.com/x", condEntry{ETag: `"e"`, Payload: []byte(`1`)})
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, %v; want 0700", info.Mode().Perm(), err)
	}
	var fileMode os.FileMode
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			fi, _ := d.Info()
			fileMode = fi.Mode().Perm()
		}
		return nil
	})
	if fileMode != 0o600 {
		t.Fatalf("entry mode = %v, want 0600", fileMode)
	}
}

// Past the size cap the OLDEST entries go first.
func TestConditionalStore_PruneEvictsOldestPastTheCap(t *testing.T) {
	s := NewConditionalStore(t.TempDir())
	now := time.Now()
	var paths []string
	for i, url := range []string{"u/old", "u/mid", "u/new"} {
		s.put("tok:a", url, condEntry{ETag: `"e"`, Payload: []byte(`"` + strings.Repeat("x", 100) + `"`)})
		p := s.path(condKey("tok:a", url))
		_ = os.Chtimes(p, now.Add(time.Duration(i-3)*time.Hour), now.Add(time.Duration(i-3)*time.Hour))
		paths = append(paths, p)
	}
	info, _ := os.Stat(paths[0])
	s.Prune(24*time.Hour, 2*info.Size())
	for i, want := range []bool{false, true, true} {
		if _, err := os.Stat(paths[i]); (err == nil) != want {
			t.Fatalf("entry %d present=%v, want %v", i, err == nil, want)
		}
	}
	s.Prune(time.Minute, 0) // everything is older than a minute now
	for _, p := range paths[1:] {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s survived maxAge", p)
		}
	}
}

func TestConditionalStore_MaintenanceStopsWithItsContext(t *testing.T) {
	s := NewConditionalStore(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunMaintenance(ctx, time.Hour, 0, time.Millisecond); close(done) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop when its context ended")
	}
}
