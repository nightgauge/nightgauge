package hooks

import (
	"context"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// #1181 — the post-merge hook used to hand AutoCloseSingle the MERGED ISSUE's
// owner/repo along with the PARENT EPIC's number. In a multi-repo workspace the
// epic lives elsewhere, and issue numbers are per-repository, so the epic number
// resolved against the wrong repo: onto a merged pull request (loud) or onto an
// unrelated real issue with no sub-issues, which reported failed:false while the
// real epic was never evaluated (silent).
//
// These tests assert the COORDINATE the hook hands over, which is the thing that
// was wrong. The two faces themselves are exercised end-to-end against a fake
// forge in internal/github/epic_cross_repo_test.go.

func crossRepoSubIssue() *mockFetcher {
	return &mockFetcher{issues: map[string]*types.Issue{
		"acme/bowlsheet-flutter#3001": {
			NodeID:            "I_sub3001",
			Number:            3001,
			ParentIssueNumber: 205,
			ParentIssueRepo:   "acme/bowlsheet-infra",
		},
	}}
}

// TestPostMergeResolvesEpicInTheEpicsOwnRepo is the acceptance criterion: a
// cross-repo sub-issue merge evaluates the epic in the epic's own repo.
func TestPostMergeResolvesEpicInTheEpicsOwnRepo(t *testing.T) {
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 205, Status: "closed", Reason: "all_closed"},
	}

	result := EvaluatePostMerge(context.Background(), crossRepoSubIssue(), &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     3001,
		RepositoryOwner: "acme",
		RepositoryName:  "bowlsheet-flutter",
	})

	if !closer.called {
		t.Fatal("expected AutoCloseSingle to be called")
	}
	ref := closer.calledWith.ref
	if ref.Owner != "acme" || ref.Repo != "bowlsheet-infra" {
		t.Errorf("epic resolved against %s/%s, want acme/bowlsheet-infra — resolving #205 in the sub-issue's repo is #1181",
			ref.Owner, ref.Repo)
	}
	if ref.Number != 205 {
		t.Errorf("epic number = %d, want 205", ref.Number)
	}
	if result.EpicRepo != "acme/bowlsheet-infra" {
		t.Errorf("result.EpicRepo = %q, want acme/bowlsheet-infra", result.EpicRepo)
	}
	if !result.AutoClosed {
		t.Error("expected AutoClosed=true for a cross-repo epic whose subs are all closed")
	}
}

// TestPostMergePassesTheMergedIssueAsTheExpectedSub pins the guard that makes
// the silent face impossible. The hook knows, by construction, that the merged
// issue is a sub-issue of the intended epic — so it must say so, and let the
// epic prove it. Without this, a coordinate that lands on an unrelated
// sub-issue-less issue still answers "no_subs" and the hook still reports
// success.
func TestPostMergePassesTheMergedIssueAsTheExpectedSub(t *testing.T) {
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 205, Status: "skipped", Reason: "has_open"},
	}

	EvaluatePostMerge(context.Background(), crossRepoSubIssue(), &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     3001,
		RepositoryOwner: "acme",
		RepositoryName:  "bowlsheet-flutter",
	})

	ref := closer.calledWith.ref
	if ref.ExpectSubIssueNumber != 3001 {
		t.Errorf("ExpectSubIssueNumber = %d, want 3001 — without it a mis-resolved epic answers no_subs and the hook calls that success",
			ref.ExpectSubIssueNumber)
	}
	if ref.ExpectSubIssueRepo != "acme/bowlsheet-flutter" {
		t.Errorf("ExpectSubIssueRepo = %q, want acme/bowlsheet-flutter — a number alone is not an identity across repos",
			ref.ExpectSubIssueRepo)
	}
}

