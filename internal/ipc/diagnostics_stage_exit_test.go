// Tests for the IPC-mode stage-exit diagnostic record write path
// (`diagnostics.recordStageExit`). This closes the #3619 gap where PR #3608
// only wired the Go-scheduler write path; the user's TS-driven autonomous
// workflow produced no diagnostic records.
package ipc

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
)

// readDailyRecords returns the records in today's daily file (or an empty
// slice when none). Used by the assertions below to verify the IPC write
// path produced a JSONL line in the same place the CLI reader looks.
func readDailyRecords(t *testing.T, rootDir string) []diagnostics.StageExitRecord {
	t.Helper()
	path := diagnostics.DailyFilePath(rootDir, time.Now())
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open daily file: %v", err)
	}
	defer f.Close()

	var out []diagnostics.StageExitRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec diagnostics.StageExitRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("decode record line: %v\nline=%q", err, string(line))
		}
		out = append(out, rec)
	}
	return out
}

// TestBuildStageExitRecordFromIPC_AllFieldsCarriedVerbatim pins the
// translation contract: every field TS populates appears in the on-disk
// record unchanged. Regression guard for accidental field drops during
// refactors (e.g., adding a new field to RecordStageExitParams but forgetting
// to copy it into StageExitRecord).
func TestBuildStageExitRecordFromIPC_AllFieldsCarriedVerbatim(t *testing.T) {
	exitCode := 137
	bashExit := 1
	okExit := 0
	p := RecordStageExitParams{
		Repo:                "nightgauge/nightgauge",
		IssueNumber:         3340,
		Stage:               "feature-dev",
		Success:             false,
		RunID:               "01HXYZ-test-runid",
		Model:               "claude-sonnet-4-6",
		ExitCode:            &exitCode,
		TerminalKind:        "stall_kill",
		ErrorText:           "[stall-killed] no stdout for 1200s",
		ElapsedMs:           1234567,
		IdleMsAtExit:        90000,
		InputTokens:         12000,
		OutputTokens:        4500,
		CacheReadTokens:     8000,
		CacheCreationTokens: 1500,
		CostUsd:             2.41,
		Signal:              "SIGKILL",
		SignalSource:        "stall-kill",
		SessionID:           "abc-def-123",
		LastBashCommand:     "npm run -w nightgauge-vscode vitest run",
		LastBashExit:        &bashExit,
		RecentBash: []diagnostics.RecentBashEntry{
			{Cmd: "go build ./...", Exit: &okExit},
			{Cmd: "npm run -w nightgauge-vscode vitest run", Exit: &bashExit},
		},
		StopHookErrored: true,
		StderrTail:      "[skillRunner] idle threshold exceeded\n",
	}

	rec := buildStageExitRecordFromIPC(p)

	if rec.Repo != p.Repo {
		t.Errorf("Repo = %q, want %q", rec.Repo, p.Repo)
	}
	if rec.Issue != p.IssueNumber {
		t.Errorf("Issue = %d, want %d", rec.Issue, p.IssueNumber)
	}
	if rec.Stage != p.Stage {
		t.Errorf("Stage = %q, want %q", rec.Stage, p.Stage)
	}
	if rec.Success != p.Success {
		t.Errorf("Success = %v, want %v", rec.Success, p.Success)
	}
	if rec.RunID != p.RunID {
		t.Errorf("RunID = %q, want %q", rec.RunID, p.RunID)
	}
	if rec.ExitCode == nil || *rec.ExitCode != *p.ExitCode {
		t.Errorf("ExitCode mismatch (got %v, want %v)", rec.ExitCode, p.ExitCode)
	}
	if rec.TerminalKind != p.TerminalKind {
		t.Errorf("TerminalKind = %q, want %q", rec.TerminalKind, p.TerminalKind)
	}
	if rec.ElapsedMs != p.ElapsedMs {
		t.Errorf("ElapsedMs = %d, want %d", rec.ElapsedMs, p.ElapsedMs)
	}
	if rec.IdleMsAtExit != p.IdleMsAtExit {
		t.Errorf("IdleMsAtExit = %d, want %d", rec.IdleMsAtExit, p.IdleMsAtExit)
	}
	if rec.Tokens.Input != p.InputTokens ||
		rec.Tokens.Output != p.OutputTokens ||
		rec.Tokens.CacheRead != p.CacheReadTokens ||
		rec.Tokens.CacheCreation != p.CacheCreationTokens ||
		rec.Tokens.CostUsd != p.CostUsd {
		t.Errorf("Tokens mismatch: got %+v", rec.Tokens)
	}
	if rec.Signal != p.Signal {
		t.Errorf("Signal = %q, want %q", rec.Signal, p.Signal)
	}
	if rec.SignalSource != p.SignalSource {
		t.Errorf("SignalSource = %q, want %q", rec.SignalSource, p.SignalSource)
	}
	if rec.SessionID != p.SessionID {
		t.Errorf("SessionID = %q, want %q", rec.SessionID, p.SessionID)
	}
	if rec.LastBashCommand != p.LastBashCommand {
		t.Errorf("LastBashCommand = %q, want %q", rec.LastBashCommand, p.LastBashCommand)
	}
	if rec.LastBashExit == nil || *rec.LastBashExit != *p.LastBashExit {
		t.Errorf("LastBashExit mismatch (got %v, want %v)", rec.LastBashExit, p.LastBashExit)
	}
	if len(rec.RecentBash) != len(p.RecentBash) {
		t.Fatalf("len(RecentBash) = %d, want %d", len(rec.RecentBash), len(p.RecentBash))
	}
	for i := range p.RecentBash {
		if rec.RecentBash[i].Cmd != p.RecentBash[i].Cmd {
			t.Errorf("RecentBash[%d].Cmd = %q, want %q", i, rec.RecentBash[i].Cmd, p.RecentBash[i].Cmd)
		}
		if rec.RecentBash[i].Exit == nil || *rec.RecentBash[i].Exit != *p.RecentBash[i].Exit {
			t.Errorf("RecentBash[%d].Exit = %v, want %v", i, rec.RecentBash[i].Exit, p.RecentBash[i].Exit)
		}
	}
	// The ring is a superset, not a replacement — its tail names the same
	// command LastBashCommand does. (#156)
	if rec.RecentBash[len(rec.RecentBash)-1].Cmd != rec.LastBashCommand {
		t.Errorf("RecentBash tail %q disagrees with LastBashCommand %q",
			rec.RecentBash[len(rec.RecentBash)-1].Cmd, rec.LastBashCommand)
	}
	if rec.StopHookErrored != p.StopHookErrored {
		t.Errorf("StopHookErrored = %v, want %v", rec.StopHookErrored, p.StopHookErrored)
	}
	if rec.StderrTail != p.StderrTail {
		t.Errorf("StderrTail mismatch")
	}
	if rec.Timestamp == "" {
		t.Error("Timestamp must always be set")
	}
}

