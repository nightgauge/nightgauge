package github

// Reading the ledger back (#1347).
//
// The writer above is one half of the instrument; this is the other. It lives
// in this package, next to the record type, so that every consumer — `doctor`,
// the Action Center producer, the extension's meter — asks the same question
// of the same bytes. The alternative was three readers with three ideas of
// what "points in the last hour" means, which is how an instrument stops
// being evidence and becomes three opinions.
//
// `nightgauge api-usage` deliberately keeps its own tolerant record type (see
// cmd/nightgauge/api_usage.go): it is the archaeology tool, pointed at files
// written by older binaries. This reader serves the live checks, which only
// ever read what this binary just wrote.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// GraphQLHourlyLimit is GitHub's per-hour GraphQL point budget for a user
// token. It is the denominator for every "how much of the budget did an idle
// workspace burn?" question.
const GraphQLHourlyLimit = 5000

// BoardReadPointsPerPage is the GraphQL cost of one page of a ProjectV2 board
// read (#842's measurement, after the nested `first:` values were already
// tuned 16x down — the cost is not the query's shape, it is how many times it
// is issued). BoardReadItemsPerPage is the item count each page pulls.
//
// This is the one multiplier every "what would a full board read cost?"
// estimate needs — `nightgauge api-usage --budget` (#1428) and the sweep's
// own read-count accounting both price against it.
const (
	BoardReadPointsPerPage = 17
	BoardReadItemsPerPage  = 100
)

// IdleBudgetWarnFraction is the share of the hourly GraphQL budget a workspace
// may spend before `doctor` warns.
//
// Half, and not something tighter, because the number has to survive a
// workspace that is legitimately busy: a pipeline running real issues reads
// boards and PRs and will cross any threshold set at "idle" levels, and a
// check that fires during normal work is a check operators learn to ignore.
// Half the budget is the point past which ONE open workspace can exhaust the
// hour on its own — which is exactly the failure #1341 exists to prevent, and
// is not reachable by idle bookkeeping under any correct configuration.
const IdleBudgetWarnFraction = 0.5

// DefaultLedgerPath returns a workspace's ledger base path, in its per-clone
// logs directory (layout.CloneLogsDir). The ledger there is written in dated
// segments beside it; LedgerFiles lists them. A relative workspaceRoot is made
// absolute first. The resolver's error ("not a git repository" outside one)
// is returned, never a path somewhere else.
func DefaultLedgerPath(workspaceRoot string) (string, error) {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("api ledger: resolve %q: %w", workspaceRoot, err)
	}
	dir, err := layout.CloneLogsDir(abs)
	if err != nil {
		return "", fmt.Errorf("api ledger: %w", err)
	}
	return filepath.Join(dir, apiLedgerFileName), nil
}

// ledgerSegmentDateLayout is the date in a segment's name, a UTC day.
const ledgerSegmentDateLayout = "2006-01-02"

// ledgerSegmentPath is the segment of base for day: github-api.jsonl and
// 2026-09-29 give github-api-2026-09-29.jsonl in the same directory.
func ledgerSegmentPath(base, day string) string {
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "-" + day + ext
}

// LedgerSegmentName is the base name of the default ledger's segment for the
// UTC day of t. Log retention keeps the current day's segment as a live file.
func LedgerSegmentName(t time.Time) string {
	return ledgerSegmentPath(apiLedgerFileName, t.UTC().Format(ledgerSegmentDateLayout))
}

// LedgerFiles returns every ledger file for path, oldest first, skipping any
// that do not exist: the pre-segment files (path's numbered backups, then
// path itself), then each dated segment in date order, a segment's size
// backup before the segment.
//
// Callers must read the backups too. The window every consumer asks about is
// the last hour, and a rotation inside that hour puts the first half of it in
// `.1`; a reader that only opens the live file reports a sudden, fictional
// drop in spending at the exact moment spending was highest enough to rotate.
func LedgerFiles(path string) []string {
	return LedgerFilesSince(path, time.Time{})
}

