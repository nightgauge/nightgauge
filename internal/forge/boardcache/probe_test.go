package boardcache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// probingBoard is a countingBoard that also implements ChangeProbe, so a test
// can drive the probe's answer and count how often it was asked. Probe calls
// are counted separately from reads because the entire claim of #847 is that
// one REPLACES the other — a cache that probes and then reads anyway has spent
// an extra point to save nothing.
type probingBoard struct {
	countingBoard

	pmu        sync.Mutex
	probeCalls int
	updatedAt  time.Time
	probeErr   error
	block      chan struct{} // when non-nil, the probe waits on it
}

func (b *probingBoard) ProjectUpdatedAt(ctx context.Context) (time.Time, error) {
	b.pmu.Lock()
	b.probeCalls++
	ts, err, block := b.updatedAt, b.probeErr, b.block
	b.pmu.Unlock()
	if block != nil {
		<-block
	}
	return ts, err
}

func (b *probingBoard) probes() int {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	return b.probeCalls
}

func (b *probingBoard) setUpdatedAt(ts time.Time) {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	b.updatedAt = ts
}

// newProbeFixture wires a cache with a 60s TTL, a controllable clock, and a
// board whose one completed read happened at `now`.
func newProbeFixture(t *testing.T) (*probingBoard, *Cache, *time.Time) {
	t.Helper()
	inner := &probingBoard{countingBoard: countingBoard{items: sampleItems(), total: 2}}
	c := New(60 * time.Second)
	now := time.Unix(1_700_000_000, 0).UTC()
	c.SetClock(func() time.Time { return now })
	board := c.Wrap(inner, "acme", 3)
	// The board moved just before the first read, so an unchanged board is
	// represented by this timestamp staying put.
	inner.setUpdatedAt(now.Add(-time.Second))
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("seed read: %v", err)
	}
	if inner.calls() != 1 {
		t.Fatalf("seed read issued %d fetches, want 1", inner.calls())
	}
	if inner.probes() != 0 {
		t.Fatalf("a COLD cache probed %d times; there is nothing to renew on a miss", inner.probes())
	}
	t.Cleanup(func() { _ = board })
	return inner, c, &now
}

// AC1, the headline: an expired snapshot whose board has not moved is renewed
// for one probe instead of re-read. This is the 34-points-to-1 trade.
func TestExpiredSnapshotIsRenewedNotRefetchedWhenTheBoardHasNotMoved(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	*now = now.Add(61 * time.Second) // past the TTL
	items, total, err := board.ListOpenItems(context.Background())
	if err != nil {
		t.Fatalf("read past TTL: %v", err)
	}
	if len(items) != 2 || total != 2 {
		t.Errorf("renewed snapshot returned %d items/total %d, want 2/2 — a renewal must serve the DATA, not an empty shell", len(items), total)
	}
	if got := inner.calls(); got != 1 {
		t.Errorf("expired-but-unmoved board issued %d board reads, want 1 (the seed) — the probe did not gate the read", got)
	}
	if got := inner.probes(); got != 1 {
		t.Errorf("probe called %d times, want exactly 1", got)
	}
}

// The renewal must actually reset the freshness window, or the next caller
// probes again immediately and the saving is one read rather than a window of
// them.
func TestRenewalRestartsTheFreshnessWindow(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	*now = now.Add(61 * time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("renewing read: %v", err)
	}
	*now = now.Add(30 * time.Second) // inside the TTL measured from the renewal
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read inside renewed window: %v", err)
	}
	if got := inner.probes(); got != 1 {
		t.Errorf("probe called %d times, want 1 — a renewal that does not restart the window re-probes on every call", got)
	}
	if got := inner.calls(); got != 1 {
		t.Errorf("board read %d times, want 1", got)
	}
}

// FetchedAt is a claim about the DATA and must survive renewal untouched; the
// probe's word goes in VerifiedAt. A consumer that reports "nothing is
// stranded" is entitled to know the underlying read is minutes old.
func TestRenewalKeepsFetchedAtTruthfulAndRecordsVerifiedAt(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)
	fetchedAt := *now

	*now = now.Add(61 * time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("renewing read: %v", err)
	}
	snap, ok := c.Peek("acme", 3, "open")
	if !ok {
		t.Fatal("Peek found no snapshot after a renewal — a renewed entry must remain servable")
	}
	if !snap.FetchedAt.Equal(fetchedAt) {
		t.Errorf("FetchedAt = %v, want %v — a renewal must not backdate the read it vouches for", snap.FetchedAt, fetchedAt)
	}
	if !snap.VerifiedAt.Equal(*now) {
		t.Errorf("VerifiedAt = %v, want %v", snap.VerifiedAt, *now)
	}
	if got, want := snap.Age(*now), 61*time.Second; got != want {
		t.Errorf("Age = %v, want %v — Age reports the age of the DATA, not of the probe", got, want)
	}
}

