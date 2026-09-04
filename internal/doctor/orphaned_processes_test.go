package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
		t.Fatal("the capture no longer contains the extension host's serve daemon — the long-lived process whose ownership this check has to get right")
	}
	for _, etime := range []string{"02-23:14:35", "20:04:18", "15:29"} {
		if !strings.Contains(raw, etime) {
			t.Fatalf("the capture no longer covers etime %q; all three ps formats must be present", etime)
		}
	}
	// The capturing account's login, asserted WITHOUT naming it and without
	// consulting the environment: the redaction rewrites the capturing
	// machine's home to /Users/operator, so any other /Users/<login> segment
	// is an un-redacted login — the capturing account's included. Spelling
	// that login out here would inscribe in a public repository the exact
	// thing the redaction exists to keep out of it.
	for _, login := range userPathLogins(raw) {
		if login != "operator" {
			t.Fatalf("the capture contains an un-redacted login %q — re-run capture-ps-snapshot.sh", login)
		}
	}
	// …and this machine's login, when there is one. Guarded on non-empty
	// because strings.Contains(raw, "") is TRUE: with USER unset (CI, most
	// containers) the unguarded form failed every test in this file.
	if u := os.Getenv("USER"); u != "" && u != "operator" && strings.Contains(raw, u) {
		t.Fatal("the capture contains a login name — re-run capture-ps-snapshot.sh")
	}
	return raw
}

// userPathLogins returns every login appearing as a `/Users/<login>` segment.
func userPathLogins(raw string) []string {
	var logins []string
	for _, seg := range strings.Split(raw, "/Users/")[1:] {
		end := strings.IndexAny(seg, "/ \t\n")
		if end < 0 {
			end = len(seg)
		}
		if end > 0 {
			logins = append(logins, seg[:end])
		}
	}
	return logins
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

func TestClassifyProcesses_ExcludesSelfAndClaimsAHeartbeatingServeDaemon(t *testing.T) {
	// doctor is itself a nightgauge process and no sidecar claims it. The
	// extension host's serve daemon IS long-lived by design — ten days is
	// ordinary — but #388 makes it say so in a file rather than in its argv:
	// a fresh heartbeat carries it through as ordinary Owned, with no
	// serve-shaped rule anywhere in the classifier.
	r := newLeakRepo(t)
	writeServeSidecar(t, r.dir, 4156, 300*time.Hour, 5*time.Minute)
	procs := parseRows(t,
		derivedRow(t, os.Getpid(), "31:14:00", "doctor --json"),
		derivedRow(t, 4156, "10-00:00:00", "serve --workspace /Users/operator/Repositories/nightgauge"),
	)

	scan := classifyProcesses(procs, sidecarPIDs(r.dir, scanClock), os.Getpid())

	if len(scan.Orphans) != 0 {
		t.Fatalf("self or a heartbeating serve daemon was reported as an orphan: %+v", scan.Orphans)
	}
	if scan.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1 (self must not be counted)", scan.Scanned)
	}
	if scan.Owned != 1 {
		t.Errorf("Owned = %d, want 1 — the serve daemon must be carried by its sidecar, not by a named exception", scan.Owned)
	}
}

func TestClassifyProcesses_AServeDaemonWithNoSidecarIsAnOrphan(t *testing.T) {
	// #388, the visibility this carrier existed for and did not have: a serve
	// daemon that outlived its extension host is the literal "everything looks
	// stopped but something is still running" symptom, and the argv exception
	// excepted it right along with the healthy ones. With the exception gone,
	// serve is claimed by a sidecar or it is reported like anything else.
	procs := parseRows(t,
		derivedRow(t, os.Getpid(), "31:14:00", "doctor --json"),
		derivedRow(t, 4156, "10-00:00:00", "serve --workspace /Users/operator/Repositories/nightgauge"),
	)

	scan := classifyProcesses(procs, map[int]bool{}, os.Getpid())

	if len(scan.Orphans) != 1 {
		t.Fatalf("an unclaimed ten-day serve daemon was not reported: %+v", scan)
	}
	if scan.Orphans[0].PID != 4156 {
		t.Errorf("wrong PID reported: %+v", scan.Orphans[0])
	}
}