// TestBuildStageExitRecordFromIPC_ClassifiesWhenTerminalKindEmpty verifies
// the fallback classifier — if TS didn't pre-classify (left TerminalKind
// empty) but provided an ErrorText, Go runs ClassifyTerminalKind so the
// record's terminal_kind matches what the Go-scheduler path would produce
// for the same error shape.
func TestBuildStageExitRecordFromIPC_ClassifiesWhenTerminalKindEmpty(t *testing.T) {
	p := RecordStageExitParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 3340,
		Stage:       "feature-dev",
		Success:     false,
		// TerminalKind intentionally empty
		ErrorText: "[stall-killed] no output for 1200000 ms",
	}
	rec := buildStageExitRecordFromIPC(p)
	if rec.TerminalKind == "" {
		t.Errorf("expected fallback classifier to fill TerminalKind from ErrorText, got empty")
	}
}

// TestBuildStageExitRecordFromIPC_HonorsPreClassifiedTerminalKind verifies
// the inverse: when TS pre-classified, Go does NOT overwrite. TS has more
// context (e.g. local subprocess state) so its classification wins.
func TestBuildStageExitRecordFromIPC_HonorsPreClassifiedTerminalKind(t *testing.T) {
	p := RecordStageExitParams{
		Repo:         "nightgauge/nightgauge",
		IssueNumber:  3340,
		Stage:        "feature-dev",
		Success:      false,
		TerminalKind: "subagent_crash", // TS-classified
		ErrorText:    "[stall-killed] something",
	}
	rec := buildStageExitRecordFromIPC(p)
	if rec.TerminalKind != "subagent_crash" {
		t.Errorf("TerminalKind = %q, want %q (TS classification must win)",
			rec.TerminalKind, "subagent_crash")
	}
}

