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
		path   string
		since  time.Duration
		byWhat string
		top    int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "api-usage",
		Short: "Report GitHub API points by caller from the request ledger",
		Long: `Aggregate the GitHub API ledger written when NIGHTGAUGE_GITHUB_API_LOG is set.

The ledger records every request at the HTTP transport with the points GitHub
actually billed it, so this report answers "what is consuming the budget?" with
measurement rather than a code audit. A ProjectV2 board read costs 17 GraphQL
points per page; a REST GET costs 1. Counting call sites cannot see that
difference — this can.

Enable the ledger, reproduce the window you care about, then read it back:

  NIGHTGAUGE_GITHUB_API_LOG=1 nightgauge serve --workspace .
  nightgauge api-usage --since 1h`,
		SilenceUsage: true,
		Example: `  nightgauge api-usage
  nightgauge api-usage --since 30m --by op
  nightgauge api-usage --by resource --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			recs, err := readAPIUsage(path, since)
			if err != nil {
				return err
			}
			if len(recs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"no ledger records%s — set NIGHTGAUGE_GITHUB_API_LOG=1 and reproduce the window\n",
					sinceSuffix(since))
				return nil
			}
			groups, total := groupAPIUsage(recs, byWhat)
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]interface{}{
					"records": len(recs),
					"points":  total,
					"by":      byWhat,
					"groups":  groups,
				})
			}
			printAPIUsage(cmd.OutOrStdout(), recs, groups, total, byWhat, top, since)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "file", "", "Ledger path (default .nightgauge/logs/github-api.jsonl)")
	cmd.Flags().DurationVar(&since, "since", 0, "Only records newer than this (e.g. 30m, 2h)")
	cmd.Flags().StringVar(&byWhat, "by", "caller", "Group by: caller, op, resource, path")
	cmd.Flags().IntVar(&top, "top", 15, "Show at most this many rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

// readAPIUsage loads ledger records, dropping anything older than `since`.
// A malformed line is skipped rather than fatal: the ledger is append-only
// from a live process, so a truncated final line is normal, not corruption.
func readAPIUsage(path string, since time.Duration) ([]apiUsageRecord, error) {
	if path == "" {
		path = apiUsageDefaultPath
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("api-usage: resolve working dir: %w", err)
		}
		path = filepath.Join(wd, path)
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("api-usage: no ledger at %s (set NIGHTGAUGE_GITHUB_API_LOG=1 to start one)", path)
		}
		return nil, fmt.Errorf("api-usage: open ledger: %w", err)
	}
	defer f.Close()

	var cutoff time.Time
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
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
