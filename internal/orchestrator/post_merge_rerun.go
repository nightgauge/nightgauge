package orchestrator

import (
	"context"
	"fmt"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// RerunPostMerge backs attention.VerbIssueRerunPostMerge (#3081): it re-runs
// the post-merge hook for one closed issue against its latest merged closing
// pull request, so a dependency closed without the validation marker can earn
// it once main is green. It errors when no merged closing pull request exists
// (a manual close has no merge to validate) and when the hook leaves no
// marker, so the Action Center card survives to say the dependency is still
// held, and why.
func (s *Scheduler) RerunPostMerge(ctx context.Context, repo string, issue int) error {
	if s == nil {
		return fmt.Errorf("post-merge re-run for %s#%d: no scheduler is configured", repo, issue)
	}
	owner, name := splitOwnerRepo(repo)
	reader := s.closingMergeReader
	if reader == nil {
		if s.client == nil {
			return fmt.Errorf("post-merge re-run for %s#%d: no forge client is configured", repo, issue)
		}
		reader = gh.NewIssueService(s.client)
	}
	merge, err := reader.LatestClosingMerge(ctx, owner, name, issue)
	if err != nil {
		return fmt.Errorf("post-merge re-run for %s#%d: read its closing pull request: %w", repo, issue, err)
	}
	if merge == nil || merge.Number <= 0 {
		return fmt.Errorf("post-merge re-run for %s#%d: no merged pull request closes it, so there is no merge to validate", repo, issue)
	}
	result := s.runPostMerge(ctx, types.BoardItem{Repo: repo, Number: issue}, nil, merge.Number)
	return postMergeRerunVerdict(repo, issue, merge.Number, result)
}

// postMergeRerunVerdict is nil only when the hook left the validation marker
// on the issue (written now, or already present).
func postMergeRerunVerdict(repo string, issue, pr int, result hooks.PostMergeResult) error {
	switch result.ValidationMarker {
	case hooks.ValidationMarkerWritten, hooks.ValidationMarkerPresent:
		return nil
	case "":
		return fmt.Errorf("post-merge re-run for %s#%d (PR #%d) did not reach the validation marker", repo, issue, pr)
	default:
		return fmt.Errorf("post-merge re-run for %s#%d (PR #%d) left no validation marker: %s", repo, issue, pr, result.ValidationMarker)
	}
}
