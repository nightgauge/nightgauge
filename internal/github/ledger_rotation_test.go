package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A ledger that is always on must be bounded, and the bound has to be enforced
// by the writer rather than by an operator remembering to truncate a file.
func TestAPILedgerRotatesPastMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	l := &apiLedger{path: path, prev: map[string]int{}}
	if err := l.open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(l.close)

	// Pre-fill past the threshold so a single record trips the rotation
	// without writing 5 MB one JSON line at a time.
	if err := os.WriteFile(path, make([]byte, ledgerMaxBytes+1), 0o644); err != nil {
		t.Fatalf("prefill: %v", err)
	}
	l.record(APILedgerRecord{TS: time.Now().UTC().Format(time.RFC3339Nano), Kind: "graphql", Caller: "x"}, "4999")

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated backup at %s.1: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live file: %v", err)
	}
	if info.Size() >= ledgerMaxBytes {
		t.Errorf("live ledger is %d bytes after rotation, want a fresh small file", info.Size())
	}

	// The writer must still work after rotating — a rotation that leaves the
	// encoder pointed at a closed file silently ends the instrument's life.
	l.record(APILedgerRecord{TS: time.Now().UTC().Format(time.RFC3339Nano), Kind: "graphql", Caller: "after"}, "4998")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live: %v", err)
	}
	if !strings.Contains(string(raw), `"after"`) {
		t.Errorf("post-rotation record missing from live file; got %q", string(raw))
	}
}

// Only ledgerKeepFiles-1 numbered backups may survive, or "bounded" is a
// comment rather than a property.
func TestAPILedgerKeepsBoundedBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	l := &apiLedger{path: path, prev: map[string]int{}}
	if err := l.open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(l.close)

	for i := 0; i < ledgerKeepFiles+3; i++ {
		if err := os.WriteFile(path, make([]byte, ledgerMaxBytes+1), 0o644); err != nil {
			t.Fatalf("prefill %d: %v", i, err)
		}
		// Reattach: WriteFile replaced the inode we hold open.
		l.close()
		if err := l.open(); err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		l.record(APILedgerRecord{TS: time.Now().UTC().Format(time.RFC3339Nano), Kind: "graphql"}, "4000")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != ledgerKeepFiles {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("kept %d files %v, want %d (live + %d backups)",
			len(entries), names, ledgerKeepFiles, ledgerKeepFiles-1)
	}
}

// The env var's job changes in #1347: it no longer switches the ledger ON (that
// is the default) but must still switch it OFF, and must still outrank config.
func TestLedgerEnablementPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       string
		setEnv    bool
		configOff bool
		want      bool
	}{
		{name: "unset is on", want: true},
		{name: "config off", configOff: true, want: false},
		{name: "env 0 beats default", env: "0", setEnv: true, want: false},
		{name: "env off beats default", env: "off", setEnv: true, want: false},
		{name: "env 1 beats config off", env: "1", setEnv: true, configOff: true, want: true},
		{name: "env path beats config off", env: "custom.jsonl", setEnv: true, configOff: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if tc.setEnv {
				t.Setenv(apiLedgerEnv, tc.env)
			} else {
				t.Setenv(apiLedgerEnv, "")
			}
			ledgerConfigOff.Store(tc.configOff)
			t.Cleanup(func() { ledgerConfigOff.Store(false) })

			l := openAPILedger()
			if got := l != nil; got != tc.want {
				t.Fatalf("ledger enabled = %v, want %v", got, tc.want)
			}
			if l != nil {
				l.close()
			}
		})
	}
}

// SetAPILedgerEnabled(false) must not be able to override an operator who
// asked for a ledger on the command line.
func TestSetAPILedgerEnabledRespectsEnvOverride(t *testing.T) {
	t.Setenv(apiLedgerEnv, "1")
	t.Cleanup(func() { ledgerConfigOff.Store(false) })
	SetAPILedgerEnabled(false)
	if ledgerConfigOff.Load() {
		t.Error("config disabled the ledger despite an explicit NIGHTGAUGE_GITHUB_API_LOG=1")
	}
}

// The reader must see across a rotation: a window is an hour, and an hour that
// rotated mid-way is exactly the busy hour someone is investigating.
func TestReadLedgerSinceSpansRotatedBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	now := time.Now().UTC()

	write := func(p string, recs ...APILedgerRecord) {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		enc := json.NewEncoder(f)
		for _, r := range recs {
			if err := enc.Encode(&r); err != nil {
				t.Fatalf("encode: %v", err)
			}
		}
		f.Close()
	}
	rec := func(offset time.Duration, caller string, cost int) APILedgerRecord {
		return APILedgerRecord{
			TS: now.Add(offset).Format(time.RFC3339Nano), Kind: "graphql",
			Caller: caller, Cost: cost, Remaining: 4000, Status: 200,
		}
	}
	write(path+".1", rec(-40*time.Minute, "old.Caller", 10))
	write(path, rec(-10*time.Minute, "new.Caller", 5))

	recs, err := ReadLedgerSince(path, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ReadLedgerSince: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("read %d records, want 2 (the backup's record was dropped)", len(recs))
	}
	if recs[0].Caller != "old.Caller" {
		t.Errorf("records[0].Caller = %q, want the backup's record first (oldest first)", recs[0].Caller)
	}

	w := SummarizeWindow(recs, now.Add(-time.Hour), now)
	if w.Points != 15 {
		t.Errorf("Points = %d, want 15 across the rotation boundary", w.Points)
	}
	if len(w.TopCallers) != 2 || w.TopCallers[0].Caller != "old.Caller" {
		t.Errorf("TopCallers = %+v, want old.Caller first by points", w.TopCallers)
	}
}

