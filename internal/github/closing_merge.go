package github

import (
	"context"
	"fmt"
	"time"

	"github.com/shurcooL/graphql"
)

// ClosingMerge is the most recent merge of a pull request that closes an
// issue (#2629). A reopened issue closed again by a second PR has two; the
// post-merge validation marker must name the newest one.
type ClosingMerge struct {
	Number   int
	SHA      string
	MergedAt time.Time
}

type closingMergeQuery struct {
	Repository struct {
		Issue *struct {
			ClosedByPullRequestsReferences struct {
				Nodes []struct {
					Number      graphql.Int
					Merged      graphql.Boolean
					MergedAt    *graphql.String
					MergeCommit *struct {
						Oid graphql.String
					}
				}
			} `graphql:"closedByPullRequestsReferences(first: 20, includeClosedPrs: true)"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// LatestClosingMerge returns the merged closing pull request of issue
// owner/repo#number with the greatest mergedAt, or nil when none has merged
// (a manual close, say). A PR without a merge commit or a readable merge time
// is skipped.
func (s *IssueService) LatestClosingMerge(ctx context.Context, owner, repo string, number int) (*ClosingMerge, error) {
	graphQLNumber, err := checkedGraphQLInt("issue number", number)
	if err != nil {
		return nil, err
	}
	var q closingMergeQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphQLNumber,
	}
	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("read closing merges for %s/%s#%d: %w", owner, repo, number, err)
	}
	if q.Repository.Issue == nil {
		return nil, fmt.Errorf("read closing merges for %s/%s#%d: issue not found", owner, repo, number)
	}
	var latest *ClosingMerge
	for _, n := range q.Repository.Issue.ClosedByPullRequestsReferences.Nodes {
		if !bool(n.Merged) || n.MergeCommit == nil || n.MergeCommit.Oid == "" || n.MergedAt == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, string(*n.MergedAt))
		if err != nil {
			continue
		}
		if latest == nil || at.After(latest.MergedAt) {
			latest = &ClosingMerge{Number: int(n.Number), SHA: string(n.MergeCommit.Oid), MergedAt: at}
		}
	}
	return latest, nil
}
