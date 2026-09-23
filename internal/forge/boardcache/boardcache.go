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
	"errors"
	"fmt"
	"math/rand/v2"
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

// MaxRenewedAge caps how long a snapshot may be served on PROBE evidence alone
// before a real read is forced, however many times the probe says the board has
// not moved.
//
// The probe (#847) answers "did this ProjectV2 object change?", and the answer
// is complete for everything the board itself owns: field values, membership,
// and a linked issue opening or closing. It is NOT complete for the issue-side
// fields a BoardItem also carries — Title, Labels, BlockedBy, SubIssues — which
// a plain issue edit changes without touching the project object at all
// (measured; see github.BoardService.ProjectUpdatedAt).
//
// So this constant is the honest name for that blind spot's width. Without it,
// an idle board would serve a snapshot whose labels were hours out of date and
// the probe would keep confirming it, correctly and uselessly. With it, the
// worst case is bounded and stated. It is a ceiling on staleness, not a TTL:
// crossing it forces the expensive read, it does not merely permit one.
const MaxRenewedAge = 10 * time.Minute

// ProbeTTL is how long one answer from the change probe stands in for the next
// ask, when the probe is asked DIRECTLY through the cached board's own
// ProjectUpdatedAt (the `board.changed` IPC verb's path).
//
// The verb exists so the extension's event-driven sweep triggers — focus
// regained, a tree refresh, a run terminating — can ask "did anything move?"
// for one point instead of re-reading every workspace board. An operator
// alt-tabbing ten times in a minute would still spend ten points on ten
// identical answers, so the answer is held briefly. Thirty seconds is short
// enough that a change is noticed on the next trigger after it lands, and long
// enough that a burst of triggers costs one point rather than one each.
//
// The renewal path inside refresh does NOT read this memo: a snapshot renewal
// already tolerates the cache TTL of staleness and must not stack a second
// window on top of it.
const ProbeTTL = 30 * time.Second

// ErrNoChangeProbe is returned by a cached board's ProjectUpdatedAt when the
// adapter underneath offers no ChangeProbe. It is an error rather than a zero
// time for the same reason the interface demands one: "I cannot tell" must
// read as "assume it changed", never as "it changed at the epoch".
var ErrNoChangeProbe = errors.New("boardcache: board offers no change probe")

// ChangeProbe is an OPTIONAL capability on a forge.BoardService: the ability to
// answer "when did this board last change?" far more cheaply than reading it.
//
// It is deliberately not part of forge.BoardService. A forge that cannot answer
// the question cheaply should not be made to implement a method that lies, and
// GitLab has no ProjectV2 equivalent — its boards are read a different way
// entirely. A service that does not implement this interface simply refetches
// on expiry, exactly as it did before #847.
//
// The cost of "optional" is that a signature drift here disables the whole
// feature silently, with nothing failing — the Unpinned Wiring shape in
// docs/FAILURE_TAXONOMY.md. That is not left to vigilance:
// TestGitHubBoardServiceImplementsChangeProbe pins the real adapter against
// this interface, and TestWrapReachesForTheProbeCapability pins that Wrap
// actually reaches for it.
type ChangeProbe interface {
	// ProjectUpdatedAt reports when the board last changed. An error must be
	// returned rather than a zero time when the probe could not look: the
	// caller treats "I do not know" as "refetch", and a silent zero would
	// instead read as "the board changed at the epoch".
	ProjectUpdatedAt(ctx context.Context) (time.Time, error)
}

