package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// OpenCode failure kinds (#1631), read from what the adapter really prints.
//
// internal/terminalkind/testdata/opencode/ holds opencode 1.18.30's stderr for
// each failure a model request can end in (scripts/
// capture-opencode-failure-fixture.sh). These tests put each capture through
// the scheduler's own reason builder — cliFailureText, which keeps the last
// lines of stderr, then terminalFailureReason's `exit N: ` — and classify the
// result, so a rule that matches a corpus row but not the text the scheduler
// actually builds from a real stage fails here.

const openCodeCaptureDir = "../terminalkind/testdata/opencode"

// openCodeStageReason is the reason the scheduler records for an opencode
// stage that exited 1 with the captured stderr.
func openCodeStageReason(t *testing.T, leg string) string {
	t.Helper()
	stderr, err := os.ReadFile(filepath.Join(openCodeCaptureDir, leg+".stderr"))
	if err != nil {
		t.Fatalf("read the %s capture: %v", leg, err)
	}
	reason, _ := cliFailureText("", string(stderr))
	if reason == "" {
		t.Fatalf("the %s capture yields no failure reason", leg)
	}
	return terminalFailureReason(1, nil, reason)
}

func TestOpenCodeCapturedFailuresClassify(t *testing.T) {
	cases := []struct {
		leg  string
		want string
	}{
		{"overflow-openai", TerminalKindContextWindowExceeded},
		{"overflow-lmstudio", TerminalKindContextWindowExceeded},
		{"overflow-ollama", TerminalKindContextWindowExceeded},
		{"overflow-llamacpp", TerminalKindContextWindowExceeded},
		{"auth", TerminalKindAdapterAuthFailed},
		{"server-down", TerminalKindNetworkUnavailable},
		{"model-not-configured", TerminalKindModelUnavailable},
		// A real `ollama serve` asked for a model it has not pulled: its 404,
		// in OpenCode's AI_APICallError wrapper.
		{"ollama-model-not-pulled", TerminalKindModelUnavailable},
		// The negative control: a failed model request that is none of the
		// above keeps the generic kind, the `exit ` fallback's.
		{"provider-error", TerminalKindSubagentCrash},
	}
	for _, c := range cases {
		t.Run(c.leg, func(t *testing.T) {
			reason := openCodeStageReason(t, c.leg)
			if got := ClassifyTerminalKind(reason); got != c.want {
				t.Errorf("the %s capture classifies %q, want %q; the reason the scheduler built:\n%s", c.leg, got, c.want, reason)
			}
		})
	}
}

// TestOpenCodeReadRejectionParks: opencode 1.18.30's own default ruleset asks
// before reading `*.env` and `*.env.*`, and a headless run auto-rejects the
// ask, so a stage allowed Read that reaches for a secret file, on its own or
// because the issue text asked it to, ends `[adapter-permission-rejected]
// tool=read`. That parks exactly as a rejection of any other granted
// permission does: one dispatch, then an operator hold. A retry would let the
// model or the issue text loop the issue against a guard nobody may loosen.
//
// The rejection goes through the real completion path: the reason the
// scheduler builds from the stage's stderr, NotifyComplete's Go-side
// classification, onPipelineComplete, then the graph reconcile and
// prioritize. It is delivered once more than permission_denied's
// consecutive-denial cap, with any retry deadline treated as elapsed, because
// that retry's cap deletes the backoff and leaves an unheld entry the
// reconcile re-admits. The issue must never be a candidate again. bash and
// edit are the controls: the permission a marker names is the model's tool
// choice and never changes the outcome.
func TestOpenCodeReadRejectionParks(t *testing.T) {
	for _, tool := range []string{"read", "bash", "edit"} {
		t.Run(tool, func(t *testing.T) {
			stubReconcileGhUnreachable(t)
			as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
			as.workspaceRoot = t.TempDir()
			as.state.LifetimeIssueFailures = map[string]int{}
			as.perIssueFailureCount = map[string]int{}
			as.retryBackoff = map[string]retryPlan{}

			// The stage's stderr as #1624's parser leaves it
			// (internal/execution/opencode_usage_test.go pins this shape).
			stderr := "! permission requested: " + tool + " (...); auto-rejecting\n[adapter-permission-rejected] tool=" + tool + "\n"
			text, _ := cliFailureText("", stderr)
			reason := terminalFailureReason(1, nil, text)

			const repo, n = "acme/app", 1631
			key := fmt.Sprintf("%s#%d", repo, n)
			g := holdTestGraph(&depgraph.Node{Repo: repo, Number: n, State: "OPEN", BoardStatus: "Ready"})
			for attempt := 1; attempt <= permissionDeniedMaxAttempts+1; attempt++ {
				addRunning(as, repo, n, "an issue whose stage reaches for a .env file")
				as.NotifyComplete(repo, n, false, false, "", reason)
				as.drainBackground()

				if plan, ok := as.retryBackoff[key]; ok {
					t.Errorf("attempt %d: a %s rejection scheduled a %s retry; it must park", attempt, tool, plan.Kind)
					// Let the backoff elapse, as a running scheduler would.
					plan.Until = time.Now().Add(-time.Second)
					as.retryBackoff[key] = plan
				}
				as.reconcileStateAgainstGraph(g)
				if isCandidate(as.prioritize(context.Background(), g), repo, n) {
					t.Fatalf("attempt %d: after a %s rejection the issue is a dispatch candidate again, "+
						"so the model or the issue text can loop it:\n%s", attempt, tool, reason)
				}
			}

			if got := as.humanHoldFor(repo, n); got != HoldOperatorResume {
				t.Errorf("hold = %q, want %q", got, HoldOperatorResume)
			}
			if len(as.state.Failed) != 1 {
				t.Fatalf("state.Failed has %d entries, want the one park", len(as.state.Failed))
			}
			f := as.state.Failed[0]
			if f.Kind != TerminalKindAdapterPermissionRejected {
				t.Errorf("failed entry kind = %q, want %q", f.Kind, TerminalKindAdapterPermissionRejected)
			}
			if !strings.Contains(f.Reason, TerminalKindRemediation(TerminalKindAdapterPermissionRejected)) {
				t.Errorf("failed entry reason does not name the remediation:\n%s", f.Reason)
			}
			if got := as.state.LifetimeIssueFailures[key]; got != 0 {
				t.Errorf("LifetimeIssueFailures = %d, want 0: the rejection is not charged to the issue", got)
			}
		})
	}
}

