package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #341. A `nightgauge autonomous run --dry-run` ran for 31 hours holding a
// scheduler slot while every doctor check passed. These tests read a CAPTURED
// process table (testdata/orphaned-processes/README.md) rather than an invented
// one: the defect lives in how real `ps` output is read, and a hand-authored
// fixture would only prove the parser agrees with whoever wrote both.

const capturedTablePath = "testdata/orphaned-processes/ps-snapshot.txt"

func loadCapturedProcessTable(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(capturedTablePath)
	if err != nil {
		t.Fatalf("read captured process table: %v", err)
	}
	raw := string(data)
	// Re-assert the properties the fixture is committed FOR, so a careless
	// re-capture cannot quietly remove them.
	if !strings.Contains(raw, "/dist/bin/nightgauge serve ") {
		t.Fatal("the capture no longer contains the extension host's serve daemon — the one process this check must never report")
	}
	for _, etime := range []string{"02-23:14:35", "20:04:18", "15:29"} {
		if !strings.Contains(raw, etime) {
			t.Fatalf("the capture no longer covers etime %q; all three ps formats must be present", etime)
		}
	}
	if strings.Contains(raw, os.Getenv("USER")) && os.Getenv("USER") != "operator" {
		t.Fatal("the capture contains a login name — re-run capture-ps-snapshot.sh")
	}
	return raw
}

// capturedServeRow returns the captured serve daemon's row.
func capturedServeRow(t *testing.T) runningProcess {
	t.Helper()
	procs, determined := parseProcessTable(loadCapturedProcessTable(t))
	if !determined {
		t.Fatal("the captured process table did not parse")
	}
	for _, p := range procs {
		if p.isNightgauge() {
			return p
		}
	}
	t.Fatal("no nightgauge process in the captured table")
	return runningProcess{}
}

// derivedRow rewrites the captured serve row into the case under test, keeping
// its real binary path and `ps`'s column shape. The capturing machine was
// clean, so every orphan-shaped row in this suite is derived — never invented
// (see the fixture README).
func derivedRow(t *testing.T, pid int, etime, args string) string {
	t.Helper()
	exe := capturedServeRow(t).firstToken()
	return fmt.Sprintf("%5d %11s %s %s", pid, etime, exe, args)
}

func TestParseProcessTable_ReadsTheCapturedTable(t *testing.T) {
	procs, determined := parseProcessTable(loadCapturedProcessTable(t))

	if !determined {
		t.Fatal("a real ps table must parse")
	}
	if len(procs) == 0 {
		t.Fatal("no rows parsed")
	}
	var serve *runningProcess
	for i, p := range procs {
		if p.isNightgauge() {
			serve = &procs[i]
		}
	}
	if serve == nil {
		t.Fatal("the nightgauge row was not recognized as a nightgauge process")
	}
	if serve.subcommand() != "serve" {
		t.Errorf("subcommand = %q, want serve (argv: %q)", serve.subcommand(), serve.Command)
	}
	// argv survives whole: the workspace path is the operator's evidence.
	if !strings.Contains(serve.Command, "--workspace") {
		t.Errorf("argv was truncated: %q", serve.Command)
	}
}

func TestParseETime_CoversEveryCapturedFormat(t *testing.T) {
	// Every input here appears verbatim in the captured table.
	tests := []struct {
		etime string
		want  time.Duration
	}{
		{"15:29", 15*time.Minute + 29*time.Second},
		{"20:04:18", 20*time.Hour + 4*time.Minute + 18*time.Second},
		{"02-23:14:35", 2*24*time.Hour + 23*time.Hour + 14*time.Minute + 35*time.Second},
	}
	for _, tt := range tests {
		got, ok := parseETime(tt.etime)
		if !ok {
			t.Errorf("parseETime(%q) failed", tt.etime)
			continue
		}
		if got != tt.want {
			t.Errorf("parseETime(%q) = %v, want %v", tt.etime, got, tt.want)
		}
	}
}

