// Tests covering Issue #3001 — terminal failure preservation, queue pause,
// and orchestrator-crash recovery. These exercise the additive surface added
// in failure_handler.go and the scheduler's deferred recordV2History path.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// mustMkdirAll creates dir or fails the test.
func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// stubRanMarker is the file writeFailingStubCLI's script touches on entry.
const stubRanMarker = "STUB-RAN"

// writeFailingStubCLI writes an executable shell script that copies stdout to
// stdout, stderr to stderr, and exits with code. It stands in for a real
// provider CLI so a test can exercise execution.Manager's spawn/wait path —
// including its (result, nil) contract on a non-zero exit — without a network
// call, an API key, or the provider binary being installed.
//
// It touches dir/STUB-RAN first. PATH-injected variants PREPEND to the
// operator's real PATH, so a stub that failed to be found would silently hand
// the test a REAL provider CLI (or nothing) and every assertion below would be
// measuring something else. assertStubRan makes the substitution provable
// instead of assumed.
//
// The payloads go in sidecar files the script cats, rather than inline
// heredocs: a heredoc would break on a payload containing the delimiter, and
// the multi-megabyte payload one of these tests needs has no business being a
// shell script.
func writeFailingStubCLI(t *testing.T, dir, stdout, stderr string, code int) string {
	t.Helper()
	outPath := filepath.Join(dir, "stub-stdout.txt")
	errPath := filepath.Join(dir, "stub-stderr.txt")
	if err := os.WriteFile(outPath, []byte(stdout), 0644); err != nil {
		t.Fatalf("write stub stdout: %v", err)
	}
	if err := os.WriteFile(errPath, []byte(stderr), 0644); err != nil {
		t.Fatalf("write stub stderr: %v", err)
	}
	path := filepath.Join(dir, "stub-cli.sh")
	script := "#!/bin/sh\n" +
		": > " + filepath.Join(dir, stubRanMarker) + "\n" +
		"cat " + outPath + "\n" +
		"cat " + errPath + " >&2\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write stub CLI: %v", err)
	}
	return path
}

// assertStubRan fails the test unless the stub written into dir actually
// executed. Without it, an exit code of 1 from some OTHER binary on the
// operator's PATH reads exactly like a successful stub run.
func assertStubRan(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, stubRanMarker)); err != nil {
		t.Fatalf("the stub CLI in %s never executed (%v) — PATH injection did not take "+
			"effect and this test measured a different binary", dir, err)
	}
}