// OpenStatusSubset is an OPTIONAL capability on a forge.BoardService: a
// promise that, for the given status, ListItems(ctx, status) returns exactly
// the items of ListOpenItems whose Status equals that status (compared
// case-insensitively), read with the same relationship lists.
//
// When a board makes that promise, a status read can be answered from a fresh
// open-item snapshot instead of issuing its own `items(query:"status:X
// is:open")` read. The Repositories tree asks for Ready, In progress and
// Backlog on every repository row, the attention sweeps and board.counts read
// the open snapshot, and before this each of those was a separate 17-point
// page on the same board inside the same minute.
//
// It is a capability rather than an assumption because it is a property of
// the adapter's query, not of boards in general: GitHub's filtered read is
// `status:"X" is:open` over the same item query ListOpenItems uses, so the
// subset holds; "Done" is read WITHOUT is:open there, so it does not. An
// adapter that has not been checked simply does not implement this and keeps
// its own per-status reads. TestGitHubBoardServiceImplementsOpenStatusSubset
// pins the real adapter against this interface.
type OpenStatusSubset interface {
	StatusReadIsOpenSubset(status string) bool
}

// OpenSummaryReader is an OPTIONAL capability on a forge.BoardService: the
// board's open items with relationship COUNTS (BoardItem.RelationSummary)
// instead of lists. It is what every consumer that only asks "which status /
// blocked / epic" should read — the Repositories tree, board counts and the
// attention sweep — because on GitHub it is a conditional REST read that costs
// nothing while the board is unchanged, where the list-carrying ListOpenItems
// is a GraphQL read billed every time.
//
// It is cached as its OWN entry ("open-summary"), separate from "open": the
// two are different reads with different contents, and serving a summary to
// a caller that needs lists (the dependency graph) would silently empty its
// edges. An adapter without the capability is answered by deriving the counts
// from its ListOpenItems snapshot (see cachedBoard.ListOpenItemsSummary), so
// GitLab and test fakes behave exactly as before.
type OpenSummaryReader interface {
	ListOpenItemsSummary(ctx context.Context) ([]forgetypes.BoardItem, int, error)
}

// IdentityReporter is an OPTIONAL capability on a forge.BoardService: the
// token identity its reads are made with. The cache keys every entry by it,
// so a snapshot one token was allowed to read is never served to a caller
// holding another. An adapter without it shares one namespace per board, as
// before.
type IdentityReporter interface {
	CacheIdentity() string
}

// SummaryAvailability is an OPTIONAL refinement of OpenSummaryReader: whether
// the adapter's summary read is its own cheap read right now. When it is not
// (GitHub Enterprise Server, or a github.com owner whose REST projects surface
// answered 404) the adapter would answer the summary from its full open read
// anyway, so the cache derives the summary from its "open" entry instead of
// holding a second copy of the same read — which keeps status reads answered
// from that one snapshot exactly as before (OpenStatusSubset).
type SummaryAvailability interface {
	SummaryReadAvailable() bool
}

func summaryReadAvailable(r OpenSummaryReader) bool {
	if a, ok := r.(SummaryAvailability); ok {
		return a.SummaryReadAvailable()
	}
	return true
}

// ListOpenSummary reads the board's open items in summary form: through
// OpenSummaryReader when the board offers it (a cached board always does),
// else from ListOpenItems with the counts derived from the lists.
func ListOpenSummary(ctx context.Context, board forge.BoardService) ([]forgetypes.BoardItem, int, error) {
	if r, ok := board.(OpenSummaryReader); ok {
		return r.ListOpenItemsSummary(ctx)
	}
	items, total, err := board.ListOpenItems(ctx)
	if err != nil {
		return nil, 0, err
	}
	return summarize(items), total, nil
}

// summarize returns a copy of items with RelationSummary set from each
// item's lists (pull requests carry none).
func summarize(items []forgetypes.BoardItem) []forgetypes.BoardItem {
	out := make([]forgetypes.BoardItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].IsPR || out[i].RelationSummary != nil {
			continue
		}
		s := forgetypes.SummarizeRelations(out[i])
		out[i].RelationSummary = &s
	}
	return out
}