// LedgerFilesSince is LedgerFiles without the segments wholly before since's
// UTC day. The pre-segment files are always included: their dates are
// unknown. A segment log retention deleted is simply not listed, so a range
// reaching past the oldest segment reads as absent, never as an error.
func LedgerFilesSince(path string, since time.Time) []string {
	if path == "" {
		return nil // DefaultLedgerPath could not resolve the workspace
	}
	var out []string
	withBackups := func(p string) {
		for i := ledgerKeepFiles - 1; i >= 1; i-- {
			b := p + "." + strconv.Itoa(i)
			if _, err := os.Stat(b); err == nil {
				out = append(out, b)
			}
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	withBackups(path)

	ext := filepath.Ext(path)
	prefix := strings.TrimSuffix(filepath.Base(path), ext) + "-"
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return out
	}
	minDay := ""
	if !since.IsZero() {
		minDay = since.UTC().Format(ledgerSegmentDateLayout)
	}
	var days []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ext)
		if _, perr := time.Parse(ledgerSegmentDateLayout, day); perr != nil {
			continue
		}
		if minDay != "" && day < minDay {
			continue
		}
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		withBackups(ledgerSegmentPath(path, day))
	}
	return out
}

// ErrNoLedger reports that no ledger file exists at the given path.
var ErrNoLedger = errors.New("no GitHub API ledger")

// ReadLedgerSince loads every record at or after `since` from the ledger at
// path (legacy files and the dated segments in range), oldest first.
//
// A malformed line is skipped rather than fatal: the ledger is append-only
// from live processes, so a torn final line is normal operation, not
// corruption. A record with an unparseable timestamp is kept only when the
// caller asked for everything (`since` zero) — it cannot be placed in a window
// otherwise, and guessing would silently inflate whichever window it is
// guessed into.
func ReadLedgerSince(path string, since time.Time) ([]APILedgerRecord, error) {
	files := LedgerFilesSince(path, since)
	if len(files) == 0 {
		if len(LedgerFiles(path)) > 0 {
			return nil, nil // a ledger exists; nothing in it reaches the window
		}
		return nil, fmt.Errorf("%w at %s", ErrNoLedger, path)
	}
	var out []APILedgerRecord
	for _, f := range files {
		recs, err := readLedgerFile(f, since)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // rotated away between the stat above and the open
			}
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func readLedgerFile(path string, since time.Time) ([]APILedgerRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []APILedgerRecord
	sc := bufio.NewScanner(f)
	// A GraphQL query path plus an operation name stays well inside this, but
	// the default 64 KiB bucket is small enough that one long line would abort
	// the scan and silently truncate the window.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec APILedgerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if !since.IsZero() {
			ts, terr := time.Parse(time.RFC3339Nano, rec.TS)
			if terr != nil || ts.Before(since) {
				continue
			}
		}
		out = append(out, rec)
	}
	// A scan error (including a torn read on a file being rotated) leaves what
	// was read intact rather than discarding a whole window.
	return out, nil
}

// LedgerCallerSpend is one caller's share of a window's bill.
type LedgerCallerSpend struct {
	Caller string `json:"caller"`
	Points int    `json:"points"`
	Calls  int    `json:"calls"`
}

