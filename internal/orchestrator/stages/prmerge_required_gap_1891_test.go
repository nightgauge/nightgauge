package stages

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Issue #1891: on the #1889 run the deterministic arm punted
// `dirty-merge-state: BLOCKED` ~9 minutes in, with nothing pending, while a
// required context had not reported (CodeQL was skipped/absent). The LLM arm
// then merged the PR 33 minutes later with no intervention, at $6.74 of
// waiting. These tests pin the predicate's split between "BLOCKED because a
// required context has not reported" (wait, bounded) and "BLOCKED for a
// non-CI reason" (punt at once), and the evidence a punt carries.

func blockedGreen(required ...string) PRViewSnapshot {
	return PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED",
		StatusCheckRollup: []PRStatusCheckRow{
			{Name: "Go Tests", Conclusion: "SUCCESS"},
			{Name: "Lint", Conclusion: "SUCCESS"},
		},
		RequiredChecks: required,
	}
}

func TestMergeBlockedByPendingCI_RequiredContextGaps_1891(t *testing.T) {
	skippedCodeQL := blockedGreen("Go Tests", "CodeQL")
	skippedCodeQL.StatusCheckRollup = append(skippedCodeQL.StatusCheckRollup,
		PRStatusCheckRow{Name: "CodeQL", Conclusion: "SKIPPED"})
	reviewRequired := blockedGreen("Go Tests", "CodeQL")
	reviewRequired.ReviewDecision = "REVIEW_REQUIRED"
	failed := blockedGreen("Go Tests")
	failed.StatusCheckRollup[0].Conclusion = "FAILURE"
	conflict := blockedGreen("Go Tests", "CodeQL")
	conflict.Mergeable, conflict.MergeStateStatus = "CONFLICTING", "DIRTY"

	cases := []struct {
		name string
		snap PRViewSnapshot
		want bool
	}{
		{"all green + required context absent → wait", blockedGreen("Go Tests", "CodeQL"), true},
		{"all green + required context skipped → wait", skippedCodeQL, true},
		{"required name matches case-insensitively", blockedGreen("go tests", "lint"), false},
		{"all green + REVIEW_REQUIRED → punt", reviewRequired, false},
		{"a failed check → punt", failed, false},
		{"conflict → punt", conflict, false},
		{"nothing pending, nothing absent → punt (non-CI block)", blockedGreen("Go Tests", "Lint"), false},
		{"nothing pending, no known required set → punt (pre-#1891)", blockedGreen(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeBlockedByPendingCI(tc.snap); got != tc.want {
				t.Errorf("MergeBlockedByPendingCI() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequiredCheckGaps_1891(t *testing.T) {
	snap := blockedGreen("Go Tests", "CodeQL", "Flag contract", "Lint")
	snap.StatusCheckRollup = append(snap.StatusCheckRollup,
		PRStatusCheckRow{Name: "Flag contract", Conclusion: "SKIPPED"},
		PRStatusCheckRow{Name: "Lint", Conclusion: "FAILURE"}, // re-run leg: worst reading wins
	)
	absent, skipped, failing := RequiredCheckGaps(snap)
	if !reflect.DeepEqual(absent, []string{"CodeQL"}) {
		t.Errorf("absent = %v, want [CodeQL]", absent)
	}
	if !reflect.DeepEqual(skipped, []string{"Flag contract"}) {
		t.Errorf("skipped = %v, want [Flag contract]", skipped)
	}
	if !reflect.DeepEqual(failing, []string{"Lint"}) {
		t.Errorf("failing = %v, want [Lint]", failing)
	}
}

// A run like #1889's: the required context reports only after the rest of CI
// has concluded. The deterministic arm must wait it out and merge, not punt.
func TestDeterministicRunner_RequiredContextAbsent_WaitsThenMerges_1891(t *testing.T) {
	absent := blockedGreen("Go Tests", "CodeQL")
	absent.ReviewDecision = "APPROVED"
	clean := PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED",
		StatusCheckRollup: []PRStatusCheckRow{
			{Name: "Go Tests", Conclusion: "SUCCESS"},
			{Name: "CodeQL", Conclusion: "SUCCESS"},
		},
	}
	seq := &sequenceGh{responses: []sequenceResp{
		{snap: absent},                          // initial fetch: all listed green, CodeQL not reported
		{snap: absent},                          // wait poll 1: unchanged
		{snap: clean},                           // wait poll 2: CodeQL reported, CLEAN
		{snap: PRViewSnapshot{State: "MERGED"}}, // post-merge EC poll
	}}
	r := newRunnerWithSeq(seq, 1889, 5)

	res, err := r.Run(context.Background(), 1787, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("Path = %q (reason %q), want merged on the deterministic path", res.Path, res.Reason)
	}
}

// A required context that never reports is bounded by the same budget and
// punts ci-wait-timeout, naming the context — not dirty-merge-state.
func TestDeterministicRunner_RequiredContextNeverReports_TimesOutWithEvidence_1891(t *testing.T) {
	absent := blockedGreen("Go Tests", "CodeQL")
	seq := &sequenceGh{responses: []sequenceResp{{snap: absent}}}
	r := newRunnerWithSeq(seq, 1889, 3)

	res, err := r.Run(context.Background(), 1787, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt || !strings.HasPrefix(res.Reason, ReasonCIWaitTimeout) {
		t.Fatalf("Path=%q Reason=%q, want a %s punt", res.Path, res.Reason, ReasonCIWaitTimeout)
	}
	if !strings.Contains(res.Reason, "absent=[CodeQL]") {
		t.Errorf("Reason %q does not name the absent required context", res.Reason)
	}
	if seq.mergeCalls != 0 {
		t.Errorf("Merge must not be called, got %d", seq.mergeCalls)
	}
}

// A genuine non-CI block still punts immediately with its own reason, and the
// punt records the required set's state at the verdict.
func TestDeterministicRunner_NonCIBlock_PuntsImmediatelyWithEvidence_1891(t *testing.T) {
	blocked := blockedGreen("Go Tests", "Lint")
	seq := &sequenceGh{responses: []sequenceResp{{snap: blocked}}}
	r := newRunnerWithSeq(seq, 1889, 5)

	res, err := r.Run(context.Background(), 1787, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt || !strings.HasPrefix(res.Reason, ReasonDirtyState+": BLOCKED") {
		t.Fatalf("Path=%q Reason=%q, want an immediate dirty-merge-state: BLOCKED punt", res.Path, res.Reason)
	}
	if !strings.Contains(res.Reason, "required checks: absent=[] skipped=[] failing=[]") {
		t.Errorf("Reason %q does not record the required-check state", res.Reason)
	}
	if seq.viewCalls != 1 {
		t.Errorf("View calls = %d, want 1 (no wait on a non-CI block)", seq.viewCalls)
	}
}
