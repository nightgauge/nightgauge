// Tests for the interactive V2/V3 RunRecord write path (#232). The
// extension/HeadlessOrchestrator path funnels every terminal exit through
// pipeline.notifyComplete, which is now the sole authoritative writer of the
// interactive RunRecord (the Go scheduler path writes its own via
// OnPipelineComplete). These tests pin that a RunRecord lands in the run's
// target-repo history JSONL for BOTH success and failure — and that a failed
// run still has a runtime to record (the failed stage transition no longer
// eagerly drops it).
package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// readHistoryRecords returns the V2 run records in today's daily history file
// under root/.nightgauge/pipeline/history (or an empty slice when absent).
func readHistoryRecords(t *testing.T, root string) []state.V2RunRecord {
	t.Helper()
	path := filepath.Join(root, ".nightgauge", "pipeline", "history", time.Now().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read history file: %v", err)
	}
	var out []state.V2RunRecord
	for _, line := range splitLinesTest(data) {
		if len(line) == 0 {
			continue
		}
		var rec state.V2RunRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("decode history line: %v\nline=%q", err, string(line))
		}
		out = append(out, rec)
	}
	return out
}

func splitLinesTest(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// A successful interactive run must write exactly one V2 RunRecord to the
// target repo's history JSONL, carrying the run's stable UUID and issue number.
func TestNotifyComplete_WritesSuccessRunRecord(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":232,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":232,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]
	if rec.RecordType != "run" {
		t.Errorf("RecordType = %q, want %q", rec.RecordType, "run")
	}
	if rec.Outcome != "complete" {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, "complete")
	}
	if rec.IssueNumber != 232 {
		t.Errorf("IssueNumber = %d, want 232", rec.IssueNumber)
	}
	if rec.RunID == "" {
		t.Error("RunID must be set (threaded from the runtime's stable UUID)")
	}
	if rec.SchemaVersion != "2" {
		t.Errorf("SchemaVersion = %q, want %q for a successful run", rec.SchemaVersion, "2")
	}
}

// #268: the served model + adapter the extension threads through
// notifyStageTransition must land on the V2 RunRecord — per-stage
// ModelSelection (→ StageMetric.model → cost_events.model_id, the by-model
// breakdown) and per-stage token Adapter (→ StageMetric.provider →
// pipeline_events.adapter, the Adapter Mix donut). Before this fix the
// VSCode-orchestrated notify path recorded neither, so both were null/'unknown'.
func TestNotifyComplete_AttributesStageModelAndAdapter(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	// running carries the requested model; complete carries the authoritative
	// served model + adapter — latest-wins, so the served model must win.
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":268,"stage":"feature-dev","status":"running","model":"claude-sonnet-4-5","adapter":"claude"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":268,"stage":"feature-dev","status":"complete","model":"claude-opus-4-8","adapter":"claude","inputTokens":1000,"outputTokens":200,"costUsd":0.05}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":268,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]

	detail, ok := rec.Stages["feature-dev"]
	if !ok {
		t.Fatalf("feature-dev stage missing from record; stages=%v", rec.Stages)
	}
	if detail.ModelSelection == nil {
		t.Fatal("feature-dev ModelSelection is nil — served model was not attributed")
	}
	if detail.ModelSelection.Model != "claude-opus-4-8" {
		t.Errorf("feature-dev ModelSelection.Model = %q, want claude-opus-4-8 (served model wins over requested)", detail.ModelSelection.Model)
	}
	tok, ok := rec.Tokens.PerStage["feature-dev"]
	if !ok {
		t.Fatalf("feature-dev per-stage tokens missing; per_stage=%v", rec.Tokens.PerStage)
	}
	if tok.Adapter != "claude" {
		t.Errorf("feature-dev token Adapter = %q, want claude", tok.Adapter)
	}
}