func TestClassifyProcesses_AServeDaemonWhoseHeartbeatWentColdIsAnOrphan(t *testing.T) {
	// The wedged / SIGKILL'd shape, and the reason the sidecar heartbeats at
	// all. A write-once pid+started_at record would look exactly like this
	// after a day, so a live daemon would read as leaked every time; a daemon
	// whose heartbeat actually stopped is the case that must read as leaked.
	// One doctrine tells them apart — progress — with no serve-specific rule.
	r := newLeakRepo(t)
	writeServeSidecar(t, r.dir, 4156, 300*time.Hour, 31*time.Hour)
	procs := parseRows(t, derivedRow(t, 4156, "10-00:00:00", "serve --workspace /Users/operator/Repositories/nightgauge"))

	claimed := sidecarPIDs(r.dir, scanClock)
	scan := classifyProcesses(procs, claimed, os.Getpid())

	if claimed[4156] {
		t.Error("a sidecar 31 hours cold still claimed its PID — ownership is progress, not presence")
	}
	if len(scan.Orphans) != 1 || scan.Orphans[0].PID != 4156 {
		t.Fatalf("the wedged daemon was not reported: %+v", scan)
	}
}

func TestSidecarPIDs_AServeSidecarLeftByADeadDaemonClaimsNothingThatRuns(t *testing.T) {
	// A SIGKILL leaves the file behind. While its heartbeat is still fresh the
	// claim stands — this reader never probes liveness, the same accepted edge
	// as every recycled PID — but it can only ever cover the PID it names, so
	// an abandoned sidecar narrows nothing about the processes actually
	// running, and expires on its own within staleSidecarClaim.
	r := newLeakRepo(t)
	writeServeSidecar(t, r.dir, 4156, time.Hour, 5*time.Minute)
	procs := parseRows(t,
		derivedRow(t, os.Getpid(), "31:14:00", "doctor --json"),
		derivedRow(t, 7788, "01-07:12:03", "autonomous run --dry-run"),
	)

	claimed := sidecarPIDs(r.dir, scanClock)
	scan := classifyProcesses(procs, claimed, os.Getpid())

	if len(claimed) != 1 || !claimed[4156] {
		t.Fatalf("the abandoned sidecar claimed something other than its own PID: %v", claimed)
	}
	if scan.Owned != 0 {
		t.Errorf("Owned = %d, want 0 — the dead daemon's PID belongs to no running process", scan.Owned)
	}
	if len(scan.Orphans) != 1 || scan.Orphans[0].PID != 7788 {
		t.Fatalf("the abandoned sidecar suppressed an unrelated orphan: %+v", scan)
	}
}

func TestSidecarPIDs_ClaimsAServeDaemonServingAnyWorkspaceOnThisMachine(t *testing.T) {
	// The location defect the #388 review found. `ps -axo` enumerates the WHOLE
	// machine, but the sidecar walk only ever visits the INVOKING workspace's
	// roots — so a serve daemon belonging to any other workspace on the box (or
	// the primary one, when doctor is run from a sibling repo whose upward walk
	// never reaches the workspace marker) was a live claim doctor could not
	// see, and got reported as an orphan on every run past the 1h floor. The
	// claim store is machine-global for exactly this reason, and this reader
	// must load it whatever directory doctor was started in.
	r := newLeakRepo(t)
	elsewhere := filepath.Join(t.TempDir(), "some-other-workspace")
	writeServeSidecar(t, elsewhere, 4156, 300*time.Hour, 5*time.Minute)

	claimed := sidecarPIDs(r.dir, scanClock)

	if !claimed[4156] {
		t.Fatalf("a heartbeating serve daemon for %s was not claimed by a doctor run in %s — it would be reported as an orphan on every run", elsewhere, r.dir)
	}
}