// TestOpenCodeIncompatibleRefusalsClassify renders the refusals the adapter
// itself raises (ADR-022 § 20) and classifies them as the scheduler records a
// dispatch refusal: execution.Manager's wrapper, then `exit 0: `.
func TestOpenCodeIncompatibleRefusalsClassify(t *testing.T) {
	if TerminalKindAdapterIncompatible != adapters.OpenCodeIncompatible {
		t.Fatalf("TerminalKindAdapterIncompatible = %q, but the refusal carries %q", TerminalKindAdapterIncompatible, adapters.OpenCodeIncompatible)
	}
	bin := adapters.OpenCodeBinary{Path: "/usr/local/bin/opencode"}
	_, belowFloor := adapters.CheckOpenCodeVersion(bin, "1.17.2", nil, "/Users/fixture")
	_, unreadable := adapters.CheckOpenCodeVersion(bin, "", fmt.Errorf("exit status 1"), "/Users/fixture")
	above, err := adapters.CheckOpenCodeVersion(bin, "99.0.0", nil, "/Users/fixture")
	if err != nil || !above.AboveMaxTested {
		t.Fatalf("CheckOpenCodeVersion(99.0.0) = %+v, %v; want an above-max-tested policy", above, err)
	}
	endpoint := adapters.OpenCodeEndpointAboveMaxTested(above, "lmstudio/qwen/qwen3.8-27b", nil, "/Users/fixture")

	for name, refusal := range map[string]error{"below-floor": belowFloor, "unreadable": unreadable, "endpoint-above-max-tested": endpoint} {
		t.Run(name, func(t *testing.T) {
			if refusal == nil {
				t.Fatal("the adapter raised no refusal")
			}
			reason := terminalFailureReason(0, fmt.Errorf("dispatch refused for adapter %q: %w", "opencode", refusal), "")
			if got := ClassifyTerminalKind(reason); got != TerminalKindAdapterIncompatible {
				t.Errorf("the %s refusal classifies %q, want %q:\n%s", name, got, TerminalKindAdapterIncompatible, reason)
			}
		})
	}
}

// TestOpenCodeParkedFailuresAreNotEscalated drives each parked kind through
// the scheduler's real stage-failure path with escalation enabled, exactly as
// TestCLIAdapterAuthFailedExcludedFromEscalation_NotSubagentCrash does for an
// auth failure. A stronger model on the same adapter meets the same context
// window, permission rule and binary, so the stage must be dispatched once and
// the run recorded with the parked kind.
func TestOpenCodeParkedFailuresAreNotEscalated(t *testing.T) {
	overflow, err := os.ReadFile(filepath.Join(openCodeCaptureDir, "overflow-lmstudio.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	_, refusal := adapters.CheckOpenCodeVersion(adapters.OpenCodeBinary{Path: "/usr/local/bin/opencode"}, "1.17.2", nil, "/Users/fixture")
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{"context-window-exceeded", string(overflow), TerminalKindContextWindowExceeded},
		// The stage's stderr as #1624's parser leaves it
		// (internal/execution/opencode_usage_test.go pins this exact text).
		{"adapter-permission-rejected", "! permission requested: bash (...); auto-rejecting\n[adapter-permission-rejected] tool=bash\n", TerminalKindAdapterPermissionRejected},
		{"adapter-incompatible", "dispatch refused for adapter \"opencode\": " + refusal.Error() + "\n", TerminalKindAdapterIncompatible},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			errText, tail := cliFailureText("", c.stderr)
			runner := &cliFailureStageRunner{
				failStage:       state.StagePRCreate,
				errText:         errText,
				lastOutputLines: tail,
			}
			s := buildStallTestScheduler(t, root, runner)
			s.retryEngine = NewRetryEngine(RetryConfig{
				MaxBacktracks:          0,
				MaxEscalationsPerStage: 1,
				ModelLadder:            []string{"haiku", "sonnet", "opus"},
			})
			item := types.BoardItem{
				Number: 16310 + i,
				Repo:   "nightgauge/nightgauge",
				ID:     fmt.Sprintf("item-%d", 16310+i),
				Title:  "an opencode stage that fails the same way on every model",
				Labels: []string{"type:bug"},
			}
			s.runPipeline(context.Background(), item)

			var rec *state.V2RunRecord
			for _, r := range readDailyJSONLRecords(t, root) {
				if r.IssueNumber == item.Number {
					rec = &r
				}
			}
			if rec == nil {
				t.Fatalf("no record for #%d", item.Number)
			}
			if rec.TerminalFailureKind != c.want {
				t.Errorf("terminal_failure_kind = %q, want %q", rec.TerminalFailureKind, c.want)
			}
			if got := s.retryEngine.CurrentModel(string(state.StagePRCreate)); got != "" {
				t.Errorf("pr-create escalated to %q — %s is met unchanged on a stronger model", got, c.want)
			}
			if runner.failStageCalls != 1 {
				t.Errorf("pr-create dispatched %d times, want 1 — %s is parked, not retried", runner.failStageCalls, c.want)
			}
		})
	}
}