func TestParseETime_RejectsShapesPsDoesNotEmit(t *testing.T) {
	for _, etime := range []string{"", "abc", "12", "1-02:03", "1:2:3:4", "-1:00", "10:xx"} {
		if _, ok := parseETime(etime); ok {
			t.Errorf("parseETime(%q) accepted a shape ps does not emit", etime)
		}
	}
}

func TestParseProcessTable_OneMalformedRowUndeterminesTheWholeScan(t *testing.T) {
	// The row that could not be read is exactly as likely to be the leak as
	// any other, so a partial parse must not be reported as a complete one
	// (same rule as execution.ActiveWorktreeIssues).
	raw := loadCapturedProcessTable(t) + "not-a-pid       13:33 /usr/bin/whatever\n"

	procs, determined := parseProcessTable(raw)

	if determined {
		t.Fatal("a malformed row must undetermine the whole table")
	}
	if procs != nil {
		t.Errorf("an undetermined scan must return no rows, got %d", len(procs))
	}
}

func TestParseProcessTable_TruncatedRowUndeterminesTheWholeScan(t *testing.T) {
	raw := loadCapturedProcessTable(t) + "  4321       13:33\n"

	if _, determined := parseProcessTable(raw); determined {
		t.Fatal("a row with no command column must undetermine the table")
	}
}

