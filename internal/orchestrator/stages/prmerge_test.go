package stages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeGh is a state-driven ghClient for unit tests. The same `preMerge`
// snapshot is returned for every View call until Merge succeeds, after which
// `postMerge` is returned. This mirrors the real behavior where the EC poll
// loop sees OPEN until the merge call lands MERGED.
type fakeGh struct {
	preMerge     PRViewSnapshot
	postMerge    PRViewSnapshot
	preMergeErr  error
	postMergeErr error
	mergeErr     error
	merged       bool
	viewCalls    int
	mergeCalls   int
}

func (f *fakeGh) View(_ context.Context, _ int) (PRViewSnapshot, error) {
	f.viewCalls++
	if f.merged {
		return f.postMerge, f.postMergeErr
	}
	return f.preMerge, f.preMergeErr
}

func (f *fakeGh) Merge(_ context.Context, _ int) error {
	f.mergeCalls++
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged = true
	return nil
}

// sequenceGh returns snapshots from a list, draining one per View call. Used
// for tests that need to simulate state changing across polls
// (eventual-consistency).
type sequenceGh struct {
	responses  []sequenceResp
	idx        int
	viewCalls  int
	mergeCalls int
	mergeErr   error
}

type sequenceResp struct {
	snap PRViewSnapshot
	err  error
}

func (s *sequenceGh) View(_ context.Context, _ int) (PRViewSnapshot, error) {
	s.viewCalls++
	idx := s.idx
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	resp := s.responses[idx]
	if s.idx < len(s.responses)-1 {
		s.idx++
	}
	return resp.snap, resp.err
}

func (s *sequenceGh) Merge(_ context.Context, _ int) error {
	s.mergeCalls++
	return s.mergeErr
}

// newRunnerWith builds a test runner with no polling delay (unit tests must
// not sleep) and a stubbed pr-context reader that returns prNumber.
func newRunnerWith(client ghClient, prNumber int) *DeterministicRunner {
	r := NewDeterministicRunnerWithClient(client)
	r.pollInterval = 0
	r.pollMax = 4
	r.prContextRead = func(_ string, _ int) (int, error) { return prNumber, nil }
	return r
}

// ── Decision matrix (pure function) ───────────────────────────────────────

func TestDecide_AlreadyMerged(t *testing.T) {
	d := Decide(PRViewSnapshot{State: "MERGED"})
	if d.ShouldMerge || d.Punt {
		t.Fatalf("MERGED should be already-merged (no merge, no punt), got %+v", d)
	}
	if d.Reason != ReasonAlreadyMerged {
		t.Errorf("Reason = %q, want %q", d.Reason, ReasonAlreadyMerged)
	}
}

func TestDecide_CleanMergeable(t *testing.T) {
	d := Decide(PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		ReviewDecision: "APPROVED",
	})
	if !d.ShouldMerge {
		t.Errorf("clean+approved should merge, got %+v", d)
	}
}

func TestDecide_NoReviewRequired_Merges(t *testing.T) {
	d := Decide(PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		ReviewDecision: "", // empty = no reviewers required
	})
	if !d.ShouldMerge {
		t.Errorf("clean+no-review-required should merge, got %+v", d)
	}
}

func TestDecide_Conflicting_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"})
	if !d.Punt {
		t.Errorf("CONFLICTING should punt, got %+v", d)
	}
	if !strings.Contains(d.Reason, "not-mergeable") {
		t.Errorf("Reason = %q, want it to mention not-mergeable", d.Reason)
	}
}

func TestDecide_DirtyMergeState_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED"})
	if !d.Punt {
		t.Errorf("BLOCKED merge state should punt, got %+v", d)
	}
}

// TestDecide_Behind_Punts locks in the "never merge while BEHIND" acceptance
// criterion (#4071): a same-wave sibling that went BEHIND after the first PR
// merged must NOT be merged — it is rebased and re-validated instead.
func TestDecide_Behind_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BEHIND"})
	if !d.Punt || d.ShouldMerge {
		t.Errorf("BEHIND must punt (never merge while BEHIND), got %+v", d)
	}
	if !strings.Contains(d.Reason, ReasonDirtyState) {
		t.Errorf("Reason = %q, want it to mention %q", d.Reason, ReasonDirtyState)
	}
}

