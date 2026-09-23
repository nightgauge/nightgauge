package boardcache

import (
	"context"
	"sync"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// summaryBoard is a countingBoard that also offers the summary read and
// reports a token identity.
type summaryBoard struct {
	countingBoard
	identity     string
	available    bool
	sumMu        sync.Mutex
	summaryCalls int
}

func (b *summaryBoard) ListOpenItemsSummary(context.Context) ([]forgetypes.BoardItem, int, error) {
	b.sumMu.Lock()
	defer b.sumMu.Unlock()
	b.summaryCalls++
	return []forgetypes.BoardItem{{Repo: "acme/web", RelationSummary: &forgetypes.RelationSummary{BlockedByOpen: 1}}}, 1, nil
}

func (b *summaryBoard) CacheIdentity() string      { return b.identity }
func (b *summaryBoard) SummaryReadAvailable() bool { return b.available }

func (b *summaryBoard) summaries() int {
	b.sumMu.Lock()
	defer b.sumMu.Unlock()
	return b.summaryCalls
}

// The summary and the list-carrying open read are different reads and are
// cached apart: a summary must never be served to a caller that needs lists.
func TestSummaryIsItsOwnEntry(t *testing.T) {
	inner := &summaryBoard{countingBoard: countingBoard{items: sampleItems(), total: 2}, available: true}
	board := New(0).Wrap(inner, "acme", 3).(*cachedBoard)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := board.ListOpenItemsSummary(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := board.ListOpenItems(ctx); err != nil {
		t.Fatal(err)
	}
	if inner.summaries() != 1 || inner.calls() != 1 {
		t.Fatalf("summary reads = %d, open reads = %d; want 1 and 1", inner.summaries(), inner.calls())
	}
	// counts and ListOpenSummary read the summary entry, not the open one.
	if _, err := CountsByStatus(ctx, board); err != nil {
		t.Fatal(err)
	}
	if inner.summaries() != 1 {
		t.Fatalf("CountsByStatus re-read the summary (%d reads)", inner.summaries())
	}
}

// Where the adapter's summary read is not its own cheap read (GHES), the
// summary is derived from the ONE open snapshot, so status reads keep being
// answered from it exactly as before.
func TestUnavailableSummaryDerivesFromTheOpenSnapshot(t *testing.T) {
	inner := &summaryBoard{countingBoard: countingBoard{items: []forgetypes.BoardItem{
		{Repo: "acme/web", BlockedBy: []forgetypes.BlockingRef{{State: "OPEN"}, {State: "CLOSED"}}},
	}, total: 1}, available: false}
	board := New(0).Wrap(inner, "acme", 3).(*cachedBoard)
	items, _, err := board.ListOpenItemsSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inner.summaries() != 0 || inner.calls() != 1 {
		t.Fatalf("summary reads = %d, open reads = %d; want 0 and 1", inner.summaries(), inner.calls())
	}
	if s := items[0].RelationSummary; s == nil || s.BlockedByOpen != 1 || s.BlockedByTotal != 2 {
		t.Fatalf("derived summary = %+v, want 1 open of 2", s)
	}
	if inner.items[0].RelationSummary != nil {
		t.Fatal("deriving the summary mutated the shared snapshot")
	}
}

// Two identities reading the same board never share a snapshot, and a write
// invalidates the board for both.
func TestCacheIsKeyedByIdentity(t *testing.T) {
	cache := New(0)
	a := &summaryBoard{countingBoard: countingBoard{items: sampleItems(), total: 2}, identity: "tok:a", available: true}
	b := &summaryBoard{countingBoard: countingBoard{items: sampleItems(), total: 2}, identity: "tok:b", available: true}
	ctx := context.Background()
	for _, inner := range []*summaryBoard{a, b, a, b} {
		if _, _, err := cache.Wrap(inner, "acme", 3).(*cachedBoard).ListOpenItemsSummary(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.summaries() != 1 || b.summaries() != 1 {
		t.Fatalf("reads a=%d b=%d, want one each — identity b must not be served a's snapshot", a.summaries(), b.summaries())
	}
	if _, ok := cache.PeekIdentity("acme", 3, "tok:a", "open-summary"); !ok {
		t.Error("Peek does not find identity a's entry")
	}
	if _, ok := cache.Peek("acme", 3, "open-summary"); ok {
		t.Error("Peek without an identity found an identity-keyed entry")
	}
	cache.Invalidate("acme", 3)
	for _, inner := range []*summaryBoard{a, b} {
		if _, _, err := cache.Wrap(inner, "acme", 3).(*cachedBoard).ListOpenItemsSummary(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.summaries() != 2 || b.summaries() != 2 {
		t.Fatalf("after Invalidate reads a=%d b=%d, want 2 each", a.summaries(), b.summaries())
	}
}

// The real GitHub adapter must satisfy every optional capability Wrap reaches
// for; a signature drift would silently switch the REST summary off.
func TestGitHubBoardServiceImplementsSummaryCapabilities(t *testing.T) {
	var b interface{} = gh.NewBoardService(gh.NewClientWithToken("x"), "acme", 3)
	if _, ok := b.(OpenSummaryReader); !ok {
		t.Error("github.BoardService does not implement OpenSummaryReader")
	}
	if _, ok := b.(IdentityReporter); !ok {
		t.Error("github.BoardService does not implement IdentityReporter")
	}
	if _, ok := b.(SummaryAvailability); !ok {
		t.Error("github.BoardService does not implement SummaryAvailability")
	}
}