func TestIsNightgauge_OnlyArgv0BasenameCounts(t *testing.T) {
	// argv is evidence, never ownership: a grep for the word, or an editor
	// with a nightgauge path open, is not a nightgauge process.
	tests := []struct {
		command string
		want    bool
	}{
		{"/opt/homebrew/bin/nightgauge autonomous run", true},
		{"nightgauge doctor", true},
		{"grep -r nightgauge /Users/operator/Repositories", false},
		{"/usr/bin/vim /Users/operator/Repositories/nightgauge/main.go", false},
		{"/usr/local/bin/nightgauge-helper serve", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := (runningProcess{Command: tt.command}).isNightgauge(); got != tt.want {
			t.Errorf("isNightgauge(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

// parseRows is the derived-row path every classification test uses.
func parseRows(t *testing.T, rows ...string) []runningProcess {
	t.Helper()
	procs, determined := parseProcessTable(strings.Join(rows, "\n") + "\n")
	if !determined {
		t.Fatalf("derived rows did not parse: %q", rows)
	}
	return procs
}

func TestClassifyProcesses_ExcludesSelfAndServe(t *testing.T) {
	// doctor is itself a nightgauge process and no sidecar claims it; the
	// extension host's serve daemon is long-lived by design (#388 replaces the
	// argv exception with a sidecar).
	procs := parseRows(t,
		derivedRow(t, os.Getpid(), "31:14:00", "doctor --json"),
		derivedRow(t, 4156, "10-00:00:00", "serve --workspace /Users/operator/Repositories/nightgauge"),
	)

	scan := classifyProcesses(procs, map[int]bool{}, os.Getpid())

	if len(scan.Orphans) != 0 {
		t.Fatalf("self or serve was reported as an orphan: %+v", scan.Orphans)
	}
	if scan.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1 (self must not be counted)", scan.Scanned)
	}
	if scan.Serve != 1 {
		t.Errorf("Serve = %d, want 1", scan.Serve)
	}
}

func TestClassifyProcesses_SidecarOwnedRunIsNotAnOrphan(t *testing.T) {
	procs := parseRows(t, derivedRow(t, 5150, "02-07:00:00", "pipeline run --issue 341"))

	scan := classifyProcesses(procs, map[int]bool{5150: true}, os.Getpid())

	if len(scan.Orphans) != 0 {
		t.Fatalf("a sidecar-claimed PID must not be reported: %+v", scan.Orphans)
	}
	if scan.Owned != 1 {
		t.Errorf("Owned = %d, want 1", scan.Owned)
	}
}

func TestClassifyProcesses_TheIncidentSpecimenIsAnOrphan(t *testing.T) {
	// The 31-hour `autonomous run --dry-run` nothing reported.
	procs := parseRows(t, derivedRow(t, 7788, "01-07:12:03", "autonomous run --dry-run"))

	scan := classifyProcesses(procs, map[int]bool{}, os.Getpid())

	if len(scan.Orphans) != 1 {
		t.Fatalf("the specimen was not reported: %+v", scan)
	}
	if scan.Orphans[0].PID != 7788 {
		t.Errorf("wrong PID reported: %+v", scan.Orphans[0])
	}
}

func TestClassifyProcesses_AgeFloorBoundary(t *testing.T) {
	// A transient CLI invocation and a scan race must not page the operator;
	// the floor is exactly one hour.
	tests := []struct {
		etime      string
		wantOrphan bool
	}{
		{"59:59", false},
		{"01:00:00", true},
		{"01:00:01", true},
	}
	for _, tt := range tests {
		procs := parseRows(t, derivedRow(t, 6001, tt.etime, "pipeline run --issue 341"))
		scan := classifyProcesses(procs, map[int]bool{}, os.Getpid())
		gotOrphan := len(scan.Orphans) == 1
		if gotOrphan != tt.wantOrphan {
			t.Errorf("etime %s: orphan=%v, want %v", tt.etime, gotOrphan, tt.wantOrphan)
		}
		if !tt.wantOrphan && scan.Recent != 1 {
			t.Errorf("etime %s: a too-recent process must still be counted, Recent=%d", tt.etime, scan.Recent)
		}
	}
}

func TestOrphanedProcessReport_CapsTheEnumeratedList(t *testing.T) {
	var rows []string
	for i := 0; i < maxLeaksReported+4; i++ {
		rows = append(rows, derivedRow(t, 9000+i, "05-00:00:00", "pipeline run --issue 341"))
	}

	item, warning := orphanedProcessReport(parseRows(t, rows...), map[int]bool{})

	if item.OK {
		t.Fatalf("orphans reported as healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "… and 4 more") {
		t.Errorf("the list is not capped: %q", item.Error)
	}
	if strings.Contains(item.Error, "9008") {
		t.Errorf("more than %d entries were named: %q", maxLeaksReported, item.Error)
	}
	if !strings.Contains(item.Detail, "12 orphaned") {
		t.Errorf("the counts do not carry the magnitude: %q", item.Detail)
	}
	if warning == "" {
		t.Error("an orphan must produce a warning, not just a check entry")
	}
}

func TestOrphanedProcessReport_NamesEvidenceAndRefusesToAct(t *testing.T) {
	item, _ := orphanedProcessReport(
		parseRows(t, derivedRow(t, 7788, "01-07:12:03", "autonomous run --dry-run")),
		map[int]bool{})

	for _, want := range []string{"7788", "31h", "autonomous run --dry-run", "verify and terminate manually"} {
		if !strings.Contains(item.Error, want) {
			t.Errorf("the report does not carry %q: %q", want, item.Error)
		}
	}
}

func TestOrphanedProcessReport_HealthyPathWritesAnOKEntry(t *testing.T) {
	// A carrier that writes nothing when healthy renders as no output at all,
	// which reads as "not checked" (#332). It always writes.
	procs, determined := parseProcessTable(loadCapturedProcessTable(t))
	if !determined {
		t.Fatal("the captured table did not parse")
	}

	item, warning := orphanedProcessReport(procs, map[int]bool{})

	if !item.OK {
		t.Fatalf("the captured (clean) machine must pass: %+v", item)
	}
	if warning != "" {
		t.Errorf("unexpected warning: %q", warning)
	}
	if !strings.Contains(item.Detail, "1 serve") || !strings.Contains(item.Detail, "0 orphaned") {
		t.Errorf("the healthy detail does not report its counts: %q", item.Detail)
	}
}

func TestUnverifiableProcessScan_IsNeverHealthy(t *testing.T) {
	// #296's lesson: "I could not look" must never render as "nothing is wrong".
	item, warning := unverifiableProcessScan(fmt.Errorf("exec: \"ps\": executable file not found"))

	if item.OK {
		t.Fatalf("an unverifiable scan reported healthy: %+v", item)
	}
	if !strings.Contains(item.Error, "unverifiable") {
		t.Errorf("the error does not say the scan could not run: %q", item.Error)
	}
	if !strings.Contains(item.Error, "a scan that never ran") {
		t.Errorf("the error does not explain what a clean report would assert: %q", item.Error)
	}
	if warning == "" {
		t.Error("an unverifiable scan must warn")
	}
}

func TestSidecarPIDs_ReadsEverySidecarThatCarriesAPID(t *testing.T) {
	r := newLeakRepo(t)
	r.write(".nightgauge/autonomous/state.json", `{"status":"running","pid":4001,"startedAt":"2026-08-08T00:00:00Z"}`)
	r.write(".nightgauge/pipeline/current-run.json", `{"issue_number":341,"pid":4002}`)
	r.write(".nightgauge/pipeline/run-state.json",
		`{"schema_version":"1.0","issue_number":341,"attempts":[{"run_id":"a","pid":4003},{"run_id":"b","pid":4004}]}`)

	claimed := sidecarPIDs(r.dir)

	for _, pid := range []int{4001, 4002, 4003, 4004} {
		if !claimed[pid] {
			t.Errorf("PID %d is claimed by a sidecar but was not collected", pid)
		}
	}
}

func TestSidecarPIDs_AnUnreadableSidecarNarrowsNothingAndBreaksNothing(t *testing.T) {
	// Ownership is a narrowing filter: failing to read one sidecar fails toward
	// UNOWNED, which fails toward REPORTING — safe only because this carrier
	// never acts. What it must NOT do is lose the sidecars it CAN read.
	r := newLeakRepo(t)
	r.write(".nightgauge/pipeline/current-run.json", "{ this is not json")
	r.write(".nightgauge/autonomous/state.json", `{"status":"running","pid":4001}`)

	claimed := sidecarPIDs(r.dir)

	if !claimed[4001] {
		t.Error("a corrupt sidecar suppressed a readable one")
	}
	if len(claimed) != 1 {
		t.Errorf("unexpected claims: %v", claimed)
	}
}

func TestSidecarPIDs_NoSidecarsClaimNothing(t *testing.T) {
	r := newLeakRepo(t)

	if claimed := sidecarPIDs(r.dir); len(claimed) != 0 {
		t.Errorf("a repo with no sidecars claimed PIDs: %v", claimed)
	}
}

func TestCheckOrphanedProcesses_RunsAgainstTheRealProcessTable(t *testing.T) {
	// The one test that spawns `ps`: everything above proves the rules, this
	// proves the wiring — enumerate, parse, and reach a verdict on this host.
	r := newLeakRepo(t)

	item, warning := checkOrphanedProcesses(r.dir, time.Now())

	if item.Detail == "" && item.Error == "" {
		t.Fatal("the check reported nothing at all")
	}
	if item.OK && warning != "" {
		t.Errorf("a passing check must not warn: %q", warning)
	}
	if !item.OK && warning == "" {
		t.Error("a failing check must warn")
	}
	if strings.Contains(item.Error, fmt.Sprintf("%d (", os.Getpid())) {
		t.Errorf("the check reported itself: %q", item.Error)
	}
}

func TestCapturedFixtureRedactionHoldsOnEveryRun(t *testing.T) {
	// The redaction is re-asserted here so a careless re-capture cannot land a
	// home path in a public repository unnoticed.
	raw := loadCapturedProcessTable(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to check against: %v", err)
	}
	if home != filepath.Clean("/Users/operator") && strings.Contains(raw, home) {
		t.Errorf("the captured table contains this machine's home path %q", home)
	}
}