// TestDecide_DirtyMergeableTrue_Punts covers the DIRTY-with-MERGEABLE edge
// (mergeable flag lags the merge-state status) — still must punt on non-CLEAN.
func TestDecide_DirtyMergeableTrue_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "DIRTY"})
	if !d.Punt || d.ShouldMerge {
		t.Errorf("DIRTY must punt, got %+v", d)
	}
}

func TestDecide_FailedCheck_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		StatusCheckRollup: []PRStatusCheckRow{
			{Name: "build", Conclusion: "SUCCESS"},
			{Name: "test", Conclusion: "FAILURE"},
		},
	})
	if !d.Punt {
		t.Errorf("failed check should punt, got %+v", d)
	}
	if !strings.Contains(d.Reason, "failed-ci-checks") {
		t.Errorf("Reason = %q, want failed-ci-checks", d.Reason)
	}
}

func TestDecide_ReviewRequired_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		ReviewDecision: "REVIEW_REQUIRED",
	})
	if !d.Punt {
		t.Errorf("REVIEW_REQUIRED should punt, got %+v", d)
	}
}

func TestDecide_ChangesRequested_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		ReviewDecision: "CHANGES_REQUESTED",
	})
	if !d.Punt {
		t.Errorf("CHANGES_REQUESTED should punt, got %+v", d)
	}
}

func TestDecide_ClosedNotMerged_Punts(t *testing.T) {
	d := Decide(PRViewSnapshot{State: "CLOSED"})
	if !d.Punt {
		t.Errorf("CLOSED state should punt, got %+v", d)
	}
}

// ── Runner end-to-end ─────────────────────────────────────────────────────

func TestDeterministicRunner_AlreadyMerged(t *testing.T) {
	gh := &fakeGh{preMerge: PRViewSnapshot{State: "MERGED"}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Errorf("Path = %q, want merged", res.Path)
	}
	if res.Reason != ReasonAlreadyMerged {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonAlreadyMerged)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge should not be called when already MERGED, got %d calls", gh.mergeCalls)
	}
}

func TestDeterministicRunner_CleanMergeable_Merges(t *testing.T) {
	gh := &fakeGh{
		preMerge: PRViewSnapshot{
			State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			ReviewDecision: "APPROVED",
		},
		postMerge: PRViewSnapshot{State: "MERGED"},
	}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Errorf("Path = %q, want merged", res.Path)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("Merge call count = %d, want exactly 1", gh.mergeCalls)
	}
	if res.PRState != "MERGED" {
		t.Errorf("PRState = %q, want MERGED", res.PRState)
	}
}

func TestDeterministicRunner_RealConflict_Punts(t *testing.T) {
	gh := &fakeGh{preMerge: PRViewSnapshot{
		State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY",
	}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if !strings.Contains(res.Reason, "not-mergeable") {
		t.Errorf("Reason = %q, want it to mention conflict/not-mergeable", res.Reason)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge should not be called on conflict, got %d", gh.mergeCalls)
	}
}

func TestDeterministicRunner_FailedCI_Punts(t *testing.T) {
	gh := &fakeGh{preMerge: PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		StatusCheckRollup: []PRStatusCheckRow{
			{Name: "ci", Conclusion: "FAILURE"},
		},
	}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if !strings.Contains(res.Reason, "failed-ci-checks") {
		t.Errorf("Reason = %q, want failed-ci-checks", res.Reason)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge should not be called on failed CI, got %d", gh.mergeCalls)
	}
}

func TestDeterministicRunner_RateLimited_Punts(t *testing.T) {
	rateErr := errors.New("HTTP 429: API rate limit exceeded")
	gh := &fakeGh{preMergeErr: rateErr}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if res.Reason != ReasonRateLimited {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonRateLimited)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge should not run when pre-flight is rate-limited, got %d", gh.mergeCalls)
	}
}

func TestDeterministicRunner_EventualConsistency(t *testing.T) {
	// Simulate state changing across polls: first two pre-flight polls return
	// OPEN, third returns MERGED. Use sequenceGh because state transitions
	// independent of any merge call.
	gh := &sequenceGh{responses: []sequenceResp{
		{snap: PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}},
		{snap: PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}},
		{snap: PRViewSnapshot{State: "MERGED"}},
	}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Reaches MERGED on the 3rd pre-flight poll → no merge call needed.
	if res.Path != PathMerged {
		t.Errorf("Path = %q, want merged", res.Path)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge should not be called when EC poll observes MERGED, got %d", gh.mergeCalls)
	}
	if gh.viewCalls < 3 {
		t.Errorf("expected ≥3 view calls (EC polling), got %d", gh.viewCalls)
	}
}