func TestSummarizeWindowExhaustionAndRate(t *testing.T) {
	now := time.Now().UTC()
	recs := []APILedgerRecord{
		{TS: now.Add(-50 * time.Minute).Format(time.RFC3339Nano), Kind: "graphql", Caller: "board.Read", Cost: 1700, Remaining: 3300, Status: 200},
		{TS: now.Add(-20 * time.Minute).Format(time.RFC3339Nano), Kind: "graphql", Caller: "board.Read", Cost: 3300, Remaining: 0, Status: 200},
		{TS: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), Kind: "core", Caller: "rest.Get", Cost: 1, Remaining: 4999, Status: 304, Cached: true},
		{TS: now.Add(-90 * time.Minute).Format(time.RFC3339Nano), Kind: "graphql", Caller: "outside.Window", Cost: 999, Remaining: 10, Status: 200},
	}
	w := SummarizeWindow(recs, now.Add(-time.Hour), now)

	if w.Calls != 3 {
		t.Errorf("Calls = %d, want 3 — the 90-minute-old record is outside the window", w.Calls)
	}
	if w.Points != 5000 {
		t.Errorf("Points = %d, want 5000 (GraphQL only; core is a separate budget)", w.Points)
	}
	if w.PointsByResource["core"] != 1 {
		t.Errorf("PointsByResource[core] = %d, want 1", w.PointsByResource["core"])
	}
	if !w.Exhausted || w.ExhaustedResource != "graphql" {
		t.Errorf("Exhausted = %v/%q, want true/graphql", w.Exhausted, w.ExhaustedResource)
	}
	if w.LowWaterRemaining != 0 {
		t.Errorf("LowWaterRemaining = %d, want 0", w.LowWaterRemaining)
	}
	if w.Cached != 1 {
		t.Errorf("Cached = %d, want 1", w.Cached)
	}
	if !w.OverIdleBudget() {
		t.Errorf("OverIdleBudget = false at %.0f points/hour, want true", w.PointsPerHour())
	}
	if len(w.TopCallers) == 0 || w.TopCallers[0].Caller != "board.Read" {
		t.Errorf("TopCallers = %+v, want board.Read first", w.TopCallers)
	}
}

// A window shorter than a minute must not project a five-figure hourly rate
// out of three calls — that is a threshold crossed by the divisor, not by spend.
func TestPointsPerHourIgnoresSubMinuteWindows(t *testing.T) {
	now := time.Now().UTC()
	w := SummarizeWindow(
		[]APILedgerRecord{{TS: now.Format(time.RFC3339Nano), Kind: "graphql", Cost: 100, Remaining: 4900, Status: 200}},
		now.Add(-10*time.Second), now,
	)
	if got := w.PointsPerHour(); got != 0 {
		t.Errorf("PointsPerHour = %.0f over a 10s window, want 0", got)
	}
	if w.OverIdleBudget() {
		t.Error("OverIdleBudget = true on a 10s window — a divisor artefact must not warn")
	}
}

func TestReadLedgerSinceMissingFile(t *testing.T) {
	_, err := ReadLedgerSince(filepath.Join(t.TempDir(), "absent.jsonl"), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "no GitHub API ledger") {
		t.Fatalf("err = %v, want ErrNoLedger", err)
	}
}

// The first live run of this code printed a 35-point GraphQL window whose "top
// caller" was 977 points, because REST spend on the separate `core` quota was
// summed into the same rows. Two pools with two quotas do not add up to a
// budget — and the inflated figure came from another process spending `core`
// between two of our calls, which derived cost cannot distinguish from our own.
func TestSummarizeWindowAttributesGraphQLOnly(t *testing.T) {
	now := time.Now().UTC()
	recs := []APILedgerRecord{
		{TS: now.Add(-30 * time.Minute).Format(time.RFC3339Nano), Kind: "core", Caller: "doctor.RunDoctor", Cost: 976, Remaining: 4024, Status: 200},
		{TS: now.Add(-20 * time.Minute).Format(time.RFC3339Nano), Kind: "graphql", Caller: "doctor.checkBoardPopulation", Cost: 34, Remaining: 4910, Status: 200},
		{TS: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), Kind: "graphql_mutation", Caller: "doctor.checkBoardPopulation", Cost: 1, Remaining: 4909, Status: 200},
	}
	w := SummarizeWindow(recs, now.Add(-time.Hour), now)

	if w.Points != 35 {
		t.Errorf("Points = %d, want 35 — GraphQL and graphql_mutation share one pool; core does not", w.Points)
	}
	if w.Calls != 3 || w.GraphQLCalls != 2 {
		t.Errorf("Calls/GraphQLCalls = %d/%d, want 3/2", w.Calls, w.GraphQLCalls)
	}
	if w.PointsByResource["core"] != 976 {
		t.Errorf("PointsByResource[core] = %d, want the REST spend still reported separately", w.PointsByResource["core"])
	}
	for _, c := range w.TopCallers {
		if c.Caller == "doctor.RunDoctor" {
			t.Fatalf("TopCallers includes a REST-only caller (%+v); attribution must follow Points", c)
		}
	}
	if len(w.TopCallers) != 1 || w.TopCallers[0].Points != 35 {
		t.Fatalf("TopCallers = %+v, want one caller at 35 points — the sum of the rows must equal Points", w.TopCallers)
	}
}
