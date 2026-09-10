package main

import (
	"context"
	"errors"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// fakeChecksCompleteReader answers pollChecksComplete from a script, the same
// shape as internal/hooks's scriptedChecks: the last frame repeats once the
// script is exhausted.
type fakeChecksCompleteReader struct {
	checkFrames  [][]gh.CheckDetail
	statusFrames [][]gh.CheckDetail
	runFrames    [][]gh.WorkflowRunSummary
	errAt        map[int]error
	polls        int
}

func (f *fakeChecksCompleteReader) GetCommitStatuses(_ context.Context, _, _, _ string) ([]gh.CheckDetail, error) {
	if len(f.statusFrames) == 0 {
		return nil, nil
	}
	i := f.polls - 1
	if i < 0 {
		i = 0
	}
	if i >= len(f.statusFrames) {
		i = len(f.statusFrames) - 1
	}
	return f.statusFrames[i], nil
}

func (f *fakeChecksCompleteReader) GetIndividualCheckRuns(_ context.Context, _, _, _ string) ([]gh.CheckDetail, error) {
	i := f.polls
	f.polls++
	if err, ok := f.errAt[i]; ok {
		return nil, err
	}
	if len(f.checkFrames) == 0 {
		return nil, nil
	}
	if i >= len(f.checkFrames) {
		i = len(f.checkFrames) - 1
	}
	return f.checkFrames[i], nil
}

func (f *fakeChecksCompleteReader) GetWorkflowRunsForRef(_ context.Context, _, _, _ string) ([]gh.WorkflowRunSummary, error) {
	if len(f.runFrames) == 0 {
		return nil, nil
	}
	i := f.polls - 1
	if i < 0 {
		i = 0
	}
	if i >= len(f.runFrames) {
		i = len(f.runFrames) - 1
	}
	return f.runFrames[i], nil
}

func noSleepCmd(context.Context, time.Duration) error { return nil }

func detail(name, status, conclusion string) gh.CheckDetail {
	return gh.CheckDetail{Name: name, Status: status, Conclusion: conclusion}
}

// TestPollChecksComplete_MissingRequiredCheckIsNotYet is the AC-1 test at the
// CLI-verb level: a rollup missing a required check must exit NOT-YET, not
// GREEN.
func TestPollChecksComplete_MissingRequiredCheckIsNotYet(t *testing.T) {
	reader := &fakeChecksCompleteReader{checkFrames: [][]gh.CheckDetail{{
		detail("build", "COMPLETED", "SUCCESS"),
	}}}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"build", "lint"}, 3, time.Millisecond, true, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksNotYet {
		t.Fatalf("Verdict = %q, want %q — lint is required and absent from every rollup", res.Verdict, gh.ChecksNotYet)
	}
	if res.Polls != 3 {
		t.Errorf("Polls = %d, want the whole budget (3) — it never observed the missing check", res.Polls)
	}
}

func TestPollChecksComplete_AllRequiredPresentIsGreenAfterConfirmation(t *testing.T) {
	reader := &fakeChecksCompleteReader{checkFrames: [][]gh.CheckDetail{{
		detail("build", "COMPLETED", "SUCCESS"),
	}}}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"build"}, 5, time.Millisecond, true, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksComplete {
		t.Fatalf("Verdict = %q, want %q", res.Verdict, gh.ChecksComplete)
	}
	// The first read is green but unconfirmed; a second, agreeing read
	// confirms it (#1540 §3).
	if res.Polls != 2 {
		t.Errorf("Polls = %d, want 2 — a terminal verdict needs a confirming second read", res.Polls)
	}
}

func TestPollChecksComplete_RequiredCommitStatusSatisfiesPresence(t *testing.T) {
	reader := &fakeChecksCompleteReader{
		checkFrames:  [][]gh.CheckDetail{{detail("build", "COMPLETED", "SUCCESS")}},
		statusFrames: [][]gh.CheckDetail{{detail("cla", "COMPLETED", "SUCCESS")}},
	}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"build", "cla"}, 2, time.Millisecond, true, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksComplete {
		t.Fatalf("Verdict = %q, want %q", res.Verdict, gh.ChecksComplete)
	}
}

func TestPollChecksComplete_SingleReadBudgetReturnsUnconfirmed(t *testing.T) {
	reader := &fakeChecksCompleteReader{checkFrames: [][]gh.CheckDetail{{
		detail("build", "COMPLETED", "SUCCESS"),
	}}}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"build"}, 1, time.Millisecond, true, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksComplete || res.Polls != 1 {
		t.Fatalf("Verdict=%q Polls=%d, want green after exactly 1 read (a single-read budget cannot confirm)", res.Verdict, res.Polls)
	}
}

// TestPollChecksComplete_CrossCheckDisagreementIsNotYet is the AC-2 test at
// the CLI-verb level: a rollup that disagrees with the per-run endpoint
// yields NOT-YET.
func TestPollChecksComplete_CrossCheckDisagreementIsNotYet(t *testing.T) {
	reader := &fakeChecksCompleteReader{
		checkFrames: [][]gh.CheckDetail{{detail("lint", "COMPLETED", "SUCCESS")}},
		runFrames:   [][]gh.WorkflowRunSummary{{{ID: 1, Name: "lint", Status: "IN_PROGRESS"}}},
	}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"lint"}, 2, time.Millisecond, false, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksNotYet {
		t.Fatalf("Verdict = %q, want %q — the rollup and actions/runs disagree", res.Verdict, gh.ChecksNotYet)
	}
}

func TestPollChecksComplete_CrossCheckSkippedWhenDisabled(t *testing.T) {
	reader := &fakeChecksCompleteReader{
		checkFrames: [][]gh.CheckDetail{{detail("lint", "COMPLETED", "SUCCESS")}},
		runFrames:   [][]gh.WorkflowRunSummary{{{ID: 1, Name: "lint", Status: "IN_PROGRESS"}}},
	}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"lint"}, 2, time.Millisecond, true, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksComplete {
		t.Fatalf("Verdict = %q, want %q — --no-cross-check must skip the actions/runs read entirely", res.Verdict, gh.ChecksComplete)
	}
	if res.CrossChecked {
		t.Error("CrossChecked = true, want false when the cross-check was skipped")
	}
}

func TestPollChecksComplete_RequiredCheckFailedIsRed(t *testing.T) {
	reader := &fakeChecksCompleteReader{checkFrames: [][]gh.CheckDetail{{
		detail("build", "COMPLETED", "FAILURE"),
	}}}

	res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		[]string{"build"}, 5, time.Millisecond, true, noSleepCmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != gh.ChecksIncomplete {
		t.Fatalf("Verdict = %q, want %q", res.Verdict, gh.ChecksIncomplete)
	}
}

func TestPollChecksComplete_ReadErrorIsReturned(t *testing.T) {
	reader := &fakeChecksCompleteReader{
		checkFrames: [][]gh.CheckDetail{{detail("build", "IN_PROGRESS", "")}},
		errAt:       map[int]error{0: errors.New("502")},
	}

	_, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
		nil, 3, time.Millisecond, true, noSleepCmd, nil)
	if err == nil {
		t.Fatal("want an error from the failed read")
	}
}
