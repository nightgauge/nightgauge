// Package boardcache holds one board read per board, per window, for the
// lifetime of a daemon.
//
// A ProjectV2 board read is the most expensive thing this product does: 17
// GraphQL points per 100-item page, against a 5,000-point hourly budget, where
// almost every other call costs 1. The nested `first:` values in that query
// were already tuned 16x down (#842), so the remaining cost is not the query's
// shape — it is **how many times we issue it**.
//
// Measured on three repo sweeps before this package existed: 71 requests, 184
// points, of which **136 points (74%) were eight board reads** split evenly
// between two producers — `CoverageGap.discoverBoard` and
// `StrandedReadyItems.boardUnreachable` — asking the same board the same
// question inside the same sweep. Neither knows the other exists, and neither
// should have to: the fix belongs under them, not in their call sites.
//
// This is a read-through cache on forge.BoardService, so producers keep calling
// `in.Forge.Board().ListOpenItems(ctx)` unchanged and the second caller in a
// window is free.
package boardcache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// DefaultTTL is how long a snapshot stands in for a fresh read.
//
// It is deliberately short. The cache's headline win is collapsing the
// duplicate reads *within one sweep*, which happen milliseconds apart; holding
// data for minutes buys little more and costs staleness in a surface whose
// whole job is telling an operator what is true right now. A sweep is the unit
// this is tuned for, not a session.
const DefaultTTL = 90 * time.Second

// Snapshot is one board read, with the time it was taken. FetchedAt is exported
// because a cache that cannot report its own age is indistinguishable from a
// cache that is lying: a consumer showing an operator "nothing is stranded" has
// to be able to say how old that claim is.
type Snapshot struct {
	Items     []forgetypes.BoardItem
	Total     int
	FetchedAt time.Time
}

// Age reports how long ago this snapshot was taken.
func (s Snapshot) Age(now time.Time) time.Duration { return now.Sub(s.FetchedAt) }

// Cache holds board snapshots keyed by (owner, project, query). One Cache is
// shared by every consumer in a process; Wrap binds it to a particular board.
type Cache struct {
	ttl time.Duration
	// now is swappable so tests can drive expiry without sleeping. A test that
	// sleeps to cross a TTL is a test that is slow and flaky at the same time.
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

// entry is either in flight or complete. `done` is closed exactly once, when
// the fetch that created the entry finishes, so concurrent callers for the same
// key wait for that one fetch instead of issuing their own. Without this,
// producers running in parallel inside a sweep would each miss a cold cache and
// the duplicate read this package exists to remove would survive.
type entry struct {
	done chan struct{}
	snap Snapshot
	err  error
}

// New returns a Cache with the given TTL; ttl <= 0 uses DefaultTTL.
func New(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{ttl: ttl, now: time.Now, entries: map[string]*entry{}}
}

// SetClock replaces the time source. Tests only.
func (c *Cache) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// boardPrefix is the key namespace for one board. Invalidation works on this
// prefix so a mutation drops every cached query for the board it touched, not
// merely the one query shape the mutating code happened to think about.
func boardPrefix(owner string, project int) string {
	return fmt.Sprintf("%s#%d|", strings.ToLower(owner), project)
}

// Invalidate drops every cached query for one board. Call it after this process
// mutates that board: a snapshot taken before a write we ourselves issued is
// not stale data, it is data we know to be wrong.
func (c *Cache) Invalidate(owner string, project int) {
	prefix := boardPrefix(owner, project)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if strings.HasPrefix(k, prefix) {
			delete(c.entries, k)
		}
	}
}

// InvalidateAll drops every board. Used when the mutating caller cannot name
// the board it touched — better a cold cache than a confidently wrong one.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*entry{}
}

// Peek reports the cached snapshot for a query without fetching, and whether
// one is present and unexpired. It is how a surface answers "how old is this?"
// without provoking a read.
func (c *Cache) Peek(owner string, project int, query string) (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[boardPrefix(owner, project)+query]
	if !ok || !c.freshLocked(e) {
		return Snapshot{}, false
	}
	select {
	case <-e.done:
		return e.snap, e.err == nil
	default:
		return Snapshot{}, false // still in flight
	}
}

// freshLocked reports whether an entry may still be served. An in-flight entry
// is always "fresh" — its fetch has not returned, so there is nothing to expire
// and a second caller should wait for it rather than start a rival fetch.
func (c *Cache) freshLocked(e *entry) bool {
	select {
	case <-e.done:
		return c.now().Sub(e.snap.FetchedAt) < c.ttl
	default:
		return true
	}
}

// get returns the snapshot for a key, fetching through `load` on a miss.
// Concurrent callers for one key share a single load.
func (c *Cache) get(ctx context.Context, key string, load func(context.Context) (Snapshot, error)) (Snapshot, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.freshLocked(e) {
		c.mu.Unlock()
		select {
		case <-e.done:
			return e.snap, e.err
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		}
	}
	e := &entry{done: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	snap, err := load(ctx)
	snap.FetchedAt = c.now()
	e.snap, e.err = snap, err
	close(e.done)

	if err != nil {
		// A failed read is never cached. "I could not look" must not be
		// memoized into "there is nothing there" for the rest of the TTL —
		// the same Invariant 1 the sweep producers are written to.
		c.mu.Lock()
		if cur, ok := c.entries[key]; ok && cur == e {
			delete(c.entries, key)
		}
		c.mu.Unlock()
	}
	return snap, err
}

// Wrap returns a forge.BoardService that reads through this cache on behalf of
// the board identified by (owner, project). The returned service satisfies the
// same interface the producers already consume, so nothing at the call sites
// changes.
func (c *Cache) Wrap(board forge.BoardService, owner string, project int) forge.BoardService {
	if board == nil || c == nil {
		return board
	}
	return &cachedBoard{cache: c, inner: board, prefix: boardPrefix(owner, project)}
}

type cachedBoard struct {
	cache  *Cache
	inner  forge.BoardService
	prefix string
}

func (b *cachedBoard) ListOpenItems(ctx context.Context) ([]forgetypes.BoardItem, int, error) {
	snap, err := b.cache.get(ctx, b.prefix+"open", func(ctx context.Context) (Snapshot, error) {
		items, total, err := b.inner.ListOpenItems(ctx)
		return Snapshot{Items: items, Total: total}, err
	})
	return snap.Items, snap.Total, err
}

func (b *cachedBoard) ListItems(ctx context.Context, statusFilter string) ([]forgetypes.BoardItem, error) {
	snap, err := b.cache.get(ctx, b.prefix+"items:"+statusFilter, func(ctx context.Context) (Snapshot, error) {
		items, err := b.inner.ListItems(ctx, statusFilter)
		return Snapshot{Items: items, Total: len(items)}, err
	})
	return snap.Items, err
}

// CountsByStatus and GetItem are NOT cached, deliberately.
//
// CountsByStatus returns a different type, and deriving it from a cached item
// list would be a second implementation of the adapter's own aggregation —
// silently divergent the first time either side changes. GetItem is per-issue:
// serving it from a board-wide list would answer "not on the board" for an item
// added since the snapshot, which is the one answer callers act on destructively.
func (b *cachedBoard) CountsByStatus(ctx context.Context) (*forgetypes.StatusCounts, error) {
	return b.inner.CountsByStatus(ctx)
}

func (b *cachedBoard) GetItem(ctx context.Context, owner, repo string, issueNumber int) (*forgetypes.BoardItem, error) {
	return b.inner.GetItem(ctx, owner, repo, issueNumber)
}