// A failed interactive run must ALSO write a RunRecord — the failed stage
// transition must no longer eagerly drop the runtime, so notifyComplete can
// still find it and build the V3 failure record (terminal_failure_kind set).
func TestNotifyComplete_WritesFailureRunRecordAndRuntimeSurvivesFailedTransition(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":233,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":233,"stage":"feature-dev","status":"failed","error":"context deadline exceeded"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}

	// The runtime must survive the failed transition so notifyComplete can
	// record the failure — assert it is still present before the terminal event.
	s.runtimesMu.Lock()
	_, alive := s.activeRuntimes["233"]
	s.runtimesMu.Unlock()
	if !alive {
		t.Fatal("runtime must survive the failed transition so notifyComplete can build the failed RunRecord")
	}

	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":233,"success":false,"totalDurationMs":2000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]
	if rec.Outcome != "failed" {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, "failed")
	}
	if rec.IssueNumber != 233 {
		t.Errorf("IssueNumber = %d, want 233", rec.IssueNumber)
	}
	if rec.TerminalFailureKind == "" {
		t.Error("TerminalFailureKind must be set for a failed run (falls back to subagent_crash when unclassifiable)")
	}
	if rec.SchemaVersion != "3" {
		t.Errorf("SchemaVersion = %q, want %q (V3 once terminal_failure_kind is populated)", rec.SchemaVersion, "3")
	}

	// notifyComplete must also drop the runtime (terminal cleanup).
	s.runtimesMu.Lock()
	_, stillAlive := s.activeRuntimes["233"]
	s.runtimesMu.Unlock()
	if stillAlive {
		t.Error("runtime must be cleaned up by notifyComplete after the terminal event")
	}
}

// Multi-repo scoping (#232): the RunRecord must land in the run's registered
// target repo history dir, not the IPC server's launch root.
func TestNotifyComplete_WritesRunRecordIntoTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launchRoot))
	s.RegisterRepo("nightgauge", "acmeapp", targetRoot)

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":244,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":244,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	if got := readHistoryRecords(t, targetRoot); len(got) != 1 {
		t.Fatalf("expected one RunRecord in the target repo, got %d", len(got))
	}
	if got := readHistoryRecords(t, launchRoot); len(got) != 0 {
		t.Fatalf("no RunRecord may leak into the launch root, got %d", len(got))
	}
}

// #266: the pure ground-truth reconciliation. A merged pr-merge that a late kill
// reported as failed must be booked complete; a failure at any other stage — or
// a genuinely unmerged pr-merge — must survive. Reported success is never
// downgraded.
func TestReconcilePrMergeGroundTruth(t *testing.T) {
	cases := []struct {
		name            string
		reportedSuccess bool
		prMerged        bool
		terminalStage   string
		want            bool
	}{
		{"reported success is always preserved", true, false, "pr-merge", true},
		{"failed + not merged stays failed", false, false, "pr-merge", false},
		{"failed + merged at pr-merge flips to complete", false, true, "pr-merge", true},
		{"failed + merged at pr-merge (case-insensitive)", false, true, "PR-MERGE", true},
		{"failed + merged at a later stage stays failed", false, true, "pipeline-finish", false},
		{"failed + merged at an earlier stage stays failed", false, true, "feature-dev", false},
		{"success + merged stays success", true, true, "pipeline-finish", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcilePrMergeGroundTruth(tc.reportedSuccess, tc.prMerged, tc.terminalStage); got != tc.want {
				t.Errorf(
					"reconcilePrMergeGroundTruth(reported=%v, prMerged=%v, stage=%q) = %v, want %v",
					tc.reportedSuccess, tc.prMerged, tc.terminalStage, got, tc.want,
				)
			}
		})
	}
}

// #266 regression: the escalation-race misattribution booked bowlsheet #261 (a
// MERGED run) as failed/stall_kill. When the extension signals a forge-confirmed
// merge (prMerged=true) but a late progress-runaway kill reported the pr-merge
// stage failed, the recording boundary must write a COMPLETE RunRecord with NO
// terminal_failure_kind — never a phantom stall_kill.
func TestNotifyComplete_MergedPrMergeFailureRecordedComplete(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":266,"stage":"pr-merge","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	// A late progress-runaway kill fires at pr-merge AFTER the merge landed.
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":266,"stage":"pr-merge","status":"failed","error":"[runaway-progress-exceeded] Stage pr-merge terminated: progress stalled"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}

	// Extension reports the run failed but signals a forge-confirmed merge.
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":266,"success":false,"totalDurationMs":3000,"prMerged":true}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]
	if rec.Outcome != "complete" {
		t.Errorf("Outcome = %q, want %q — a merged pr-merge must not record as failed (#266)", rec.Outcome, "complete")
	}
	if rec.TerminalFailureKind != "" {
		t.Errorf("TerminalFailureKind = %q, want empty for a ground-truth-merged run (#266)", rec.TerminalFailureKind)
	}
	if rec.SchemaVersion != "2" {
		t.Errorf("SchemaVersion = %q, want %q for a run recorded complete", rec.SchemaVersion, "2")
	}
}