// TestPostMergeWrongEpicIsAFailureNotASilentSkip: the epic service reports
// "wrong_epic"; the hook must surface that as a failure. Face 2 was invisible
// precisely because the hook's own output said failed:false.
func TestPostMergeWrongEpicIsAFailureNotASilentSkip(t *testing.T) {
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{
			EpicNumber: 205,
			Status:     "error",
			Reason:     "wrong_epic",
			Error:      "epic acme/bowlsheet-flutter#205 does not list acme/bowlsheet-flutter#3001 among its 0 sub-issue(s)",
		},
	}

	result := EvaluatePostMerge(context.Background(), crossRepoSubIssue(), &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     3001,
		RepositoryOwner: "acme",
		RepositoryName:  "bowlsheet-flutter",
	})

	if !result.Failed {
		t.Error("Failed must be true for a wrong_epic result — AGENTS.md tells operators to read this field")
	}
	if result.Reason != "auto_close_error" {
		t.Errorf("Reason = %q, want auto_close_error", result.Reason)
	}
	if result.EpicReason != "wrong_epic" {
		t.Errorf("EpicReason = %q, want wrong_epic", result.EpicReason)
	}
	if result.AutoClosed {
		t.Error("AutoClosed must be false when the epic coordinate was wrong")
	}
}

// TestPostMergeSameRepoEpicUnchanged: this fix is about cross-repo. An epic in
// the merged issue's own repository must resolve exactly where it always did.
func TestPostMergeSameRepoEpicUnchanged(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#100": {
			NodeID:            "I_node100",
			Number:            100,
			ParentIssueNumber: 99,
			ParentIssueRepo:   "nightgauge/nightgauge",
		},
	}}
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 99, Status: "closed", Reason: "all_closed"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     100,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   5,
	})

	ref := closer.calledWith.ref
	if ref.Owner != "nightgauge" || ref.Repo != "nightgauge" || ref.Number != 99 {
		t.Errorf("same-repo epic resolved to %s/%s#%d, want nightgauge/nightgauge#99", ref.Owner, ref.Repo, ref.Number)
	}
	if closer.calledWith.projectNumber != 5 {
		t.Errorf("projectNumber = %d, want 5", closer.calledWith.projectNumber)
	}
	if !result.AutoClosed {
		t.Error("expected AutoClosed=true")
	}
}

// TestPostMergeFallsBackToTheMergedIssueRepoWhenTheParentLinkHasNoRepo covers a
// parent link that reports no repository. Falling back to the merged issue's
// repo is the old behaviour and is right for the same-repo case; the identity
// guard is what stops it from being silently wrong in the cross-repo case, so
// the expected-sub fields must still be set.
func TestPostMergeFallsBackToTheMergedIssueRepoWhenTheParentLinkHasNoRepo(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"acme/bowlsheet-flutter#3001": {
			NodeID:            "I_sub3001",
			Number:            3001,
			ParentIssueNumber: 205,
			ParentIssueRepo:   "", // no repository reported
		},
	}}
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 205, Status: "skipped", Reason: "has_open"},
	}

	EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     3001,
		RepositoryOwner: "acme",
		RepositoryName:  "bowlsheet-flutter",
	})

	ref := closer.calledWith.ref
	if ref.Owner != "acme" || ref.Repo != "bowlsheet-flutter" {
		t.Errorf("fallback resolved to %s/%s, want the merged issue's repo", ref.Owner, ref.Repo)
	}
	if ref.ExpectSubIssueNumber == 0 {
		t.Error("the fallback MUST still be guarded: without an expected sub-issue it can be silently wrong")
	}
}

// TestPostMergeMalformedParentRepoDoesNotProduceAHalfCoordinate: garbage in the
// parent-repo field must not yield an owner with an empty repo (which would
// query "acme/" and fail confusingly). It falls back whole.
func TestPostMergeMalformedParentRepoDoesNotProduceAHalfCoordinate(t *testing.T) {
	for _, bad := range []string{"bowlsheet-infra", "acme/", "/infra", "a/b/c", ""} {
		fetcher := &mockFetcher{issues: map[string]*types.Issue{
			"acme/flutter#3001": {
				NodeID:            "I_sub3001",
				Number:            3001,
				ParentIssueNumber: 205,
				ParentIssueRepo:   bad,
			},
		}}
		closer := &mockEpicAutoCloser{
			result: &gh.AutoCloseSingleResult{EpicNumber: 205, Status: "skipped", Reason: "has_open"},
		}
		EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
			IssueNumber:     3001,
			RepositoryOwner: "acme",
			RepositoryName:  "flutter",
		})
		ref := closer.calledWith.ref
		if ref.Owner != "acme" || ref.Repo != "flutter" {
			t.Errorf("ParentIssueRepo=%q resolved to %s/%s, want the caller's repo as a whole fallback",
				bad, ref.Owner, ref.Repo)
		}
	}
}