func TestStaleServeClaims_NameTheWorkspaceAColdClaimBelongedTo(t *testing.T) {
	// The claim's file name is a hash, so workspace_root is the only thing that
	// can tell an operator WHICH daemon went cold — and a cold serve claim on a
	// process that is still running is the exact specimen #388 exists to
	// surface. A claim still making progress is not stale and must not be
	// attributed to anything.
	r := newLeakRepo(t)
	cold := filepath.Join(t.TempDir(), "abandoned-workspace")
	warm := filepath.Join(t.TempDir(), "healthy-workspace")
	writeServeSidecar(t, cold, 4156, 300*time.Hour, 31*time.Hour)
	writeServeSidecar(t, warm, 4157, 300*time.Hour, 5*time.Minute)

	stale := staleServeClaims(scanClock)

	if got := stale[4156]; got != cold {
		t.Errorf("the cold claim was attributed to %q, want %q", got, cold)
	}
	if _, ok := stale[4157]; ok {
		t.Errorf("a heartbeating daemon was reported as a stale claim: %v", stale)
	}
	// …and it reaches the operator, which is the only place attribution counts.
	procs := parseRows(t, derivedRow(t, 4156, "10-00:00:00", "serve --workspace "+cold))
	_, warning := orphanedProcessReport(procs, sidecarPIDs(r.dir, scanClock), stale, nil)
	if !strings.Contains(warning, cold) {
		t.Errorf("the orphan report does not name the workspace whose claim went cold: %q", warning)
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

// scanClock is the fixed `now` the sidecar-claim tests date their fixtures
// against. Fixed rather than time.Now() so a stamp written 24h-minus-a-second
// ago cannot drift across the boundary while the test runs.
var scanClock = time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)

// stamp renders an RFC3339 timestamp `ago` before scanClock.
func stamp(ago time.Duration) string {
	return scanClock.Add(-ago).Format(time.RFC3339)
}

// isolateMachineState points the per-user machine-global state root
// (os.UserHomeDir → $HOME, the same seam internal/ipc and the binary-resolution
// suite use) at a temp dir.
//
// Mandatory for every test that reads claims: since #388 the serve daemons'
// claims live in <home>/.nightgauge/serve/, so without this a developer's own
// running daemon would leak into the fixtures — and, worse, a test that plants
// a claim would write into the real directory and could delete a live one.
func isolateMachineState(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// writeServeSidecar plants a serve daemon's marker (#388) through the
// PRODUCTION writer rather than a JSON literal. Every other fixture in this
// file is a captured or derived artifact for the same reason: a hand-authored
// sidecar proves only that this file agrees with itself, and the shape the
// daemon actually writes is precisely what the reader below has to survive.
//
// root is the WORKSPACE the daemon serves; the file itself lands in the
// machine-global claim directory under the isolated HOME, keyed by a hash of
// that path — which is the whole point of the relocation, so tests must not
// look for it beside the workspace.
func writeServeSidecar(t *testing.T, root string, pid int, startedAgo, heartbeatAgo time.Duration) {
	t.Helper()
	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID:             pid,
		StartedAt:       scanClock.Add(-startedAgo),
		LastHeartbeatAt: scanClock.Add(-heartbeatAgo),
	}); err != nil {
		t.Fatalf("write serve sidecar: %v", err)
	}
}

func TestClassifyProcesses_TheIncidentSpecimenIsAnOrphan(t *testing.T) {
	// The 31-hour `autonomous run --dry-run` nothing reported — reproduced
	// through the REAL claim path, because the claim is where the defect was:
	// the scheduler writes its OWN pid into state.json (autonomous.go), so a
	// presence test made the specimen vouch for itself and read as owned
	// forever. Its progress stamp is 31 hours cold.
	r := newLeakRepo(t)
	r.write(".nightgauge/autonomous/state.json", fmt.Sprintf(
		`{"status":"running","pid":7788,"startedAt":%q,"lastScanAt":%q}`, stamp(31*time.Hour), stamp(31*time.Hour)))
	procs := parseRows(t, derivedRow(t, 7788, "01-07:12:03", "autonomous run --dry-run"))

	claimed := sidecarPIDs(r.dir, scanClock)
	scan := classifyProcesses(procs, claimed, os.Getpid())

	if claimed[7788] {
		t.Error("a 31-hour-cold sidecar still claimed its own PID — ownership is presence, not progress")
	}
	if len(scan.Orphans) != 1 {
		t.Fatalf("the specimen was not reported: %+v", scan)
	}
	if scan.Orphans[0].PID != 7788 {
		t.Errorf("wrong PID reported: %+v", scan.Orphans[0])
	}
}