// A board that MOVED must be re-read. This is the direction that would be a
// correctness regression rather than a cost one.
func TestMovedBoardIsRefetched(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	*now = now.Add(61 * time.Second)
	inner.setUpdatedAt(*now) // someone dragged an issue to Ready
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read after board moved: %v", err)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("a MOVED board issued %d reads, want 2 — the probe suppressed a read it must have allowed", got)
	}
	snap, _ := c.Peek("acme", 3, "open")
	if !snap.VerifiedAt.IsZero() {
		t.Errorf("a freshly READ snapshot carries VerifiedAt %v, want zero — only a probe sets it", snap.VerifiedAt)
	}
}

// AC3, fail open. Every way the probe can fail to produce a confident "did not
// move" must end in a read. Table-driven because the failure modes are what the
// acceptance criterion is actually about, and testing one of them would leave
// the others free to suppress a read.
func TestEveryUncertainProbeOutcomeFailsOpen(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*probingBoard, time.Time)
	}{
		{"probe errors", func(b *probingBoard, _ time.Time) {
			b.pmu.Lock()
			defer b.pmu.Unlock()
			b.probeErr = errors.New("probe: 502 bad gateway")
		}},
		{"probe returns the zero time", func(b *probingBoard, _ time.Time) {
			b.setUpdatedAt(time.Time{})
		}},
		{"board moved at exactly the read instant", func(b *probingBoard, fetchedAt time.Time) {
			// Equal timestamps are NOT movement — a write in the same second as
			// the read is indistinguishable from the one that preceded it, and
			// this pins which way that ambiguity is resolved.
			b.setUpdatedAt(fetchedAt.Add(time.Nanosecond))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, c, now := newProbeFixture(t)
			board := c.Wrap(inner, "acme", 3)
			fetchedAt := *now

			tc.setup(inner, fetchedAt)
			*now = now.Add(61 * time.Second)
			if _, _, err := board.ListOpenItems(context.Background()); err != nil {
				t.Fatalf("read: %v", err)
			}
			if got := inner.calls(); got != 2 {
				t.Errorf("issued %d board reads, want 2 — an uncertain probe must never suppress a read", got)
			}
		})
	}
}

// The ceiling binds. The probe is blind to issue-side edits (title, labels,
// blockedBy), so renewal cannot be unbounded however cooperative the board is.
func TestRenewalStopsAtMaxRenewedAge(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	// Renew repeatedly across the whole ceiling. The board never moves, so
	// nothing but the cap can force a read.
	renewals := 0
	for elapsed := 61 * time.Second; elapsed < MaxRenewedAge; elapsed += 61 * time.Second {
		*now = now.Add(61 * time.Second)
		if _, _, err := board.ListOpenItems(context.Background()); err != nil {
			t.Fatalf("renewing read at +%v: %v", elapsed, err)
		}
		renewals++
	}
	if renewals < 2 {
		t.Fatalf("fixture renewed only %d times; the test cannot distinguish a cap from a broken renewal", renewals)
	}
	if got := inner.calls(); got != 1 {
		t.Fatalf("inside the ceiling the board was read %d times, want 1", got)
	}

	// Cross it.
	*now = now.Add(MaxRenewedAge)
	probesBefore := inner.probes()
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read past the ceiling: %v", err)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("past MaxRenewedAge the board was read %d times, want 2 — the staleness ceiling did not bind", got)
	}
	if got := inner.probes(); got != probesBefore {
		t.Errorf("a snapshot past the ceiling was probed (%d -> %d); the read is already forced, so the probe is a wasted point", probesBefore, got)
	}
}

// The ceiling is measured from the READ, not from the last renewal. If renewal
// reset it, the cap would recede by one TTL on every probe and never bind at
// all — the silent version of having no cap.
func TestRenewalDoesNotPushTheCeilingAway(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	*now = now.Add(61 * time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("renewing read: %v", err)
	}
	if got := inner.calls(); got != 1 {
		t.Fatalf("setup: board read %d times, want 1", got)
	}
	// Now step to just past MaxRenewedAge measured from the ORIGINAL read.
	*now = time.Unix(1_700_000_000, 0).UTC().Add(MaxRenewedAge + time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read past ceiling: %v", err)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("board read %d times, want 2 — the ceiling is being measured from the renewal, so it recedes forever", got)
	}
}

