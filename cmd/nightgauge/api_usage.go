package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

// apiUsageRecord mirrors github.APILedgerRecord. It is redeclared rather than
// imported so the reader stays tolerant of ledger files written by an older
// binary: unknown fields are ignored and missing ones read as zero, which
// matters because the whole point of the ledger is comparing a window from
// before a change against one from after it.
type apiUsageRecord struct {
	TS         string `json:"ts"`
	Kind       string `json:"kind"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Op         string `json:"op"`
	Caller     string `json:"caller"`
	Status     int    `json:"status"`
	Cost       int    `json:"cost"`
	Remaining  int    `json:"remaining"`
	Cached     bool   `json:"cached"`
	DurationMs int64  `json:"duration_ms"`
}

// apiUsageGroup is one row of the report: a caller, operation, or resource
// with its share of the bill.
type apiUsageGroup struct {
	Key    string `json:"key"`
	Points int    `json:"points"`
	Calls  int    `json:"calls"`
	Cached int    `json:"cached,omitempty"`
	Errors int    `json:"errors,omitempty"`
}

const apiUsageDefaultPath = ".nightgauge/logs/github-api.jsonl"

func apiUsageCmd() *cobra.Command {
	var (
		path     string
		since    time.Duration
		byWhat   string
		resource string
		top      int
		asJSON   bool
		budget   bool
	)
	cmd := &cobra.Command{
		Use:   "api-usage",
		Short: "Report GitHub API points by caller from the request ledger",
		Long: `Aggregate the GitHub API ledger, which is written by default (#1347).

The ledger records every request at the HTTP transport with the points GitHub
actually billed it, so this report answers "what is consuming the budget?" with
measurement rather than a code audit. A ProjectV2 board read costs 17 GraphQL
points per page; a REST GET costs 1. Counting call sites cannot see that
difference — this can.

The ledger runs unattended, so the window you care about is already on disk
when you go looking — including the window in which a quota was exhausted,
which is never reproducible on demand:

  nightgauge api-usage --since 1h

Set NIGHTGAUGE_GITHUB_API_LOG=0 (or github.api_ledger.enabled: false) to switch
the ledger off; set it to a path to write somewhere other than the default
.nightgauge/logs/github-api.jsonl.`,
		SilenceUsage: true,
		Example: `  nightgauge api-usage
  nightgauge api-usage --since 30m --by op
  nightgauge api-usage --by resource --json
  nightgauge api-usage --since 1h --resource graphql   # just the pool that runs out`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if budget && (since <= 0 || since > time.Hour) {
				// The GraphQL quota resets on a rolling hour, not on whatever
				// window the operator happened to ask for — clamp the budget
				// report to that hour (regardless of an explicit --since)
				// so "remaining" means the number GitHub is actually about
				// to enforce. An unclamped wider window sums spend across
				// hours that have already reset, which reads as a false
				// exhaustion (#1428 review).
				since = time.Hour
			}
			recs, err := readAPIUsage(path, since)
			if err != nil {
				return err
			}
			if budget {
				printAPIBudget(cmd.OutOrStdout(), recs, since)
				return nil
			}
			recs = filterAPIUsageResource(recs, resource)
			if len(recs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"no ledger records%s — the workspace made no GitHub requests in that window\n",
					sinceSuffix(since))
				return nil
			}
			groups, total := groupAPIUsage(recs, byWhat)
			if asJSON {
				payload := map[string]interface{}{
					"records": len(recs),
					"points":  total,
					"by":      byWhat,
					"groups":  groups,
				}
				if resource != "" {
					// Named in the payload so a consumer cannot mistake a
					// filtered total for the whole bill — the two differ by
					// more than an order of magnitude on a normal window.
					payload["resource"] = resource
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
			}
			printAPIUsage(cmd.OutOrStdout(), recs, groups, total, byWhat, top, since)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "file", "", "Read one specific ledger file (default: the rolling set at .nightgauge/logs/github-api.jsonl)")
	cmd.Flags().DurationVar(&since, "since", 0, "Only records newer than this (e.g. 30m, 2h)")
	cmd.Flags().StringVar(&byWhat, "by", "caller", "Group by: caller, op, resource, path")
	cmd.Flags().StringVar(&resource, "resource", "",
		"Only records billed to this rate-limit pool (e.g. graphql, core). "+
			"graphql includes graphql_mutation — they share one quota")
	cmd.Flags().IntVar(&top, "top", 15, "Show at most this many rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&budget, "budget", false,
		"Report the remaining hourly GraphQL budget and what a full board read would cost against it "+
			"(clamps --since to at most 1h — the GraphQL quota window; overrides --by/--resource/--top/--json)")
	return cmd
}