func TestSidecarPIDs_AClaimCountsOnlyWhileTheSidecarIsMakingProgress(t *testing.T) {
	// One rule, three sidecars: a claim is believed only while the file that
	// makes it shows recent progress. Each case names the timestamp its writer
	// actually records (orchestrator.AutonomousState.lastScanAt,
	// orchestrator.CurrentRunSidecar.stage_started_at, runstate.updated_at)
	// and the start-of-life stamp it falls back to.
	tests := []struct {
		name      string
		path      string
		body      string
		wantClaim bool
	}{
		{
			name:      "scheduler scanned minutes ago",
			path:      ".nightgauge/autonomous/state.json",
			body:      fmt.Sprintf(`{"pid":4001,"startedAt":%q,"lastScanAt":%q}`, stamp(300*time.Hour), stamp(5*time.Minute)),
			wantClaim: true,
		},
		{
			name:      "scheduler has not scanned in 31h",
			path:      ".nightgauge/autonomous/state.json",
			body:      fmt.Sprintf(`{"pid":4001,"startedAt":%q,"lastScanAt":%q}`, stamp(31*time.Hour), stamp(31*time.Hour)),
			wantClaim: false,
		},
		{
			// The first write leaves lastScanAt empty — observed live.
			name:      "no scan yet, started just now",
			path:      ".nightgauge/autonomous/state.json",
			body:      fmt.Sprintf(`{"pid":4001,"startedAt":%q,"lastScanAt":""}`, stamp(2*time.Minute)),
			wantClaim: true,
		},
		{
			name:      "no scan yet, started 31h ago",
			path:      ".nightgauge/autonomous/state.json",
			body:      fmt.Sprintf(`{"pid":4001,"startedAt":%q,"lastScanAt":""}`, stamp(31*time.Hour)),
			wantClaim: false,
		},
		{
			name:      "no timestamps at all",
			path:      ".nightgauge/autonomous/state.json",
			body:      `{"pid":4001}`,
			wantClaim: false,
		},
		{
			name:      "unparsable timestamp",
			path:      ".nightgauge/autonomous/state.json",
			body:      `{"pid":4001,"startedAt":"yesterday","lastScanAt":"soon"}`,
			wantClaim: false,
		},
		{
			name:      "run sidecar entered its stage an hour ago",
			path:      ".nightgauge/pipeline/current-run.json",
			body:      fmt.Sprintf(`{"issue_number":341,"pid":4001,"started_at":%q,"stage_started_at":%q}`, stamp(300*time.Hour), stamp(time.Hour)),
			wantClaim: true,
		},
		{
			name:      "run sidecar stuck in one stage for 31h",
			path:      ".nightgauge/pipeline/current-run.json",
			body:      fmt.Sprintf(`{"issue_number":341,"pid":4001,"started_at":%q,"stage_started_at":%q}`, stamp(40*time.Hour), stamp(31*time.Hour)),
			wantClaim: false,
		},
		{
			name:      "run sidecar with no stage stamp yet, just started",
			path:      ".nightgauge/pipeline/current-run.json",
			body:      fmt.Sprintf(`{"issue_number":341,"pid":4001,"started_at":%q}`, stamp(time.Minute)),
			wantClaim: true,
		},
		{
			name:      "run-state updated minutes ago",
			path:      ".nightgauge/pipeline/run-state.json",
			body:      fmt.Sprintf(`{"schema_version":"1.0","issue_number":341,"created_at":%q,"updated_at":%q,"attempts":[{"run_id":"a","pid":4001}]}`, stamp(300*time.Hour), stamp(9*time.Minute)),
			wantClaim: true,
		},
		{
			name:      "run-state untouched for 31h",
			path:      ".nightgauge/pipeline/run-state.json",
			body:      fmt.Sprintf(`{"schema_version":"1.0","issue_number":341,"created_at":%q,"updated_at":%q,"attempts":[{"run_id":"a","pid":4001}]}`, stamp(31*time.Hour), stamp(31*time.Hour)),
			wantClaim: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newLeakRepo(t)
			r.write(tt.path, tt.body)

			claimed := sidecarPIDs(r.dir, scanClock)

			if got := claimed[4001]; got != tt.wantClaim {
				t.Errorf("claimed = %v, want %v (%s: %s)", got, tt.wantClaim, tt.path, tt.body)
			}
		})
	}
}

