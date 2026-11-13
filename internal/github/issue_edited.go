package github

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/shurcooL/graphql"
)

type issueLastEditedQuery struct {
	Repository struct {
		Issue *struct {
			LastEditedAt *graphql.String
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// IssueLastEditedAt returns when owner/repo#number's body was last edited, or
// nil when it never was (#3235).
func (c *Client) IssueLastEditedAt(ctx context.Context, owner, repo string, number int) (*time.Time, error) {
	if number <= 0 || number > math.MaxInt32 {
		return nil, fmt.Errorf("read %s/%s#%d: not an issue number", owner, repo, number)
	}
	var q issueLastEditedQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(number),
	}
	if err := c.Query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("read %s/%s#%d: %w", owner, repo, number, err)
	}
	if q.Repository.Issue == nil {
		return nil, fmt.Errorf("read %s/%s#%d: no such issue", owner, repo, number)
	}
	s := q.Repository.Issue.LastEditedAt
	if s == nil || *s == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339, string(*s))
	if err != nil {
		return nil, fmt.Errorf("read %s/%s#%d: lastEditedAt: %w", owner, repo, number, err)
	}
	return &at, nil
}

type prCreatedAtAndBodyQuery struct {
	Repository struct {
		PullRequest *struct {
			CreatedAt graphql.String
			Body      graphql.String
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// PRCreatedAtAndBody returns when owner/repo#number was opened and its body
// (#3235). A partial-delivery re-run reads both: the opening time bounds when
// its unmet list was computed, and the body names the criteria it covered.
func (c *Client) PRCreatedAtAndBody(ctx context.Context, owner, repo string, number int) (time.Time, string, error) {
	if number <= 0 || number > math.MaxInt32 {
		return time.Time{}, "", fmt.Errorf("read %s/%s#%d: not a pull request number", owner, repo, number)
	}
	var q prCreatedAtAndBodyQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(number),
	}
	if err := c.Query(ctx, &q, vars); err != nil {
		return time.Time{}, "", fmt.Errorf("read %s/%s#%d: %w", owner, repo, number, err)
	}
	pr := q.Repository.PullRequest
	if pr == nil {
		return time.Time{}, "", fmt.Errorf("read %s/%s#%d: no such pull request", owner, repo, number)
	}
	at, err := time.Parse(time.RFC3339, string(pr.CreatedAt))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("read %s/%s#%d: createdAt: %w", owner, repo, number, err)
	}
	return at, string(pr.Body), nil
}
