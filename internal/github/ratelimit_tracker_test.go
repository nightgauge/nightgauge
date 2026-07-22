package github

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSharedTracker_GetMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	entry, fresh, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry != nil || fresh {
		t.Fatalf("expected (nil,false) for missing file, got (%v,%v)", entry, fresh)
	}
}

func TestSharedTracker_SetThenGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	info := &RateLimitInfo{Remaining: 4200, Limit: 5000, ResetAt: time.Now().Add(30 * time.Minute).Unix()}
	if err := tr.Set("alice", info); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entry, fresh, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil {
		t.Fatalf("expected entry, got nil")
	}
	if !fresh {
		t.Fatalf("just-written entry should be fresh")
	}
	if entry.Remaining != 4200 || entry.Limit != 5000 {
		t.Fatalf("values not persisted: got %+v", entry)
	}
	if entry.CheckedAt == 0 {
		t.Fatalf("CheckedAt should be set by Set()")
	}
}

func TestSharedTracker_StalenessBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	// Manually write an entry whose CheckedAt is older than the freshness window.
	info := &RateLimitInfo{Remaining: 100, Limit: 5000, ResetAt: time.Now().Add(30 * time.Minute).Unix()}
	if err := tr.Set("alice", info); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Rewrite the file with a stale CheckedAt.
	file, err := tr.readLocked()
	if err != nil {
		t.Fatalf("readLocked: %v", err)
	}
	file.Entries["alice"].CheckedAt = time.Now().Unix() - int64(SharedTrackerMinCheckIntervalSecs) - 5
	if err := tr.writeLocked(file); err != nil {
		t.Fatalf("writeLocked: %v", err)
	}

	entry, fresh, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil {
		t.Fatalf("entry should still be returned even when stale")
	}
	if fresh {
		t.Fatalf("stale entry should not be fresh")
	}
}

func TestSharedTracker_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	tr := NewSharedRateLimitTracker(path)

	// Corrupt file should not error — it should be treated as empty so the
	// next Set() can bootstrap a valid file.
	entry, fresh, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("Get on corrupt: %v", err)
	}
	if entry != nil || fresh {
		t.Fatalf("expected (nil,false) for corrupt file")
	}

	info := &RateLimitInfo{Remaining: 50, Limit: 5000, ResetAt: time.Now().Add(1 * time.Hour).Unix()}
	if err := tr.Set("alice", info); err != nil {
		t.Fatalf("Set after corrupt: %v", err)
	}
	entry, fresh, err = tr.Get("alice")
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if entry == nil || !fresh || entry.Remaining != 50 {
		t.Fatalf("recovery path failed: %+v fresh=%v", entry, fresh)
	}
}

func TestSharedTracker_MultipleUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	if err := tr.Set("alice", &RateLimitInfo{Remaining: 1, Limit: 5000, ResetAt: 123}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set("bob", &RateLimitInfo{Remaining: 2, Limit: 5000, ResetAt: 456}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Set("", &RateLimitInfo{Remaining: 3, Limit: 5000, ResetAt: 789}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		user string
		want int
	}{{"alice", 1}, {"bob", 2}, {"", 3}} {
		entry, _, err := tr.Get(tc.user)
		if err != nil || entry == nil {
			t.Fatalf("Get(%q): err=%v entry=%v", tc.user, err, entry)
		}
		if entry.Remaining != tc.want {
			t.Errorf("Get(%q) Remaining=%d, want %d", tc.user, entry.Remaining, tc.want)
		}
	}
}

func TestSharedTracker_ConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	// Hammer Set() from many goroutines to verify atomic-rename writes do
	// not corrupt the file. The final state must still be a readable JSON
	// object with one of the written values.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = tr.Set("alice", &RateLimitInfo{Remaining: i, Limit: 5000, ResetAt: int64(i)})
		}(i)
	}
	wg.Wait()

	entry, _, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("Get after concurrent writes: %v", err)
	}
	if entry == nil {
		t.Fatalf("expected entry after concurrent writes")
	}
	if entry.Remaining < 0 || entry.Remaining >= 50 {
		t.Fatalf("final Remaining out of written range: %d", entry.Remaining)
	}
}