// #266 guard scoping: without a forge-confirmed merge (prMerged omitted/false),
// a genuine pr-merge runaway kill must STILL record as failed with the transient
// runaway_progress kind. The ground-truth override must not over-fire.
func TestNotifyComplete_UnmergedPrMergeFailureStaysFailed(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":268,"stage":"pr-merge","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":268,"stage":"pr-merge","status":"failed","error":"[runaway-progress-exceeded] Stage pr-merge terminated: progress stalled"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}

	// No prMerged signal — the PR did not merge; this is a real failure.
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":268,"success":false,"totalDurationMs":3000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]
	if rec.Outcome != "failed" {
		t.Errorf("Outcome = %q, want %q — an unmerged pr-merge failure must survive (#266)", rec.Outcome, "failed")
	}
	if rec.TerminalFailureKind != "runaway_progress" {
		t.Errorf("TerminalFailureKind = %q, want %q (transient runaway-progress classification)", rec.TerminalFailureKind, "runaway_progress")
	}
}

// #309: the dogfood-path observability regression. The TS HeadlessOrchestrator
// runs the deterministic-first pr-create/pr-merge hooks in-process and threads
// its stageExecutionPaths map through pipeline.notifyComplete. The Go handler
// must replay those decisions onto the runtime BEFORE it snapshots, so the
// authoritative history stage records carry execution_path (and punt_reason
// when a deterministic attempt punted). This mirrors the exact platform#209
// evidence: pr-merge ran deterministically ($0), pr-create punted to the LLM —
// yet the record was silent on both until this fix.
func TestNotifyComplete_ThreadsStageExecutionPathsFromParams(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	// pr-create ran (its LLM path completed) and pr-merge ran (deterministic).
	for _, stage := range []string{"pr-create", "pr-merge"} {
		if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":309,"stage":"`+stage+`","status":"running"}`)); err != nil {
			t.Fatalf("notifyStageTransition(%s running): %v", stage, err)
		}
		if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":309,"stage":"`+stage+`","status":"complete"}`)); err != nil {
			t.Fatalf("notifyStageTransition(%s complete): %v", stage, err)
		}
	}

	// notifyComplete carries the orchestrator's per-stage execution-path map:
	// pr-merge=deterministic (no punt reason), pr-create=llm (punted, with why).
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":309,"success":true,"totalDurationMs":1000,`+
		`"stageExecutionPaths":{"pr-merge":"deterministic","pr-create":"llm"},`+
		`"stagePuntReasons":{"pr-create":"missing-validate-context"}}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]

	merge, ok := rec.Stages["pr-merge"]
	if !ok {
		t.Fatalf("pr-merge stage missing from record; stages=%v", rec.Stages)
	}
	if merge.ExecutionPath != "deterministic" {
		t.Errorf("pr-merge execution_path = %q, want deterministic (the $0 deterministic runner ran)", merge.ExecutionPath)
	}
	if merge.PuntReason != "" {
		t.Errorf("pr-merge punt_reason = %q, want empty (deterministic path did not punt)", merge.PuntReason)
	}

	create, ok := rec.Stages["pr-create"]
	if !ok {
		t.Fatalf("pr-create stage missing from record; stages=%v", rec.Stages)
	}
	if create.ExecutionPath != "llm" {
		t.Errorf("pr-create execution_path = %q, want llm (the deterministic runner punted)", create.ExecutionPath)
	}
	if create.PuntReason != "missing-validate-context" {
		t.Errorf("pr-create punt_reason = %q, want %q — the record must answer WHY the LLM ran", create.PuntReason, "missing-validate-context")
	}
}
