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

type partialPRQuery struct {
	Repository struct {
		PullRequest *struct {
			CreatedAt   graphql.String
			Body        graphql.String
			MergeCommit *struct {
				Oid graphql.String
			}
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// PartialPR is what a partial-delivery re-run reads from the PR that left the
// remainder (#3235, #3423): when it was opened, which bounds when its unmet
// list was computed; its body, which names the criteria it covered; and its
// merge commit, the diff a remaining criterion is re-judged against. MergeSHA
// is empty for an unmerged PR.
type PartialPR struct {
	CreatedAt time.Time
	Body      string
	MergeSHA  string
}

// PartialPR reads owner/repo#number's opening time, body and merge commit.
func (c *Client) PartialPR(ctx context.Context, owner, repo string, number int) (PartialPR, error) {
	if number <= 0 || number > math.MaxInt32 {
		return PartialPR{}, fmt.Errorf("read %s/%s#%d: not a pull request number", owner, repo, number)
	}
	var q partialPRQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphql.Int(number),
	}
	if err := c.Query(ctx, &q, vars); err != nil {
		return PartialPR{}, fmt.Errorf("read %s/%s#%d: %w", owner, repo, number, err)
	}
	pr := q.Repository.PullRequest
	if pr == nil {
		return PartialPR{}, fmt.Errorf("read %s/%s#%d: no such pull request", owner, repo, number)
	}
	at, err := time.Parse(time.RFC3339, string(pr.CreatedAt))
	if err != nil {
		return PartialPR{}, fmt.Errorf("read %s/%s#%d: createdAt: %w", owner, repo, number, err)
	}
	out := PartialPR{CreatedAt: at, Body: string(pr.Body)}
	if pr.MergeCommit != nil {
		out.MergeSHA = string(pr.MergeCommit.Oid)
	}
	return out, nil
}