// Snapshot is one board read, with the time it was taken. FetchedAt is exported
// because a cache that cannot report its own age is indistinguishable from a
// cache that is lying: a consumer showing an operator "nothing is stranded" has
// to be able to say how old that claim is.
type Snapshot struct {
	Items     []forgetypes.BoardItem
	Total     int
	FetchedAt time.Time

	// VerifiedAt is when the change probe last CONFIRMED this snapshot still
	// matches the board, which is not the same claim as FetchedAt and must not
	// overwrite it. FetchedAt stays truthful about when the data was read —
	// a consumer telling an operator "nothing is stranded" is entitled to know
	// the read is eight minutes old even though the board has not moved since.
	// Freshness is computed from the later of the two; Age still reports the
	// age of the DATA. Zero when the snapshot has never been re-verified.
	VerifiedAt time.Time
}

// Age reports how long ago this snapshot was taken.
func (s Snapshot) Age(now time.Time) time.Duration { return now.Sub(s.FetchedAt) }

// servedAt is the instant freshness counts from: the read, or the last probe
// that vouched for it.
func (s Snapshot) servedAt() time.Time {
	if s.VerifiedAt.After(s.FetchedAt) {
		return s.VerifiedAt
	}
	return s.FetchedAt
}

// Cache holds board snapshots keyed by (owner, project, query). One Cache is
// shared by every consumer in a process; Wrap binds it to a particular board.
type Cache struct {
	// ttl and maxRenewedAge are DefaultTTL / MaxRenewedAge with ±20% jitter
	// applied once at construction, so the daemons on one machine do not all
	// expire the same board in the same second and re-read it in lockstep.
	ttl           time.Duration
	maxRenewedAge time.Duration
	// now is swappable so tests can drive expiry without sleeping. A test that
	// sleeps to cross a TTL is a test that is slow and flaky at the same time.
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
	// probes memoises direct ProjectUpdatedAt answers per board for ProbeTTL.
	// Keyed by board prefix alone (no query): the probe is a property of the
	// board, not of any one read. Cleared with the snapshots on Invalidate,
	// because a mutation we issued is a change the memo would otherwise hide.
	probes map[string]probeMemo
}

// probeMemo is one held probe answer and when it was taken.
type probeMemo struct {
	updatedAt time.Time
	askedAt   time.Time
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

// jitterRand supplies the uniform variate for deadline jitter. A variable so a
// test can pin it (0.5 → no jitter) and assert on exact TTLs.
var jitterRand = rand.Float64

// jitter spreads d by ±20%: d·(0.8 + 0.4·u) for u in [0, 1).
func jitter(d time.Duration, u float64) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*u))
}

// New returns a Cache with the given TTL; ttl <= 0 uses DefaultTTL. The TTL and
// the MaxRenewedAge ceiling each get ±20% jitter, fixed for the cache's life.
func New(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{
		ttl:           jitter(ttl, jitterRand()),
		maxRenewedAge: jitter(MaxRenewedAge, jitterRand()),
		now:           time.Now,
		entries:       map[string]*entry{},
		probes:        map[string]probeMemo{},
	}
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
	// Every identity's entries for the board: a write we issued changed the
	// board for every token that reads it.
	for k := range c.entries {
		if strings.HasPrefix(k, prefix) {
			delete(c.entries, k)
		}
	}
	for k := range c.probes {
		if strings.HasPrefix(k, prefix) {
			delete(c.probes, k)
		}
	}
}

// identityPrefix is the key namespace for one board as read by one token
// identity. The empty identity keeps the board's bare prefix.
func identityPrefix(owner string, project int, identity string) string {
	if identity == "" {
		return boardPrefix(owner, project)
	}
	return boardPrefix(owner, project) + "@" + identity + "|"
}

// InvalidateAll drops every board. Used when the mutating caller cannot name
// the board it touched — better a cold cache than a confidently wrong one.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*entry{}
	c.probes = map[string]probeMemo{}
}

// peekKey is Peek for a full key: the completed, successful, unexpired
// snapshot held for it, if any. Never fetches and never waits on an in-flight
// read.
func (c *Cache) peekKey(key string) (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.freshLocked(e) {
		return Snapshot{}, false
	}
	select {
	case <-e.done:
		return e.snap, e.err == nil
	default:
		return Snapshot{}, false
	}
}