func TestDeterministicRunner_PRContextMissing_Punts(t *testing.T) {
	gh := &fakeGh{preMerge: PRViewSnapshot{State: "MERGED"}}
	r := NewDeterministicRunnerWithClient(gh)
	r.pollInterval = 0
	r.prContextRead = func(_ string, _ int) (int, error) {
		return 0, errors.New("read pr context: open ...: no such file or directory")
	}

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want punt when pr context is missing", res.Path)
	}
	if res.Reason != ReasonNoPRContext {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonNoPRContext)
	}
}

func TestDeterministicRunner_MergeCallFails_Punts(t *testing.T) {
	gh := &fakeGh{
		preMerge: PRViewSnapshot{
			State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			ReviewDecision: "APPROVED",
		},
		mergeErr: errors.New("required status checks have not passed"),
	}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want punt when merge call fails", res.Path)
	}
	if !strings.Contains(res.Reason, ReasonMergeFailed) {
		t.Errorf("Reason = %q, want it to mention %q", res.Reason, ReasonMergeFailed)
	}
}

func TestDeterministicRunner_RecordsDuration(t *testing.T) {
	gh := &fakeGh{preMerge: PRViewSnapshot{State: "MERGED"}}
	r := newRunnerWith(gh, 42)
	calls := 0
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time {
		calls++
		// First call = start; subsequent = +50ms each.
		return base.Add(time.Duration(calls-1) * 50 * time.Millisecond)
	}

	res, _ := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if res.DurationMs <= 0 {
		t.Errorf("DurationMs = %d, want > 0", res.DurationMs)
	}
}

func TestDeterministicRunner_ECTimeoutReturnsPunt(t *testing.T) {
	// Simulate: merge call succeeds, but post-merge EC polls never observe MERGED.
	// The runner must return PathPunt with ReasonMergeECTimeout — NOT PathMerged.
	gh := &fakeGh{
		preMerge:  PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
		postMerge: PRViewSnapshot{State: "OPEN"}, // EC window exhausted without MERGED
	}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want PathPunt when EC budget exhausted without MERGED", res.Path)
	}
	if res.Reason != ReasonMergeECTimeout {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonMergeECTimeout)
	}
	if res.PRState == "MERGED" {
		t.Error("PRState must not be MERGED when EC timed out without observing MERGED")
	}
}

func TestDeterministicRunner_PostVerifyFailureReturnsPunt(t *testing.T) {
	// #4070: merge call succeeds but the post-merge re-fetch errors (transient
	// API failure / eventual-consistency). The runner must NOT self-report
	// PathMerged — it cannot OBSERVE MERGED, so it punts to the canonical
	// scheduler gate (verifyPRMerged) which is the sole MERGED authority.
	// Phantom-success guard: self-reporting merged here would have closed the
	// issue on an unconfirmed merge.
	gh := &fakeGh{
		preMerge:     PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"},
		postMergeErr: errors.New("API temporarily unavailable"),
	}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want PathPunt when post-merge verification fails", res.Path)
	}
	if res.PRState == "MERGED" {
		t.Error("PRState must not be MERGED when post-merge verification failed")
	}
	if !strings.Contains(res.Reason, ReasonMergeECTimeout) {
		t.Errorf("Reason = %q, want it to mention %q", res.Reason, ReasonMergeECTimeout)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("Merge should have been called once, got %d", gh.mergeCalls)
	}
}

// ── Bounded CI wait (Issue #297) ──────────────────────────────────────────
//
// pr-merge starts immediately after pr-create, so on repos whose CI takes
// minutes the first snapshot is BLOCKED/UNSTABLE with pending checks. Pre-#297
// the deterministic runner punted `dirty-merge-state: BLOCKED` on EVERY run and
// the LLM skill "won" pr-merge only by babysitting CI at ~$3–4.44/run. The
// runner now polls until the merge state clears (or CI fails / the budget
// expires) so the deterministic path can actually merge.