// readAPIUsage loads ledger records, dropping anything older than `since`.
// A malformed line is skipped rather than fatal: the ledger is append-only
// from a live process, so a truncated final line is normal, not corruption.
func readAPIUsage(path string, since time.Duration) ([]apiUsageRecord, error) {
	var cutoff time.Time
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}

	// An explicit --file is read exactly as given — that is the archaeology
	// case, pointed at one preserved file. The DEFAULT path is the live,
	// rolling ledger (#1347), so it is read as the rolling SET: oldest backup
	// first, live file last. Reading only the live file would report a sudden
	// collapse in spending at precisely the moment spending was heavy enough
	// to rotate, which is the one window anybody opens this report for.
	var files []string
	if path != "" {
		files = []string{path}
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("api-usage: resolve working dir: %w", err)
		}
		path = filepath.Join(wd, apiUsageDefaultPath)
		files = github.LedgerFiles(path)
		if len(files) == 0 {
			return nil, fmt.Errorf("api-usage: no ledger at %s (the ledger is on by default; %s=0 switches it off)", path, apiLedgerEnvName)
		}
	}

	var out []apiUsageRecord
	for _, file := range files {
		recs, err := readAPIUsageFile(file, cutoff)
		if err != nil {
			if os.IsNotExist(err) && len(files) > 1 {
				continue // rotated away between listing and opening
			}
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// apiLedgerEnvName is the env var the ledger reads, named here for the error
// message above so the CLI and the writer cannot drift apart silently.
const apiLedgerEnvName = "NIGHTGAUGE_GITHUB_API_LOG"

func readAPIUsageFile(path string, cutoff time.Time) ([]apiUsageRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("api-usage: no ledger at %s (the ledger is on by default; %s=0 switches it off): %w",
				path, apiLedgerEnvName, os.ErrNotExist)
		}
		return nil, fmt.Errorf("api-usage: open ledger: %w", err)
	}
	defer f.Close()

	var out []apiUsageRecord
	sc := bufio.NewScanner(f)
	// Ledger lines are small, but a long GraphQL op name plus a caller path
	// can exceed bufio's 64KB default on a pathological record.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r apiUsageRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if !cutoff.IsZero() {
			ts, terr := time.Parse(time.RFC3339Nano, r.TS)
			if terr != nil || ts.Before(cutoff) {
				continue
			}
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("api-usage: read ledger: %w", err)
	}
	return out, nil
}

// filterAPIUsageResource keeps only records billed to one rate-limit pool.
//
// The pools have SEPARATE hourly quotas, so a report that sums them is not a
// budget: a live run of the always-on ledger showed a 70-point GraphQL window
// whose headline total read 2216, because REST traffic on the `core` pool was
// added in — and most of that 2216 was another process spending `core` between
// two of our calls, which derived cost cannot tell apart from our own.
//
// A consumer asking "how close am I to the GraphQL cliff?" must be able to ask
// for the GraphQL pool alone. An empty filter keeps everything, which is the
// right default for the human report (it prints a per-resource table alongside).
func filterAPIUsageResource(recs []apiUsageRecord, resource string) []apiUsageRecord {
	resource = strings.TrimSpace(strings.ToLower(resource))
	if resource == "" {
		return recs
	}
	out := recs[:0:0]
	for _, r := range recs {
		kind := strings.ToLower(r.Kind)
		// graphql and graphql_mutation are one quota, so the obvious filter
		// "graphql" must match both — otherwise every mutation silently drops
		// out of the number an operator is watching.
		if kind == resource || strings.HasPrefix(kind, resource+"_") {
			out = append(out, r)
		}
	}
	return out
}

// groupAPIUsage buckets records by the requested dimension and returns them
// most-expensive first, along with the total points across all records.
func groupAPIUsage(recs []apiUsageRecord, by string) ([]apiUsageGroup, int) {
	idx := map[string]*apiUsageGroup{}
	total := 0
	for _, r := range recs {
		key := apiUsageKey(r, by)
		g, ok := idx[key]
		if !ok {
			g = &apiUsageGroup{Key: key}
			idx[key] = g
		}
		g.Points += r.Cost
		g.Calls++
		if r.Cached {
			g.Cached++
		}
		if r.Status >= 400 {
			g.Errors++
		}
		total += r.Cost
	}
	out := make([]apiUsageGroup, 0, len(idx))
	for _, g := range idx {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Points != out[j].Points {
			return out[i].Points > out[j].Points
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		// Ties broken by key so the report is stable across runs — a report
		// whose row order shuffles cannot be diffed before against after.
		return out[i].Key < out[j].Key
	})
	return out, total
}

// apiUsageKey picks the grouping dimension. Every branch has a non-empty
// fallback: a record that could not be attributed still has to appear in the
// report, because unattributed points are exactly the ones worth chasing.
func apiUsageKey(r apiUsageRecord, by string) string {
	switch by {
	case "op":
		if r.Op != "" {
			return r.Op
		}
		return r.Method + " " + r.Path
	case "resource":
		if r.Kind != "" {
			return r.Kind
		}
		return "unknown"
	case "path":
		return r.Method + " " + r.Path
	default:
		if r.Caller != "" {
			return r.Caller
		}
		return "(unattributed)"
	}
}

func sinceSuffix(since time.Duration) string {
	if since <= 0 {
		return ""
	}
	return " in the last " + since.String()
}

