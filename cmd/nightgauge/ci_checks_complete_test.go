package main

import (
	"context"
	"errors"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
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

// GetCommitChecks mirrors *github.CIService.GetCommitChecks: check runs
// followed by commit statuses, one frame of each per poll.
func (f *fakeChecksCompleteReader) GetCommitChecks(_ context.Context, _, _, _ string) ([]gh.CheckDetail, error) {
	i := f.polls
	f.polls++
	if err, ok := f.errAt[i]; ok {
		return nil, err
	}
	var out []gh.CheckDetail
	if len(f.checkFrames) > 0 {
		out = append(out, f.checkFrames[min(i, len(f.checkFrames)-1)]...)
	}
	if len(f.statusFrames) > 0 {
		out = append(out, f.statusFrames[min(i, len(f.statusFrames)-1)]...)
	}
	return out, nil
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

// TestPollChecksComplete_EveryContextCounts pins the post-merge contract that
// scripts/post-merge-check.sh delegates here for: a failed OPTIONAL check is
// RED, a running optional check is NOT-YET, and required-ness never hides
// either. Before this, a required-only evaluation reported GREEN for both.
func TestPollChecksComplete_EveryContextCounts(t *testing.T) {
	cases := []struct {
		name   string
		checks []gh.CheckDetail
		want   gh.ChecksCompleteVerdict
	}{
		{"optional failed is red", []gh.CheckDetail{
			detail("build", "COMPLETED", "SUCCESS"), detail("advisory", "COMPLETED", "FAILURE")}, gh.ChecksIncomplete},
		{"optional in progress is not-yet", []gh.CheckDetail{
			detail("build", "COMPLETED", "SUCCESS"), detail("nightly", "IN_PROGRESS", "")}, gh.ChecksNotYet},
		{"all green is green", []gh.CheckDetail{
			detail("build", "COMPLETED", "SUCCESS"), detail("advisory", "COMPLETED", "NEUTRAL")}, gh.ChecksComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeChecksCompleteReader{
				checkFrames:  [][]gh.CheckDetail{tc.checks},
				statusFrames: [][]gh.CheckDetail{{detail("cla", "COMPLETED", "SUCCESS")}},
			}
			res, err := pollChecksComplete(context.Background(), reader, "o", "r", "abc123", "main",
				[]string{"build", "cla"}, 3, time.Millisecond, true, noSleepCmd, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Verdict != tc.want {
				t.Fatalf("Verdict = %q (%v), want %q", res.Verdict, res.Reasons, tc.want)
			}
		})
	}
}

// hookReader adapts the fake to hooks.MainCheckReader, so the hook and the
// verb read the very same frames.
type hookReader struct {
	*fakeChecksCompleteReader
	required []string
}

func (h hookReader) GetRequiredCheckNames(context.Context, string, string, string) ([]string, error) {
	return h.required, nil
}

// TestChecksCompleteAndHookAgree is #1674's third acceptance criterion: the
// hook and `ci checks-complete` return the same verdict for the same commit,
// including a required context that exists only as a commit status.
func TestChecksCompleteAndHookAgree(t *testing.T) {
	required := []string{"build", "cla"}
	cases := []struct {
		name     string
		checks   []gh.CheckDetail
		statuses []gh.CheckDetail
		verb     gh.ChecksCompleteVerdict
		hook     hooks.MainCheckVerdict
	}{
		{"required status-only context", []gh.CheckDetail{detail("build", "COMPLETED", "SUCCESS")},
			[]gh.CheckDetail{detail("cla", "COMPLETED", "SUCCESS")}, gh.ChecksComplete, hooks.MainChecksGreen},
		{"failed optional check", []gh.CheckDetail{detail("build", "COMPLETED", "SUCCESS"), detail("advisory", "COMPLETED", "FAILURE")},
			[]gh.CheckDetail{detail("cla", "COMPLETED", "SUCCESS")}, gh.ChecksIncomplete, hooks.MainChecksRed},
		{"running optional check", []gh.CheckDetail{detail("build", "COMPLETED", "SUCCESS"), detail("nightly", "IN_PROGRESS", "")},
			[]gh.CheckDetail{detail("cla", "COMPLETED", "SUCCESS")}, gh.ChecksNotYet, hooks.MainChecksPending},
		{"absent required status", []gh.CheckDetail{detail("build", "COMPLETED", "SUCCESS")},
			nil, gh.ChecksNotYet, hooks.MainChecksPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := func() *fakeChecksCompleteReader {
				r := &fakeChecksCompleteReader{checkFrames: [][]gh.CheckDetail{tc.checks}}
				if tc.statuses != nil {
					r.statusFrames = [][]gh.CheckDetail{tc.statuses}
				}
				return r
			}
			res, err := pollChecksComplete(context.Background(), frames(), "o", "r", "abc123", "main",
				required, 1, time.Millisecond, true, noSleepCmd, nil)
			if err != nil {
				t.Fatalf("pollChecksComplete: %v", err)
			}
			hookRes := hooks.VerifyMergeCommit(context.Background(), hookReader{frames(), required},
				"o", "r", "main", "abc123", hooks.MainCheckWait{Progress: func(string) {}})
			if res.Verdict != tc.verb || hookRes.Verdict != tc.hook {
				t.Fatalf("verb = %q, hook = %q; want %q and %q", res.Verdict, hookRes.Verdict, tc.verb, tc.hook)
			}
		})
	}
}
