package boardcache

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// subsetBoard is a countingBoard that promises its non-Done status reads are
// slices of its open read, as the GitHub adapter does.
type subsetBoard struct{ countingBoard }

func (*subsetBoard) StatusReadIsOpenSubset(status string) bool {
	return status != "" && status != "Done"
}

func openBoardItems() []forgetypes.BoardItem {
	return []forgetypes.BoardItem{
		{Number: 1, Status: "Ready"},
		{Number: 2, Status: "In progress"},
		{Number: 3, Status: "backlog"},
		{Number: 4, Status: "Ready"},
		{Number: 5, Status: "In review"},
	}
}

// The Repositories tree asks for Ready, In progress and Backlog on every row,
// and board.counts and the sweeps read the open snapshot of the same board.
// Red before the fix: three status reads after an open read reached upstream
// three more times.
func TestStatusReadsAreServedFromAFreshOpenSnapshot(t *testing.T) {
	inner := &subsetBoard{countingBoard{items: openBoardItems(), total: 5}}
	board := New(0).Wrap(inner, "acme", 3)
	ctx := context.Background()

	if _, _, err := board.ListOpenItems(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string][]int{"Ready": {1, 4}, "In progress": {2}, "Backlog": {3}}
	for status, numbers := range want {
		items, err := board.ListItems(ctx, status)
		if err != nil {
			t.Fatalf("ListItems(%q): %v", status, err)
		}
		var got []int
		for _, it := range items {
			got = append(got, it.Number)
		}
		if len(got) != len(numbers) {
			t.Fatalf("ListItems(%q) = %v, want %v", status, got, numbers)
		}
		for i := range got {
			if got[i] != numbers[i] {
				t.Fatalf("ListItems(%q) = %v, want %v (board order)", status, got, numbers)
			}
		}
	}
	if inner.itemCalls != 0 {
		t.Fatalf("status reads reached upstream %d times with a fresh open snapshot held, want 0", inner.itemCalls)
	}
	if got := inner.calls(); got != 1 {
		t.Fatalf("open read reached upstream %d times, want 1", got)
	}
}

// A status read never STARTS the open read on its own behalf: a caller that
// only asks for Ready (the scheduler) keeps paying one filtered page, not the
// whole open board.
func TestStatusReadWithoutAnOpenSnapshotKeepsItsOwnRead(t *testing.T) {
	inner := &subsetBoard{countingBoard{items: openBoardItems(), total: 5}}
	board := New(0).Wrap(inner, "acme", 3)

	if _, err := board.ListItems(context.Background(), "Ready"); err != nil {
		t.Fatal(err)
	}
	if inner.itemCalls != 1 || inner.calls() != 0 {
		t.Fatalf("status read: itemCalls=%d openCalls=%d, want 1 and 0", inner.itemCalls, inner.calls())
	}
}

// "Done" is read without is:open, so it is not a slice of the open snapshot
// and must keep its own read even when the open snapshot is fresh.
func TestDoneIsNeverServedFromTheOpenSnapshot(t *testing.T) {
	inner := &subsetBoard{countingBoard{items: openBoardItems(), total: 5}}
	board := New(0).Wrap(inner, "acme", 3)
	ctx := context.Background()

	_, _, _ = board.ListOpenItems(ctx)
	if _, err := board.ListItems(ctx, "Done"); err != nil {
		t.Fatal(err)
	}
	if inner.itemCalls != 1 {
		t.Fatalf("Done read reached upstream %d times, want 1", inner.itemCalls)
	}
}

// An expired open snapshot does not answer a status read: the status entry
// keeps its own freshness rules.
func TestExpiredOpenSnapshotDoesNotAnswerAStatusRead(t *testing.T) {
	inner := &subsetBoard{countingBoard{items: openBoardItems(), total: 5}}
	c := New(0)
	now := time.Now()
	c.SetClock(func() time.Time { return now })
	board := c.Wrap(inner, "acme", 3)
	ctx := context.Background()

	_, _, _ = board.ListOpenItems(ctx)
	now = now.Add(2 * DefaultTTL)
	if _, err := board.ListItems(ctx, "Ready"); err != nil {
		t.Fatal(err)
	}
	if inner.itemCalls != 1 {
		t.Fatalf("status read after the open snapshot expired reached upstream %d times, want 1", inner.itemCalls)
	}
}

// A board that has not made the promise keeps every status read its own.
func TestBoardWithoutTheCapabilityKeepsItsStatusReads(t *testing.T) {
	inner := &countingBoard{items: openBoardItems(), total: 5}
	board := New(0).Wrap(inner, "acme", 3)
	ctx := context.Background()

	_, _, _ = board.ListOpenItems(ctx)
	_, _ = board.ListItems(ctx, "Ready")
	if inner.itemCalls != 1 {
		t.Fatalf("status read on a board without OpenStatusSubset reached upstream %d times, want 1", inner.itemCalls)
	}
}

// Pins the real adapter against the capability; without it the saving above
// silently never happens on GitHub (the Unpinned Wiring shape).
func TestGitHubBoardServiceImplementsOpenStatusSubset(t *testing.T) {
	var cap OpenStatusSubset = (*github.BoardService)(nil)
	for status, want := range map[string]bool{"Ready": true, "In progress": true, "Backlog": true, "Done": false, "": false} {
		if got := cap.StatusReadIsOpenSubset(status); got != want {
			t.Errorf("StatusReadIsOpenSubset(%q) = %v, want %v", status, got, want)
		}
	}
}
