package clonelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// TestForPathTakesTheCloneLock: an append under CLONE holds CLONE/.lock, so a
// second holder waits; outside CLONE nothing is locked.
func TestForPathTakesTheCloneLock(t *testing.T) {
	root := layouttest.Repo(t)
	clone, err := layout.CloneDir(root)
	if err != nil {
		t.Fatal(err)
	}
	release := ForPath(filepath.Join(clone, "pipeline", "history", "x.jsonl"))
	f, err := os.OpenFile(filepath.Join(clone, layout.CloneLockName), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("CLONE/.lock not created: %v", err)
	}
	defer f.Close()
	switch err := flock.Exclusive(f, 0); err {
	case flock.ErrUnsupported:
		release()
		t.Skip("advisory locks unavailable")
	case flock.ErrWouldBlock:
	default:
		t.Errorf("CLONE/.lock was free while ForPath held it (err %v)", err)
	}
	release()
	if err := flock.Exclusive(f, time.Second); err != nil {
		t.Errorf("CLONE/.lock still held after release: %v", err)
	}
	_ = flock.Unlock(f)

	// Outside a per-clone root: a no-op, and no lock file anywhere.
	dir := t.TempDir()
	ForPath(filepath.Join(dir, "x.jsonl"))()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("ForPath outside CLONE created %v", entries)
	}
}
