package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// readExitRecords loads every JSONL line from today's exit-records file.
// Returns an empty slice if the file does not exist (the writer creates it
// lazily and skips on empty rootDir).
func readExitRecords(t *testing.T, rootDir string) []diagnostics.StageExitRecord {
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

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []diagnostics.StageExitRecord
	for scanner.Scan() {
		var rec diagnostics.StageExitRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("unmarshal: %v (line %q)", err, scanner.Text())
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// TestWriteStageExitRecord_Success pins the schema for a healthy stage exit.
// The record carries success=true, the actual cost, and an elapsed_ms ≥ 0.
func TestWriteStageExitRecord_Success(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3605, "item-id")
	runtime.RunID = "run-3605-success"
	item := types.BoardItem{Number: 3605, Repo: "nightgauge/nightgauge"}
	result := &StageRunResult{
		ExitCode:        0,
		InputTokens:     1000,
		OutputTokens:    200,
		CacheReadTokens: 5000,
		CostUsd:         0.0421,
	}

	stageStart := time.Now().Add(-2 * time.Second)
	s.writeStageExitRecord(item, state.StageFeatureDev, runtime, result,
		0, nil, 0.0421, "sonnet-4-5", 1000, 200, 5000, stageStart, root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if !got.Success {
		t.Errorf("Success = false, want true")
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want pointer to 0", got.ExitCode)
	}
	if got.Stage != string(state.StageFeatureDev) {
		t.Errorf("Stage = %q, want %q", got.Stage, state.StageFeatureDev)
	}
	if got.Issue != 3605 {
		t.Errorf("Issue = %d, want 3605", got.Issue)
	}
	if got.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q", got.Repo)
	}
	if got.RunID != "run-3605-success" {
		t.Errorf("RunID = %q, want run-3605-success", got.RunID)
	}
	if got.Tokens.Input != 1000 || got.Tokens.Output != 200 || got.Tokens.CacheRead != 5000 {
		t.Errorf("Tokens = %+v", got.Tokens)
	}
	if got.Tokens.CostUsd != 0.0421 {
		t.Errorf("Tokens.CostUsd = %v, want 0.0421", got.Tokens.CostUsd)
	}
	if got.ElapsedMs < 1000 {
		// stageStart was 2s ago — at least 1s should have accrued.
		t.Errorf("ElapsedMs = %d, want >= 1000", got.ElapsedMs)
	}
	if got.TerminalKind != "" {
		t.Errorf("TerminalKind = %q, want empty (success)", got.TerminalKind)
	}
}

// TestWriteStageExitRecord_FailureClassifiesTerminalKind ensures the daily
// JSONL carries a terminal_kind on failure so retros can `jq` straight on the
// file without joining the V3 record. Stall-kill exit text gets mapped to
// the canonical TerminalKindStallKill.
func TestWriteStageExitRecord_FailureClassifiesTerminalKind(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3605, "item-id")
	runtime.RunID = "run-3605-stallkill"
	item := types.BoardItem{Number: 3605, Repo: "nightgauge/nightgauge"}
	result := &StageRunResult{ExitCode: 137}

	stallErr := errors.New("[stall-killed] subprocess idle for 20m, exceeded idle threshold")
	s.writeStageExitRecord(item, state.StageFeatureDev, runtime, result,
		137, stallErr, 0, "sonnet-4-5", 0, 0, 0, time.Now().Add(-5*time.Minute), root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Success {
		t.Errorf("Success = true, want false")
	}
	if got.ExitCode == nil || *got.ExitCode != 137 {
		t.Errorf("ExitCode = %v, want pointer to 137", got.ExitCode)
	}
	if got.TerminalKind != TerminalKindStallKill {
		t.Errorf("TerminalKind = %q, want %q", got.TerminalKind, TerminalKindStallKill)
	}
	if got.ElapsedMs < 1000 {
		t.Errorf("ElapsedMs = %d, want >= 1000 (5min stage)", got.ElapsedMs)
	}
}

// TestWriteStageExitRecord_ForwardsTSDiagnosticFields verifies that diagnostic
// fields populated by the TS SkillRunner (signal, signal_source, stderr_tail,
// last_bash_command, stop_hook_errored, session_id, idle_ms_at_exit) round-trip
// from StageRunResult into the persisted record.
func TestWriteStageExitRecord_ForwardsTSDiagnosticFields(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3591, "item-id")
	runtime.RunID = "run-3591-mystery"
	item := types.BoardItem{Number: 3591, Repo: "nightgauge/nightgauge"}

	lastBashExit := 1
	result := &StageRunResult{
		ExitCode:            137,
		SessionID:           "abc-123-def-456",
		Signal:              "SIGKILL",
		SignalSource:        "stall-kill",
		ElapsedMs:           397_123,
		IdleMsAtExit:        4521,
		CacheCreationTokens: 80,
		LastBashCommand:     "nightgauge project move-status 3591 in-progress",
		LastBashExit:        &lastBashExit,
		StopHookErrored:     true,
		StderrTail:          "[skillRunner] Stage exceeded stall idle threshold (20m without output)",
	}

	s.writeStageExitRecord(item, state.StageFeaturePlanning, runtime, result,
		137, errors.New("[stall-killed] mystery exit"), 0, "sonnet-4-5",
		0, 0, 0, time.Now().Add(-time.Second), root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.SessionID != "abc-123-def-456" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
	if got.Signal != "SIGKILL" {
		t.Errorf("Signal = %q", got.Signal)
	}
	if got.SignalSource != "stall-kill" {
		t.Errorf("SignalSource = %q", got.SignalSource)
	}
	if got.IdleMsAtExit != 4521 {
		t.Errorf("IdleMsAtExit = %d", got.IdleMsAtExit)
	}
	if got.LastBashCommand != "nightgauge project move-status 3591 in-progress" {
		t.Errorf("LastBashCommand = %q", got.LastBashCommand)
	}
	if got.LastBashExit == nil || *got.LastBashExit != 1 {
		t.Errorf("LastBashExit = %v", got.LastBashExit)
	}
	if !got.StopHookErrored {
		t.Errorf("StopHookErrored = false, want true")
	}
	if !strings.Contains(got.StderrTail, "skillRunner") {
		t.Errorf("StderrTail did not round-trip: %q", got.StderrTail)
	}
	if got.Tokens.CacheCreation != 80 {
		t.Errorf("Tokens.CacheCreation = %d, want 80 (from TS forward)", got.Tokens.CacheCreation)
	}
	// TS-provided ElapsedMs wins when present.
	if got.ElapsedMs != 397_123 {
		t.Errorf("ElapsedMs = %d, want 397123 (TS-provided)", got.ElapsedMs)
	}
}

// TestWriteStageExitRecord_PreUpdateTSStillWrites verifies that when the TS
// SkillRunner has not yet been updated to populate diagnostic fields (all
// fields zero), Go still writes a valid record carrying just the Go-side
// truth (timestamp, repo, issue, stage, success, exit_code, tokens, elapsed).
func TestWriteStageExitRecord_PreUpdateTSStillWrites(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3605, "item-id")
	item := types.BoardItem{Number: 3605, Repo: "nightgauge/nightgauge"}

	// result with no TS-side diagnostics populated (mirrors pre-#3605 TS).
	result := &StageRunResult{ExitCode: 0, InputTokens: 500, OutputTokens: 100}
	s.writeStageExitRecord(item, state.StageIssuePickup, runtime, result,
		0, nil, 0, "sonnet-4-5", 500, 100, 0,
		time.Now().Add(-100*time.Millisecond), root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Signal != "" || got.SignalSource != "" || got.StderrTail != "" {
		t.Errorf("expected empty TS-forwarded fields, got Signal=%q SignalSource=%q StderrTail=%q",
			got.Signal, got.SignalSource, got.StderrTail)
	}
	if !got.Success {
		t.Errorf("Success = false, want true")
	}
	if got.Tokens.Input != 500 || got.Tokens.Output != 100 {
		t.Errorf("Tokens = %+v", got.Tokens)
	}
}

// TestWriteStageExitRecord_GateNoOpSnapshot verifies the forensic gate snapshot
// (Issue #3863): when a terminal stage's post-condition gate recorded KindNoOp
// (skill exited cleanly but produced no state change — e.g. pr-merge that never
// merged), the daily exit record carries gate_kind="no_op" + the gate reason so
// retros can `jq` cancelled-at-gate exits straight from the file. The latest
// gate result for the stage is the one recorded.
func TestWriteStageExitRecord_GateNoOpSnapshot(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3863, "item-id")
	runtime.RunID = "run-3863-noop"
	item := types.BoardItem{Number: 3863, Repo: "nightgauge/nightgauge"}

	// Mirror the scheduler: the gate result is appended to the runtime before
	// the exit record is written. KindNoOp == pr-merge said success but the PR
	// never merged / pr_number is null.
	runtime.AppendStageGateResult(state.StagePRMerge, state.StageGateResult{
		GateName: "pr-merge",
		Passed:   false,
		Reason:   "pr context missing pr_number",
		Kind:     "no_op",
	})

	// The gate failing flips the stage to a failure exit (exitCode=2, err set) —
	// this is the post-#3835 reality: a no-op gate is NOT recorded as success.
	s.writeStageExitRecord(item, state.StagePRMerge, runtime,
		&StageRunResult{ExitCode: 2}, 2, fmt.Errorf("stage gate failed: pr context missing pr_number"),
		0, "sonnet-4-5", 0, 0, 0, time.Now().Add(-time.Second), root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Success {
		t.Errorf("Success = true, want false (no-op gate is not success)")
	}
	if got.GateKind != "no_op" {
		t.Errorf("GateKind = %q, want %q", got.GateKind, "no_op")
	}
	if got.GateReason != "pr context missing pr_number" {
		t.Errorf("GateReason = %q", got.GateReason)
	}
}

// TestWriteStageExitRecord_PrematureTurnEndClassified verifies the #74 chain
// at the exit-record layer: the scheduler's `premature turn end:` stamp (set
// when a stage exits 0 but its gate reports KindNoOp) classifies into
// terminal_kind="premature_turn_end", with the no-op gate snapshot alongside
// so retros can jq the ended-on-a-promise exits straight from the file.
func TestWriteStageExitRecord_PrematureTurnEndClassified(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 74, "item-id")
	runtime.RunID = "run-74-premature"
	item := types.BoardItem{Number: 74, Repo: "nightgauge/nightgauge"}

	runtime.AppendStageGateResult(state.StageFeaturePlanning, state.StageGateResult{
		GateName: "feature-planning",
		Passed:   false,
		Reason:   "planning context file missing",
		Kind:     "no_op",
	})

	s.writeStageExitRecord(item, state.StageFeaturePlanning, runtime,
		&StageRunResult{ExitCode: 2}, 2,
		fmt.Errorf("premature turn end: stage exited 0 with no state change (gate no-op): planning context file missing"),
		0, "sonnet-4-5", 0, 0, 0, time.Now().Add(-time.Second), root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Success {
		t.Errorf("Success = true, want false")
	}
	if got.TerminalKind != TerminalKindPrematureTurnEnd {
		t.Errorf("TerminalKind = %q, want %q", got.TerminalKind, TerminalKindPrematureTurnEnd)
	}
	if got.GateKind != "no_op" {
		t.Errorf("GateKind = %q, want %q", got.GateKind, "no_op")
	}
}

// TestWriteStageExitRecord_GatePassedSnapshot verifies a passing gate records
// gate_kind="ok", and that when multiple gate results exist for a stage the
// latest one wins (the reconcile path can append a second result).
func TestWriteStageExitRecord_GatePassedSnapshot(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3863, "item-id")
	item := types.BoardItem{Number: 3863, Repo: "nightgauge/nightgauge"}

	// First a no_op, then a reconcile that passed — latest must win.
	runtime.AppendStageGateResult(state.StagePRMerge, state.StageGateResult{
		GateName: "pr-merge", Passed: false, Reason: "pr still OPEN", Kind: "no_op",
	})
	runtime.AppendStageGateResult(state.StagePRMerge, state.StageGateResult{
		GateName: "pr-merge", Passed: true, Reason: "PR #876 merged", Kind: "ok",
	})

	s.writeStageExitRecord(item, state.StagePRMerge, runtime,
		&StageRunResult{ExitCode: 0}, 0, nil, 0, "sonnet-4-5",
		0, 0, 0, time.Now().Add(-time.Second), root, "MERGED", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.GateKind != "ok" {
		t.Errorf("GateKind = %q, want %q (latest gate result wins)", got.GateKind, "ok")
	}
	if got.GateReason != "PR #876 merged" {
		t.Errorf("GateReason = %q", got.GateReason)
	}
}

// TestWriteStageExitRecord_NoGateLeavesGateFieldsEmpty confirms that a stage
// with no recorded gate result (e.g. a non-gated stage) produces empty
// gate_kind/gate_reason — omitempty keeps the daily line terse.
func TestWriteStageExitRecord_NoGateLeavesGateFieldsEmpty(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	root := t.TempDir()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 3863, "item-id")
	item := types.BoardItem{Number: 3863, Repo: "nightgauge/nightgauge"}

	s.writeStageExitRecord(item, state.StageFeatureDev, runtime,
		&StageRunResult{ExitCode: 0}, 0, nil, 0, "sonnet-4-5",
		0, 0, 0, time.Now().Add(-time.Second), root, "", "")

	recs := readExitRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].GateKind != "" || recs[0].GateReason != "" {
		t.Errorf("expected empty gate fields, got GateKind=%q GateReason=%q",
			recs[0].GateKind, recs[0].GateReason)
	}
}