func TestSidecarPIDs_TheClaimWindowIsWiderThanTheProcessAgeFloor(t *testing.T) {
	// A scheduler on a long --interval is idle, not leaked: its scan cadence is
	// operator-configurable and may legally exceed the 1h process-age floor, so
	// the claim window must not be that floor. It is staleWorktreeAge (24h).
	if staleSidecarClaim <= staleProcessAge {
		t.Fatalf("staleSidecarClaim (%v) must exceed staleProcessAge (%v)", staleSidecarClaim, staleProcessAge)
	}
	if staleSidecarClaim != staleWorktreeAge {
		t.Errorf("staleSidecarClaim = %v, want it to mirror staleWorktreeAge (%v)", staleSidecarClaim, staleWorktreeAge)
	}

	r := newLeakRepo(t)
	r.write(".nightgauge/autonomous/state.json", fmt.Sprintf(
		`{"pid":4001,"startedAt":%q,"lastScanAt":%q}`, stamp(300*time.Hour), stamp(3*time.Hour)))

	if !sidecarPIDs(r.dir, scanClock)[4001] {
		t.Error("a scheduler that scanned 3h ago was not credited — a long --interval must not read as a leak")
	}
}

func TestSidecarPIDs_ClaimsTheWorkspaceRootScheduler(t *testing.T) {
	// The scheduler writes .nightgauge/autonomous/state.json relative to the
	// WORKSPACE root, which in a multi-repo workspace is not a repo root at
	// all — the one directory config.WorkspaceRepoRoots never yields.
	isolateMachineState(t)
	ws := t.TempDir()
	ws, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".vscode"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".vscode", "nightgauge-workspace.yaml"), []byte("repositories: []\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".nightgauge", "autonomous"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".nightgauge", "autonomous", "state.json"),
		[]byte(fmt.Sprintf(`{"pid":4242,"startedAt":%q,"lastScanAt":%q}`, stamp(time.Hour), stamp(time.Minute))), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	repo := filepath.Join(ws, "some-repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if !sidecarPIDs(repo, scanClock)[4242] {
		t.Error("the workspace-root scheduler sidecar was never read — its process would be reported as an orphan")
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

	item, warning := orphanedProcessReport(parseRows(t, rows...), map[int]bool{}, nil, nil)

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
		map[int]bool{}, nil, nil)

	for _, want := range []string{"7788", "31h", "autonomous run --dry-run", "verify and terminate manually"} {
		if !strings.Contains(item.Error, want) {
			t.Errorf("the report does not carry %q: %q", want, item.Error)
		}
	}
}

