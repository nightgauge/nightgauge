package gitlab_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gitlab"
)

func newTestDedupeCache(t *testing.T, window time.Duration) *gitlab.DedupeCache {
	t.Helper()
	c, err := gitlab.NewDedupeCache(":memory:", window)
	if err != nil {
		t.Fatalf("NewDedupeCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestDedupeCache_NewDelivery_NotDuplicate(t *testing.T) {
	c := newTestDedupeCache(t, time.Hour)
	ctx := context.Background()

	isDup, err := c.IsDuplicate(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("IsDuplicate: %v", err)
	}
	if isDup {
		t.Error("fresh delivery reported as duplicate")
	}
}

func TestDedupeCache_MarkSeen_ThenDuplicate(t *testing.T) {
	c := newTestDedupeCache(t, time.Hour)
	ctx := context.Background()

	if err := c.MarkSeen(ctx, "delivery-1"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	isDup, err := c.IsDuplicate(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("IsDuplicate: %v", err)
	}
	if !isDup {
		t.Error("delivery not reported as duplicate after MarkSeen")
	}
}

func TestDedupeCache_DifferentDeliveryIDs_Independent(t *testing.T) {
	c := newTestDedupeCache(t, time.Hour)
	ctx := context.Background()

	_ = c.MarkSeen(ctx, "delivery-1")

	isDup, _ := c.IsDuplicate(ctx, "delivery-2")
	if isDup {
		t.Error("delivery-2 incorrectly reported as duplicate after delivery-1 was seen")
	}
}

func TestDedupeCache_Prune_RemovesExpiredEntries(t *testing.T) {
	// Use a 1-second window so we can test expiry.
	c := newTestDedupeCache(t, 1*time.Second)
	ctx := context.Background()

	_ = c.MarkSeen(ctx, "delivery-old")

	// Wait for the window to pass.
	time.Sleep(1100 * time.Millisecond)

	if err := c.Prune(ctx); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	isDup, _ := c.IsDuplicate(ctx, "delivery-old")
	if isDup {
		t.Error("expired delivery still reported as duplicate after prune")
	}
}

func TestDedupeCache_MarkSeen_Idempotent(t *testing.T) {
	c := newTestDedupeCache(t, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := c.MarkSeen(ctx, "delivery-1"); err != nil {
			t.Fatalf("MarkSeen attempt %d: %v", i, err)
		}
	}

	isDup, _ := c.IsDuplicate(ctx, "delivery-1")
	if !isDup {
		t.Error("delivery not reported as duplicate after repeated MarkSeen")
	}
}

// TestDedupeCache_FileStore_PersistsAcrossReopen: a receiver restarted within
// the window must still recognise a delivery GitLab retries.
func TestDedupeCache_FileStore_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedupe.json")
	ctx := context.Background()

	c, err := gitlab.NewDedupeCache(path, time.Hour)
	if err != nil {
		t.Fatalf("NewDedupeCache: %v", err)
	}
	if err := c.MarkSeen(ctx, "delivery-1"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	_ = c.Close()

	reopened, err := gitlab.NewDedupeCache(path, time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if isDup, err := reopened.IsDuplicate(ctx, "delivery-1"); err != nil || !isDup {
		t.Errorf("after reopen IsDuplicate = %v, %v; want true, nil", isDup, err)
	}
	if isDup, _ := reopened.IsDuplicate(ctx, "delivery-2"); isDup {
		t.Error("a delivery never marked reads as a duplicate after reopen")
	}
}

// TestDedupeCache_FileStore_DropsExpiredOnLoad: an entry older than the window
// is not a duplicate after reopening, and the next write removes it from disk.
func TestDedupeCache_FileStore_DropsExpiredOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedupe.json")
	old := time.Now().Add(-2 * time.Hour).Unix()
	body := fmt.Sprintf(`{"delivery-old":%d}`, old)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	c, err := gitlab.NewDedupeCache(path, time.Hour)
	if err != nil {
		t.Fatalf("NewDedupeCache: %v", err)
	}
	if isDup, _ := c.IsDuplicate(ctx, "delivery-old"); isDup {
		t.Error("expired delivery reported as duplicate after load")
	}
	if err := c.MarkSeen(ctx, "delivery-new"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got == body || !strings.Contains(got, "delivery-new") || strings.Contains(got, "delivery-old") {
		t.Errorf("store after MarkSeen = %s, want delivery-new only", got)
	}
}

// TestDedupeCache_FileStore_CorruptFileIsAnError: starting empty over an
// unreadable store would re-dispatch every retried delivery.
func TestDedupeCache_FileStore_CorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedupe.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitlab.NewDedupeCache(path, time.Hour); err == nil {
		t.Fatal("NewDedupeCache accepted a corrupt store")
	}
}
