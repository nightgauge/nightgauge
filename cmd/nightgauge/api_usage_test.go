package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLedger(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	return path
}

func TestReadAPIUsageSkipsMalformedLines(t *testing.T) {
	// A live daemon appends to this file, so the last line can be a partial
	// write. Refusing to read the whole ledger over one torn record would make
	// the tool useless exactly when it is needed — during a live burn.
	path := writeLedger(t,
		`{"ts":"2026-08-24T11:00:00Z","kind":"graphql","cost":17,"caller":"a"}`,
		`{"ts":"2026-08-24T11:00:01Z","kind":"graphql","cost":1,"calle`,
		``,
		`{"ts":"2026-08-24T11:00:02Z","kind":"core","cost":1,"caller":"b"}`,
	)
	recs, err := readAPIUsage(path, 0)
	if err != nil {
		t.Fatalf("readAPIUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (the torn and blank lines skipped)", len(recs))
	}
}

func TestReadAPIUsageSinceFiltersOldRecords(t *testing.T) {
	recent := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano)
	path := writeLedger(t,
		`{"ts":"`+old+`","kind":"graphql","cost":99,"caller":"old"}`,
		`{"ts":"`+recent+`","kind":"graphql","cost":17,"caller":"new"}`,
	)
	recs, err := readAPIUsage(path, time.Hour)
	if err != nil {
		t.Fatalf("readAPIUsage: %v", err)
	}
	if len(recs) != 1 || recs[0].Caller != "new" {
		t.Fatalf("got %+v, want only the record inside the window", recs)
	}
}

func TestReadAPIUsageMissingFileNamesTheEnvVar(t *testing.T) {
	_, err := readAPIUsage(filepath.Join(t.TempDir(), "absent.jsonl"), 0)
	if err == nil {
		t.Fatal("expected an error for a missing ledger")
	}
	// The failure mode this tool will hit most often is "nobody turned it on".
	// The error has to say how, or the operator is left guessing.
	if !strings.Contains(err.Error(), "NIGHTGAUGE_GITHUB_API_LOG") {
		t.Errorf("error %q does not tell the operator how to enable the ledger", err)
	}
}

func TestGroupAPIUsageRanksByPointsNotCalls(t *testing.T) {
	// The whole point of the ledger: 2 board reads outrank 11 REST calls.
	// A report that ranked by call count would hide the actual bill.
	recs := []apiUsageRecord{
		{Caller: "board", Cost: 17, Kind: "graphql"},
		{Caller: "board", Cost: 17, Kind: "graphql"},
		{Caller: "alerts", Cost: 1, Kind: "core"},
		{Caller: "alerts", Cost: 1, Kind: "core"},
		{Caller: "alerts", Cost: 1, Kind: "core", Cached: true},
		{Caller: "alerts", Cost: 0, Kind: "core", Status: 404},
	}
	groups, total := groupAPIUsage(recs, "caller")
	if total != 37 {
		t.Errorf("total = %d, want 37", total)
	}
	if groups[0].Key != "board" {
		t.Errorf("top row = %q, want the caller with the most POINTS", groups[0].Key)
	}
	if groups[0].Points != 34 || groups[0].Calls != 2 {
		t.Errorf("board row = %d pts / %d calls, want 34/2", groups[0].Points, groups[0].Calls)
	}
	if groups[1].Cached != 1 || groups[1].Errors != 1 {
		t.Errorf("alerts row cached=%d errors=%d, want 1/1", groups[1].Cached, groups[1].Errors)
	}
}

func TestAPIUsageKeyAlwaysAttributes(t *testing.T) {
	// Unattributed points are the ones worth chasing, so no grouping may drop
	// a record for lacking the field it groups on.
	bare := apiUsageRecord{Method: "GET", Path: "/repos/o/r"}
	for _, by := range []string{"caller", "op", "resource", "path"} {
		if got := apiUsageKey(bare, by); got == "" {
			t.Errorf("apiUsageKey(by=%q) returned an empty key", by)
		}
	}
}

// The pools have separate hourly quotas. Summing them yields a number that is
// not a budget — and the first live run of the always-on ledger reported 2216
// points for a 70-point GraphQL window on exactly that arithmetic.
func TestFilterAPIUsageResource(t *testing.T) {
	recs := []apiUsageRecord{
		{Kind: "graphql", Cost: 17},
		{Kind: "graphql_mutation", Cost: 1},
		{Kind: "core", Cost: 976},
		{Kind: "GraphQL", Cost: 5}, // header casing is not guaranteed
		{Kind: "search", Cost: 2},
	}

	graphql := filterAPIUsageResource(recs, "graphql")
	if len(graphql) != 3 {
		t.Fatalf("graphql filter kept %d records, want 3 (graphql + graphql_mutation + mixed case)", len(graphql))
	}
	total := 0
	for _, r := range graphql {
		total += r.Cost
	}
	if total != 23 {
		t.Errorf("filtered points = %d, want 23 — mutations share the GraphQL quota and must not drop out", total)
	}

	if got := len(filterAPIUsageResource(recs, "core")); got != 1 {
		t.Errorf("core filter kept %d records, want 1", got)
	}
	if got := len(filterAPIUsageResource(recs, "")); got != len(recs) {
		t.Errorf("empty filter kept %d records, want all %d", got, len(recs))
	}
	if got := len(filterAPIUsageResource(recs, "nonesuch")); got != 0 {
		t.Errorf("unknown resource kept %d records, want 0", got)
	}
	// A filter must not alias the caller's backing array.
	if recs[0].Kind != "graphql" || len(recs) != 5 {
		t.Errorf("input slice was mutated: %+v", recs)
	}
}

// AC1/#1428: `nightgauge api-usage --budget` prices what remains of the
// hourly GraphQL quota and what a full board read would cost against it, so
// an agent can check before spending 7% of the hour on a board pull instead
// of discovering the exhaustion when `gh pr checks` starts failing.
func TestAPIUsageBudgetReportsRemainingAndBoardCost(t *testing.T) {
	recent := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano)
	path := writeLedger(t,
		`{"ts":"`+recent+`","kind":"graphql","cost":680,"caller":"agent"}`,
		// Outside the 1h budget window (the hourly quota resets — a 3h-old
		// spend must not count against what remains this hour).
		`{"ts":"`+old+`","kind":"graphql","cost":4000,"caller":"stale"}`,
	)

	cmd := apiUsageCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--file", path, "--budget"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("api-usage --budget: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "680 pts") {
		t.Errorf("output does not report the in-window spend (680 pts): %q", out)
	}
	if !strings.Contains(out, "4320 pts") {
		t.Errorf("output does not report the remaining budget (5000-680=4320): %q", out)
	}
	if !strings.Contains(out, "17 pts per 100-item page") {
		t.Errorf("output does not price a board read at 17 pts/100-item page: %q", out)
	}
	if strings.Contains(out, "4000") {
		t.Errorf("the 3h-stale record leaked into the 1h budget window: %q", out)
	}
}
