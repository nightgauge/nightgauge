package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

// #3081: issue.rerunPostMerge resolves its card only when the hook left the
// validation marker; every other outcome keeps the card open, saying why.

type fakeClosingMerges struct {
	merge *gh.ClosingMerge
	err   error
	asked string
}

func (f *fakeClosingMerges) LatestClosingMerge(_ context.Context, owner, repo string, number int) (*gh.ClosingMerge, error) {
	f.asked = fmt.Sprintf("%s/%s#%d", owner, repo, number)
	return f.merge, f.err
}

func TestPostMergeRerunVerdict(t *testing.T) {
	cases := []struct {
		marker  hooks.ValidationMarkerOutcome
		wantErr bool
	}{
		{hooks.ValidationMarkerWritten, false},
		{hooks.ValidationMarkerPresent, false},
		{"", true},
		{hooks.ValidationMarkerNotGreen, true},
		{hooks.ValidationMarkerNotClosing, true},
		{hooks.ValidationMarkerNotWired, true},
		{hooks.ValidationMarkerError, true},
	}
	for _, tc := range cases {
		err := postMergeRerunVerdict("o/app", 21, 7, hooks.PostMergeResult{ValidationMarker: tc.marker})
		if (err != nil) != tc.wantErr {
			t.Errorf("marker %q: err = %v, want error %v", tc.marker, err, tc.wantErr)
		}
		if err != nil && tc.marker != "" && !strings.Contains(err.Error(), string(tc.marker)) {
			t.Errorf("marker %q: error does not say which outcome: %v", tc.marker, err)
		}
	}
}

func TestRerunPostMerge(t *testing.T) {
	marker := hooks.ValidationMarkerWritten
	var gotPR int
	newSched := func(reader ClosingMergeReader) *Scheduler {
		return &Scheduler{closingMergeReader: reader,
			evaluatePostMergeFn: func(_ context.Context, _ hooks.IssueFetcher, _ hooks.IssueCloser,
				_ hooks.EpicAutoCloser, _ hooks.PRVerifier, _ hooks.BoardSyncer, in hooks.PostMergeInput) hooks.PostMergeResult {
				gotPR = in.PRNumber
				if in.IssueNumber != 21 || in.RepositoryOwner != "o" || in.RepositoryName != "app" {
					t.Errorf("post-merge ran for %s/%s#%d, want o/app#21", in.RepositoryOwner, in.RepositoryName, in.IssueNumber)
				}
				return hooks.PostMergeResult{ValidationMarker: marker}
			}}
	}

	// The latest merged closing PR is the one the hook runs against.
	reader := &fakeClosingMerges{merge: &gh.ClosingMerge{Number: 7, SHA: "abc"}}
	if err := rerunBoth(newSched(reader)); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if gotPR != 7 || reader.asked != "o/app#21" {
		t.Fatalf("hook ran against PR #%d after asking for %q; want PR #7 for o/app#21", gotPR, reader.asked)
	}

	// Main still red: no marker, the verb fails and the card stays.
	marker = hooks.ValidationMarkerNotGreen
	if err := rerunBoth(newSched(reader)); err == nil {
		t.Fatal("a re-run that left no marker reported success")
	}

	gotPR = 0
	for name, r := range map[string]*fakeClosingMerges{
		"read error":       {err: errors.New("forge down")},
		"no merge":         {},
		"merge without PR": {merge: &gh.ClosingMerge{}},
	} {
		if err := rerunBoth(newSched(r)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if gotPR != 0 {
		t.Fatalf("the hook ran without a merged closing PR (PR #%d)", gotPR)
	}

	if err := rerunBoth((&Scheduler{})); err == nil {
		t.Fatal("no forge client and no reader: want an error")
	}
}

// #3352: over the real issue service, a merged PR in another repository that
// closes the issue is not a merge to validate, so the hook never runs.
func TestRerunPostMergeIgnoresCrossRepoClosingMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"closedByPullRequestsReferences":{"nodes":[{"number":7,"repository":{"nameWithOwner":"other/app"},"merged":true,"mergedAt":"2026-11-02T12:00:00Z","mergeCommit":{"oid":"f0re1gn0"}}]}}}}}`))
	}))
	defer srv.Close()
	ran := false
	s := &Scheduler{client: gh.NewClientWithURL("t", srv.URL),
		evaluatePostMergeFn: func(_ context.Context, _ hooks.IssueFetcher, _ hooks.IssueCloser,
			_ hooks.EpicAutoCloser, _ hooks.PRVerifier, _ hooks.BoardSyncer, _ hooks.PostMergeInput) hooks.PostMergeResult {
			ran = true
			return hooks.PostMergeResult{ValidationMarker: hooks.ValidationMarkerWritten}
		}}
	err := rerunBoth(s)
	if err == nil || !strings.Contains(err.Error(), "no merged pull request closes it") {
		t.Fatalf("err = %v, want no merged pull request", err)
	}
	if ran {
		t.Fatal("the post-merge hook ran against another repository's PR")
	}
}

// #3353: the verdict names the main-check outcome, not just "no marker".
func TestPostMergeRerunVerdictNamesMainChecks(t *testing.T) {
	cases := []struct {
		mc   hooks.MainCheckResult
		want string
	}{
		{hooks.MainCheckResult{Verdict: hooks.MainChecksError, MergeCommitSha: "abc", Error: "deadline exceeded"},
			"main checks could not be read at abc: deadline exceeded"},
		{hooks.MainCheckResult{Verdict: hooks.MainChecksRed, MergeCommitSha: "abc"}, "main is red at abc"},
		{hooks.MainCheckResult{Verdict: hooks.MainChecksPending, MergeCommitSha: "abc"}, "main is pending at abc"},
	}
	for _, tc := range cases {
		mc := tc.mc
		err := postMergeRerunVerdict("o/app", 21, 7, hooks.PostMergeResult{
			ValidationMarker: hooks.ValidationMarkerNotGreen, MainChecks: &mc})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("verdict %s: err = %v, want %q", mc.Verdict, err, tc.want)
		}
	}
}

func TestResolveRerunTargetReturnsThePR(t *testing.T) {
	s := &Scheduler{closingMergeReader: &fakeClosingMerges{merge: &gh.ClosingMerge{Number: 7}}}
	pr, err := s.ResolveRerunTarget(context.Background(), "o/app", 21)
	if err != nil || pr != 7 {
		t.Fatalf("ResolveRerunTarget = %d, %v; want 7, nil", pr, err)
	}
	if _, err := (&Scheduler{closingMergeReader: &fakeClosingMerges{}}).ResolveRerunTarget(
		context.Background(), "o/app", 21); err == nil || !strings.Contains(err.Error(), "no merged pull request") {
		t.Fatalf("no closing merge: err = %v", err)
	}
}

// rerunBoth runs both halves in order, as the daemon does across its
// synchronous resolve and its background run.
func rerunBoth(s *Scheduler) error {
	pr, err := s.ResolveRerunTarget(context.Background(), "o/app", 21)
	if err != nil {
		return err
	}
	return s.RunPostMergeRerun(context.Background(), "o/app", 21, pr)
}