// LedgerWindow is the aggregate every live consumer of the ledger reads.
type LedgerWindow struct {
	// Since and Until bound the window the numbers below describe.
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// Calls is every request in the window, Cached the subset GitHub answered
	// 304. A high cached ratio is the signal that conditional requests work.
	Calls  int `json:"calls"`
	Cached int `json:"cached"`
	Errors int `json:"errors"`
	// Points is GraphQL points only — the budget that actually runs out here.
	// PointsByResource carries the rest (core, search) unaggregated, because
	// they are separate budgets and summing them means nothing.
	Points           int            `json:"points"`
	PointsByResource map[string]int `json:"points_by_resource"`
	// GraphQLCalls is the subset of Calls billed to the GraphQL pool — the
	// denominator that goes with Points. Reporting Points over Calls mixes a
	// GraphQL-only numerator with an all-resource denominator, which is how a
	// coherent summary starts reading like a broken one.
	GraphQLCalls int `json:"graphql_calls"`
	// TopCallers attributes the GRAPHQL points above, and only those.
	//
	// Mixing resources here produced a summary that contradicted itself in the
	// first live run of this code: a 35-point GraphQL window whose top caller
	// was listed at 977 points, because REST spend on the `core` pool was
	// summed into the same row. The two pools have separate quotas, so adding
	// them produces a number that is not a budget at all — and the inflated
	// figure came from cross-process drift (another tool spending `core`
	// between two of our calls), which derived cost cannot tell apart from our
	// own spending. GraphQL is the pool that actually runs out here; it is the
	// pool this attributes.
	//
	// Descending by points, then by calls, then by name so the order is stable
	// for a check that must not flap between runs.
	TopCallers []LedgerCallerSpend `json:"top_callers"`
	// Exhausted records that some request in the window saw a zero remaining
	// budget. This is the observation the whole feature exists to keep: it is
	// true after the fact, from the file, without anyone having predicted the
	// outage in time to switch an instrument on.
	Exhausted         bool      `json:"exhausted"`
	ExhaustedResource string    `json:"exhausted_resource,omitempty"`
	ExhaustedAt       time.Time `json:"exhausted_at,omitempty"`
	// LowWaterRemaining is the smallest remaining GraphQL budget observed, or
	// -1 when no record in the window carried one.
	LowWaterRemaining int `json:"low_water_remaining"`
	// LowWaterIdentity names the identity whose GraphQL bucket reported
	// LowWaterRemaining (#2087): App installations and personal tokens have
	// separate buckets, so "which one is under pressure" is part of the
	// evidence. ExhaustedIdentity is the same for the exhaustion.
	LowWaterIdentity  string `json:"low_water_identity,omitempty"`
	ExhaustedIdentity string `json:"exhausted_identity,omitempty"`
	// Buckets is one entry per rate-limit bucket (identity, resource, reset)
	// seen in the window (#2692). Parallel buckets each count down from their
	// own ceiling, so budget is judged per bucket, never summed against one
	// limit. Sorted by identity, resource, reset.
	Buckets []LedgerBucket `json:"buckets"`
}

// LedgerBucket is one rate-limit bucket's activity in a window.
//
// Drain is High - Low over records whose header was observed. It comes from
// GitHub's own Remaining, so it is correct even for records whose Cost was
// inflated by a writer that priced across buckets (before #2692).
type LedgerBucket struct {
	Identity string `json:"identity"`
	Resource string `json:"resource"`
	Reset    int64  `json:"reset,omitempty"`
	// Limit is the bucket's ceiling; GraphQLHourlyLimit when no record in the
	// window carried one.
	Limit int `json:"limit"`
	Calls int `json:"calls"`
	High  int `json:"high"`
	Low   int `json:"low"`
	Drain int `json:"drain"`
	// observed is false until a record with a real Remaining is seen.
	observed bool
}

// ProjectedPerHour projects the bucket's drain to an hourly rate over the
// overlap of [since, until] with the bucket's hour [reset-1h, reset] (the
// whole window when Reset is unknown), dividing by at least
// bucketProjectionFloor. Returns 0 under one minute, the same artefact guard
// as PointsPerHour.
func (b LedgerBucket) ProjectedPerHour(since, until time.Time) float64 {
	from, to := since, until
	if b.Reset != 0 {
		end := time.Unix(b.Reset, 0)
		if start := end.Add(-time.Hour); start.After(from) {
			from = start
		}
		if end.Before(to) {
			to = end
		}
	}
	d := to.Sub(from)
	if d < time.Minute {
		return 0
	}
	// A bucket that reset minutes ago holds only its startup burst; scaling
	// that slice to an hour invents a rate (1600 points in 5 min reads as
	// 19200/h). Divide by at least bucketProjectionFloor instead.
	if d < bucketProjectionFloor {
		d = bucketProjectionFloor
	}
	return float64(b.Drain) / d.Hours()
}

