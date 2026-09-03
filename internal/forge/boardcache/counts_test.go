package boardcache

import (
	"context"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// The counts used to be a live five-alias `items(query:){totalCount}` query
// that the wrapper forwarded past the cache on every call. The Repositories
// tree asks once per repo per refresh, so on a six-repo shared board it was
// the largest idle consumer of the GraphQL budget. Red before the fix: two
// calls issued two upstream count queries and zero snapshot reads.
func TestCountsWithinTTLReadTheSnapshotOnce(t *testing.T) {
	inner := &countingBoard{items: []forgetypes.BoardItem{{Status: "Ready"}, {Status: "Backlog"}}, total: 2}
	board := New(0).Wrap(inner, "acme", 3)

	for i := 0; i < 2; i++ {
		if _, err := CountsByStatus(context.Background(), board); err != nil {
			t.Fatalf("counts %d: %v", i, err)
		}
	}
	if got := inner.calls(); got != 1 {
		t.Fatalf("two CountsByStatus calls reached ListOpenItems %d times, want exactly 1", got)
	}
}

// The counts share the open-item snapshot with ListOpenItems rather than
// keeping a second entry: a producer that already read the board has paid
// for the counts too.
func TestCountsShareTheListOpenItemsSnapshot(t *testing.T) {
	inner := &countingBoard{items: sampleItems(), total: 2}
	board := New(0).Wrap(inner, "acme", 3)

	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := CountsByStatus(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if got := inner.calls(); got != 1 {
		t.Fatalf("ListOpenItems then CountsByStatus reached upstream %d times, want 1", got)
	}
}

// The numbers are the snapshot's own statuses, one bucket per open status.
// Status comparison is case-insensitive because the forge's `status:"Ready"`
// filter was, and a board whose option is spelled "In Progress" must not
// silently report zero. Unknown statuses are simply not counted; there is no
// Done bucket because a Done item is closed and not in an open snapshot.
func TestCountsMatchTheSnapshotStatuses(t *testing.T) {
	inner := &countingBoard{items: []forgetypes.BoardItem{
		{Status: "Ready"}, {Status: "ready"}, {Status: "Ready"},
		{Status: "In progress"}, {Status: "In Progress"},
		{Status: "In review"},
		{Status: "Backlog"}, {Status: "Backlog"}, {Status: "Backlog"}, {Status: "Backlog"},
		{Status: "Done"}, {Status: ""}, {Status: "Icebox"},
	}}
	board := New(0).Wrap(inner, "acme", 3)

	got, err := CountsByStatus(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	want := forgetypes.StatusCounts{Ready: 3, InProgress: 2, InReview: 1, Backlog: 4}
	if *got != want {
		t.Fatalf("counts = %+v, want %+v", *got, want)
	}
}

// A read that failed must surface as an error, never as a board with zero
// items in every column — the same Invariant 1 the cache itself holds.
func TestCountsPropagateAFailedRead(t *testing.T) {
	inner := &countingBoard{err: context.DeadlineExceeded}
	board := New(0).Wrap(inner, "acme", 3)

	got, err := CountsByStatus(context.Background(), board)
	if err == nil || got != nil {
		t.Fatalf("got (%+v, %v), want (nil, error)", got, err)
	}
}
