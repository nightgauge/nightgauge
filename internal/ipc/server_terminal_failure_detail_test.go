// #1329: a terminal failure that never started a stage must still persist its
// reason. The TS layer forwards the failure text as `failureDetail`; before
// this fix the notifyComplete handler wrote a record carrying only the generic
// `subagent_crash` fallback kind, `stages: {}`, and no exit record — a red run
// with no reason anywhere on disk.
package ipc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/state"
)

func readExitRecords(t *testing.T, root string) []diagnostics.StageExitRecord {
	t.Helper()
	data, err := os.ReadFile(diagnostics.DailyFilePath(root, time.Now()))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read exit records: %v", err)
	}
	var out []diagnostics.StageExitRecord
	for _, line := range splitLinesTest(data) {
		if len(line) == 0 {
			continue
		}
		var rec diagnostics.StageExitRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("decode exit record: %v\nline=%q", err, string(line))
		}
		out = append(out, rec)
	}
	return out
}

// An unclassifiable pre-stage error: the run was claimed (a running
// transition), no stage ever failed, and the extension reports failure with
// the raw error text. The record's detail must equal the forwarded message and
// the run must leave an exit record even though no stage exited.
func TestNotifyComplete_PreStageFailurePersistsFailureDetail(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	const runID = "019001c1-0000-7000-8000-000000001329"
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":1329,"stage":"issue-pickup","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	const detail = "ENOENT: no such file or directory, open '/workspace/.nightgauge/config.yaml'"
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":1329,"success":false,"totalDurationMs":1239,"failureDetail":`+strconvQuote(detail)+`,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]
	if rec.Outcome != "failed" {
		t.Errorf("Outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind == "" {
		t.Error("TerminalFailureKind must be populated on a failed record")
	}
	if rec.TerminalFailureDetail != detail {
		t.Errorf("TerminalFailureDetail = %q, want the forwarded message %q", rec.TerminalFailureDetail, detail)
	}
	if rec.SchemaVersion != "3" {
		t.Errorf("SchemaVersion = %q, want 3 for a failed record", rec.SchemaVersion)
	}

	exits := readExitRecords(t, dir)
	if len(exits) != 1 {
		t.Fatalf("expected one pre-dispatch exit record, got %d", len(exits))
	}
	ex := exits[0]
	if ex.Stage != diagnostics.StagePreDispatch {
		t.Errorf("exit record stage = %q, want %q", ex.Stage, diagnostics.StagePreDispatch)
	}
	if ex.Success {
		t.Error("exit record must be a failure")
	}
	if ex.RunID != runID {
		t.Errorf("exit record run_id = %q, want %q", ex.RunID, runID)
	}
	if ex.FailureDetail != detail {
		t.Errorf("exit record failure_detail = %q, want %q", ex.FailureDetail, detail)
	}
	if ex.TerminalKind != rec.TerminalFailureKind {
		t.Errorf("exit record terminal_kind = %q, want the record's %q", ex.TerminalKind, rec.TerminalFailureKind)
	}
}

// A failure that DID exit a stage keeps the stage's own error as the detail
// and writes no synthetic pre-dispatch exit record — the stage's real exit
// record (written by diagnostics.recordStageExit) is the one that counts.
func TestNotifyComplete_StageFailureKeepsStageErrorAsDetail(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	const runID = "019001c1-0001-7000-8000-000000001329"
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":1329,"stage":"feature-dev","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":1329,"stage":"feature-dev","status":"failed","error":"[stall-kill] idle 1800s","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":1329,"success":false,"totalDurationMs":5,"failureDetail":"outer wrapper text","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected one RunRecord, got %d", len(records))
	}
	if got := records[0].TerminalFailureDetail; got != "[stall-kill] idle 1800s" {
		t.Errorf("TerminalFailureDetail = %q, want the stage's own error", got)
	}
	if exits := readExitRecords(t, dir); len(exits) != 0 {
		t.Errorf("expected no synthetic exit record when a stage failed, got %d", len(exits))
	}
}

// The detail is bounded and tail-truncated: the END of an error is where the
// cause usually sits (stack traces and wrappers prefix it).
func TestBuildV2Record_TerminalFailureDetailIsTailTruncated(t *testing.T) {
	hw := state.NewHistoryWriter(t.TempDir())
	long := strings.Repeat("x", state.TerminalFailureDetailMax) + "TAIL"
	snap := &state.RuntimeState{IssueNumber: 1, Stage: state.StageFeatureDev}
	rec := hw.BuildV2Record(snap, false, long, state.V2RunInput{
		TerminalFailureKind:   "subagent_crash",
		TerminalFailureDetail: long,
	}, time.Now())
	if len([]rune(rec.TerminalFailureDetail)) != state.TerminalFailureDetailMax {
		t.Fatalf("detail length = %d, want %d", len([]rune(rec.TerminalFailureDetail)), state.TerminalFailureDetailMax)
	}
	if !strings.HasSuffix(rec.TerminalFailureDetail, "TAIL") {
		t.Errorf("detail must keep the tail; got suffix %q", rec.TerminalFailureDetail[len(rec.TerminalFailureDetail)-8:])
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
