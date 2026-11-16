package orchestrator

import (
	"context"
	"fmt"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// PostMergeRerunBudget bounds one background re-run of the post-merge hook
// (#3353). The hook closes the issue, syncs the board and checks the epic
// before it polls main for up to hooks.DefaultMainCheckTimeout, so the budget
// is that poll plus headroom for the forge writes in front of it. It is far
// longer than any attention deadline, which is why the re-run never runs
// inside the attention store's lock.
const PostMergeRerunBudget = hooks.DefaultMainCheckTimeout + 5*time.Minute

// ResolveRerunTarget is the fast half of attention.VerbIssueRerunPostMerge
// (#3081, #3353), run inside the attention store's lock: one forge read for the
// issue's latest merged closing pull request. It errors when none exists (a
// manual close has no merge to validate) or the read fails.
func (s *Scheduler) ResolveRerunTarget(ctx context.Context, repo string, issue int) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("post-merge re-run for %s#%d: no scheduler is configured", repo, issue)
	}
	owner, name := splitOwnerRepo(repo)
	reader := s.closingMergeReader
	if reader == nil {
		if s.client == nil {
			return 0, fmt.Errorf("post-merge re-run for %s#%d: no forge client is configured", repo, issue)
		}
		reader = gh.NewIssueService(s.client)
	}
	merge, err := reader.LatestClosingMerge(ctx, owner, name, issue)
	if err != nil {
		return 0, fmt.Errorf("post-merge re-run for %s#%d: could not read its closing pull request: %w", repo, issue, err)
	}
	if merge == nil || merge.Number <= 0 {
		return 0, fmt.Errorf("post-merge re-run for %s#%d: no merged pull request closes it, so there is no merge to validate", repo, issue)
	}
	return merge.Number, nil
}

// RunPostMergeRerun is the slow half: the whole post-merge hook against pr,
// nil only when the hook left the validation marker on the issue.
func (s *Scheduler) RunPostMergeRerun(ctx context.Context, repo string, issue, pr int) error {
	if s == nil {
		return fmt.Errorf("post-merge re-run for %s#%d: no scheduler is configured", repo, issue)
	}
	result := s.runPostMerge(ctx, types.BoardItem{Repo: repo, Number: issue}, nil, pr)
	return postMergeRerunVerdict(repo, issue, pr, result)
}

// postMergeRerunVerdict is nil only when the hook left the validation marker
// on the issue (written now, or already present). Otherwise it names why,
// reading the main-check verdict first because that is the usual cause.
func postMergeRerunVerdict(repo string, issue, pr int, result hooks.PostMergeResult) error {
	switch result.ValidationMarker {
	case hooks.ValidationMarkerWritten, hooks.ValidationMarkerPresent:
		return nil
	}
	prefix := fmt.Sprintf("post-merge re-run for %s#%d (PR #%d)", repo, issue, pr)
	if mc := result.MainChecks; mc != nil {
		switch mc.Verdict {
		case hooks.MainChecksError:
			return fmt.Errorf("%s: main checks could not be read at %s: %s", prefix, shaOrUnknown(mc.MergeCommitSha), mc.Error)
		case hooks.MainChecksRed, hooks.MainChecksPending, hooks.MainChecksNone:
			return fmt.Errorf("%s: main is %s at %s; marker not written", prefix, mc.Verdict, shaOrUnknown(mc.MergeCommitSha))
		}
	}
	if result.ValidationMarker == "" {
		return fmt.Errorf("%s did not reach the validation marker", prefix)
	}
	return fmt.Errorf("%s left no validation marker: %s", prefix, result.ValidationMarker)
}

func shaOrUnknown(sha string) string {
	if sha == "" {
		return "an unknown commit"
	}
	return sha
}
