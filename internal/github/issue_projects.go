package github

// Issue → project-board membership (#280).
//
// Every other board query in this package starts from a board and asks what is
// on it. That direction cannot answer the question `doctor` needs when the
// configured board turns up empty: "then where ARE the issues?" — and without
// an answer the operator is told only that something is wrong, not where to
// look. This file asks the question from the issue's side.

import (
	"context"
	"fmt"
	"sort"

	"github.com/shurcooL/graphql"
)

// ProjectNumbersForIssue returns the project board numbers the issue is on, in
// ascending order. An issue on no board returns an empty slice and no error —
// that is a real, common state, not a failure.
func ProjectNumbersForIssue(ctx context.Context, client *Client, owner, repo string, issueNumber int) ([]int, error) {
	if client == nil {
		return nil, fmt.Errorf("no github client")
	}
	num, err := checkedGraphQLInt("issue number", issueNumber)
	if err != nil {
		return nil, err
	}
	var q issueProjectItemsQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": num,
	}
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("look up project membership for %s/%s#%d: %w", owner, repo, issueNumber, err)
	}
	seen := map[int]bool{}
	out := []int{}
	for _, item := range q.Repository.Issue.ProjectItems.Nodes {
		n := int(item.Project.Number)
		if n == 0 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}
