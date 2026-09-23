package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// CountOpenIssues counts the repository's open issues — pull requests
// excluded, as the issue list mixes them in — over REST, every page read
// conditionally, so an unchanged repository costs nothing to re-count.
//
// It is the question the attention sweep's board-reachability check asks
// ("does this repository have open work at all?"), which needs a number, not
// the issues; ListIssues answers it with a GraphQL read of every open issue.
func (s *IssueService) CountOpenIssues(ctx context.Context, owner, repo string) (int, error) {
	first := fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=100", url.PathEscape(owner), url.PathEscape(repo))
	pages, _, err := s.client.condGetAll(ctx, first, "open-issue-count/v1", func(body []byte) (any, error) {
		var raw []struct {
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		n := 0
		for _, r := range raw {
			if len(r.PullRequest) == 0 || string(r.PullRequest) == "null" {
				n++
			}
		}
		return n, nil
	})
	if err != nil {
		return 0, fmt.Errorf("count open issues for %s/%s: %w", owner, repo, err)
	}
	total := 0
	for _, p := range pages {
		var n int
		if err := json.Unmarshal(p, &n); err != nil {
			return 0, fmt.Errorf("count open issues for %s/%s: decode: %w", owner, repo, err)
		}
		total += n
	}
	return total, nil
}