func TestMergeBlockedByPendingCI(t *testing.T) {
	pending := PRStatusCheckRow{Name: "ci", Conclusion: ""}
	cases := []struct {
		name string
		snap PRViewSnapshot
		want bool
	}{
		{"blocked+pending-check → wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", StatusCheckRollup: []PRStatusCheckRow{pending}}, true},
		{"unstable+pending-check → wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "UNSTABLE", StatusCheckRollup: []PRStatusCheckRow{pending}}, true},
		{"clean → no wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", StatusCheckRollup: []PRStatusCheckRow{pending}}, false},
		{"conflict → no wait", PRViewSnapshot{State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", StatusCheckRollup: []PRStatusCheckRow{pending}}, false},
		{"failed-check → no wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: "FAILURE"}}}, false},
		{"review-required → no wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "REVIEW_REQUIRED", StatusCheckRollup: []PRStatusCheckRow{pending}}, false},
		{"blocked+all-checks-concluded → no wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: "SUCCESS"}}}, false},
		{"behind → no wait", PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BEHIND", StatusCheckRollup: []PRStatusCheckRow{pending}}, false},
		{"not-open → no wait", PRViewSnapshot{State: "MERGED", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", StatusCheckRollup: []PRStatusCheckRow{pending}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeBlockedByPendingCI(tc.snap); got != tc.want {
				t.Errorf("mergeBlockedByPendingCI() = %v, want %v", got, tc.want)
			}
		})
	}
}

// newRunnerWithSeq builds a runner over a sequenceGh with no polling delay,
// a single-shot initial/EC fetch budget (so each View drains exactly one
// scripted snapshot), and a bounded CI-wait budget of ciPollMax.
func newRunnerWithSeq(seq *sequenceGh, prNumber, ciPollMax int) *DeterministicRunner {
	r := NewDeterministicRunnerWithClient(seq)
	r.pollInterval = 0
	r.pollMax = 1
	r.ciPollInterval = 0
	r.ciPollMax = ciPollMax
	r.prContextRead = func(_ string, _ int) (int, error) { return prNumber, nil }
	return r
}

func TestDeterministicRunner_CIPending_WaitsThenMerges(t *testing.T) {
	pending := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: ""}}}
	clean := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: "SUCCESS"}}}
	merged := PRViewSnapshot{State: "MERGED"}
	seq := &sequenceGh{responses: []sequenceResp{
		{snap: pending}, // initial fetch → still building CI
		{snap: pending}, // wait poll 1 → still pending
		{snap: clean},   // wait poll 2 → CI green, merge state CLEAN
		{snap: merged},  // post-merge EC re-poll → MERGED
	}}
	r := newRunnerWithSeq(seq, 42, 5)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("Path = %q (reason %q), want merged — deterministic runner must WAIT for pending CI, not punt (#297)", res.Path, res.Reason)
	}
	if seq.mergeCalls != 1 {
		t.Errorf("Merge call count = %d, want exactly 1", seq.mergeCalls)
	}
}

func TestDeterministicRunner_CIPending_TimesOut_Punts(t *testing.T) {
	pending := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: ""}}}
	seq := &sequenceGh{responses: []sequenceResp{{snap: pending}}} // never clears
	r := newRunnerWithSeq(seq, 42, 3)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Fatalf("Path = %q, want punt on CI-wait timeout", res.Path)
	}
	if res.Reason != ReasonCIWaitTimeout {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonCIWaitTimeout)
	}
	if seq.mergeCalls != 0 {
		t.Errorf("Merge must not be called after a CI-wait timeout, got %d", seq.mergeCalls)
	}
}

func TestDeterministicRunner_CIPending_ThenFails_Punts(t *testing.T) {
	pending := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: ""}}}
	failed := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "UNSTABLE", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: "FAILURE"}}}
	seq := &sequenceGh{responses: []sequenceResp{
		{snap: pending}, // initial fetch
		{snap: failed},  // wait poll 1 → CI failed
	}}
	r := newRunnerWithSeq(seq, 42, 5)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Fatalf("Path = %q, want punt when CI fails during the wait", res.Path)
	}
	// The wait must stop the moment a check reports FAILURE (not spin out the
	// full budget), and must never merge a PR with failed CI. The exact reason
	// is Decide()'s merge-state verdict (`dirty-merge-state: UNSTABLE`/`BLOCKED`
	// or `failed-ci-checks` depending on rollup) — the load-bearing invariant is
	// that it is a punt, not a timeout, and not a merge.
	if res.Reason == ReasonCIWaitTimeout {
		t.Errorf("Reason = %q, want an early failure punt, not a CI-wait timeout", res.Reason)
	}
	if seq.mergeCalls != 0 {
		t.Errorf("Merge must not be called when CI failed, got %d", seq.mergeCalls)
	}
}
