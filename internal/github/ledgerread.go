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

// DefaultLedgerPath returns the ledger file for a workspace.
func DefaultLedgerPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, filepath.FromSlash(apiLedgerDefaultPath))
}

// LedgerFiles returns the rolling set for path — the numbered backups oldest
// first, then the live file — skipping any that do not exist.
//
// Callers must read the backups too. The window every consumer asks about is
// the last hour, and a rotation inside that hour puts the first half of it in
// `.1`; a reader that only opens the live file reports a sudden, fictional
// drop in spending at the exact moment spending was highest enough to rotate.
func LedgerFiles(path string) []string {
	var out []string
	for i := ledgerKeepFiles - 1; i >= 1; i-- {
		p := path + "." + strconv.Itoa(i)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	if _, err := os.Stat(path); err == nil {
		out = append(out, path)
	}
	return out
}

// ErrNoLedger reports that no ledger file exists at the given path.
var ErrNoLedger = errors.New("no GitHub API ledger")

// ReadLedgerSince loads every record at or after `since` from the rolling set
// at path, oldest first.
//
// A malformed line is skipped rather than fatal: the ledger is append-only
// from live processes, so a torn final line is normal operation, not
// corruption. A record with an unparseable timestamp is kept only when the
// caller asked for everything (`since` zero) — it cannot be placed in a window
// otherwise, and guessing would silently inflate whichever window it is
// guessed into.
func ReadLedgerSince(path string, since time.Time) ([]APILedgerRecord, error) {
	files := LedgerFiles(path)
	if len(files) == 0 {
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

// OverIdleBudget reports whether the projected hourly spend is past the point
// where one workspace can exhaust the GraphQL quota by itself.
func (w LedgerWindow) OverIdleBudget() bool {
	return w.PointsPerHour() > GraphQLHourlyLimit*IdleBudgetWarnFraction
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
				}
			}
			if (r.HeaderObserved || r.Cost > 0) && r.Remaining == 0 && !w.Exhausted {
				w.Exhausted = true
				w.ExhaustedResource = resource
				w.LowWaterRemaining = 0
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
	recs, err := ReadLedgerSince(DefaultLedgerPath(workspaceRoot), since)
	if err != nil {
		return LedgerWindow{Since: since, Until: now, LowWaterRemaining: -1}, err
	}
	return SummarizeWindow(recs, since, now), nil
}