// TestWriteStageExitRecord_EmptyWorkspaceRootIsNoop confirms that the writer
// silently skips when the scheduler has no workspace root configured (test
// harness convention). This prevents tests that don't care about diagnostics
// from accidentally scribbling into CWD.
func TestWriteStageExitRecord_EmptyWorkspaceRootIsNoop(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	runtime := state.NewRuntimeState("repo", 1, "id")
	item := types.BoardItem{Number: 1, Repo: "repo"}
	// Should not panic, should not error, should not write.
	s.writeStageExitRecord(item, state.StageFeatureDev, runtime,
		&StageRunResult{ExitCode: 0}, 0, nil, 0, "sonnet-4-5",
		0, 0, 0, time.Now(), "", "", "")
}

// TestSnapshotConcurrentPipelines_ExcludesSelf asserts that a stage exit
// record's concurrent_pipelines_at_exit lists every other active stage in
// the scheduler (the cross-pipeline forensic signal) and never the caller
// itself.
func TestSnapshotConcurrentPipelines_ExcludesSelf(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	// Seed two siblings + self.
	_, c1 := context.WithCancel(context.Background())
	_, c2 := context.WithCancel(context.Background())
	_, c3 := context.WithCancel(context.Background())
	s.registerActiveStage(3591,
		func(error) { c1() })
	s.registerActiveStage(3604,
		func(error) { c2() })
	s.registerActiveStage(3605,
		func(error) { c3() })

	got := s.snapshotConcurrentPipelines("nightgauge/nightgauge", 3605)
	if len(got) != 2 {
		t.Fatalf("expected 2 siblings, got %d (%v)", len(got), got)
	}
	for _, key := range got {
		if !strings.HasPrefix(key, "?#") {
			t.Errorf("sibling key %q missing ?# prefix", key)
		}
		if key == fmt.Sprintf("?#%d", 3605) {
			t.Errorf("self leaked into sibling list: %q", key)
		}
	}
}