// Concurrent callers share one probe for the same reason they share one fetch.
func TestConcurrentReadersShareOneProbe(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	inner.pmu.Lock()
	inner.block = make(chan struct{})
	inner.pmu.Unlock()

	*now = now.Add(61 * time.Second)

	const readers = 8
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = board.ListOpenItems(context.Background())
		}()
	}
	// Let them all pile onto the in-flight entry before the probe returns.
	time.Sleep(20 * time.Millisecond)
	inner.pmu.Lock()
	close(inner.block)
	inner.block = nil
	inner.pmu.Unlock()
	wg.Wait()

	if got := inner.probes(); got != 1 {
		t.Errorf("%d concurrent readers issued %d probes, want 1", readers, got)
	}
	if got := inner.calls(); got != 1 {
		t.Errorf("%d concurrent readers issued %d board reads, want 1", readers, got)
	}
}

// A local write still drops the snapshot outright. The probe is a way to avoid
// re-reading data we believe is current, not a second opinion that can override
// our own knowledge that we changed the board.
func TestInvalidationBeatsTheProbe(t *testing.T) {
	inner, c, now := newProbeFixture(t)
	board := c.Wrap(inner, "acme", 3)

	c.Invalidate("acme", 3)
	*now = now.Add(time.Second) // still well inside the TTL
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read after invalidate: %v", err)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("board read %d times after an invalidate, want 2", got)
	}
	if got := inner.probes(); got != 0 {
		t.Errorf("an INVALIDATED entry was probed %d times; there is no snapshot left to vouch for", got)
	}
}

// A board adapter with no probe behaves exactly as it did before #847.
func TestBoardWithoutAProbeRefetchesOnExpiry(t *testing.T) {
	inner := &countingBoard{items: sampleItems(), total: 2}
	c := New(60 * time.Second)
	now := time.Unix(1_700_000_000, 0).UTC()
	c.SetClock(func() time.Time { return now })
	board := c.Wrap(inner, "acme", 3)

	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("first read: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read past TTL: %v", err)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("a probe-less board read %d times, want 2 — expiry must still refetch", got)
	}
}

// A failed read is never renewable: there is no snapshot to vouch for, and
// memoizing "I could not look" as "nothing is there" is the exact Invariant the
// package is written to.
func TestAFailedReadIsNotRenewable(t *testing.T) {
	inner := &probingBoard{countingBoard: countingBoard{err: errors.New("boom")}}
	c := New(60 * time.Second)
	now := time.Unix(1_700_000_000, 0).UTC()
	c.SetClock(func() time.Time { return now })
	board := c.Wrap(inner, "acme", 3)
	inner.setUpdatedAt(now.Add(-time.Second))

	if _, _, err := board.ListOpenItems(context.Background()); err == nil {
		t.Fatal("seed read succeeded, want the injected error")
	}
	now = now.Add(time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err == nil {
		t.Fatal("second read succeeded, want the injected error")
	}
	if got := inner.probes(); got != 0 {
		t.Errorf("a failed read was probed %d times, want 0", got)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("board read %d times, want 2 — a failed read must not be cached", got)
	}
}

// Unpinned Wiring guard (docs/FAILURE_TAXONOMY.md). ChangeProbe is an OPTIONAL
// interface, so if github.BoardService's method signature ever drifts, the type
// assertion in probeFor simply stops matching: no build error, no test failure,
// and the board read silently goes back to costing 34 points forever. This is
// the test that turns that into a red build.
func TestGitHubBoardServiceImplementsChangeProbe(t *testing.T) {
	var _ ChangeProbe = (*github.BoardService)(nil)
}

// ...and this one pins that Wrap actually REACHES for the capability. The
// assertion above would keep passing if probeFor were deleted outright.
func TestWrapReachesForTheProbeCapability(t *testing.T) {
	if probeFor(&probingBoard{}) == nil {
		t.Fatal("Wrap does not detect a board that implements ChangeProbe — the whole feature is dead wiring")
	}
	if probeFor(&countingBoard{}) != nil {
		t.Fatal("probeFor invented a probe for a board that has none")
	}
}