// bucketProjectionFloor is the shortest span a bucket's drain is projected
// over. Shorter overlaps are startup bursts, not a sustained rate.
const bucketProjectionFloor = 45 * time.Minute

// HottestBucket returns the GraphQL bucket whose projection is the largest
// fraction of its own limit, and false when there is none.
func (w LedgerWindow) HottestBucket() (LedgerBucket, bool) {
	var best LedgerBucket
	bestFrac, found := -1.0, false
	for _, b := range w.Buckets {
		if !isGraphQLResource(b.Resource) || b.Limit <= 0 {
			continue
		}
		if f := b.ProjectedPerHour(w.Since, w.Until) / float64(b.Limit); f > bestFrac {
			best, bestFrac, found = b, f, true
		}
	}
	return best, found
}

// GraphQLResource is the X-RateLimit-Resource value GitHub bills GraphQL
// queries to. Mutations are billed to "graphql_mutation" against the same
// hourly pool, so both count toward Points.
const GraphQLResource = "graphql"

// PointsPerHour projects the window's GraphQL spend to an hourly rate.
//
// Returns 0 for a window shorter than a minute: dividing a handful of calls by
// a few seconds produces a five-figure "rate" that is an artefact of the
// divisor, and a warning threshold crossed by an artefact is a false alarm
// that trains operators to ignore the real one.
func (w LedgerWindow) PointsPerHour() float64 {
	d := w.Until.Sub(w.Since)
	if d < time.Minute {
		return 0
	}
	return float64(w.Points) / d.Hours()
}

// OverIdleBudget reports whether some GraphQL bucket's projected hourly drain
// is past IdleBudgetWarnFraction of that bucket's own limit (#2692). Parallel
// buckets are judged separately, never summed against one limit.
func (w LedgerWindow) OverIdleBudget() bool {
	b, ok := w.HottestBucket()
	return ok && b.ProjectedPerHour(w.Since, w.Until) > float64(b.Limit)*IdleBudgetWarnFraction
}