// countLines returns the number of lines in s (a trailing newline does not
// start a new line).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

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
		// #252 — zombie-run guard: the first-output watchdog classifies as a
		// transient stall (retry with backoff, no lifetime-cap increment).
		// This is the row's ONLY clause since #470 retired its sibling (whose
		// producer #427 had deleted), so this case is what pins the row's
		// survival. Assert the kind explicitly.
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
		// Issue #229 AC #4 -- ordering: api_overloaded must be matched before
		// api_connection_lost when an error string contains both patterns.
		{
			"api_overloaded_beats_api_connection_lost",
			"API Error: Overloaded - connection closed mid-response",
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
		// Issue #230 — a write-containment breach. The stage exits 0 and reports
		// success (it did work, just in a repository it does not own), so
		// nothing else in the chain marks it failed. Unclassified, this reached
		// an issue's lifetime failure cap and halted the fleet while telling the
		// operator only "unclassified" — with a manifest on disk naming the
		// repository, every escaped path, and a restorable patch.
		{
			"containment_breach_marker",
			"[stage:worktree-containment] Stage feature-planning wrote outside its worktree into 1 repository/repositories it does not own.",
			TerminalKindContainmentBreach,
		},
		// Must beat premature_turn_end, which would otherwise claim a clean
		// exit-0 breach and describe entirely the wrong problem.
		{
			"containment_breach_beats_premature_turn_end",
			"premature turn end: stage exited 0 with no state change (gate no-op): [stage:worktree-containment] wrote outside its worktree",
			TerminalKindContainmentBreach,
		},
		// Issue #227 — the verbatim string from a transport drop that killed two
		// concurrent runs in two repositories in the same instant, one at
		// pr-create with $21.54 already spent. It matched
		// nothing: `socket connection was closed` needs the `socket` prefix, and
		// `connection reset`/`refused` are different verbs. Falling through cost
		// a fleet halt and a lifetime failure on two blameless issues.
		{
			"api_connection_lost_mid_response",
			"API Error: Connection closed mid-response. The response above may be incomplete.",
			TerminalKindApiConnectionLost,
		},
		// The marker skillRunner now stamps from the envelope's terminal_reason.
		{
			"api_connection_lost_marker",
			"[api_connection_lost] API Error: Connection closed mid-response. The response above may be incomplete.",
			TerminalKindApiConnectionLost,
		},
		// An unrelated stage failure that merely mentions a closed connection
		// must NOT be excused as a transport blip — that would silently disable
		// the lifetime cap for a whole class of real defects. It falls through
		// unclassified here, which is the pre-existing behavior for this text;
		// what this case pins is that widening the transport matcher did not
		// swallow it. The `api error` gate is what holds the line.
		{
			"integration_test_mentioning_connection_closed_is_not_transient",
			"validation failed: 3 tests failed — DatabasePool: connection closed while acquiring",
			"",
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
		// Issue #9 — the "invalid json" classifier bug: gates actually emit
		// "is not valid JSON" / "unparseable JSON", neither of which
		// contained the old literal "invalid json" substring, so these fell
		// through to the generic subagent-crash bucket instead of
		// validation_error. Regression coverage for the fixed matcher.
		{
			"gate_json_not_valid_phrasing",
			"issue context file is not valid JSON",
			TerminalKindValidationError,
		},
		{
			"gate_json_unparseable_phrasing",
			"gh pr view returned unparseable JSON",
			TerminalKindValidationError,
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

// TestResolveTerminalKind covers the three-way precedence rule introduced by
// Issue #9: a gate-sourced structured kind wins when present; otherwise fall
// back to prose classification, whether or not a gate ran at all.
func TestResolveTerminalKind(t *testing.T) {
	tests := []struct {
		name             string
		gateRan          bool
		gateTerminalKind string
		errorText        string
		want             string
	}{
		{
			name:             "gate_ran_structured_kind_set_wins",
			gateRan:          true,
			gateTerminalKind: TerminalKindValidationError,
			errorText:        "subagent crash: exit 1",
			want:             TerminalKindValidationError,
		},
		{
			name:             "gate_ran_structured_kind_empty_falls_back_to_prose",
			gateRan:          true,
			gateTerminalKind: "",
			errorText:        "exit 1: subprocess died",
			want:             TerminalKindSubagentCrash,
		},
		{
			name:             "gate_did_not_run_falls_back_unconditionally",
			gateRan:          false,
			gateTerminalKind: TerminalKindValidationError, // must be ignored — gate didn't run
			errorText:        "exit 1: subprocess died",
			want:             TerminalKindSubagentCrash,
		},
		{
			name:             "gate_did_not_run_no_error_text",
			gateRan:          false,
			gateTerminalKind: "",
			errorText:        "",
			want:             "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveTerminalKind(tc.gateRan, tc.gateTerminalKind, tc.errorText)
			if got != tc.want {
				t.Errorf("ResolveTerminalKind(%v, %q, %q) = %q, want %q",
					tc.gateRan, tc.gateTerminalKind, tc.errorText, got, tc.want)
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

// TestSynthesizeOrchestratorCrashRecord_NeverFabricatesABranch pins the second
// of the two `feat/{N}` fabrications #397 deleted, directly at its writer.
//
// The input is the exact shape the old code fabricated from: a sidecar with a
// POSITIVE issue number, which is the only shape production ever writes
// (writeCurrentRunSidecar stamps IssueNumber from a board item). Pre-#397 this
// synthesizer ran `if sc.IssueNumber > 0 { branch = fmt.Sprintf("feat/%d",
// sc.IssueNumber) }`, so essentially every crash record on disk claimed a
// branch the crashed run may never have had — a claim no reader, upload or
// human could tell apart from a branch that really existed.
//
// The sidecar has no branch field at all, so this writer never knows one; "" is
// the record saying exactly that. Asserted here rather than only through the
// committed capture in
// packages/nightgauge-vscode/tests/fixtures/undetermined-branch/crash-record.jsonl:
// a static fixture cannot fail when the Go writer regresses, it only goes stale.
func TestSynthesizeOrchestratorCrashRecord_NeverFabricatesABranch(t *testing.T) {
	now := time.Now().UTC()
	sc := CurrentRunSidecar{
		RunID:       testRunID(),
		IssueNumber: 397,
		Repo:        "nightgauge/nightgauge",
		Title:       "Crash mid-stage with no branch on record",
		StartedAt:   now.Add(-42 * time.Minute),
		Stage:       "feature-dev",
		StageStart:  now.Add(-7 * time.Minute),
	}

	rec := SynthesizeOrchestratorCrashRecord(sc, now)

	if rec.IssueNumber != 397 {
		t.Fatalf("rec.IssueNumber = %d, want 397 — the positive issue number IS the input under test",
			rec.IssueNumber)
	}
	if rec.Branch != "" {
		t.Errorf("rec.Branch = %q, want \"\" — the sidecar carries no branch, so this synthesizer never "+
			"knows one; a value here is the pre-#397 `feat/{IssueNumber}` fabrication, indistinguishable "+
			"from a branch the crashed run really used (#397)", rec.Branch)
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
		RunID:       testRunID(),
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
	if rec.Branch != "" {
		t.Errorf("rec.Branch = %q, want \"\" — the sidecar carries no branch, so any value here is a "+
			"fabrication indistinguishable from a branch the run really used (#397)", rec.Branch)
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
	indexBytes, err := os.ReadFile(filepath.Join(tmpDir, ".nightgauge", "pipeline", "history", "index.json"))
	if err != nil {
		t.Fatalf("read crash-recovery index: %v", err)
	}
	var index state.V2Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatalf("unmarshal crash-recovery index: %v", err)
	}
	if index.SchemaVersion != "2" {
		t.Errorf("index.SchemaVersion = %q, want 2 (terminal failure identity projection)", index.SchemaVersion)
	}
	if len(index.Entries) != 1 {
		t.Fatalf("index entries = %d, want 1", len(index.Entries))
	}
	if index.Entries[0].TerminalFailureKind != TerminalKindOrchestratorCrash {
		t.Errorf("index terminal_failure_kind = %q, want %q",
			index.Entries[0].TerminalFailureKind, TerminalKindOrchestratorCrash)
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
		RunID:       testRunID(),
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
	// 6) branch/base_branch. Nothing in this fixture ever creates a branch —
	//    the workspace is a bare temp dir and the stage runner is a stub — so
	//    the honest record for this run carries branch "" (#397). Before #397
	//    this asserted non-empty and passed on BuildV2Record's `feat/{N}`
	//    fabrication, i.e. it certified as "the issue branch" a string the run
	//    never had. base_branch is different: "main" is a real default the
	//    record chooses, not a claim about something it observed.
	if rec.Branch != "" {
		t.Errorf("branch = %q, want \"\" — no branch exists in this fixture, and any non-empty value "+
			"is a fabrication indistinguishable from a branch the run really used (#397)", rec.Branch)
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

// ── #533: CLI-mode failure text never reached classification ──────────────
//
// CLI mode and IPC mode fail in two DIFFERENT shapes, and only the IPC one was
// ever wired up:
//
//	IPC — IpcStageRunner returns a non-nil error AND populates
//	      StageRunResult.ErrorText / LastOutputLines (ipc_stage_runner.go:201).
//	CLI — execution.Manager.RunStage turns a non-zero exit into result.ExitCode
//	      and returns a NIL error (manager.go:385-391), with the real reason on
//	      result.Stderr. ExecutionManagerRunner then built a StageRunResult that
//	      never set ErrorText or LastOutputLines.
//
// Every classification input in the scheduler derived from that nil err, so
// ClassifyTerminalKind was handed the literal string "exit 1: <nil>" and
// answered subagent_crash — the #520 signature — for every CLI-mode failure of
// every adapter. The two tests below cover the two halves of the carry: the
// runner projection (Step 3) and the scheduler's use of it (Step 4).

// grokUnknownModelStderr is the REAL stderr of `grok --model grok-build-0.1`,
// captured in internal/execution/testdata/grok_unknown_model_stderr.txt on
// grok 1.0.4. Duplicated as a literal rather than read across package
// boundaries; the fixture file is the provenance record.
const grokUnknownModelStderr = `Error: Couldn't set model 'grok-build-0.1': Invalid params: "unknown model id". Run 'grok models' to see available models.` + "\n"

// grokNotSignedInStderr is the REAL stderr of a grok Build CLI dispatch with
// no credentials present, transcribed verbatim from the #528 live probe (run
// 2026-08-15 against #590) that this issue (#591) fixes. Unlike
// grokUnknownModelStderr, no pipeline-start auth-gate marker ever wraps this
// text: the CLI fails too fast for the preflight probe to have run, so this
// sentence reaches the classifier as ordinary stderr via the #533 carry —
// which is exactly why it fell through to subagent_crash pre-#591.
const grokNotSignedInStderr = `Error: Not signed in. To authenticate without a browser, run: grok login --device-code. Alternatively, set the XAI_API_KEY environment variable or run grok login on a machine with a browser.` + "\n"

// TestCLIStageErrorTextReachesClassification_GrokUnknownModel is the #533
// regression test for the runner projection. It drives the REAL
// ExecutionManagerRunner over a REAL execution.Manager whose adapter spawns a
// stub CLI that reproduces the captured failure — non-zero exit, reason on
// stderr, nil Go error — and asserts the projection carries that text.
//
// This test MUST fail at 8dbbeb95: ErrorText and LastOutputLines were never
// assigned in the ExecutionManagerRunner.RunStage literal, so both are "" and
// ClassifyTerminalKind falls back to subagent_crash.
func TestCLIStageErrorTextReachesClassification_GrokUnknownModel(t *testing.T) {
	root := t.TempDir()
	stubCLI := writeFailingStubCLI(t, root, "", grokUnknownModelStderr, 1)
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", stubCLI)

	runner := &ExecutionManagerRunner{
		execMgr: execution.NewManager(root, adapters.NewGrokAdapter()),
	}
	// ensureWorktree returns early when the path already exists, so the stage
	// needs no git repo behind it.
	mustMkdirAll(t, filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-533"))

	res, err := runner.RunStage(context.Background(), StageRunParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 533,
		Stage:       state.StagePRCreate,
		Timeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunStage returned a non-nil error (%v) — the CLI path's whole "+
			"defect is that a non-zero exit arrives with err==nil; if this "+
			"changed, the test no longer covers #533", err)
	}
	if res == nil {
		t.Fatal("RunStage returned a nil result")
	}
	if res.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1 — the stub CLI did not run", res.ExitCode)
	}

	if res.ErrorText == "" {
		t.Fatal("StageRunResult.ErrorText is empty — the CLI runner dropped the " +
			"reason the process printed on stderr (#533)")
	}
	if !strings.Contains(res.ErrorText, "unknown model id") {
		t.Errorf("ErrorText = %q, expected the CLI's verbatim stderr reason", res.ErrorText)
	}
	if res.LastOutputLines == "" {
		t.Error("StageRunResult.LastOutputLines is empty — retros have no evidence for a CLI failure")
	}

	// The point of the carry: classification now sees the real reason and
	// routes to the #42 sticky downgrade instead of escalating on a
	// subagent_crash misread.
	if got := ResolveTerminalKind(false, "", res.ErrorText); got != TerminalKindModelUnavailable {
		t.Errorf("ResolveTerminalKind(ErrorText) = %q, want %q", got, TerminalKindModelUnavailable)
	}
	// And the pre-fix classification input really was the wrong answer, so the
	// assertion above is testing a behavior change rather than a coincidence.
	if got := ClassifyTerminalKind("exit 1: <nil>"); got != "" && got != TerminalKindSubagentCrash {
		t.Errorf("ClassifyTerminalKind(\"exit 1: <nil>\") = %q — #533's premise "+
			"is that the nil-error string classifies as nothing useful", got)
	}
}

// cliFailureStageRunner reproduces the CLI-mode terminal shape at the
// StageRunner seam: a non-zero exit, the reason on ErrorText, a captured tail
// on LastOutputLines, and — the part that distinguishes CLI from IPC — a NIL
// Go error. It is deliberately NOT the IPC shape: faking a non-nil error here
// would test the path that already worked.
type cliFailureStageRunner struct {
	mu             sync.Mutex
	failStageCalls int
	failStage      state.PipelineStage
	// failTimes bounds how many of failStage's dispatches fail; 0 means every
	// one of them does. A finite value models the retry that SUCCEEDS, which is
	// how a superseded attempt's evidence ends up on a `complete` stage.
	failTimes       int
	errText         string
	lastOutputLines string
}

func (r *cliFailureStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	failThisCall := params.Stage == r.failStage
	if failThisCall {
		r.mu.Lock()
		r.failStageCalls++
		if r.failTimes > 0 && r.failStageCalls > r.failTimes {
			failThisCall = false
		}
		r.mu.Unlock()
	}

	if !failThisCall {
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

	return &StageRunResult{
		ExitCode:        1,
		ErrorText:       r.errText,
		LastOutputLines: r.lastOutputLines,
	}, nil // nil error — the CLI shape
}

// TestCLIModelUnavailableRoutesToDowngrade_NotSubagentCrash is the scheduler
// half of #533. It mirrors TestStallKillJSONLRecord_IPCMode, but with the CLI
// failure shape (err == nil, reason on ErrorText). The scheduler must fold
// ErrorText into failText / terminalReason so:
//
//  1. the run classifies as model_unavailable, not subagent_crash;
//  2. the #42 sticky-downgrade branch fires (the stage is re-dispatched on a
//     weaker tier) instead of an upward escalation;
//  3. the persisted terminalReason is the CLI's sentence, not "exit 1: <nil>".
//
// This test MUST fail at 8dbbeb95 — with err==nil, failText stayed "" and
// terminalReason was literally "exit 1: <nil>".
func TestCLIModelUnavailableRoutesToDowngrade_NotSubagentCrash(t *testing.T) {
	root := t.TempDir()

	tail := "some earlier stdout chatter\n" + strings.TrimSpace(grokUnknownModelStderr)
	runner := &cliFailureStageRunner{
		failStage:       state.StagePRCreate,
		errText:         strings.TrimSpace(grokUnknownModelStderr),
		lastOutputLines: tail,
	}

	s := buildStallTestScheduler(t, root, runner)
	// Escalation deliberately ENABLED. Disabling it would make the
	// "did not escalate" assertion below vacuous — it has to be possible for
	// the scheduler to escalate before "it didn't" means anything. Pre-fix,
	// pr-create's haiku misclassifies as subagent_crash and escalates
	// haiku → sonnet, re-dispatching the stage; post-fix it recognizes a
	// rejected model and never moves up the ladder.
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          0,
		MaxEscalationsPerStage: 1,
		ModelLadder:            []string{"haiku", "sonnet", "opus"},
	})

	item := types.BoardItem{
		Number: 8533,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8533",
		Title:  "CLI-mode unknown model must not read as subagent_crash",
		Labels: []string{"type:bug", "component:pipeline"},
	}
	s.runPipeline(context.Background(), item)

	records := readDailyJSONLRecords(t, root)
	var rec state.V2RunRecord
	found := false
	for _, r := range records {
		if r.IssueNumber == item.Number {
			rec, found = r, true
			break
		}
	}
	if !found {
		t.Fatalf("no record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}

	if rec.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind != TerminalKindModelUnavailable {
		t.Errorf("terminal_failure_kind = %q, want %q — %q is the #520 signature "+
			"that CLI mode produced for EVERY failure because the classifier only "+
			"ever saw \"exit 1: <nil>\"",
			rec.TerminalFailureKind, TerminalKindModelUnavailable, rec.TerminalFailureKind)
	}

	detail, ok := rec.Stages[string(state.StagePRCreate)]
	if !ok {
		t.Fatalf("pr-create stage missing from record; got stages=%v", rec.Stages)
	}
	if strings.Contains(detail.Error, "<nil>") {
		t.Errorf("stages[pr-create].Error = %q — the nil-error placeholder means the "+
			"CLI's real reason was never folded into terminalReason", detail.Error)
	}
	if !strings.Contains(detail.Error, "unknown model id") {
		t.Errorf("stages[pr-create].Error = %q, expected the CLI's verbatim reason", detail.Error)
	}
	if detail.LastOutputLines == "" {
		t.Error("stages[pr-create].LastOutputLines empty — a CLI failure must carry its tail for retros")
	}

	// The routing consequence, which is the whole point of classifying
	// correctly. pr-create runs on haiku — the BOTTOM of the downgrade ladder —
	// so #42 correctly finds no weaker tier and goes terminal. What must not
	// happen is the other direction: pre-fix, subagent_crash sent this straight
	// to EvaluateEscalation, which moved haiku → sonnet and burned a second
	// dispatch on a model the API had just rejected.
	if got := s.retryEngine.CurrentModel(string(state.StagePRCreate)); got != "" {
		t.Errorf("pr-create escalated to %q — escalating UP on a rejected model is exactly "+
			"what the subagent_crash misclassification caused (#533/#42)", got)
	}
	if runner.failStageCalls != 1 {
		t.Errorf("pr-create dispatched %d times, want 1 — a rejected model with no weaker "+
			"tier is terminal; a second dispatch means the escalation ladder ran",
			runner.failStageCalls)
	}
}

// TestCLIAdapterAuthFailedExcludedFromEscalation_NotSubagentCrash is the
// scheduler half of #591. It mirrors
// TestCLIModelUnavailableRoutesToDowngrade_NotSubagentCrash's setup exactly —
// same CLI failure shape (err == nil, reason on ErrorText), same stage, same
// escalation-eligible retry engine — but for grok's own "not signed in"
// vendor text rather than a rejected model.
//
// The #528 live probe (2026-08-15 against #590) observed the pre-fix
// behavior: this exact text fell through the terminal-kind ladder to
// subagent_crash, and the generic escalation branch then moved haiku → sonnet
// before giving up. This test pins the fix: the text now classifies as
// adapter_auth_failed and escalation is skipped entirely — a stronger model
// cannot authenticate a CLI whose credentials are absent, so escalating is
// pure wasted spend on a failure only the operator fixing credentials clears.
func TestCLIAdapterAuthFailedExcludedFromEscalation_NotSubagentCrash(t *testing.T) {
	root := t.TempDir()

	runner := &cliFailureStageRunner{
		failStage:       state.StagePRCreate,
		errText:         strings.TrimSpace(grokNotSignedInStderr),
		lastOutputLines: "some earlier stdout chatter\n" + strings.TrimSpace(grokNotSignedInStderr),
	}

	s := buildStallTestScheduler(t, root, runner)
	// Escalation deliberately ENABLED, exactly as the model-rejection sibling
	// test above: it has to be possible for the scheduler to escalate before
	// "it didn't" means anything.
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          0,
		MaxEscalationsPerStage: 1,
		ModelLadder:            []string{"haiku", "sonnet", "opus"},
	})

	item := types.BoardItem{
		Number: 8591,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8591",
		Title:  "CLI-mode grok auth failure must not escalate the model",
		Labels: []string{"type:bug", "component:pipeline"},
	}
	s.runPipeline(context.Background(), item)

	records := readDailyJSONLRecords(t, root)
	var rec state.V2RunRecord
	found := false
	for _, r := range records {
		if r.IssueNumber == item.Number {
			rec, found = r, true
			break
		}
	}
	if !found {
		t.Fatalf("no record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}

	if rec.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind != TerminalKindAdapterAuthFailed {
		t.Errorf("terminal_failure_kind = %q, want %q — grok's 'Not signed in' vendor text "+
			"is the exact misclassification #528 forbids when it lands as subagent_crash instead",
			rec.TerminalFailureKind, TerminalKindAdapterAuthFailed)
	}

	detail, ok := rec.Stages[string(state.StagePRCreate)]
	if !ok {
		t.Fatalf("pr-create stage missing from record; got stages=%v", rec.Stages)
	}
	if !strings.Contains(detail.Error, "Not signed in") {
		t.Errorf("stages[pr-create].Error = %q, expected the CLI's verbatim reason", detail.Error)
	}

	// The whole point of this issue: escalation must never fire on an
	// auth-shaped failure. Pre-#591 this is exactly the haiku → sonnet bump
	// the live probe observed.
	if got := s.retryEngine.CurrentModel(string(state.StagePRCreate)); got != "" {
		t.Errorf("pr-create escalated to %q — model escalation cannot fix an adapter that "+
			"isn't logged in; escalation is for capability-shaped failures, not auth-shaped "+
			"ones (#591)", got)
	}
	if runner.failStageCalls != 1 {
		t.Errorf("pr-create dispatched %d times, want 1 — an auth failure with no escalation "+
			"path is terminal on the first attempt; a second dispatch means the escalation "+
			"ladder ran anyway", runner.failStageCalls)
	}
}

// TestCLIStallKill_RewindsAndPersistsTheRealReason covers the OTHER unprefixed
// consumer of the #533 carry: `stallErrMsg`, which decides whether adaptive
// stall recovery rewinds to feature-planning. The sticky-downgrade test above
// covers `failText`; this one covers the stall path, which had zero coverage.
//
// It also pins the M3 site. The second-stall branch persists the stage error
// with its own Sprintf, and that line was DEAD for CLI mode before #533 —
// stallErrMsg was always "", so isStallKill was false and the whole stall block
// was unreachable. Making it reachable is what re-introduced "exit 1: <nil>" on
// this path, so the branch is asserted here rather than left to inspection.
func TestCLIStallKill_RewindsAndPersistsTheRealReason(t *testing.T) {
	root := t.TempDir()
	enableAdaptiveStallRecovery(t, root)

	const stallText = "[stall-killed] feature-dev terminated: exceeded stall idle threshold (1200s without output)"
	runner := &cliFailureStageRunner{
		failStage:       state.StageFeatureDev,
		errText:         stallText,
		lastOutputLines: "…earlier transcript…\n" + stallText,
	}

	s := buildStallTestScheduler(t, root, runner)
	item := types.BoardItem{
		Number: 8534,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8534",
		Title:  "CLI-mode stall must reach stall recovery",
	}
	s.runPipeline(context.Background(), item)

	// 1. The rewind fired at all — the synthetic feedback context only exists
	//    if isStallKill was true, which requires stallErrMsg to be non-empty.
	feedbackPath := filepath.Join(root, ".nightgauge", "pipeline", "feedback-8534.json")
	if _, statErr := os.Stat(feedbackPath); statErr != nil {
		t.Fatalf("no synthetic feedback context at %s (%v) — a CLI-mode stall never "+
			"reached adaptive stall recovery, so stallErrMsg was still empty (#533)",
			feedbackPath, statErr)
	}

	// 2. …and the rewind re-dispatched feature-dev, which stalls again and goes
	//    terminal on the second-stall branch.
	if runner.failStageCalls != 2 {
		t.Fatalf("feature-dev dispatched %d times, want 2 (stall → rewind → stall → terminal)",
			runner.failStageCalls)
	}

	records := readDailyJSONLRecords(t, root)
	var rec state.V2RunRecord
	found := false
	for _, r := range records {
		if r.IssueNumber == item.Number {
			rec, found = r, true
			break
		}
	}
	if !found {
		t.Fatalf("no record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}
	if rec.TerminalFailureKind != TerminalKindStallKill {
		t.Errorf("terminal_failure_kind = %q, want %q", rec.TerminalFailureKind, TerminalKindStallKill)
	}

	// 3. The M3 assertion: the reason the second-stall branch persisted.
	detail, ok := rec.Stages[string(state.StageFeatureDev)]
	if !ok {
		t.Fatalf("feature-dev missing from record; got stages=%v", rec.Stages)
	}
	if strings.Contains(detail.Error, "<nil>") {
		t.Errorf("stages[feature-dev].Error = %q — the stall-after-retry branch still "+
			"renders `%%v` of a nil error, which is the exact string #533 removes", detail.Error)
	}
	if !strings.Contains(detail.Error, "stall-killed") {
		t.Errorf("stages[feature-dev].Error = %q, expected the CLI's own stall reason", detail.Error)
	}
}

// TestSupersededStageTailIsCleared covers the other half of carrying evidence
// per stage: a stage that FAILED and then succeeded on a retry must not be
// written to the record still holding the failed attempt's tail.
//
// Before #533 this could not happen on the CLI path at all, because the runner
// never populated LastOutputLines. Populating it makes the stale-tail window
// real: RecordStageOutputTail is keyed by stage, the failed attempt writes it,
// and nothing removed it when the retry succeeded — so the V3 record showed a
// `complete` feature-dev carrying a crash transcript.
func TestSupersededStageTailIsCleared(t *testing.T) {
	root := t.TempDir()

	const crashTail = "…transcript…\nError: Cannot read properties of undefined (reading 'text')"
	runner := &cliFailureStageRunner{
		failStage:       state.StageFeatureDev,
		failTimes:       1, // fails once, then the escalated retry succeeds
		errText:         "Error: Cannot read properties of undefined (reading 'text')",
		lastOutputLines: crashTail,
	}

	s := buildStallTestScheduler(t, root, runner)
	// An ordinary crash classifies subagent_crash, which routes to the upward
	// escalation ladder — the plain retry path. One rung is enough.
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          0,
		MaxEscalationsPerStage: 1,
		ModelLadder:            []string{"haiku", "sonnet", "opus"},
	})

	item := types.BoardItem{
		Number: 8535,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8535",
		Title:  "a superseded attempt must not leave evidence on a complete stage",
	}
	s.runPipeline(context.Background(), item)

	if runner.failStageCalls != 2 {
		t.Fatalf("feature-dev dispatched %d times, want 2 (fail → escalate → succeed)",
			runner.failStageCalls)
	}

	records := readDailyJSONLRecords(t, root)
	var rec state.V2RunRecord
	found := false
	for _, r := range records {
		if r.IssueNumber == item.Number {
			rec, found = r, true
			break
		}
	}
	if !found {
		t.Fatalf("no record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}

	detail, ok := rec.Stages[string(state.StageFeatureDev)]
	if !ok {
		t.Fatalf("feature-dev missing from record; got stages=%v", rec.Stages)
	}
	if detail.Status != "complete" {
		t.Fatalf("stages[feature-dev].Status = %q, want complete — the retry was supposed "+
			"to succeed, so this test is no longer exercising a superseded attempt", detail.Status)
	}
	if detail.LastOutputLines != "" {
		t.Errorf("stages[feature-dev] is %q but carries the FAILED attempt's tail %q — "+
			"a successful stage must not be written with someone else's evidence (#533)",
			detail.Status, detail.LastOutputLines)
	}
}

// TestStderrFailureReason_Bounds covers the curated-reason extractor directly.
// ErrorText is the string ClassifyTerminalKind reads, so its size and shape are
// part of the classifier's input contract, not a cosmetic detail.
func TestStderrFailureReason_Bounds(t *testing.T) {
	t.Run("empty and whitespace yield no reason", func(t *testing.T) {
		for _, in := range []string{"", "\n", "\n\n\n", "   \n\t\n"} {
			if got := stderrFailureReason(in); got != "" {
				t.Errorf("stderrFailureReason(%q) = %q, want empty — a stage with no "+
					"stderr must keep its pre-#533 subagent_crash routing", in, got)
			}
		}
	})

	t.Run("keeps the LAST non-empty lines, in order", func(t *testing.T) {
		got := stderrFailureReason("first\n\nsecond\nthird\n\nfourth\n\n")
		want := "second\nthird\nfourth"
		if got != want {
			t.Errorf("stderrFailureReason = %q, want %q", got, want)
		}
	})

	t.Run("caps the line count", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 50; i++ {
			fmt.Fprintf(&sb, "line-%d\n", i)
		}
		got := stderrFailureReason(sb.String())
		if n := countLines(got); n != stderrReasonMaxLines {
			t.Errorf("line count = %d, want %d\n  got: %q", n, stderrReasonMaxLines, got)
		}
		if !strings.HasSuffix(got, "line-49") {
			t.Errorf("reason does not end at the last stderr line; got %q", got)
		}
	})

	t.Run("caps the byte length", func(t *testing.T) {
		got := stderrFailureReason(strings.Repeat("y", stderrReasonMaxBytes*4))
		if got == "" {
			t.Fatal("a single huge line was dropped entirely — the reason would be lost")
		}
		if len(got) > stderrReasonMaxBytes {
			t.Errorf("byte length = %d, want ≤ %d", len(got), stderrReasonMaxBytes)
		}
	})

	t.Run("the reason does not alias the caller's buffer", func(t *testing.T) {
		// A single-line stderr is the case that matters: strings.Join returns
		// its one element unchanged, and TrimSpace hands back a SLICE of the
		// input. Without the clone the returned reason pins the whole capture
		// for the lifetime of the run.
		big := strings.Repeat("z", 1<<20) + "\nboom\n"
		got := stderrFailureReason(big)
		if got != "boom" {
			t.Fatalf("stderrFailureReason = %q, want %q", got, "boom")
		}
		if unsafe.StringData(got) == unsafe.StringData(big[len(big)-len("boom\n"):]) {
			t.Error("the returned reason shares the caller's backing array — a 1MB " +
				"capture stays pinned for the whole run (#533)")
		}
	})
}

// TestCLIFailureTail_UsesTheStateCaps pins that the forensic tail obeys the
// SAME bounds RecordStageOutputTail applies, because it is now the same
// function. #533 briefly shipped a second implementation with its own copy of
// the constants, and the two already disagreed.
func TestCLIFailureTail_UsesTheStateCaps(t *testing.T) {
	t.Run("line cap keeps the LAST lines", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < state.StageOutputBufferLineLimit*3; i++ {
			fmt.Fprintf(&sb, "line-%d\n", i)
		}
		_, tail := cliFailureText(sb.String(), "")
		if n := countLines(tail); n > state.StageOutputBufferLineLimit {
			t.Errorf("line count = %d, want ≤ %d", n, state.StageOutputBufferLineLimit)
		}
		if !strings.Contains(tail, fmt.Sprintf("line-%d", state.StageOutputBufferLineLimit*3-1)) {
			t.Error("tail does not reach the last line — the cap kept the head")
		}
		if strings.Contains(tail, "line-0\n") {
			t.Error("tail retained the first line — the cap kept the head instead of the tail")
		}
	})

	t.Run("byte cap binds when lines are long", func(t *testing.T) {
		// 10 lines × 100KB = 1MB: well under the line cap, 5× the byte cap.
		var sb strings.Builder
		for i := 0; i < 10; i++ {
			sb.WriteString(strings.Repeat("x", 100*1024))
			sb.WriteByte('\n')
		}
		_, tail := cliFailureText(sb.String(), "")
		if len(tail) > state.StageOutputBufferByteCap {
			t.Errorf("byte length = %d, want ≤ %d", len(tail), state.StageOutputBufferByteCap)
		}
	})

	t.Run("the tail does not alias the caller's buffer", func(t *testing.T) {
		// execution.Manager accumulates the ENTIRE process output. Slicing a Go
		// string is O(1) and shares the backing array, so a 20KB tail retained
		// on the runtime for the run's lifetime would pin the whole capture.
		big := strings.Repeat("q", 4<<20) + "\ntrailer\n"
		_, tail := cliFailureText(big, "")
		if len(tail) >= len(big) {
			t.Fatalf("tail is %d bytes for a %d-byte input — not truncated", len(tail), len(big))
		}
		if unsafe.StringData(tail) == unsafe.StringData(big[len(big)-len(tail):]) {
			t.Error("the tail shares the caller's backing array — a 4MB capture stays " +
				"pinned for the whole run (#533)")
		}
	})
}

// TestCLIStageOutputCarry_BoundedForHugeStdout is the persistence-safety half
// of the #533 carry. ErrorText and LastOutputLines are the only two fields that
// reach the runtime snapshot (via terminalReason) and the daily history JSONL
// (via StageDetail.last_output_lines), and CLI mode's source for them is the
// stage's ENTIRE captured output. A ~10MB run must not write a ~10MB record.
func TestCLIStageOutputCarry_BoundedForHugeStdout(t *testing.T) {
	root := t.TempDir()

	var sb strings.Builder
	sb.Grow(11 << 20)
	for i := 0; sb.Len() < 10<<20; i++ {
		fmt.Fprintf(&sb, "chatter %07d %s\n", i, strings.Repeat("x", 80))
	}
	huge := sb.String()
	if len(huge) < 10<<20 {
		t.Fatalf("synthetic stdout is only %d bytes", len(huge))
	}

	stubCLI := writeFailingStubCLI(t, root, huge, grokUnknownModelStderr, 1)
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", stubCLI)

	runner := &ExecutionManagerRunner{
		execMgr: execution.NewManager(root, adapters.NewGrokAdapter()),
	}
	mustMkdirAll(t, filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-533"))

	res, err := runner.RunStage(context.Background(), StageRunParams{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 533,
		Stage:       state.StagePRCreate,
		Timeout:     2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}

	if len(res.ErrorText) > stderrReasonMaxBytes {
		t.Errorf("ErrorText = %d bytes, want ≤ %d", len(res.ErrorText), stderrReasonMaxBytes)
	}
	if n := countLines(res.ErrorText); n > stderrReasonMaxLines {
		t.Errorf("ErrorText = %d lines, want ≤ %d", n, stderrReasonMaxLines)
	}
	if len(res.LastOutputLines) > state.StageOutputBufferByteCap {
		t.Errorf("LastOutputLines = %d bytes, want ≤ %d — a 10MB stage would persist a 10MB record",
			len(res.LastOutputLines), state.StageOutputBufferByteCap)
	}
	if n := countLines(res.LastOutputLines); n > state.StageOutputBufferLineLimit {
		t.Errorf("LastOutputLines = %d lines, want ≤ %d", n, state.StageOutputBufferLineLimit)
	}

	// Bounded, but still the RIGHT bytes: stderr is what names the failure, and
	// it must survive a stdout flood 50× the byte cap.
	if !strings.Contains(res.ErrorText, "unknown model id") {
		t.Errorf("ErrorText lost the failure reason under a stdout flood; got %q", res.ErrorText)
	}
	// And the 10MB of transcript chatter must NOT be in the classifier's input.
	if strings.Contains(res.ErrorText, "chatter") {
		t.Errorf("ErrorText carries stdout transcript text (%d bytes) — ErrorText is the "+
			"curated reason ClassifyTerminalKind reads, never a buffer (#533)", len(res.ErrorText))
	}
	if !strings.Contains(res.LastOutputLines, "unknown model id") {
		t.Error("LastOutputLines lost the failure reason — stderr must be the tail's last segment, not its first")
	}
	if !strings.Contains(res.LastOutputLines, "chatter") {
		t.Error("LastOutputLines dropped the stdout transcript — it is the RAW evidence " +
			"field and must keep the context around the failure")
	}
}

// TestCLIFailureText_ErrorTextIsStderrOnly pins the source-selection rule, which
// is the whole safety argument for #533.
//
// ErrorText is CURATED — it is what ClassifyTerminalKind reads — so its only
// source is the adapter's own stderr. LastOutputLines is RAW evidence, never
// classified, so it keeps the stdout transcript too. A stdout fallback for
// ErrorText is the misrouting bug: stdout is the streaming-JSON transcript,
// every `tool_result` in it included, and the classifier is an ordered
// substring ladder.
func TestCLIFailureText_ErrorTextIsStderrOnly(t *testing.T) {
	tests := []struct {
		name           string
		stdout, stderr string
		wantErrText    string
		wantInTail     []string
	}{
		{
			name:        "stderr is the reason; stdout is evidence only",
			stdout:      `{"type":"error","message":"stdout copy"}`,
			stderr:      "the real reason",
			wantErrText: "the real reason",
			wantInTail:  []string{"stdout copy", "the real reason"},
		},
		{
			name:        "a silent stderr yields NO reason even when stdout names one",
			stdout:      `{"type":"error","message":"only on stdout"}`,
			stderr:      "",
			wantErrText: "",
			wantInTail:  []string{"only on stdout"},
		},
		{
			name: "a tool_result naming a classifier term never becomes the reason",
			stdout: `{"type":"user","message":{"content":[{"type":"tool_result",` +
				`"content":"npm ERR! missing prerequisite: node >= 20"}]}}`,
			stderr:      "",
			wantErrText: "",
			wantInTail:  []string{"missing prerequisite"},
		},
		{
			name:        "both empty yields both empty",
			stdout:      "",
			stderr:      "",
			wantErrText: "",
		},
		{
			name:        "whitespace-only output is not a reason",
			stdout:      "\n\n",
			stderr:      "\n",
			wantErrText: "",
			wantInTail:  []string{"\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErr, gotTail := cliFailureText(tt.stdout, tt.stderr)
			if gotErr != tt.wantErrText {
				t.Errorf("errorText = %q, want %q", gotErr, tt.wantErrText)
			}
			for _, want := range tt.wantInTail {
				if !strings.Contains(gotTail, want) {
					t.Errorf("lastOutputLines = %q, missing %q", gotTail, want)
				}
			}
			if len(tt.wantInTail) == 0 && gotTail != "" {
				t.Errorf("lastOutputLines = %q, want empty", gotTail)
			}
		})
	}
}

// TestStageFailureText_Precedence pins the precedence rule that the helper's
// doc comment claims: a non-nil Go error ALWAYS wins, so IPC mode and CLI
// mode's non-exit failures behave exactly as they did before #533, and the
// runner's carried ErrorText is consulted only when there is no Go error.
//
// Asserted directly rather than left to prose (the mutation review's M3): every
// scheduler consumer reads this one function, so inverting the two branches
// would silently re-route every IPC failure through a CLI-shaped result.
func TestStageFailureText_Precedence(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		result *StageRunResult
		want   string
	}{
		{
			name:   "go error wins over a carried ErrorText",
			err:    errors.New("go error"),
			result: &StageRunResult{ExitCode: 1, ErrorText: "carried text"},
			want:   "go error",
		},
		{
			name:   "carried ErrorText is used only when err is nil",
			err:    nil,
			result: &StageRunResult{ExitCode: 1, ErrorText: "carried text"},
			want:   "carried text",
		},
		{
			name:   "go error wins even with no result at all",
			err:    errors.New("go error"),
			result: nil,
			want:   "go error",
		},
		{
			name:   "no error and no result yields no text",
			err:    nil,
			result: nil,
			want:   "",
		},
		{
			name:   "no error and an empty carry yields no text (pre-#533 behavior)",
			err:    nil,
			result: &StageRunResult{ExitCode: 1},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stageFailureText(tt.err, tt.result); got != tt.want {
				t.Errorf("stageFailureText = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── #533 regression guard: the carry is adapter-AGNOSTIC ──────────────────
//
// The grok framing of #533 hides the blast radius. ExecutionManagerRunner is
// the CLI seam for EVERY adapter, so after the fix every CLI-mode non-zero-exit
// stage — claude, codex, gemini, copilot — suddenly has a non-empty failText
// where it used to have "". That changes ResolveTerminalKind answers repo-wide,
// and terminal kind drives retry, downgrade and escalation routing.
//
// The invariant these tests pin: an ORDINARY CLI crash must still classify as
// subagent_crash, exactly as it did when the classifier only ever saw
// "exit 1: <nil>". Only outputs that genuinely name a recognized condition are
// allowed to move — that is the fix, not a side effect of it.

// cliTranscriptLine wraps body in the stream-JSON `tool_result` envelope a CLI
// adapter writes to STDOUT for every tool the agent runs — a `go test` run, an
// `npm install`, a `git push`. Anything the agent's tools print arrives here,
// which is precisely why stdout must never reach the classifier.
func cliTranscriptLine(body string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"running the suite"}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","content":` +
		fmt.Sprintf("%q", body) + `}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"that did not work"}]}}` + "\n"
}

// TestTerminalReasonClassification_CLIShapeParity is the table. Every row is a
// realistic CLI-mode capture pushed through the REAL cliFailureText, then
// through BOTH classification consumers the scheduler has:
//
//	prefixed   — terminalFailureReason → ClassifyTerminalKind → the V3 record's
//	             terminal_failure_kind and the persisted stage error.
//	unprefixed — the bare failText / stallErrMsg that drive the #42 sticky
//	             downgrade, the stall rewind, and HasCostCapKillMarker.
//
// The rows are keyed by CHANNEL, not by adapter: ExecutionManagerRunner is the
// one CLI seam for every adapter, so an adapter column would imply per-adapter
// coverage that does not exist (per-adapter wiring is proven by
// TestCLIRunnerCarriesFailureTextForEveryAdapter). What actually decides the
// answer is whether the text arrived on the adapter's own stderr or inside the
// stdout transcript.
func TestTerminalReasonClassification_CLIShapeParity(t *testing.T) {
	// What the classifier answered for EVERY CLI failure before #533: the
	// nil-error placeholder.
	const preFixReason = "exit 1: <nil>"
	preFixKind := ClassifyTerminalKind(preFixReason)
	if preFixKind != TerminalKindSubagentCrash {
		t.Fatalf("premise check: ClassifyTerminalKind(%q) = %q, want %q — #533's "+
			"whole story is that CLI mode collapsed to this one kind",
			preFixReason, preFixKind, TerminalKindSubagentCrash)
	}

	tests := []struct {
		name           string
		stdout, stderr string
		want           string
		why            string
	}{
		// ── MUST NOT MOVE: tool output inside the stdout transcript ──────
		//
		// These are the measured misroutes from the #533 review. Each names a
		// term the ordered substring ladder matches, and each lands on a
		// recovery branch with NO attempt cap: stall_kill re-dispatches every
		// 30 minutes forever without incrementing lifetime failures or feeding
		// the cascade breaker; model_unavailable adds a sticky tier downgrade
		// AND skips escalation; branch_forked deliberately strands the issue
		// for a human. They must all read subagent_crash.
		{
			name:   "node JSON-parse error in a tool_result",
			stdout: cliTranscriptLine(`SyntaxError: Unexpected token 'o', "not json"... is not valid JSON`),
			want:   TerminalKindSubagentCrash,
			why:    "`is not valid JSON` matches validation_error; it is the agent's jq, not the pipeline's schema",
		},
		{
			name:   "npm prerequisite failure in a tool_result",
			stdout: cliTranscriptLine("npm ERR! code EBADENGINE\nnpm ERR! missing prerequisite: node >= 20"),
			want:   TerminalKindSubagentCrash,
			why:    "`missing prerequisite` matches validation_error",
		},
		{
			name:   "git push rejection in a tool_result",
			stdout: cliTranscriptLine(" ! [rejected]  fix/533 -> fix/533 (non-fast-forward)\nerror: failed to push some refs"),
			want:   TerminalKindSubagentCrash,
			why:    "`non-fast-forward` matches branch_forked, which strands the issue with no board revert",
		},
		{
			name:   "503 upstream overload in a tool_result",
			stdout: cliTranscriptLine("HTTP/1.1 503 Service Unavailable\nupstream overloaded, retry later"),
			want:   TerminalKindSubagentCrash,
			why:    "`overloaded` matches api_overloaded; an unrelated gateway is not the model API",
		},
		{
			name:   "lint line quoting the cost-cap marker",
			stdout: cliTranscriptLine("scheduler.go:4820:2: comment references [cost-cap-exceeded] (godot)"),
			want:   TerminalKindSubagentCrash,
			why:    "`cost-cap-exceeded` matches budget_exceeded and suppresses stall recovery outright",
		},
		{
			name:   "go test naming the hard cap",
			stdout: cliTranscriptLine("--- FAIL: TestBudget/hard_cap (0.01s)\n    budget_test.go:88: hard cap not enforced"),
			want:   TerminalKindSubagentCrash,
			why:    "`hard cap` matches stall_kill — the uncapped 30-minute retry loop",
		},
		{
			name:   "assistant prose naming a model and a usage limit",
			stdout: cliTranscriptLine("Claude Opus 4.5 usage limit reached — resets at 5pm."),
			want:   TerminalKindSubagentCrash,
			why:    "`usage limit` + a registry model matches model_unavailable's sticky downgrade",
		},
		{
			name:   "grep hit on the classifier table itself",
			stdout: cliTranscriptLine(`table.json:  "clauses": [["invalid model"]],`),
			want:   TerminalKindSubagentCrash,
			why:    "the corpus's own vocabulary must not classify when the agent merely reads it",
		},
		{
			name:   "a rejected tool call echoed in the transcript",
			stdout: cliTranscriptLine("tool_use_result: the operator rejected this Bash call"),
			want:   TerminalKindSubagentCrash,
			why:    "`tool_use_result`+`rejected` matches permission_denied's harness bucket",
		},
		{
			name:   "a multi-line transcript tail with no reason at all",
			stdout: strings.Repeat(cliTranscriptLine("ok  github.com/nightgauge/nightgauge/internal/state\t0.4s"), 40),
			want:   TerminalKindSubagentCrash,
			why:    "a buffer is not a reason; with a silent stderr there is nothing to classify",
		},

		// ── MUST NOT MOVE: ordinary adapter crashes on stderr ────────────
		{
			name: "node crash on stderr",
			stderr: "Error: Cannot read properties of undefined (reading 'text')\n" +
				"    at process.processTicksAndRejections (node:internal/process/task_queues:105:5)",
			want: TerminalKindSubagentCrash,
			why:  "an ordinary CLI crash routes exactly as it did pre-#533",
		},
		{
			name:   "bad flag on stderr",
			stderr: "error: unknown option '--nope'",
			want:   TerminalKindSubagentCrash,
			why:    "a usage error is not a recognized pipeline condition",
		},
		{
			name:   "stream disconnect on stderr",
			stderr: "codex exec: stream disconnected before completion",
			want:   TerminalKindSubagentCrash,
			why:    "a transport hiccup with no recognized marker stays generic",
		},
		{
			name:   "sandbox denial on stderr",
			stderr: "ERROR: Unable to complete the task: the sandbox denied write access to /etc",
			want:   TerminalKindSubagentCrash,
			why:    "sandbox refusals must not be mistaken for permission_denied's harness bucket",
		},
		{
			name:   "go panic on stderr",
			stderr: "panic: runtime error: invalid memory address or nil pointer dereference",
			want:   TerminalKindSubagentCrash,
			why:    "a hard crash is what subagent_crash is FOR",
		},
		{
			name: "no output at all",
			want: TerminalKindSubagentCrash,
			why:  "no evidence means no behavior change at all",
		},

		// ── ALLOWED TO MOVE — this is the fix ────────────────────────────
		{
			name:   "unknown model on stderr becomes model_unavailable",
			stderr: grokUnknownModelStderr,
			want:   TerminalKindModelUnavailable,
			why:    "#42's sticky downgrade, not an upward escalation",
		},
		{
			name:   "a stall marker on stderr becomes stall_kill",
			stderr: "[stall-killed] feature-dev terminated: exceeded stall idle threshold (1200s without output)",
			want:   TerminalKindStallKill,
			why:    "CLI mode reaches the same recovery branch IPC mode already did",
		},
		{
			name:   "grok not-signed-in on stderr becomes adapter_auth_failed",
			stderr: grokNotSignedInStderr,
			want:   TerminalKindAdapterAuthFailed,
			why:    "#591: an auth-shaped failure, not a process death — and never eligible for model escalation, which cannot fix a login problem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failText, _ := cliFailureText(tt.stdout, tt.stderr)

			// Consumer 1 — the PREFIXED reason: persisted on the runtime
			// snapshot and re-classified into the V3 terminal_failure_kind.
			reason := terminalFailureReason(1, nil, failText)
			// The `exit N: ` prefix is load-bearing: subagent-crash's fallback
			// clause is the literal "exit ", so a reason that lost the prefix
			// falls out of the ladder entirely and classifies as nothing.
			if !strings.HasPrefix(reason, "exit 1: ") {
				t.Fatalf("terminalFailureReason lost the %q prefix: %q — subagent_crash's "+
					"fallback clause matches on it", "exit 1: ", reason)
			}
			got := ClassifyTerminalKind(reason)
			if got == "" {
				got = TerminalKindSubagentCrash // recordV2History's fallback
			}
			if got != tt.want {
				t.Errorf("prefixed terminal kind = %q, want %q (%s)\n  reason: %q",
					got, tt.want, tt.why, reason)
			}
			if tt.want == TerminalKindSubagentCrash && got != preFixKind {
				t.Errorf("routing MOVED (%q → %q) for an ordinary failure — #533 must not "+
					"reroute retries for shapes it was not about (%s)", preFixKind, got, tt.why)
			}

			// Consumer 2 — the UNPREFIXED text. `failText` drives the #42
			// sticky downgrade and `stallErrMsg` drives the stall rewind; both
			// read the bare string, never the prefixed reason, so the prefixed
			// assertion above does not cover them.
			bare := ResolveTerminalKind(false, "", failText)
			if bare == "" {
				bare = TerminalKindSubagentCrash
			}
			if bare != tt.want {
				t.Errorf("unprefixed terminal kind = %q, want %q (%s)\n  failText: %q",
					bare, tt.want, tt.why, failText)
			}

			// HasCostCapKillMarker is its own consumer: it gates whether a
			// stall-kill may be retried at all, and it reads the same bare
			// text. Only a genuine cost-cap kill may set it.
			if wantMarker := tt.want == TerminalKindBudgetExceeded; HasCostCapKillMarker(failText) != wantMarker {
				t.Errorf("HasCostCapKillMarker = %v, want %v — a lint line quoting the "+
					"marker must not suppress stall recovery\n  failText: %q",
					!wantMarker, wantMarker, failText)
			}
		})
	}
}

// TestCLIRunnerCarriesFailureTextForEveryAdapter drives the REAL
// ExecutionManagerRunner over the claude and codex adapters with PATH-injected
// stub binaries. The carry lives in the shared runner, not in anything
// grok-specific, and this is what proves it — plus the other half of the
// non-regression: a stage that exits 0 must carry no failure evidence at all.
func TestCLIRunnerCarriesFailureTextForEveryAdapter(t *testing.T) {
	tests := []struct {
		binary  string
		adapter adapters.SkillRunner
		stderr  string
	}{
		{"claude", adapters.NewClaudeAdapter(), "Error: Cannot read properties of undefined (reading 'text')"},
		{"codex", adapters.NewCodexAdapter(), "codex exec: stream disconnected before completion"},
	}

	for _, tt := range tests {
		t.Run(tt.binary+"/non-zero exit carries the reason", func(t *testing.T) {
			root := t.TempDir()
			stubDir := t.TempDir()
			// claude and codex hardcode their command name (no CLI_COMMAND
			// override), so the stub is injected by name on PATH.
			stub := writeFailingStubCLI(t, stubDir, "", tt.stderr, 1)
			if err := os.Rename(stub, filepath.Join(stubDir, tt.binary)); err != nil {
				t.Fatalf("rename stub: %v", err)
			}
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			runner := &ExecutionManagerRunner{execMgr: execution.NewManager(root, tt.adapter)}
			mustMkdirAll(t, filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-533"))

			res, err := runner.RunStage(context.Background(), StageRunParams{
				Repo:        "nightgauge/nightgauge",
				IssueNumber: 533,
				Stage:       state.StagePRCreate,
				Timeout:     30 * time.Second,
			})
			if err != nil {
				t.Fatalf("RunStage: %v", err)
			}
			assertStubRan(t, stubDir)
			if res.ExitCode != 1 {
				t.Fatalf("ExitCode = %d, want 1 — the %s stub did not run", res.ExitCode, tt.binary)
			}
			if !strings.Contains(res.ErrorText, tt.stderr) {
				t.Errorf("%s ErrorText = %q, want it to carry the CLI's stderr — the carry "+
					"must not be grok-specific", tt.binary, res.ErrorText)
			}
			if res.LastOutputLines == "" {
				t.Errorf("%s LastOutputLines empty", tt.binary)
			}
		})

		t.Run(tt.binary+"/exit 0 carries nothing", func(t *testing.T) {
			root := t.TempDir()
			stubDir := t.TempDir()
			// A successful stage whose stderr still has content: deprecation
			// notices and progress chatter routinely land there. None of it is
			// a failure reason, and treating it as one would hand the
			// classifier text for a run that never failed.
			stub := writeFailingStubCLI(t, stubDir, "", "warning: --foo is deprecated", 0)
			if err := os.Rename(stub, filepath.Join(stubDir, tt.binary)); err != nil {
				t.Fatalf("rename stub: %v", err)
			}
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			runner := &ExecutionManagerRunner{execMgr: execution.NewManager(root, tt.adapter)}
			mustMkdirAll(t, filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-533"))

			res, err := runner.RunStage(context.Background(), StageRunParams{
				Repo:        "nightgauge/nightgauge",
				IssueNumber: 533,
				Stage:       state.StagePRCreate,
				Timeout:     30 * time.Second,
			})
			if err != nil {
				t.Fatalf("RunStage: %v", err)
			}
			assertStubRan(t, stubDir)
			if res.ExitCode != 0 {
				t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
			}
			if res.ErrorText != "" || res.LastOutputLines != "" {
				t.Errorf("%s succeeded but carried failure evidence (ErrorText=%q LastOutputLines=%q) — "+
					"a successful stage's stderr is not a failure reason",
					tt.binary, res.ErrorText, res.LastOutputLines)
			}
		})
	}
}