// TestBuildStageExitRecordFromIPC_CarriesGateVerdict pins the #125 channel:
// a stage whose skill exited 0 but whose post-condition gate failed it arrives
// with Success=false plus the gate's kind/reason, and both reach the on-disk
// record. Before #125 the TS dispatch path had no way to send either, so a
// gate-caught failure was written as success=true with no trace of why — and
// landed in the healthy baseline of every ratio-based health analysis.
func TestBuildStageExitRecordFromIPC_CarriesGateVerdict(t *testing.T) {
	cleanExit := 0
	p := RecordStageExitParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 125,
		Stage:       "feature-validate",
		// Post-GATE outcome, not the skill's exit code.
		Success:    false,
		ExitCode:   &cleanExit,
		GateKind:   "fail",
		GateReason: `[validation-failed] feature-validate reported validation_status="failed"`,
		ErrorText:  `[validation-failed] feature-validate reported validation_status="failed"`,
	}
	rec := buildStageExitRecordFromIPC(p)
	if rec.Success {
		t.Error("Success = true, want false — the gate's verdict is authoritative")
	}
	if rec.GateKind != "fail" {
		t.Errorf("GateKind = %q, want %q", rec.GateKind, "fail")
	}
	if rec.GateReason != p.GateReason {
		t.Errorf("GateReason = %q, want %q", rec.GateReason, p.GateReason)
	}
	// The reason must still classify: retros join on terminal_kind, and the
	// run record books validation_failed for this exact shape.
	if rec.TerminalKind != "validation_failed" {
		t.Errorf("TerminalKind = %q, want %q", rec.TerminalKind, "validation_failed")
	}
	// A clean exit code alongside success=false is the signature of a
	// gate-caught failure — it must survive as 0, not be erased.
	if rec.ExitCode == nil || *rec.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0 (the skill really did exit 0)", rec.ExitCode)
	}
}

// TestBuildStageExitRecordFromIPC_BoundsRecentBash proves the retention limits
// are enforced on THIS path too, not only on the Go-scheduler path. Both write
// into the same daily file that operator tooling greps, so a bound applied to
// one and not the other would let the TS dispatch path — the one autonomous
// mode actually uses — write unbounded command history. (#156)
func TestBuildStageExitRecordFromIPC_BoundsRecentBash(t *testing.T) {
	var oversized []diagnostics.RecentBashEntry
	for i := 0; i < diagnostics.RecentBashMaxEntries*2; i++ {
		oversized = append(oversized, diagnostics.RecentBashEntry{Cmd: "cmd"})
	}
	oversized = append(oversized, diagnostics.RecentBashEntry{
		Cmd: strings.Repeat("z", diagnostics.RecentBashCommandMaxRunes+50),
	})

	rec := buildStageExitRecordFromIPC(RecordStageExitParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 156,
		Stage:       "feature-dev",
		RecentBash:  oversized,
	})

	if len(rec.RecentBash) != diagnostics.RecentBashMaxEntries {
		t.Errorf("len(RecentBash) = %d, want %d", len(rec.RecentBash), diagnostics.RecentBashMaxEntries)
	}
	tail := rec.RecentBash[len(rec.RecentBash)-1].Cmd
	if !strings.HasSuffix(tail, "…") {
		t.Errorf("over-long command not elided: len=%d", len([]rune(tail)))
	}
	if len([]rune(tail)) != diagnostics.RecentBashCommandMaxRunes+1 {
		t.Errorf("len(tail runes) = %d, want %d", len([]rune(tail)), diagnostics.RecentBashCommandMaxRunes+1)
	}
}

// TestRecordStageExitIPC_EndToEnd_WritesDailyJSONL is the integration test:
// invoke the IPC handler and assert the JSONL line appeared in the right
// place. This is the regression guard that would have caught the #3608 gap
// (where the Go scheduler write path was the only path).
func TestRecordStageExitIPC_EndToEnd_WritesDailyJSONL(t *testing.T) {
	dir := t.TempDir()

	srv := &Server{
		workspaceRoot: dir,
		methods:       map[string]Handler{},
	}
	srv.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(srv)

	handler := srv.methods["diagnostics.recordStageExit"]
	if handler == nil {
		t.Fatal("diagnostics.recordStageExit handler not registered")
	}

	exitCode := 1
	params := RecordStageExitParams{
		Repo:         "nightgauge/nightgauge",
		IssueNumber:  3340,
		Stage:        "feature-dev",
		Success:      false,
		ExitCode:     &exitCode,
		TerminalKind: "stall_kill",
		ErrorText:    "[stall-killed]",
		Signal:       "SIGKILL",
		SignalSource: "stall-kill",
		ElapsedMs:    1200000,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	result, err := handler(nil, raw)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res, ok := result.(*RecordStageExitResult); !ok || !res.Recorded {
		t.Errorf("expected Recorded=true, got %+v", result)
	}

	records := readDailyRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected 1 record in daily file, got %d", len(records))
	}
	rec := records[0]
	if rec.Repo != params.Repo {
		t.Errorf("on-disk Repo = %q, want %q", rec.Repo, params.Repo)
	}
	if rec.Issue != params.IssueNumber {
		t.Errorf("on-disk Issue = %d, want %d", rec.Issue, params.IssueNumber)
	}
	if rec.Stage != params.Stage {
		t.Errorf("on-disk Stage = %q, want %q", rec.Stage, params.Stage)
	}
	if rec.TerminalKind != params.TerminalKind {
		t.Errorf("on-disk TerminalKind = %q, want %q", rec.TerminalKind, params.TerminalKind)
	}
}