// TestSnapshotConcurrentPipelines_EmptyWhenAlone returns nil (not []string{})
// when the caller is the only active pipeline. nil flows through `omitempty`
// on the JSON tag so healthy single-pipeline runs don't carry a useless
// empty array.
func TestSnapshotConcurrentPipelines_EmptyWhenAlone(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	_, c1 := context.WithCancel(context.Background())
	s.registerActiveStage(3605, func(error) { c1() })

	got := s.snapshotConcurrentPipelines("repo", 3605)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// TestRateLimitRemainingAtExit_NoClientReturnsSentinel verifies the
// -1 sentinel for "tracker unavailable" when the scheduler runs without a
// configured github client. Production code distinguishes -1 from 0
// (genuinely empty bucket) via this sentinel — see #3368 retro.
func TestRateLimitRemainingAtExit_NoClientReturnsSentinel(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	if got := s.rateLimitRemainingAtExit(); got != -1 {
		t.Errorf("rateLimitRemainingAtExit = %d, want -1 (no tracker)", got)
	}
}

// TestWriteStageExitRecord_DailyPathHasOwnerSlashName confirms the on-disk
// path stays `.nightgauge/pipeline/exit-records/YYYY-MM-DD.jsonl` —
// any rename of this path would break the `exit-records tail` CLI reader.
func TestWriteStageExitRecord_DailyPathHasOwnerSlashName(t *testing.T) {
	root := t.TempDir()
	s := newSchedulerForDeterministicTest()
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 1, "id")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/nightgauge"}

	s.writeStageExitRecord(item, state.StageIssuePickup, runtime,
		&StageRunResult{ExitCode: 0}, 0, nil, 0, "sonnet-4-5",
		0, 0, 0, time.Now(), root, "", "")

	expected := filepath.Join(root, ".nightgauge", "pipeline", "exit-records")
	entries, err := os.ReadDir(expected)
	if err != nil {
		t.Fatalf("read dir %s: %v", expected, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in %s, got %d", expected, len(entries))
	}
	name := entries[0].Name()
	if !strings.HasSuffix(name, ".jsonl") {
		t.Errorf("daily filename %q missing .jsonl suffix", name)
	}
	if len(name) != len("YYYY-MM-DD.jsonl") {
		t.Errorf("daily filename %q does not match YYYY-MM-DD.jsonl shape", name)
	}
}
