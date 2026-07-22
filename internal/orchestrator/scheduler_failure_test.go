// Tests covering Issue #3001 — terminal failure preservation, queue pause,
// and orchestrator-crash recovery. These exercise the additive surface added
// in failure_handler.go and the scheduler's deferred recordV2History path.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// readDailyJSONLRecords reads every line from today's history JSONL file
// and returns parsed V2RunRecord values. Test helper.
func readDailyJSONLRecords(t *testing.T, workspaceRoot string) []state.V2RunRecord {
	t.Helper()
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	day := time.Now().Format("2006-01-02") + ".jsonl"
	data, err := os.ReadFile(filepath.Join(dir, day))
	if err != nil {
		t.Fatalf("read daily JSONL: %v", err)
	}
	var out []state.V2RunRecord
	for _, line := range splitJSONLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec state.V2RunRecord
		if uerr := json.Unmarshal(line, &rec); uerr != nil {
			t.Fatalf("parse record %q: %v", string(line), uerr)
		}
		out = append(out, rec)
	}
	return out
}

func splitJSONLines(data []byte) [][]byte {
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

// TestClassifyTerminalKind covers the heuristic mapping from error text to
// terminal kind so future error-string changes in the failure paths get caught
// by tests rather than silently dropping records into the "unknown" bucket.
func TestClassifyTerminalKind(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want string
	}{
		{"empty", "", ""},
		{"stall_kill", "subagent stalled and killed after 4800s", TerminalKindStallKill},
		{"stall_threshold", "feature-dev stall kill threshold reached", TerminalKindStallKill},
		{"hard_cap", "pr-create hard cap exceeded", TerminalKindStallKill},
		// Issue #3207 — canonical IPC markers from PipelineBridge.
		{"ipc_stall_marker", "[stall-killed] feature-dev terminated", TerminalKindStallKill},
		// #252 — zombie-run guards: reload-swept orphan slots and the
		// first-output watchdog both classify as transient stall (retry with
		// backoff, no lifetime-cap increment).
		{
			"stale_slot_orphan_marker",
			`[stale-slot-orphan] process not running after extension reload; stage was stuck in "running" (PID 12168 exited)`,
			TerminalKindStallKill,
		},
		{
			"stage_no_output_timeout_marker",
			"[stage-no-output-timeout] Stage feature-dev produced no output within 10 minutes of start — presumed wedged during startup (pre-spawn await or silent session). Failing the stage so the run can terminate and retry. (#252)",
			TerminalKindStallKill,
		},
		{"ipc_stall_idle_threshold", "exceeded stall idle threshold (1200s without output)", TerminalKindStallKill},
		{"ipc_stall_hard_cap", "exceeded stage_hard_cap (4800s total runtime)", TerminalKindStallKill},
		{"ipc_cost_cap_marker", "[cost-cap-exceeded] pr-create terminated ($5.20 ≥ $5.00 cap)", TerminalKindBudgetExceeded},
		{"budget_pipeline", "pipeline_budget_exceeded: 12345 > 10000", TerminalKindBudgetExceeded},
		{"budget_stage", "stage_budget_exceeded for feature-dev", TerminalKindBudgetExceeded},
		// Reclassified by #74: validateStageOutput's phrase only ever fires
		// on exit-0 paths, so it IS the ended-on-a-promise failure mode
		// (was validation_error before #74).
		{"validation_missing_output", "stage feature-dev exited 0 but did not write expected output context: /x", TerminalKindPrematureTurnEnd},
		{"subagent_exit", "exit 1: subprocess died", TerminalKindSubagentCrash},
		// Issue #74 — premature turn end: the scheduler stamps this marker
		// when a stage exits 0 but its post-condition gate reports KindNoOp
		// (the agent ended its turn on a promise). Matched BEFORE the
		// validation heuristics so the embedded gate reason — which names
		// the missing context file — doesn't bucket into validation_error.
		{
			"premature_turn_end_scheduler_stamp",
			"premature turn end: stage exited 0 with no state change (gate no-op): planning context file missing",
			TerminalKindPrematureTurnEnd,
		},
		{
			"premature_turn_end_underscore_form",
			"stage feature-planning: premature_turn_end recorded by exit diagnostics",
			TerminalKindPrematureTurnEnd,
		},
		// #3691 precedence: a pr-merge no-op wrapped in the #74 stamp keeps
		// the richer pr_merge_unmerged classification — its matcher runs
		// first and carries PR-specific recovery semantics.
		{
			"premature_turn_end_prmerge_keeps_unmerged",
			"premature turn end: stage exited 0 with no state change (gate no-op): pr-merge reported success but PR #55 is not merged (state: OPEN)",
			TerminalKindPrMergeUnmerged,
		},
		// Issue #3398 — Anthropic stream idle timeout (the literal CLI message).
		{
			"stream_idle_timeout_canonical",
			"API Error: Stream idle timeout - partial response received",
			TerminalKindStreamIdleTimeout,
		},
		{
			"stream_idle_timeout_lowercase",
			"stream idle timeout occurred while waiting for the next chunk",
			TerminalKindStreamIdleTimeout,
		},
		// Issue #3386 — rate-limit quota exhausted (silent stall pattern).
		// Marker text comes from skillRunner when an idle stall fires AND
		// the last rate_limit_event indicated quota exhaustion. MUST match
		// before the generic stall-kill heuristics — the kill reason
		// embeds the "stall idle threshold" phrase below it.
		{
			"rate_limit_quota_exhausted_marker",
			"[rate-limit-quota-exhausted] idle 1200s after rate_limit_event with overage rejected (five_hour bucket)",
			TerminalKindRateLimitQuotaExhausted,
		},
		{
			"rate_limit_quota_exhausted_underscore_form",
			"stage feature-dev: rate_limit_quota_exhausted while waiting for bucket reset",
			TerminalKindRateLimitQuotaExhausted,
		},
		// The classifier MUST match stream-idle-timeout BEFORE the generic
		// stall-kill heuristics — the literal "timeout" substring in the
		// message would otherwise route into infra/stall and bypass the
		// per-category retry policy.
		// Issue #3835 (WS4) — Anthropic 529 "Overloaded" is a transient capacity
		// blip and MUST classify as api_overloaded, not fall through to
		// subagent_crash (which pauses the queue and counts toward the cap).
		{
			"api_overloaded_canonical",
			"API Error: Overloaded",
			TerminalKindApiOverloaded,
		},
		{
			"api_overloaded_lowercase_embedded",
			"stage feature-dev: api error: overloaded (529)",
			TerminalKindApiOverloaded,
		},
		// Issue #3896 — GitHub API quota too low at pipeline-start. Both the
		// explicit stderr marker and the error-text token (embedded so the Go
		// fallback can re-classify failureDetail) must route to github_quota_low,
		// NOT bucket into a generic auth/validation/crash kind.
		{
			"github_quota_low_marker",
			"[pipeline-start-failure] github-quota-low: GitHub API quota too low to start pipeline (8/5000 remaining, need ≥200). Resets in ~1 min. (transient; resetInSec=58)",
			TerminalKindGitHubQuotaLow,
		},
		{
			"github_quota_low_error_token",
			"[github-quota-low] GitHub API quota too low — pipeline deferred before AI stages (transient; resetInSec=58).",
			TerminalKindGitHubQuotaLow,
		},
		{
			"github_quota_low_underscore_form",
			"stage pipeline-start: github_quota_low while waiting for bucket reset",
			TerminalKindGitHubQuotaLow,
		},
		// Issue #4002 — Anthropic API transport drop (the literal CLI message
		// from the acmeapp incident). Must classify environmental, NOT fall
		// through to subagent_crash (which pauses the queue and counts toward
		// the lifetime cap).
		{
			"api_connection_lost_canonical",
			"API Error: The socket connection was closed unexpectedly",
			TerminalKindApiConnectionLost,
		},
		{
			"api_connection_lost_hang_up",
			"API Error: socket hang up",
			TerminalKindApiConnectionLost,
		},
		{
			"api_connection_lost_dns",
			"api error: getaddrinfo ENOTFOUND api.anthropic.com",
			TerminalKindApiConnectionLost,
		},
		// A bare error code WITHOUT the "api error" context must NOT classify
		// as a transport drop — e.g. a failing integration test that merely
		// mentions ECONNRESET in its output.
		{
			"econnreset_without_api_context",
			"exit 1: test server.test.ts failed: read ECONNRESET",
			TerminalKindSubagentCrash,
		},
		// Issue #4002 — GitHub unreachable at pipeline-start. Both the stderr
		// marker and the error-text token must route to github_network_outage.
		{
			"github_network_outage_marker",
			"[pipeline-start-failure] github-network-outage: GitHub API unreachable — `gh auth status` could not connect to api.github.com. (transient; retryInSec=120)",
			TerminalKindGitHubNetworkOutage,
		},
		{
			"github_network_outage_error_token",
			"[github-network-outage] GitHub API unreachable — pipeline deferred before AI stages (transient; retryInSec=120).",
			TerminalKindGitHubNetworkOutage,
		},
		{
			"github_network_outage_underscore_form",
			"stage pipeline-start: github_network_outage while waiting for connectivity",
			TerminalKindGitHubNetworkOutage,
		},
		// A REAL auth failure (gh exits non-zero with no connectivity
		// signature) must NOT classify as github_network_outage — it stays on
		// the generic path (the "exit " heuristic buckets it subagent_crash)
		// so the queue pauses and the operator is paged to re-authenticate.
		{
			"github_auth_failed_not_environmental",
			"[pipeline-start-failure] github-auth-failed: GitHub auth check failed: `gh auth status` returned a non-zero exit code. Run `gh auth login` to authenticate.",
			TerminalKindSubagentCrash,
		},
		// Issue #305 — blocked-dependency deferral is a NON-FAILURE. Both the
		// TS-stamped bracket marker and the underscore form (used by the
		// NotifyComplete defense-in-depth reclassify) must route to
		// blocked_dependency, NOT bucket into a generic crash/validation kind.
		{
			"blocked_dependency_marker",
			"[blocked-dependency] issue #305 dispatched while blockedBy #300 is still open — deferring",
			TerminalKindBlockedDependency,
		},
		{
			"blocked_dependency_marker_case_insensitive",
			"[Blocked-Dependency] deferred: dependencies still open",
			TerminalKindBlockedDependency,
		},
		{
			"blocked_dependency_underscore_form",
			"pipeline deferred: blocked_dependency (blockedBy still open)",
			TerminalKindBlockedDependency,
		},
		// Issue #312 — adapter auth pre-flight failure. A probe TIMEOUT under a
		// concurrent dispatch burst must route to adapter_auth_failed (retryable
		// infra), NOT subagent_crash — even though the human-readable reason
		// mentions "timed out". The stable `[adapter-auth-failed]` marker wins.
		{
			"adapter_auth_failed_timeout_marker",
			"[adapter-auth-failed] Auth pre-flight failed — auth probe timed out after retry (adapter CLI unresponsive — transient, not a logged-out session). Pipeline halted before AI stages (zero tokens spent).\n- **claude-headless**: auth probe timed out after 5s and again after 10s on retry",
			TerminalKindAdapterAuthFailed,
		},
		// A definitive logged-out negative also routes to adapter_auth_failed —
		// it is a credential state, not a subagent process death, so it must not
		// feed the cascade breaker as a crash.
		{
			"adapter_auth_failed_logged_out_marker",
			"[adapter-auth-failed] Auth pre-flight failed — adapter not authenticated. Pipeline halted before AI stages (zero tokens spent).\n- **claude-headless**: claude CLI is not authenticated. Run `claude auth login`.",
			TerminalKindAdapterAuthFailed,
		},
		// The stderr wrapper form (`adapter-auth-failed` without brackets) and
		// the underscore kind (NotifyComplete defense-in-depth reclassify) both
		// resolve to the same kind.
		{
			"adapter_auth_failed_stderr_wrapper",
			"[pipeline-start-failure] adapter-auth-failed: claude-headless=auth probe timed out after 5s",
			TerminalKindAdapterAuthFailed,
		},
		{
			"adapter_auth_failed_underscore_form",
			"pipeline-start halted: adapter_auth_failed (probe timed out)",
			TerminalKindAdapterAuthFailed,
		},
		// Issue #317 — a human-only (`owner-action`) issue dispatched before the
		// exclusion existed correctly produced zero commits and pr-create's
		// deterministic fallback declined to fabricate a PR. The stable
		// `[no-changes-produced]` marker must win BEFORE the generic
		// subagent_crash fallback below, even though the human-readable message
		// also says "the skill may have exited" (which alone would NOT match —
		// "exited" lacks the trailing space "exit " requires — but the marker
		// makes the classification explicit and robust to wording changes).
		{
			"no_changes_produced_marker",
			`pr-create reported success but no open PR exists (pr context file missing). Deterministic fallback could not open one: [no-changes-produced] feature branch "feat/317-x" has no commits ahead of main. The skill may have exited without pushing the branch or opening the PR.`,
			TerminalKindNoChangesProduced,
		},
		{
			"no_changes_produced_underscore_form",
			"pr-create declined: no_changes_produced (zero commits ahead of base)",
			TerminalKindNoChangesProduced,
		},
		// The bare "no commits ahead of" phrase (without the marker) must NOT
		// classify as no_changes_produced on its own — that phrase also appears
		// in feature-validate's unrelated lost-implementation check, which is a
		// genuine organic defect and must keep falling through to the generic
		// subagent_crash path (it says "exited" which also doesn't match "exit ",
		// so today it lands in "" — the important assertion is that it does NOT
		// land in TerminalKindNoChangesProduced).
		{
			"lost_implementation_not_reclassified",
			`feature-validate reported success but the commit contract (#1608) is unmet: the branch has no commits ahead of origin/main AND the working tree has no source changes, while the dev context lists 2 implemented file(s) (e.g. src/foo.ts). The implementation was lost or never written — advancing to pr-create would push an empty branch.`,
			"",
		},
		// Issue #326 — feature-validate honestly failed its own quality gates:
		// validation_status="failed" written to the context, code deliberately
		// left uncommitted for retry. This is an organic implementation defect,
		// not a subagent process death, so the stable `[validation-failed]`
		// marker must win BEFORE the generic subagent_crash fallback below —
		// the human-readable text never mentions "exit " or "crash", but a
		// wrapping caller's "exit 2: ..." prefix could otherwise misbucket it.
		{
			"validation_failed_marker",
			`[validation-failed] feature-validate reported validation_status="failed" (tests-failed). The validated code was intentionally NOT committed or pushed — the skill leaves it on disk for retry. Advancing to pr-create would push an empty branch and fail the no-commits-ahead gate. Halting at feature-validate so the failure is surfaced for retry/triage instead.`,
			TerminalKindValidationFailed,
		},
		{
			"validation_failed_marker_case_insensitive",
			`[Validation-Failed] feature-validate reported validation_status="failed"`,
			TerminalKindValidationFailed,
		},
		{
			"validation_failed_underscore_form",
			"pipeline halted: validation_failed (quality gates did not pass)",
			TerminalKindValidationFailed,
		},
		{"unclassifiable", "something brand new and weird", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyTerminalKind(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestPauseQueuedItemsUnlocked verifies that an active queue-pause action
// only touches pending/ready items and copies the reason struct so each item
// owns its own pointer (mutating one doesn't poison others).
func TestPauseQueuedItemsUnlocked(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s.queue = []QueueItem{
		{IssueNumber: 1, Status: "pending"},
		{IssueNumber: 2, Status: "ready"},
		{IssueNumber: 3, Status: "completed"},
		{IssueNumber: 4, Status: "failed"},
		{IssueNumber: 5, Status: ""}, // empty status is treated like pending
	}
	reason := QueuePausedReason{
		Kind:        "upstream_failure",
		FailedRunID: "42-2026-04-25T00:00:00Z",
		Summary:     "stage feature-dev: stall_kill",
	}
	s.mu.Lock()
	count := s.pauseQueuedItemsUnlocked(reason)
	s.mu.Unlock()

	if count != 3 {
		t.Errorf("paused count = %d, want 3 (#1, #2, #5)", count)
	}
	for _, want := range []int{1, 2, 5} {
		idx := want - 1
		if s.queue[idx].Status != "paused" {
			t.Errorf("queue[#%d].Status = %q, want paused", want, s.queue[idx].Status)
		}
		if s.queue[idx].PausedReason == nil {
			t.Fatalf("queue[#%d].PausedReason is nil", want)
		}
		if s.queue[idx].PausedReason.FailedRunID != reason.FailedRunID {
			t.Errorf("queue[#%d].PausedReason.FailedRunID = %q, want %q",
				want, s.queue[idx].PausedReason.FailedRunID, reason.FailedRunID)
		}
	}
	// Completed/failed items must remain untouched — paused-on-failure should
	// never transition a terminal item back into a non-terminal state.
	if s.queue[2].Status != "completed" {
		t.Errorf("queue[#3].Status = %q, want completed (untouched)", s.queue[2].Status)
	}
	if s.queue[3].Status != "failed" {
		t.Errorf("queue[#4].Status = %q, want failed (untouched)", s.queue[3].Status)
	}

	// Per-item PausedReason pointers must be independent so a later mutation
	// can't bleed across items.
	s.queue[0].PausedReason.Summary = "MUTATED"
	if s.queue[1].PausedReason.Summary == "MUTATED" {
		t.Error("PausedReason aliasing: mutating queue[0] altered queue[1]")
	}
}

// TestQueueStatusDerivedFromPausedItems exercises ADR-005: the queue-level
// status is *derived* — true paused iff any item is paused. Prevents the
// dual-source-of-truth class of bugs.
func TestQueueStatusDerivedFromPausedItems(t *testing.T) {
	s := &Scheduler{repoRunning: make(map[string]int), mergeLocks: make(map[string]*sync.Mutex)}
	s.queue = []QueueItem{{IssueNumber: 1, Status: "pending"}, {IssueNumber: 2, Status: "ready"}}

	s.mu.Lock()
	if got := s.queueStatusLocked(); got != "waiting" {
		s.mu.Unlock()
		t.Fatalf("status before pause = %q, want waiting", got)
	}
	s.mu.Unlock()

	s.mu.Lock()
	s.pauseQueuedItemsUnlocked(QueuePausedReason{Kind: "upstream_failure", FailedRunID: "1-x"})
	got := s.queueStatusLocked()
	s.mu.Unlock()
	if got != "paused" {
		t.Errorf("status after pause = %q, want paused", got)
	}
}

// TestResumePausedItems verifies that the operator-driven resume path only
// touches items that match the FailedRunID (so resuming run A doesn't
// accidentally release items paused by an unrelated run B).
func TestResumePausedItems(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	rA := QueuePausedReason{Kind: "upstream_failure", FailedRunID: "A"}
	rB := QueuePausedReason{Kind: "upstream_failure", FailedRunID: "B"}
	s.queue = []QueueItem{
		{IssueNumber: 1, Status: "paused", PausedReason: &rA},
		{IssueNumber: 2, Status: "paused", PausedReason: &rB},
		{IssueNumber: 3, Status: "paused", PausedReason: &rA},
	}

	count := s.ResumePausedItems("A")
	if count != 2 {
		t.Errorf("resumed = %d, want 2", count)
	}
	if s.queue[0].Status != "pending" || s.queue[0].PausedReason != nil {
		t.Errorf("queue[0] not resumed: status=%q reason=%v", s.queue[0].Status, s.queue[0].PausedReason)
	}
	if s.queue[1].Status != "paused" {
		t.Errorf("queue[1] (other run) should still be paused, got %q", s.queue[1].Status)
	}
	if s.queue[2].Status != "pending" {
		t.Errorf("queue[2] (same run as #1) should be resumed, got %q", s.queue[2].Status)
	}
}

// TestDequeueIndependentSkipsPaused proves the paused guard added to the
// dispatcher: paused items are never picked even when first in the queue.
func TestDequeueIndependentSkipsPaused(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		issueSvc:      newMockIssueSvc(), // refreshBlockerStates needs non-nil
	}
	pausedReason := QueuePausedReason{Kind: "upstream_failure", FailedRunID: "x"}
	s.queue = []QueueItem{
		{IssueNumber: 1, Status: "paused", PausedReason: &pausedReason},
		{IssueNumber: 2, Status: "pending"},
	}

	got := s.DequeueIndependent(t.Context(), 5, nil)
	if len(got) != 1 {
		t.Fatalf("dequeued %d items, want 1 (#1 is paused, #2 is pending)", len(got))
	}
	if got[0].IssueNumber != 2 {
		t.Errorf("dequeued issue = #%d, want #2", got[0].IssueNumber)
	}
}

// TestSidecarRoundTripAndOrchestratorCrashRecovery exercises the full
// crash-recovery contract:
//
//  1. write a current-run.json sidecar
//  2. instantiate a fresh scheduler — it scans the sidecar and synthesizes a
//     terminal-failure RunRecord with failure_kind=orchestrator_crash
//  3. queue items behind the crashed run move to "paused" with structured
//     PausedReason linking back to the synthesized run id
//  4. the sidecar is removed (so a subsequent restart doesn't re-synthesize)
func TestSidecarRoundTripAndOrchestratorCrashRecovery(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-seed a stale sidecar — simulates the previous orchestrator crashing
	// mid-stage. StartedAt deliberately in the past so the in-future-skip
	// guard doesn't reject it.
	startedAt := time.Now().UTC().Add(-30 * time.Second)
	stageStart := startedAt.Add(5 * time.Second)
	if err := writeCurrentRunSidecar(tmpDir, CurrentRunSidecar{
		IssueNumber: 999,
		Repo:        "nightgauge/nightgauge",
		Title:       "Fix terminal failure preservation",
		StartedAt:   startedAt,
		Stage:       "feature-dev",
		StageStart:  stageStart,
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	// Pre-seed a queue file with one downstream item — recovery should pause it.
	queueDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(queueDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	queueState := QueueState{
		SchemaVersion: queueSchemaVersion,
		Status:        "waiting",
		Items: []QueueItem{
			{IssueNumber: 1000, Status: "pending", Repo: "nightgauge/nightgauge", Title: "Next up"},
		},
		UpdatedAt: time.Now().UTC(),
	}
	qb, _ := json.MarshalIndent(queueState, "", "  ")
	if err := os.WriteFile(filepath.Join(queueDir, "queue-state.json"), qb, 0644); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	// Construct a scheduler the way NewScheduler does (queue load + recovery)
	// without going through the constructor (which requires a real GitHub client).
	s := &Scheduler{
		workspaceRoot: tmpDir,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s.loadQueue()

	// Sidecar must be cleared so a second NewScheduler doesn't double-synthesize.
	if _, err := os.Stat(filepath.Join(tmpDir, currentRunSidecarFile)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed after recovery, stat err=%v", err)
	}

	// Daily JSONL must contain exactly one synthesized record.
	records := readDailyJSONLRecords(t, tmpDir)
	if len(records) != 1 {
		t.Fatalf("expected 1 synthesized record, got %d", len(records))
	}
	rec := records[0]
	if rec.IssueNumber != 999 {
		t.Errorf("rec.IssueNumber = %d, want 999", rec.IssueNumber)
	}
	if rec.SchemaVersion != "3" {
		t.Errorf("rec.SchemaVersion = %q, want 3 (V3 — terminal_failure_kind populated)", rec.SchemaVersion)
	}
	if rec.Outcome != "failed" {
		t.Errorf("rec.Outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind != TerminalKindOrchestratorCrash {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindOrchestratorCrash)
	}
	if !rec.IsRecovery {
		t.Error("rec.IsRecovery = false, want true (recovery runs are excluded from cost-trend baselines per #1261)")
	}
	stageDetail, ok := rec.Stages["feature-dev"]
	if !ok {
		t.Fatalf("expected feature-dev stage in synthesized record, got stages=%v", rec.Stages)
	}
	if stageDetail.Status != "failed" {
		t.Errorf("stages[feature-dev].Status = %q, want failed", stageDetail.Status)
	}
	if stageDetail.Error == "" {
		t.Errorf("stages[feature-dev].Error empty — should describe the crash")
	}

	// Downstream queue item must be paused with a PausedReason that links to
	// the synthesized FailedRunID. Reload queue from disk to confirm
	// persistence (recovery wrote it via persistQueue).
	loaded := s.GetState()
	if len(loaded.Items) != 1 {
		t.Fatalf("queue item count = %d, want 1", len(loaded.Items))
	}
	item := loaded.Items[0]
	if item.Status != "paused" {
		t.Errorf("item.Status = %q, want paused", item.Status)
	}
	if item.PausedReason == nil {
		t.Fatalf("item.PausedReason is nil")
	}
	wantRunID := FailedRunID(999, startedAt)
	if item.PausedReason.FailedRunID != wantRunID {
		t.Errorf("item.PausedReason.FailedRunID = %q, want %q",
			item.PausedReason.FailedRunID, wantRunID)
	}
	if item.PausedReason.Kind != "upstream_failure" {
		t.Errorf("item.PausedReason.Kind = %q, want upstream_failure", item.PausedReason.Kind)
	}
	// Queue-level status must derive paused (ADR-005)
	if loaded.Status != "paused" {
		t.Errorf("queue Status = %q, want paused (derived from item)", loaded.Status)
	}
}

// TestSidecarRecoverySkipsFutureStartedAt is the guard against rogue sidecars
// (clock skew, workspace move). The synthesizer must refuse and remove a
// sidecar with a future StartedAt rather than write a phantom record.
func TestSidecarRecoverySkipsFutureStartedAt(t *testing.T) {
	tmpDir := t.TempDir()
	if err := writeCurrentRunSidecar(tmpDir, CurrentRunSidecar{
		IssueNumber: 1,
		StartedAt:   time.Now().UTC().Add(24 * time.Hour), // future
		Stage:       "feature-dev",
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	s := &Scheduler{
		workspaceRoot: tmpDir,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s.recoverOrchestratorCrash()

	// Sidecar removed even when synthesis is skipped — otherwise a stale
	// future-dated sidecar would block the queue forever.
	if _, err := os.Stat(filepath.Join(tmpDir, currentRunSidecarFile)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed; stat err=%v", err)
	}

	// Daily JSONL must NOT contain a synthesized record for the future sidecar.
	histDir := filepath.Join(tmpDir, ".nightgauge", "pipeline", "history")
	if entries, err := os.ReadDir(histDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("daily JSONL %q should not exist for future-skipped sidecar", e.Name())
			}
		}
	}
}

// TestGetPipelineFailureModeDefaults verifies the conservative default and the
// env-var override path. The config-file path is exercised via integration
// against the test workspace.
func TestGetPipelineFailureModeDefaults(t *testing.T) {
	// Empty workspace → halt
	if got := GetPipelineFailureMode(""); got != FailureModeHalt {
		t.Errorf("default = %q, want halt", got)
	}
	// Env override
	t.Setenv("NIGHTGAUGE_PIPELINE_FAILURE_MODE", "continue-queue")
	if got := GetPipelineFailureMode(""); got != FailureModeContinueQueue {
		t.Errorf("env override = %q, want continue-queue", got)
	}
	// Bogus env value falls back to default
	t.Setenv("NIGHTGAUGE_PIPELINE_FAILURE_MODE", "bogus")
	if got := GetPipelineFailureMode(""); got != FailureModeHalt {
		t.Errorf("bogus env value = %q, want halt fallback", got)
	}
}

// TestGetPipelineFailureModeFromConfigYAML covers the YAML reader so a typo
// in the config-file scanner doesn't silently drop the operator's choice.
func TestGetPipelineFailureModeFromConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "pipeline:\n  failure_mode: auto-resume\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	got := GetPipelineFailureMode(tmpDir)
	if got != FailureModeAutoResume {
		t.Errorf("config-yaml parse = %q, want auto-resume", got)
	}
}

// ipcStallStageRunner mirrors the IpcStageRunner behavior at terminal
// failure: it returns a non-nil error whose text uses the canonical
// PipelineBridge marker (`[stall-killed]` / `[cost-cap-exceeded]`) and
// populates StageRunResult.LastOutputLines with a stderr tail. Used to
// reproduce Issue #3207 — without the IPC fix the scheduler arrived at the
// failure block with err==nil and the daily JSONL either dropped the record
// or mis-classified it as subagent_crash.
type ipcStallStageRunner struct {
	mu              sync.Mutex
	callCount       int
	errText         string
	lastOutputLines string
}

func (r *ipcStallStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.callCount++
	r.mu.Unlock()

	// First few stages succeed; we want the failure to happen on a specific
	// non-rewindable stage so the test isolates the recording path from the
	// adaptive stall-recovery branch. pr-create is the standard target.
	if params.Stage != state.StagePRCreate {
		// Write a minimal output context so the next stage's prereq check
		// passes (mirrors stallStageRunner behavior).
		if params.OutputFile != "" {
			if mkErr := os.MkdirAll(filepath.Dir(params.OutputFile), 0755); mkErr == nil {
				payload := map[string]any{
					"schema_version":   "1.0",
					"issue_number":     params.IssueNumber,
					"plan_file":        "plan.md",
					"approach":         "test",
					"files_to_create":  []string{},
					"files_to_modify":  []string{},
					"files_to_read":    []string{},
					"validation_steps": []string{},
					"ok":               true,
				}
				data, _ := json.Marshal(payload)
				_ = os.WriteFile(params.OutputFile, data, 0644)
			}
		}
		return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
	}

	// pr-create: simulate the IPC stall-kill — non-zero exit, real Go error
	// containing the canonical marker, plus a captured stderr tail.
	return &StageRunResult{
		ExitCode:        1,
		ErrorText:       r.errText,
		LastOutputLines: r.lastOutputLines,
	}, errors.New(r.errText)
}

// TestStallKillJSONLRecord_IPCMode is the regression test for Issue #3207.
// It exercises the full deferred recordV2History path with the IPC-mode
// failure shape: stage runner returns a non-nil stall-kill error, scheduler
// must write a V3 RunRecord with terminal_failure_kind=stall_kill and a
// populated last_output_lines snippet on the failed stage.
func TestStallKillJSONLRecord_IPCMode(t *testing.T) {
	root := t.TempDir()

	stallErrText := "[stall-killed] pr-create terminated: exceeded stall idle threshold (1200s without output)"
	tail := "[skillRunner] Stage exceeded stall idle threshold (20m 0s without output) — forcibly terminating process after 1h 20m 0s (idle for 20m 0s).\n[skillRunner] last claude api response: tool_use Read /tmp/x"

	runner := &ipcStallStageRunner{
		errText:         stallErrText,
		lastOutputLines: tail,
	}

	s := buildStallTestScheduler(t, root, runner)
	// Disable model escalation so the stage failure goes straight to terminal.
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          0,
		MaxEscalationsPerStage: 0,
	})

	item := types.BoardItem{
		Number: 8001,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8001",
		Title:  "Reproduce stall-kill JSONL gap",
		Labels: []string{"type:bug", "component:pipeline"},
	}
	s.runPipeline(context.Background(), item)

	// 1) The daily JSONL MUST exist with exactly one record for this issue.
	records := readDailyJSONLRecords(t, root)
	if len(records) == 0 {
		t.Fatal("no run record written — IPC stall-kill produced zero JSONL rows")
	}
	var rec state.V2RunRecord
	found := false
	for _, r := range records {
		if r.IssueNumber == item.Number {
			rec = r
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}

	// 2) Schema version 3 — terminal_failure_kind populated bumps to V3.
	if rec.SchemaVersion != "3" {
		t.Errorf("schema_version = %q, want 3", rec.SchemaVersion)
	}
	// 3) Outcome is failed.
	if rec.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	// 4) terminal_failure_kind is stall_kill (NOT subagent_crash).
	if rec.TerminalFailureKind != TerminalKindStallKill {
		t.Errorf("terminal_failure_kind = %q, want %q (subagent_crash means classification fell back — the IPC fix did not propagate the error)",
			rec.TerminalFailureKind, TerminalKindStallKill)
	}
	// 5) The failed stage's StageDetail is populated with status=failed and
	//    a non-empty last_output_lines snippet.
	prCreateDetail, ok := rec.Stages[string(state.StagePRCreate)]
	if !ok {
		t.Fatalf("pr-create stage missing from record; got stages=%v", rec.Stages)
	}
	if prCreateDetail.Status != "failed" {
		t.Errorf("stages[pr-create].Status = %q, want failed", prCreateDetail.Status)
	}
	if prCreateDetail.Error == "" {
		t.Error("stages[pr-create].Error empty — should carry the stall-kill text")
	}
	if !strings.Contains(prCreateDetail.Error, "[stall-killed]") {
		t.Errorf("stages[pr-create].Error = %q, expected to contain canonical [stall-killed] marker",
			prCreateDetail.Error)
	}
	if prCreateDetail.LastOutputLines == "" {
		t.Error("stages[pr-create].LastOutputLines empty — IPC fix must propagate the captured tail")
	}
	if !strings.Contains(prCreateDetail.LastOutputLines, "exceeded stall idle threshold") {
		t.Errorf("stages[pr-create].LastOutputLines missing kill diagnostic; got %q",
			prCreateDetail.LastOutputLines)
	}
	// 6) Routing/branch/base_branch sane defaults.
	if rec.Branch == "" {
		t.Error("branch empty — V3 record should always carry the issue branch")
	}
	if rec.BaseBranch == "" {
		t.Error("base_branch empty — V3 record should always carry main as the default base")
	}
	// 7) RecordedAt is set.
	if rec.RecordedAt == "" {
		t.Error("recorded_at empty")
	}
}

// TestCostCapKillJSONLRecord_IPCMode mirrors the stall-kill test but for the
// other terminal kind that the IPC fix needs to classify (#3002 cost-cap).
// The canonical [cost-cap-exceeded] marker MUST take precedence over any
// stall-shaped substring in the error text.
func TestCostCapKillJSONLRecord_IPCMode(t *testing.T) {
	root := t.TempDir()

	costCapErrText := "[cost-cap-exceeded] pr-create terminated ($5.21 ≥ $5.00 cap)"
	tail := "[skillRunner] cost cap polling tick: $5.21 ≥ $5.00 — terminating subagent for pr-create"

	runner := &ipcStallStageRunner{
		errText:         costCapErrText,
		lastOutputLines: tail,
	}

	s := buildStallTestScheduler(t, root, runner)
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          0,
		MaxEscalationsPerStage: 0,
	})

	item := types.BoardItem{
		Number: 8002,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8002",
		Title:  "Reproduce cost-cap-kill JSONL gap",
		Labels: []string{"type:bug"},
	}
	s.runPipeline(context.Background(), item)

	records := readDailyJSONLRecords(t, root)
	var rec state.V2RunRecord
	for _, r := range records {
		if r.IssueNumber == item.Number {
			rec = r
		}
	}
	if rec.IssueNumber != item.Number {
		t.Fatalf("no record for issue #%d in daily JSONL", item.Number)
	}
	if rec.SchemaVersion != "3" {
		t.Errorf("schema_version = %q, want 3", rec.SchemaVersion)
	}
	if rec.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind != TerminalKindBudgetExceeded {
		t.Errorf("terminal_failure_kind = %q, want %q",
			rec.TerminalFailureKind, TerminalKindBudgetExceeded)
	}
	detail, ok := rec.Stages[string(state.StagePRCreate)]
	if !ok {
		t.Fatalf("pr-create stage detail missing; got %v", rec.Stages)
	}
	if !strings.Contains(detail.Error, "[cost-cap-exceeded]") {
		t.Errorf("stages[pr-create].Error missing cost-cap marker; got %q", detail.Error)
	}
}