// toLedgerRecords adapts the CLI's tolerant apiUsageRecord to the shared
// github.APILedgerRecord shape so printAPIBudget can hand the window to
// github.SummarizeWindow instead of re-deriving its own opinion of "how much
// is left" from summed cost alone.
func toLedgerRecords(recs []apiUsageRecord) []github.APILedgerRecord {
	out := make([]github.APILedgerRecord, len(recs))
	for i, r := range recs {
		out[i] = github.APILedgerRecord{
			TS:        r.TS,
			Kind:      r.Kind,
			Method:    r.Method,
			Path:      r.Path,
			Op:        r.Op,
			Caller:    r.Caller,
			Status:    r.Status,
			Cost:      r.Cost,
			Remaining: r.Remaining,
			Cached:    r.Cached,
		}
	}
	return out
}

// printAPIBudget answers "can I afford another board read this hour?" from
// the ledger alone — no request against GitHub is needed, which matters
// because the whole failure this guards against (#1428) is an agent
// discovering the exhaustion mid-poll, via a failing `gh pr checks`, instead
// of checking first.
//
// It reports GitHub's own observed X-RateLimit-Remaining
// (github.LedgerWindow.LowWaterRemaining), not a total derived from summing
// this process's own Cost column: the ledger cannot attribute another
// process's spend (a raw `gh` call outside the binary — precisely #1428's
// scenario), so a derived total silently ignores exactly the spend it exists
// to catch. When no record in the window carried that observation, the
// report says so instead of printing a confident number computed from zero
// evidence — an absence of counted spend is not the presence of budget.
func printAPIBudget(w io.Writer, recs []apiUsageRecord, since time.Duration) {
	fmt.Fprintf(w, "GitHub GraphQL budget — %d pts/hour\n\n", github.GraphQLHourlyLimit)

	lw := github.SummarizeWindow(toLedgerRecords(recs), time.Time{}, time.Time{})

	if lw.GraphQLCalls == 0 {
		fmt.Fprintf(w, "  No GraphQL calls observed%s in the ledger — cannot tell how much\n", sinceSuffix(since))
		fmt.Fprintf(w, "  of the hourly budget is left. This is not the same as a full budget:\n")
		fmt.Fprintf(w, "  it means the ledger made no observation in this window (a raw `gh`\n")
		fmt.Fprintf(w, "  call outside this binary would not appear here either).\n")
		return
	}
	if lw.LowWaterRemaining < 0 {
		fmt.Fprintf(w, "  %d GraphQL call(s) observed%s, but none carried GitHub's own\n", lw.GraphQLCalls, sinceSuffix(since))
		fmt.Fprintf(w, "  rate-limit header — cannot tell how much of the hourly budget is left.\n")
		return
	}

	remaining := lw.LowWaterRemaining
	pages := remaining / github.BoardReadPointsPerPage
	items := pages * github.BoardReadItemsPerPage

	fmt.Fprintf(w, "  Spent%s:      %d pts\n", sinceSuffix(since), lw.Points)
	if lw.Exhausted {
		fmt.Fprintf(w, "  Remaining (observed): 0 pts — GitHub reported this pool EXHAUSTED\n\n")
	} else {
		fmt.Fprintf(w, "  Remaining (observed): %d pts\n\n", remaining)
	}
	fmt.Fprintf(w, "  A ProjectV2 board read costs ~%d pts per %d-item page.\n",
		github.BoardReadPointsPerPage, github.BoardReadItemsPerPage)
	fmt.Fprintf(w, "  What's left affords ~%d more page(s) (~%d items) before the hour resets.\n",
		pages, items)
}

func printAPIUsage(w io.Writer, recs []apiUsageRecord, groups []apiUsageGroup, total int, by string, top int, since time.Duration) {
	byResource, _ := groupAPIUsage(recs, "resource")
	cached, gets := 0, 0
	for _, r := range recs {
		if r.Cached {
			cached++
		}
		if r.Method == "GET" {
			gets++
		}
	}

	fmt.Fprintf(w, "GitHub API ledger — %d requests%s, %d points billed\n\n", len(recs), sinceSuffix(since), total)

	fmt.Fprintf(w, "By resource:\n")
	for _, g := range byResource {
		fmt.Fprintf(w, "  %-18s %6d pts  %5d calls\n", g.Key, g.Points, g.Calls)
	}
	if gets > 0 {
		fmt.Fprintf(w, "\nConditional GETs served from cache: %d/%d (%.0f%% free)\n",
			cached, gets, float64(cached)/float64(gets)*100)
	}

	fmt.Fprintf(w, "\nBy %s:\n", by)
	for i, g := range groups {
		if top > 0 && i >= top {
			fmt.Fprintf(w, "  … and %d more\n", len(groups)-top)
			break
		}
		share := ""
		if total > 0 {
			share = fmt.Sprintf(" %4.1f%%", float64(g.Points)/float64(total)*100)
		}
		extra := ""
		if g.Cached > 0 {
			extra += fmt.Sprintf("  %d cached", g.Cached)
		}
		if g.Errors > 0 {
			extra += fmt.Sprintf("  %d errored", g.Errors)
		}
		fmt.Fprintf(w, "  %6d pts%s  %4d calls  %s%s\n", g.Points, share, g.Calls, g.Key, extra)
	}
}