// SummarizeWindow aggregates records into the window [since, until].
//
// Records outside the bounds are dropped here as well as in the reader, so a
// caller that read everything (since zero) can still ask about one hour of it
// without a second pass over the disk.
func SummarizeWindow(recs []APILedgerRecord, since, until time.Time) LedgerWindow {
	w := LedgerWindow{
		Since:             since,
		Until:             until,
		PointsByResource:  map[string]int{},
		LowWaterRemaining: -1,
	}
	byCaller := map[string]*LedgerCallerSpend{}
	buckets := map[ledgerBucketKey]*LedgerBucket{}
	for _, r := range recs {
		ts, err := time.Parse(time.RFC3339Nano, r.TS)
		if err == nil {
			if !since.IsZero() && ts.Before(since) {
				continue
			}
			if !until.IsZero() && ts.After(until) {
				continue
			}
		}
		w.Calls++
		if r.Cached {
			w.Cached++
		}
		if r.Status >= 400 || r.Status == 0 {
			w.Errors++
		}
		resource := r.Kind
		if resource == "" {
			resource = "unknown"
		}
		w.PointsByResource[resource] += r.Cost
		bk := ledgerBucketKey{Identity: LedgerIdentity(r.Identity), Kind: resource, Reset: r.Reset}
		b, ok := buckets[bk]
		if !ok {
			b = &LedgerBucket{Identity: bk.Identity, Resource: resource, Reset: r.Reset}
			buckets[bk] = b
		}
		b.Calls++
		if r.Limit > b.Limit {
			b.Limit = r.Limit
		}
		// Same acceptance rule as the low-water mark below.
		if r.HeaderObserved || r.Remaining > 0 || r.Cost > 0 {
			if !b.observed || r.Remaining > b.High {
				b.High = r.Remaining
			}
			if !b.observed || r.Remaining < b.Low {
				b.Low = r.Remaining
			}
			b.observed = true
			b.Drain = b.High - b.Low
		}
		isGraphQL := isGraphQLResource(resource)
		if isGraphQL {
			w.GraphQLCalls++
			w.Points += r.Cost
			if r.Remaining >= 0 && (w.LowWaterRemaining < 0 || r.Remaining < w.LowWaterRemaining) {
				// Remaining is only meaningful when the response actually
				// carried the header; a record without one leaves Remaining
				// at its zero value, which would otherwise read as
				// "exhausted". HeaderObserved is the signal #1452 added for
				// the case Cost and Status cannot cover on their own: a
				// cached (304) hit legitimately has Cost == 0 while still
				// carrying a real, current Remaining value.
				//
				// A record written by a binary built before HeaderObserved
				// existed decodes it as false regardless — but its nonzero
				// Remaining or Cost still proves a header really was parsed
				// at write time (record() in apiledger.go only ever sets
				// either field inside the header-parse branch), so both are
				// accepted here as additional signals rather than dropped.
				// Trusting only HeaderObserved would read every ledger
				// record from a pre-upgrade binary as unknown quota, and
				// since several nightgauge processes can share one
				// workspace ledger, a lagging daemon keeps writing such
				// records indefinitely.
				if r.HeaderObserved || r.Remaining > 0 || r.Cost > 0 {
					w.LowWaterRemaining = r.Remaining
					w.LowWaterIdentity = LedgerIdentity(r.Identity)
				}
			}
			if (r.HeaderObserved || r.Cost > 0) && r.Remaining == 0 && !w.Exhausted {
				w.Exhausted = true
				w.ExhaustedResource = resource
				w.ExhaustedIdentity = LedgerIdentity(r.Identity)
				w.LowWaterRemaining = 0
				w.LowWaterIdentity = w.ExhaustedIdentity
				if err == nil {
					w.ExhaustedAt = ts
				}
			}
		}
		if !isGraphQL {
			continue // attribution follows Points, which is GraphQL-only
		}
		caller := r.Caller
		if caller == "" {
			caller = "unattributed"
		}
		c, ok := byCaller[caller]
		if !ok {
			c = &LedgerCallerSpend{Caller: caller}
			byCaller[caller] = c
		}
		c.Points += r.Cost
		c.Calls++
	}
	for _, c := range byCaller {
		w.TopCallers = append(w.TopCallers, *c)
	}
	for _, b := range buckets {
		if b.Limit == 0 {
			b.Limit = GraphQLHourlyLimit
		}
		w.Buckets = append(w.Buckets, *b)
	}
	sort.Slice(w.Buckets, func(i, j int) bool {
		a, b := w.Buckets[i], w.Buckets[j]
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		if a.Resource != b.Resource {
			return a.Resource < b.Resource
		}
		return a.Reset < b.Reset
	})
	sort.Slice(w.TopCallers, func(i, j int) bool {
		a, b := w.TopCallers[i], w.TopCallers[j]
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return a.Caller < b.Caller
	})
	return w
}

func isGraphQLResource(resource string) bool {
	return resource == GraphQLResource || strings.HasPrefix(resource, GraphQLResource+"_")
}

// ReadWindow is the one call a live consumer needs: the last `d` of the
// workspace's ledger, summarized.
func ReadWindow(workspaceRoot string, d time.Duration, now time.Time) (LedgerWindow, error) {
	since := now.Add(-d)
	path, err := DefaultLedgerPath(workspaceRoot)
	if err != nil {
		return LedgerWindow{Since: since, Until: now, LowWaterRemaining: -1}, err
	}
	recs, err := ReadLedgerSince(path, since)
	if err != nil {
		return LedgerWindow{Since: since, Until: now, LowWaterRemaining: -1}, err
	}
	return SummarizeWindow(recs, since, now), nil
}
