package github

import (
	"context"
	"fmt"

	"github.com/shurcooL/graphql"
)

// OpenPRHead is one open pull request and the branch it merges from.
type OpenPRHead struct {
	Number      int
	HeadRefName string
}

// maxOpenPRPages bounds ListOpenPRHeads' walk: 20 pages of 100 open pull
// requests. A repository with more is an error, never a partial list.
const maxOpenPRPages = 20

type openPRHeadsQuery struct {
	Repository struct {
		PullRequests struct {
			PageInfo struct {
				HasNextPage graphql.Boolean
				EndCursor   graphql.String
			}
			Nodes []struct {
				Number      graphql.Int
				HeadRefName graphql.String
			}
		} `graphql:"pullRequests(first: 100, after: $after, states: OPEN)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ListOpenPRHeads returns every open pull request of owner/repo with the
// branch it merges from, for a caller that decides whether an open PR backs an
// issue, such as the failed-run revert, which must not return an issue to
// Ready on top of its own open PR (#1707). It reads every page, two scalars a
// pull request, so a page costs one GraphQL point. A walk that cannot be
// finished, whether a page fails or the repository has more than
// maxOpenPRPages pages, is an error rather than a short list: "no open PR
// backs this issue" must never be read off part of the list.
func (s *PRService) ListOpenPRHeads(ctx context.Context, owner, repo string) ([]OpenPRHead, error) {
	var heads []OpenPRHead
	var after *graphql.String
	for page := 0; page < maxOpenPRPages; page++ {
		var q openPRHeadsQuery
		vars := map[string]interface{}{
			"owner": graphql.String(owner),
			"name":  graphql.String(repo),
			"after": after,
		}
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list open PRs of %s/%s: %w", owner, repo, err)
		}
		prs := q.Repository.PullRequests
		for _, n := range prs.Nodes {
			heads = append(heads, OpenPRHead{Number: int(n.Number), HeadRefName: string(n.HeadRefName)})
		}
		if !prs.PageInfo.HasNextPage {
			return heads, nil
		}
		if prs.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("list open PRs of %s/%s: page %d reports more pull requests and no cursor to read them from", owner, repo, page+1)
		}
		cursor := prs.PageInfo.EndCursor
		after = &cursor
	}
	return nil, fmt.Errorf("list open PRs of %s/%s: more than %d pages of open pull requests", owner, repo, maxOpenPRPages)
}