func TestSharedTracker_EmptyUserCollapsesToDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	if err := tr.Set("", &RateLimitInfo{Remaining: 7, Limit: 5000, ResetAt: 100}); err != nil {
		t.Fatal(err)
	}
	// Reading under "default" should find the entry written under "".
	file, err := tr.readLocked()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := file.Entries["default"]; !ok {
		t.Fatalf("empty user should have been stored under 'default', got keys: %v", keys(file.Entries))
	}
}

func keys(m map[string]*SharedTrackerEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── SetFromHeaders tests (#3291) ──────────────────────────────────────────────

func TestSharedTracker_SetFromHeaders_Valid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	resetUnix := time.Now().Add(45 * time.Minute).Unix()
	updated, err := tr.SetFromHeaders(
		"alice",
		"4321",
		"5000",
		fmt.Sprintf("%d", resetUnix),
	)
	if err != nil {
		t.Fatalf("SetFromHeaders: %v", err)
	}
	if !updated {
		t.Fatalf("expected updated=true for valid headers")
	}

	entry, fresh, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil || !fresh {
		t.Fatalf("expected fresh entry, got entry=%v fresh=%v", entry, fresh)
	}
	if entry.Remaining != 4321 || entry.Limit != 5000 || entry.ResetAt != resetUnix {
		t.Fatalf("header values not persisted: %+v", entry)
	}
	if entry.CheckedAt == 0 {
		t.Fatalf("CheckedAt should be set by SetFromHeaders")
	}
}

func TestSharedTracker_SetFromHeaders_EmptyIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	for _, tc := range []struct{ name, r, l, rs string }{
		{"all empty", "", "", ""},
		{"missing remaining", "", "5000", "1700000000"},
		{"missing limit", "100", "", "1700000000"},
		{"missing reset", "100", "5000", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated, err := tr.SetFromHeaders("alice", tc.r, tc.l, tc.rs)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if updated {
				t.Fatalf("empty header should be a no-op (updated=false)")
			}
			entry, _, _ := tr.Get("alice")
			if entry != nil {
				t.Fatalf("no entry should be persisted, got %+v", entry)
			}
		})
	}
}

func TestSharedTracker_SetFromHeaders_NonIntegerIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	for _, tc := range []struct{ name, r, l, rs string }{
		{"bad remaining", "abc", "5000", "1700000000"},
		{"bad limit", "100", "five-thousand", "1700000000"},
		{"bad reset", "100", "5000", "noon"},
		{"float remaining", "1.5", "5000", "1700000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated, err := tr.SetFromHeaders("alice", tc.r, tc.l, tc.rs)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if updated {
				t.Fatalf("non-integer header should be a no-op (updated=false)")
			}
		})
	}
}

func TestSharedTracker_SetFromHeaders_NoDoubleCount(t *testing.T) {
	// Verifies the "no double count" acceptance criterion: when both a
	// header-driven update and a GraphQL probe-driven update arrive, the
	// later observation simply overwrites — never additively combines.
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	resetUnix := time.Now().Add(30 * time.Minute).Unix()
	if _, err := tr.SetFromHeaders("alice", "4500", "5000", fmt.Sprintf("%d", resetUnix)); err != nil {
		t.Fatalf("first SetFromHeaders: %v", err)
	}
	// A subsequent GraphQL probe (Set) reports a slightly newer remaining
	// value. The persisted entry should reflect the newer value exactly,
	// not the sum or any aggregate.
	if err := tr.Set("alice", &RateLimitInfo{Remaining: 4480, Limit: 5000, ResetAt: resetUnix}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entry, _, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Remaining != 4480 {
		t.Fatalf("expected exactly 4480 (overwrite, not double-count), got %d", entry.Remaining)
	}

	// And the reverse direction: header update after a Set must not double-count.
	if _, err := tr.SetFromHeaders("alice", "4470", "5000", fmt.Sprintf("%d", resetUnix)); err != nil {
		t.Fatalf("second SetFromHeaders: %v", err)
	}
	entry, _, _ = tr.Get("alice")
	if entry.Remaining != 4470 {
		t.Fatalf("expected exactly 4470, got %d", entry.Remaining)
	}
}

func TestSharedTracker_FreshnessWindowIs15s(t *testing.T) {
	// #3291 acceptance: the freshness window dropped from 60s to 15s now
	// that headers feed the tracker for free.
	if SharedTrackerMinCheckIntervalSecs != 15 {
		t.Fatalf("SharedTrackerMinCheckIntervalSecs = %d, want 15 (#3291)",
			SharedTrackerMinCheckIntervalSecs)
	}
}