// itemsWithStatus returns the items whose Status equals status,
// case-insensitively (the forge's `status:"X"` filter is), in board order.
// A fresh slice, so a caller cannot mutate the shared snapshot through it.
func itemsWithStatus(items []forgetypes.BoardItem, status string) []forgetypes.BoardItem {
	out := make([]forgetypes.BoardItem, 0, len(items))
	for _, it := range items {
		if strings.EqualFold(it.Status, status) {
			out = append(out, it)
		}
	}
	return out
}

// Peek reports the cached snapshot for a query without fetching, and whether
// one is present and unexpired. It is how a surface answers "how old is this?"
// without provoking a read.
func (c *Cache) Peek(owner string, project int, query string) (Snapshot, bool) {
	return c.peekKey(boardPrefix(owner, project) + query)
}

// freshLocked reports whether an entry may still be served. An in-flight entry
// is always "fresh" — its fetch has not returned, so there is nothing to expire
// and a second caller should wait for it rather than start a rival fetch.
func (c *Cache) freshLocked(e *entry) bool {
	select {
	case <-e.done:
		return c.now().Sub(e.snap.servedAt()) < c.ttl
	default:
		return true
	}
}

// get returns the snapshot for a key, fetching through `load` on a miss.
// Concurrent callers for one key share a single load.
//
// An EXPIRED entry is not automatically a refetch. If the board offers a change
// probe and the stale snapshot is still inside MaxRenewedAge, the probe is
// asked first, and a board that has not moved renews the snapshot for one point
// instead of re-reading it for thirty-four (#847). The renewal runs inside the
// same single-flight entry as a real load, so concurrent callers wait on one
// probe rather than each issuing their own — the same reason the entry
// machinery exists at all.
func (c *Cache) get(ctx context.Context, key string, probe func(context.Context) (time.Time, error), load func(context.Context) (Snapshot, error)) (Snapshot, error) {
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
	// Capture the outgoing snapshot BEFORE replacing the entry: it is the only
	// thing the probe can renew, and the replacement drops the map's last
	// reference to it.
	prev, renewable := c.renewableLocked(c.entries[key])
	e := &entry{done: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	snap, err := c.refresh(ctx, prev, renewable, probe, load)
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

// renewableLocked reports the snapshot an expired entry could still have
// renewed, and whether renewal is permitted at all.
//
// Three conditions, each of which would otherwise renew something that must not
// be renewed: the entry must have COMPLETED (an in-flight one has no snapshot),
// it must have succeeded (a failed read is never cached, so there is nothing to
// vouch for), and its DATA must be inside MaxRenewedAge — measured from
// FetchedAt, never from servedAt, or each renewal would extend the ceiling it
// is supposed to be bounded by and the cap would never bind.
func (c *Cache) renewableLocked(e *entry) (Snapshot, bool) {
	if e == nil {
		return Snapshot{}, false
	}
	select {
	case <-e.done:
	default:
		return Snapshot{}, false
	}
	if e.err != nil || e.snap.FetchedAt.IsZero() {
		return Snapshot{}, false
	}
	if c.now().Sub(e.snap.FetchedAt) >= c.maxRenewedAge {
		return Snapshot{}, false
	}
	return e.snap, true
}

// refresh either renews `prev` on the probe's word or loads a new snapshot.
//
// Every path that is not a confident "the board has not moved" ends in a load.
// That is #847's AC 3 and it is the property worth stating explicitly: a probe
// that errors, a probe that is not offered, a timestamp that fails to parse,
// and a board that moved all produce the SAME outcome as having no probe at
// all. The probe can only ever save a read, never suppress one.
func (c *Cache) refresh(ctx context.Context, prev Snapshot, renewable bool, probe func(context.Context) (time.Time, error), load func(context.Context) (Snapshot, error)) (Snapshot, error) {
	if renewable && probe != nil {
		if updatedAt, err := probe(ctx); err == nil && !updatedAt.IsZero() && !updatedAt.After(prev.FetchedAt) {
			prev.VerifiedAt = c.now()
			return prev, nil
		}
	}
	snap, err := load(ctx)
	snap.FetchedAt = c.now()
	snap.VerifiedAt = time.Time{}
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
	subset, _ := board.(OpenStatusSubset)
	summary, _ := board.(OpenSummaryReader)
	identity := ""
	if r, ok := board.(IdentityReporter); ok {
		identity = r.CacheIdentity()
	}
	return &cachedBoard{cache: c, inner: board, prefix: identityPrefix(owner, project, identity), identity: identity, probe: probeFor(board), openSubset: subset, summary: summary}
}

// probeFor extracts the optional change-probe capability from a board service,
// returning nil when the adapter does not offer one. Resolved ONCE at Wrap time
// rather than per call so the type assertion is not repeated on a hot path.
func probeFor(board forge.BoardService) func(context.Context) (time.Time, error) {
	cp, ok := board.(ChangeProbe)
	if !ok {
		return nil
	}
	return cp.ProjectUpdatedAt
}

type cachedBoard struct {
	cache  *Cache
	inner  forge.BoardService
	prefix string
	// probe is nil for an adapter with no cheap change signal, which is a
	// supported state and not a degraded one: every nil-probe path refetches
	// exactly as it did before #847.
	probe func(context.Context) (time.Time, error)
	// openSubset is nil for an adapter that has not promised its status reads
	// are slices of its open read; see OpenStatusSubset.
	openSubset OpenStatusSubset
	// summary is nil for an adapter with no summary read; the summary entry is
	// then derived from the "open" entry.
	summary OpenSummaryReader
	// identity is the wrapped board's token identity ("" when it reports none).
	identity string
}

// CacheIdentity passes the wrapped board's identity through, so a caller
// holding only the cached board can key its own memo the same way.
func (b *cachedBoard) CacheIdentity() string { return b.identity }

// ListOpenItemsSummary is the board's open items with relationship counts,
// cached as its own "open-summary" entry with the same TTL, single-flight and
// probe renewal as every other read.
func (b *cachedBoard) ListOpenItemsSummary(ctx context.Context) ([]forgetypes.BoardItem, int, error) {
	if b.summary == nil || !summaryReadAvailable(b.summary) {
		items, total, err := b.ListOpenItems(ctx)
		if err != nil {
			return nil, 0, err
		}
		return summarize(items), total, nil
	}
	snap, err := b.cache.get(ctx, b.prefix+"open-summary", b.probe, func(ctx context.Context) (Snapshot, error) {
		items, total, err := b.summary.ListOpenItemsSummary(ctx)
		return Snapshot{Items: items, Total: total}, err
	})
	return snap.Items, snap.Total, err
}

func (b *cachedBoard) ListOpenItems(ctx context.Context) ([]forgetypes.BoardItem, int, error) {
	snap, err := b.cache.get(ctx, b.prefix+"open", b.probe, func(ctx context.Context) (Snapshot, error) {
		items, total, err := b.inner.ListOpenItems(ctx)
		return Snapshot{Items: items, Total: total}, err
	})
	return snap.Items, snap.Total, err
}

func (b *cachedBoard) ListItems(ctx context.Context, statusFilter string) ([]forgetypes.BoardItem, error) {
	// A status read on a board that promises it is a slice of the open read is
	// answered from a FRESH open snapshot when one is held, for zero requests.
	// Only a fresh one: this never starts the (larger) open read on a status
	// read's behalf, so a caller that only ever asks for Ready pays exactly
	// what it paid before, and an expired open snapshot is not renewed here —
	// the per-status entry below keeps its own freshness and probe.
	if b.openSubset != nil && statusFilter != "" && b.openSubset.StatusReadIsOpenSubset(statusFilter) {
		if open, ok := b.cache.peekKey(b.prefix + "open"); ok {
			return itemsWithStatus(open.Items, statusFilter), nil
		}
	}
	snap, err := b.cache.get(ctx, b.prefix+"items:"+statusFilter, b.probe, func(ctx context.Context) (Snapshot, error) {
		items, err := b.inner.ListItems(ctx, statusFilter)
		return Snapshot{Items: items, Total: len(items)}, err
	})
	return snap.Items, err
}

// ProjectUpdatedAt exposes the wrapped adapter's change probe through the
// cache, so a caller holding only the cached board (every sweep producer, and
// the `board.changed` IPC verb) can still ask the one-point question. The
// answer is memoised for ProbeTTL per board — see that constant for why.
//
// The memo is consulted and written under the cache lock, but the probe itself
// runs outside it: a forge round-trip must not serialise every other board
// read in the process behind it. Two concurrent callers on a cold memo may
// therefore both probe once; the memo exists to collapse a BURST, and that
// property holds from the first answer onwards.
func (b *cachedBoard) ProjectUpdatedAt(ctx context.Context) (time.Time, error) {
	if b.probe == nil {
		return time.Time{}, ErrNoChangeProbe
	}
	b.cache.mu.Lock()
	memo, ok := b.cache.probes[b.prefix]
	now := b.cache.now()
	b.cache.mu.Unlock()
	if ok && now.Sub(memo.askedAt) < ProbeTTL {
		return memo.updatedAt, nil
	}
	updatedAt, err := b.probe(ctx)
	if err != nil {
		// A failed probe is never memoised, for the same reason a failed read
		// is never cached: "I could not look" must not become "nothing moved"
		// for the next thirty seconds.
		return time.Time{}, err
	}
	b.cache.mu.Lock()
	b.cache.probes[b.prefix] = probeMemo{updatedAt: updatedAt, askedAt: b.cache.now()}
	b.cache.mu.Unlock()
	return updatedAt, nil
}

// GetItem is NOT cached, deliberately. It is per-issue: serving it from a
// board-wide list would answer "not on the board" for an item added since the
// snapshot, which is the one answer callers act on destructively.
func (b *cachedBoard) GetItem(ctx context.Context, owner, repo string, issueNumber int) (*forgetypes.BoardItem, error) {
	return b.inner.GetItem(ctx, owner, repo, issueNumber)
}

// CountsByStatus reports how many of the board's OPEN items sit in each
// status, derived from ListOpenItems — which, on a board this package wraps, is
// the cached snapshot: zero requests inside the TTL, one 1-point probe after
// it, and a full read only when the board actually moved.
//
// This used to be a forge method, and on GitHub it was a live five-alias
// `items(query:){totalCount}` document that the cache deliberately forwarded
// (the earlier worry being a second, divergent aggregation). Measured on a
// six-repo shared board it was the single largest idle consumer of the
// GraphQL budget: the Repositories tree asked it once per repo on every
// refresh, expand and focus-regain, and nothing could collapse the calls
// because they were not reads of the snapshot. The snapshot already holds
// every open item WITH its status, so the counts are one loop over data the
// process has already paid for. There is exactly one aggregation now, and it
// is this one.
//
// Done is not reported: Done items are closed and therefore not in the
// `is:open` snapshot, and no consumer ever read the bucket — the tree wants
// Ready / In progress / Backlog, and the dashboard derives its own counts from
// the item list it already holds.
func CountsByStatus(ctx context.Context, board forge.BoardService) (*forgetypes.StatusCounts, error) {
	items, _, err := ListOpenSummary(ctx, board)
	if err != nil {
		return nil, err
	}
	var out forgetypes.StatusCounts
	for _, it := range items {
		switch {
		case strings.EqualFold(it.Status, "Ready"):
			out.Ready++
		case strings.EqualFold(it.Status, "In progress"):
			out.InProgress++
		case strings.EqualFold(it.Status, "In review"):
			out.InReview++
		case strings.EqualFold(it.Status, "Backlog"):
			out.Backlog++
		}
	}
	return &out, nil
}
