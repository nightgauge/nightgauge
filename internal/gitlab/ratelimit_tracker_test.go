package gitlab

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSet_roundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit-gitlab-test.json")
	tr := NewSharedRateLimitTracker(path)

	info := &RateLimitInfo{Remaining: 3000, Limit: 5000, ResetAt: time.Now().Add(30 * time.Minute).Unix()}
	if err := tr.Set("gitlab.com", info); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entry, fresh, err := tr.Get("gitlab.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil {
		t.Fatalf("expected entry, got nil")
	}
	if !fresh {
		t.Fatalf("just-written entry should be fresh")
	}
	if entry.Remaining != 3000 || entry.Limit != 5000 {
		t.Fatalf("values not persisted: got %+v", entry)
	}
	if entry.CheckedAt == 0 {
		t.Fatalf("CheckedAt should be set by Set()")
	}
}

func TestGet_missingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	tr := NewSharedRateLimitTracker(path)

	entry, fresh, err := tr.Get("gitlab.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry != nil || fresh {
		t.Fatalf("expected (nil,false) for missing file, got (%v,%v)", entry, fresh)
	}
}

func TestGet_corruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not valid json!!"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	tr := NewSharedRateLimitTracker(path)

	entry, fresh, err := tr.Get("gitlab.com")
	if err != nil {
		t.Fatalf("Get on corrupt: %v", err)
	}
	if entry != nil || fresh {
		t.Fatalf("expected (nil,false) for corrupt file")
	}

	// Recovery: writing a new entry after corrupt file should work.
	info := &RateLimitInfo{Remaining: 50, Limit: 5000, ResetAt: time.Now().Add(1 * time.Hour).Unix()}
	if err := tr.Set("gitlab.com", info); err != nil {
		t.Fatalf("Set after corrupt: %v", err)
	}
	entry, fresh, err = tr.Get("gitlab.com")
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if entry == nil || !fresh || entry.Remaining != 50 {
		t.Fatalf("recovery failed: %+v fresh=%v", entry, fresh)
	}
}

func TestGet_freshnessWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit-gitlab-test.json")
	tr := NewSharedRateLimitTracker(path)

	info := &RateLimitInfo{Remaining: 100, Limit: 5000, ResetAt: time.Now().Add(30 * time.Minute).Unix()}
	if err := tr.Set("gitlab.com", info); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Just written — should be fresh.
	entry, fresh, err := tr.Get("gitlab.com")
	if err != nil || entry == nil || !fresh {
		t.Fatalf("expected fresh entry: entry=%v fresh=%v err=%v", entry, fresh, err)
	}

	// Backdate CheckedAt to make it stale.
	file, err := tr.readLocked()
	if err != nil {
		t.Fatalf("readLocked: %v", err)
	}
	file.Entries[keyForInstance("gitlab.com")].CheckedAt = time.Now().Unix() - int64(SharedTrackerMinCheckIntervalSecs) - 10
	if err := tr.writeLocked(file); err != nil {
		t.Fatalf("writeLocked: %v", err)
	}

	entry, fresh, err = tr.Get("gitlab.com")
	if err != nil {
		t.Fatalf("Get stale: %v", err)
	}
	if entry == nil {
		t.Fatalf("entry should still be returned when stale")
	}
	if fresh {
		t.Fatalf("stale entry should not be fresh")
	}
}

func TestSetFromHeaders_allOrNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit-gitlab-test.json")
	tr := NewSharedRateLimitTracker(path)

	// Partial headers — no-op.
	ok, err := tr.SetFromHeaders("gitlab.com", "100", "", "")
	if err != nil || ok {
		t.Fatalf("partial headers should be no-op: ok=%v err=%v", ok, err)
	}

	// All headers present.
	resetAt := time.Now().Add(30 * time.Minute).Unix()
	ok, err = tr.SetFromHeaders("gitlab.com", "200", "5000", itoa(resetAt))
	if err != nil {
		t.Fatalf("SetFromHeaders: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}

	entry, fresh, err := tr.Get("gitlab.com")
	if err != nil || entry == nil || !fresh {
		t.Fatalf("expected fresh entry after SetFromHeaders: %v %v %v", entry, fresh, err)
	}
	if entry.Remaining != 200 || entry.Limit != 5000 {
		t.Fatalf("unexpected values: %+v", entry)
	}
}

func TestSetFromHeaders_outOfOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit-gitlab-test.json")
	tr := NewSharedRateLimitTracker(path)

	resetAt := time.Now().Add(30 * time.Minute).Unix()

	// Write initial entry with remaining=300.
	ok, err := tr.SetFromHeaders("gitlab.com", "300", "5000", itoa(resetAt))
	if err != nil || !ok {
		t.Fatalf("first SetFromHeaders: ok=%v err=%v", ok, err)
	}

	// Manually backdate the CheckedAt to simulate a future-dated first write.
	file, err := tr.readLocked()
	if err != nil {
		t.Fatalf("readLocked: %v", err)
	}
	file.Entries[keyForInstance("gitlab.com")].CheckedAt = time.Now().Unix() + 1000
	if err := tr.writeLocked(file); err != nil {
		t.Fatalf("writeLocked: %v", err)
	}

	// Second write should be no-op (out-of-order guard).
	ok, err = tr.SetFromHeaders("gitlab.com", "100", "5000", itoa(resetAt))
	if err != nil {
		t.Fatalf("second SetFromHeaders: %v", err)
	}
	if ok {
		t.Fatalf("out-of-order write should be rejected (ok should be false)")
	}

	entry, _, err := tr.Get("gitlab.com")
	if err != nil || entry == nil {
		t.Fatalf("Get: %v", err)
	}
	// Original 300 should still be there.
	if entry.Remaining != 300 {
		t.Fatalf("out-of-order write should not overwrite: got remaining=%d", entry.Remaining)
	}
}

func TestSet_concurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit-gitlab-test.json")
	tr := NewSharedRateLimitTracker(path)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			info := &RateLimitInfo{Remaining: n * 100, Limit: 5000, ResetAt: time.Now().Add(1 * time.Hour).Unix()}
			if err := tr.Set("gitlab.com", info); err != nil {
				t.Errorf("goroutine %d Set: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	// File should still be valid JSON.
	entry, _, err := tr.Get("gitlab.com")
	if err != nil {
		t.Fatalf("Get after concurrent writes: %v", err)
	}
	if entry == nil {
		t.Fatalf("entry should not be nil after concurrent writes")
	}
}

func TestDefaultSharedTrackerPath(t *testing.T) {
	path, err := DefaultSharedTrackerPath("gitlab.mycompany.com")
	if err != nil {
		t.Fatalf("DefaultSharedTrackerPath: %v", err)
	}
	if !strings.Contains(path, "ratelimit-gitlab-") {
		t.Fatalf("path should contain 'ratelimit-gitlab-': %s", path)
	}
	if !strings.Contains(path, "gitlab-mycompany-com") {
		t.Fatalf("path should contain sanitized host 'gitlab-mycompany-com': %s", path)
	}
	if !strings.Contains(path, ".nightgauge") {
		t.Fatalf("path should be under .nightgauge: %s", path)
	}
}

func TestDefaultSharedTrackerPath_withPort(t *testing.T) {
	path, err := DefaultSharedTrackerPath("gitlab.example.com:8080")
	if err != nil {
		t.Fatalf("DefaultSharedTrackerPath: %v", err)
	}
	// Port should be stripped.
	if strings.Contains(path, "8080") {
		t.Fatalf("path should not contain port: %s", path)
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
