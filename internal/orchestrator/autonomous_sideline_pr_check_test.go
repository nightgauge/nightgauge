package orchestrator

import (
	"context"
	"log"
	"strings"
	"testing"
)

// Tests for sidelineHalt (#281): the architecture-approval and
// pr-merge-unmerged halt paths must not park an issue at "In review" unless
// an OPEN PR actually backs it. No project config is wired on the fixture
// scheduler, so moveIssueToInReview/moveIssueToInProgress bail out at the
// "no project config" log line before touching ghClient — that line is
// enough to distinguish which status was chosen without a real GitHub call.

func withCapturedLog(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})
	return &buf
}

// TestSidelineHalt_NoPR_MovesToInProgress covers the architecture-approval
// case: no OPEN PR exists (the gate fires before feature-dev ever runs), so
// the log must never claim "PR exists" and the chosen status must be "In
// progress".
func TestSidelineHalt_NoPR_MovesToInProgress(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "list") {
			return []byte("[]"), nil // repo has no open PRs at all
		}
		return []byte("[]"), nil
	})
	buf := withCapturedLog(t)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.sidelineHalt("O/app", 900, "architecture approval required")

	got := buf.String()
	if strings.Contains(got, "PR exists") {
		t.Errorf("no-PR halt must not claim a PR exists, got log: %q", got)
	}
	if !strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("expected move-to-in-progress path, got log: %q", got)
	}
	if strings.Contains(got, "move-to-in-review:") {
		t.Errorf("no-PR halt must not take the In-review path, got log: %q", got)
	}
}

// TestSidelineHalt_PRExists_MovesToInReview is the regression guard for the
// real pr-merge-unmerged case: a PR genuinely exists, so the behavior is
// unchanged — "In review" with the existing truthful log.
func TestSidelineHalt_PRExists_MovesToInReview(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "list") {
			return []byte(`[{"number":42,"headRefName":"feat/900-x","mergeStateStatus":"BLOCKED"}]`), nil
		}
		return []byte("[]"), nil
	})
	buf := withCapturedLog(t)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.sidelineHalt("O/app", 900, "pr-merge: PR was not merged")

	got := buf.String()
	if !strings.Contains(got, "move-to-in-review:") {
		t.Errorf("expected move-to-in-review path when a PR exists, got log: %q", got)
	}
	if strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("PR-backed halt must not take the In-progress path, got log: %q", got)
	}
}

// TestSidelineHalt_QueryFails_FailsToInProgress: a transient gh error must
// never be read as "PR confirmed" — the safer state (In progress) wins.
func TestSidelineHalt_QueryFails_FailsToInProgress(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	buf := withCapturedLog(t)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.sidelineHalt("O/app", 900, "architecture approval required")

	got := buf.String()
	if !strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("query failure must fail toward In progress, got log: %q", got)
	}
}
