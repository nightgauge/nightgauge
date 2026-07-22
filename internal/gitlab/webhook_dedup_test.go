package gitlab_test

import (
	"context"
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