// TestRecordStageExitIPC_ScopedToTargetRepo (#232) — a run targeting a
// registered sibling repo must write its exit-record into THAT repo's
// .nightgauge/pipeline/exit-records dir (the same root its runtime snapshot and
// history RunRecord use), not the IPC server's launch root. Regression guard
// for the pre-#232 behavior where the handler ignored p.Repo and always wrote
// to srv.workspaceRoot.
func TestRecordStageExitIPC_ScopedToTargetRepo(t *testing.T) {
	launchRoot := t.TempDir() // IPC server workspaceRoot (launch root)
	targetRoot := t.TempDir() // the run's registered target repo
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("owner", "repo", targetRoot)

	handler := s.methods["diagnostics.recordStageExit"]
	if handler == nil {
		t.Fatal("diagnostics.recordStageExit handler not registered")
	}

	params := RecordStageExitParams{
		Repo:         "owner/repo",
		IssueNumber:  232,
		Stage:        "feature-dev",
		Success:      false,
		TerminalKind: "stall_kill",
		ErrorText:    "[stall-killed]",
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handler(nil, raw); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	// The record must land in the TARGET repo's exit-records dir…
	if got := readDailyRecords(t, targetRoot); len(got) != 1 {
		t.Fatalf("expected 1 record under the target repo, got %d", len(got))
	}
	expectedDir := filepath.Join(targetRoot, ".nightgauge", "pipeline", "exit-records")
	if _, err := os.Stat(expectedDir); err != nil {
		t.Errorf("expected target-repo exit-records dir %s: %v", expectedDir, err)
	}
	// …and NOT in the launch root.
	if got := readDailyRecords(t, launchRoot); len(got) != 0 {
		t.Fatalf("no exit-record may leak into the launch root, got %d", len(got))
	}
}

// TestRecordStageExitIPC_RejectsEmptyWorkspaceRoot — guard against silent
// writes to CWD when the server hasn't been configured. Mirrors the same
// check in WriteStageExitRecord.
func TestRecordStageExitIPC_RejectsEmptyWorkspaceRoot(t *testing.T) {
	srv := &Server{
		workspaceRoot: "", // not configured
		methods:       map[string]Handler{},
	}
	srv.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(srv)

	handler := srv.methods["diagnostics.recordStageExit"]
	params := RecordStageExitParams{
		Repo:        "x/y",
		IssueNumber: 1,
		Stage:       "feature-dev",
		Success:     true,
	}
	raw, _ := json.Marshal(params)
	_, err := handler(nil, raw)
	if err == nil {
		t.Error("expected error when workspaceRoot is empty, got nil")
	}
}

// TestRecordStageExitIPC_AppendSemantics — multiple calls in one day append
// to the same daily file (no overwrites). Pins the file-format contract.
func TestRecordStageExitIPC_AppendSemantics(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{
		workspaceRoot: dir,
		methods:       map[string]Handler{},
	}
	srv.methods["diagnostics.recordStageExit"] = makeDiagnosticsRecordStageExitHandler(srv)

	handler := srv.methods["diagnostics.recordStageExit"]

	for i, stage := range []string{"feature-planning", "feature-dev", "pr-merge"} {
		params := RecordStageExitParams{
			Repo:        "nightgauge/nightgauge",
			IssueNumber: 3340,
			Stage:       stage,
			Success:     i == 0, // first succeeds, rest fail
		}
		raw, _ := json.Marshal(params)
		if _, err := handler(nil, raw); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	records := readDailyRecords(t, dir)
	if len(records) != 3 {
		t.Fatalf("expected 3 appended records, got %d", len(records))
	}

	// Verify the daily file exists at the canonical location
	dailyPath := diagnostics.DailyFilePath(dir, time.Now())
	if _, err := os.Stat(dailyPath); err != nil {
		t.Errorf("daily file missing at %s: %v", dailyPath, err)
	}
	// And the parent directory matches the documented path
	expectedDir := filepath.Join(dir, ".nightgauge", "pipeline", "exit-records")
	if _, err := os.Stat(expectedDir); err != nil {
		t.Errorf("expected directory %s missing: %v", expectedDir, err)
	}
}

// TestRecordStageExitIPC_TokenConsumingStageWritesNonEmptyTokens (#109) is the
// regression guard for the defect that made every IPC-path record useless for
// cost forensics: the TS caller passed literal `undefined` for the exit code
// and all five token/cost fields, so 1,735 of 1,735 records across 24 days
// landed with `"tokens": {}` while the runtime state for the same stage showed
// millions of tokens and dollars of spend.
//
// `ExitRecordTokens` is a struct, so `omitempty` cannot elide it — the empty
// object IS the tell. This asserts on the raw JSON line rather than the decoded
// record so the on-disk shape (what `nightgauge exit-records tail`, the
// pipeline-audit skill, and ad-hoc `jq` all read) is what is pinned.
func TestRecordStageExitIPC_TokenConsumingStageWritesNonEmptyTokens(t *testing.T) {
	dir := t.TempDir()

	srv := &Server{
		workspaceRoot: dir,
		methods:       map[string]Handler{},
	}
	handler := makeDiagnosticsRecordStageExitHandler(srv)

	exitCode := 0
	params := RecordStageExitParams{
		Repo:                "nightgauge/nightgauge",
		IssueNumber:         307,
		Stage:               "feature-planning",
		Success:             true,
		ExitCode:            &exitCode,
		ElapsedMs:           784194,
		InputTokens:         4937274,
		OutputTokens:        49252,
		CacheReadTokens:     4937180,
		CacheCreationTokens: 61204,
		CostUsd:             3.7963349,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handler(nil, raw); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	records := readDailyRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected 1 record in daily file, got %d", len(records))
	}
	tok := records[0].Tokens
	if tok.Input != params.InputTokens {
		t.Errorf("tokens.input = %d, want %d", tok.Input, params.InputTokens)
	}
	if tok.Output != params.OutputTokens {
		t.Errorf("tokens.output = %d, want %d", tok.Output, params.OutputTokens)
	}
	if tok.CacheRead != params.CacheReadTokens {
		t.Errorf("tokens.cache_read = %d, want %d", tok.CacheRead, params.CacheReadTokens)
	}
	if tok.CacheCreation != params.CacheCreationTokens {
		t.Errorf("tokens.cache_creation = %d, want %d", tok.CacheCreation, params.CacheCreationTokens)
	}
	if tok.CostUsd != params.CostUsd {
		t.Errorf("tokens.cost_usd = %v, want %v", tok.CostUsd, params.CostUsd)
	}
	if records[0].ExitCode == nil || *records[0].ExitCode != 0 {
		t.Errorf("exit_code = %v, want a real 0 (pointer-shaped, not omitted)", records[0].ExitCode)
	}

	// The literal on-disk line must never again read `"tokens":{}`.
	lines, err := os.ReadFile(diagnostics.DailyFilePath(dir, time.Now()))
	if err != nil {
		t.Fatalf("read daily file: %v", err)
	}
	if strings.Contains(string(lines), `"tokens":{}`) {
		t.Errorf("daily line still carries an empty tokens object:\n%s", string(lines))
	}
	if !strings.Contains(string(lines), `"exit_code":0`) {
		t.Errorf("daily line dropped exit_code:\n%s", string(lines))
	}
}

// TestRecordStageExitIPC_DeterministicStageStillRecordsZero (#109) pins the
// other half of the contract: a no-LLM stage (pipeline-start, issue-pickup on
// the deterministic path) consumed nothing, so it must record nothing. The fix
// forwards real figures — it never synthesizes them.
func TestRecordStageExitIPC_DeterministicStageStillRecordsZero(t *testing.T) {
	dir := t.TempDir()

	srv := &Server{
		workspaceRoot: dir,
		methods:       map[string]Handler{},
	}
	handler := makeDiagnosticsRecordStageExitHandler(srv)

	raw, err := json.Marshal(RecordStageExitParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 307,
		Stage:       "pipeline-start",
		Success:     true,
		ElapsedMs:   12,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handler(nil, raw); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	records := readDailyRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected 1 record in daily file, got %d", len(records))
	}
	if got := records[0].Tokens; got != (diagnostics.ExitRecordTokens{}) {
		t.Errorf("deterministic stage booked tokens = %+v, want the zero value", got)
	}
	if records[0].ExitCode != nil {
		t.Errorf("exit_code = %v, want nil (never observed)", *records[0].ExitCode)
	}
}