func TestOrphanedProcessReport_HealthyPathWritesAnOKEntry(t *testing.T) {
	// A carrier that writes nothing when healthy renders as no output at all,
	// which reads as "not checked" (#332). It always writes.
	//
	// The captured machine's one nightgauge process is the extension host's
	// serve daemon, and after #388 what makes it healthy is its sidecar — the
	// claim, not its argv.
	procs, determined := parseProcessTable(loadCapturedProcessTable(t))
	if !determined {
		t.Fatal("the captured table did not parse")
	}

	item, warning := orphanedProcessReport(procs, map[int]bool{capturedServeRow(t).PID: true}, nil, nil)

	if !item.OK {
		t.Fatalf("the captured (clean) machine must pass: %+v", item)
	}
	if warning != "" {
		t.Errorf("unexpected warning: %q", warning)
	}
	if !strings.Contains(item.Detail, "1 owned") || !strings.Contains(item.Detail, "0 orphaned") {
		t.Errorf("the healthy detail does not report its counts: %q", item.Detail)
	}
	if strings.Contains(item.Detail, "serve") {
		t.Errorf("the counts still carry a serve-shaped class: %q", item.Detail)
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
	r.write(".nightgauge/autonomous/state.json", fmt.Sprintf(
		`{"status":"running","pid":4001,"startedAt":%q,"lastScanAt":%q}`, stamp(time.Hour), stamp(time.Minute)))
	r.write(".nightgauge/pipeline/current-run.json", fmt.Sprintf(
		`{"issue_number":341,"pid":4002,"started_at":%q,"stage_started_at":%q}`, stamp(time.Hour), stamp(time.Minute)))
	r.write(".nightgauge/pipeline/run-state.json", fmt.Sprintf(
		`{"schema_version":"1.0","issue_number":341,"created_at":%q,"updated_at":%q,"attempts":[{"run_id":"a","pid":4003},{"run_id":"b","pid":4004}]}`,
		stamp(time.Hour), stamp(time.Minute)))
	writeServeSidecar(t, r.dir, 4005, 300*time.Hour, time.Minute)

	claimed := sidecarPIDs(r.dir, scanClock)

	for _, pid := range []int{4001, 4002, 4003, 4004, 4005} {
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
	r.write(".nightgauge/autonomous/state.json", fmt.Sprintf(
		`{"status":"running","pid":4001,"lastScanAt":%q}`, stamp(time.Minute)))

	claimed := sidecarPIDs(r.dir, scanClock)

	if !claimed[4001] {
		t.Error("a corrupt sidecar suppressed a readable one")
	}
	if len(claimed) != 1 {
		t.Errorf("unexpected claims: %v", claimed)
	}
}

func TestSidecarPIDs_NoSidecarsClaimNothing(t *testing.T) {
	r := newLeakRepo(t)

	if claimed := sidecarPIDs(r.dir, scanClock); len(claimed) != 0 {
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
	// On every platform that HAS `ps`, the scan must actually have been
	// determined. Without this the suite passes on a permanently-unverifiable
	// carrier: a `ps` whose columns drift from this parser would ship green,
	// warning forever in production and failing nothing in CI.
	if runtime.GOOS != "windows" && strings.Contains(item.Error, "unverifiable") {
		t.Fatalf("the real process table was not understood on %s — the parser has drifted from this host's ps: %q", runtime.GOOS, item.Error)
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

func TestProcessTableReport_ATableWithoutThisProcessIsUnverifiable(t *testing.T) {
	// `ps -ax` always lists its own caller. A table that parsed cleanly but
	// does not contain us did not enumerate this machine — a foreign or
	// stubbed `ps` this parser happened to agree with — and "0 orphaned" from
	// it would be #296's defect wearing a determined parse.
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty table", raw: "\n"},
		{name: "rows, but none of them us", raw: derivedRow(t, os.Getpid()+1, "10:00", "serve --workspace /Users/operator/x") + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, warning := processTableReport(t.TempDir(), tt.raw, map[int]bool{}, nil)

			if item.OK {
				t.Fatalf("a table that never listed this process reported healthy: %+v", item)
			}
			if !strings.Contains(item.Error, "unverifiable") {
				t.Errorf("the error does not say the scan could not be trusted: %q", item.Error)
			}
			if warning == "" {
				t.Error("an unverifiable scan must warn")
			}
		})
	}
}

func TestProcessTableReport_ATableContainingThisProcessIsRead(t *testing.T) {
	// The other side of the rule: a table that DOES list us is reported on.
	raw := derivedRow(t, os.Getpid(), "10:00", "doctor --json") + "\n" +
		derivedRow(t, os.Getpid()+1, "05-00:00:00", "autonomous run --dry-run") + "\n"

	item, _ := processTableReport(t.TempDir(), raw, map[int]bool{}, nil)

	if strings.Contains(item.Error, "unverifiable") {
		t.Fatalf("a table containing this process was rejected: %q", item.Error)
	}
	if item.OK {
		t.Fatalf("the aged unowned process was not reported: %+v", item)
	}
}

func TestClassifyProcesses_TheClassifierNeverReadsTheVerb(t *testing.T) {
	// #388 retired the argv exception AND the verb reader behind it. Ownership
	// is the sidecar's answer for every process, so how the daemon was invoked
	// — `serve`, `--verbose serve`, `--config serve …` (the flag-value
	// ambiguity that used to suppress a real report) — changes nothing.
	rows := []string{
		"serve --workspace /Users/operator/Repositories/nightgauge",
		"--verbose serve --workspace /Users/operator/Repositories/nightgauge",
		"--config serve --workspace /Users/operator/Repositories/nightgauge",
	}
	for i, args := range rows {
		pid := 4157 + i
		procs := parseRows(t, derivedRow(t, pid, "10-00:00:00", args))

		unclaimed := classifyProcesses(procs, map[int]bool{}, os.Getpid())
		claimed := classifyProcesses(procs, map[int]bool{pid: true}, os.Getpid())

		if len(unclaimed.Orphans) != 1 {
			t.Errorf("argv %q: an unclaimed daemon was not reported: %+v", args, unclaimed)
		}
		if claimed.Owned != 1 || len(claimed.Orphans) != 0 {
			t.Errorf("argv %q: a sidecar-claimed daemon was not carried: %+v", args, claimed)
		}
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

// #519. A `.nightgauge/worktrees/issue-N` directory is not only where the
// pipeline runs — interactive agent harnesses (Claude Code, Codex, both
// inside VSCode) run their shells there too, and can leak a detached one. An
// operator found several `/bin/zsh` processes still parked with cwd inside
// `.nightgauge/worktrees/issue-488`, held open by a VSCode extension-host
// background task long after the session ended and the worktree was removed.
// None of those shells was ever a nightgauge process, so #341's argv filter
// could never have seen them — this half of the scan is keyed on cwd instead.

func TestParseLsofCwd(t *testing.T) {
	raw := "p101\nfcwd\nn/Users/operator/work/a\n" +
		"p102\nfcwd\nn/Users/operator/work/b\n" +
		"p103\nfcwd\nn/Users/operator/work/c\n"

	cwds, determined := parseLsofCwd(raw)
	if !determined {
		t.Fatal("parseLsofCwd reported undetermined on a well-formed table")
	}
	want := map[int]string{
		101: "/Users/operator/work/a",
		102: "/Users/operator/work/b",
		103: "/Users/operator/work/c",
	}
	if len(cwds) != len(want) {
		t.Fatalf("got %d cwds, want %d: %+v", len(cwds), len(want), cwds)
	}
	for pid, path := range want {
		if cwds[pid] != path {
			t.Errorf("pid %d: got cwd %q, want %q", pid, cwds[pid], path)
		}
	}

	// A malformed block — an 'n' line with no preceding 'p' — is skipped, not
	// fatal to the well-formed block that follows it.
	raw2 := "n/orphaned/no/pid\np201\nfcwd\nn/Users/operator/work/d\n"
	cwds2, determined2 := parseLsofCwd(raw2)
	if !determined2 {
		t.Fatal("a malformed block undetermined the whole table")
	}
	if len(cwds2) != 1 || cwds2[201] != "/Users/operator/work/d" {
		t.Errorf("the well-formed block after a malformed one was lost: %+v", cwds2)
	}
}

// foreignCwdFixture builds a leakRepo with one live pipeline worktree
// (issue-488) already registered with git, plus the derived facts
// (worktreesRoots, ActiveWorktreeIssues) a real buildForeignCwdScan would
// produce for it — assembled by hand here so each test can stub only the cwd
// half without spawning `ps` or `lsof`.
func foreignCwdFixture(t *testing.T) (r *leakRepo, liveWorktree string) {
	t.Helper()
	r = newLeakRepo(t)
	liveWorktree = filepath.Join(r.dir, ".nightgauge", "worktrees", "issue-488")
	r.git("worktree", "add", "-q", liveWorktree, "-b", "fix/488", "main")
	return r, liveWorktree
}

func TestForeignCwdHolder_Flagged(t *testing.T) {
	r, liveWorktree := foreignCwdFixture(t)

	row := fmt.Sprintf("%d %s %s", 42488, "02:00:00", "/bin/zsh")
	procs := parseRows(t, row)
	fc := &foreignCwdScan{
		Cwds:           map[int]string{42488: liveWorktree},
		WorktreesRoots: worktreesRoots(r.dir),
	}
	var determined bool
	fc.ActiveIssues, determined = execution.ActiveWorktreeIssues(config.WorkspaceRepoRoots(r.dir))
	if !determined {
		t.Fatal("active worktree set undetermined")
	}

	item, warning := orphanedProcessReport(procs, map[int]bool{}, nil, fc)

	if item.OK {
		t.Fatalf("a foreign process with cwd inside a live worktree reported healthy: %+v", item)
	}
	for _, want := range []string{"42488", "2h", "/bin/zsh", liveWorktree, "[cwd inside worktree issue-488]"} {
		if !strings.Contains(item.Error, want) {
			t.Errorf("the report does not carry %q: %q", want, item.Error)
		}
	}
	if warning == "" {
		t.Error("a foreign cwd holder must produce a warning, not just a check entry")
	}
}

func TestForeignCwdHolder_StaleWorktree(t *testing.T) {
	r, liveWorktree := foreignCwdFixture(t)

	// A leftover directory git no longer tracks as a worktree — the shape left
	// behind by `git worktree remove`, and the case that can also block a
	// worktree removal (#110). Deliberately no `git worktree add` for this one.
	staleWorktree := filepath.Join(r.dir, ".nightgauge", "worktrees", "issue-489")
	if err := os.MkdirAll(staleWorktree, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	procs := parseRows(t,
		fmt.Sprintf("%d %s %s", 42488, "01:00:00", "/bin/zsh"),
		fmt.Sprintf("%d %s %s", 42489, "03:00:00", "/bin/bash"),
	)
	fc := &foreignCwdScan{
		Cwds:           map[int]string{42488: liveWorktree, 42489: staleWorktree},
		WorktreesRoots: worktreesRoots(r.dir),
	}
	var determined bool
	fc.ActiveIssues, determined = execution.ActiveWorktreeIssues(config.WorkspaceRepoRoots(r.dir))
	if !determined {
		t.Fatal("active worktree set undetermined")
	}

	item, _ := orphanedProcessReport(procs, map[int]bool{}, nil, fc)

	liveTag := "[cwd inside worktree issue-488]"
	staleTag := "[cwd inside REMOVED worktree issue-489]"
	if !strings.Contains(item.Error, liveTag) {
		t.Fatalf("the live-worktree holder was not tagged: %q", item.Error)
	}
	if !strings.Contains(item.Error, staleTag) {
		t.Fatalf("the removed-worktree holder was not distinctly tagged REMOVED: %q", item.Error)
	}
	if strings.Index(item.Error, staleTag) > strings.Index(item.Error, liveTag) {
		t.Errorf("the stale (REMOVED) holder must sort before the live one: %q", item.Error)
	}
}

func TestForeignCwdHolder_PrefixIsNotContainment(t *testing.T) {
	r, _ := foreignCwdFixture(t)

	// Shares the literal string prefix ".../worktrees" with the real root but
	// is a SIBLING directory, not something inside it.
	decoy := filepath.Join(r.dir, ".nightgauge", "worktrees-old", "x")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if base, ok := worktreeDirContaining(decoy, worktreesRoots(r.dir)); ok {
		t.Fatalf("a sibling directory sharing a string prefix was treated as contained: base=%q", base)
	}
}

// TestOrphanedProcesses_NeverSignals pins the report-only contract at the
// source level: this file must never call a signal primitive. The positive
// control proves the detector actually trips on an offending call rather than
// passing because neither string happens to appear in healthy source.
func TestOrphanedProcesses_NeverSignals(t *testing.T) {
	src, err := os.ReadFile("orphaned_processes.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}

	if found := forbiddenSignalCalls(string(src)); len(found) != 0 {
		t.Errorf("orphaned_processes.go calls a signal primitive — this carrier must never signal a process: %v", found)
	}

	poisoned := string(src) + "\nfunc neverCalled() { syscall.Kill(1, 9); var p *os.Process; p.Signal(nil) }\n"
	if found := forbiddenSignalCalls(poisoned); len(found) != 2 {
		t.Fatalf("positive control did not trip the detector: found %v — the check cannot be trusted to go red", found)
	}
}

func forbiddenSignalCalls(src string) []string {
	var found []string
	for _, sym := range []string{"syscall.Kill", ".Signal("} {
		if strings.Contains(src, sym) {
			found = append(found, sym)
		}
	}
	return found
}
