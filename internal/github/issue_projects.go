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

// BoardPlacement is one issue as the dispatch scan sees it on one board (#3278).
type BoardPlacement struct {
	OnBoard  bool
	Status   string
	Priority string
	Labels   []string
	Author   string
}

// issueBoardPlacementQuery reads an issue's labels and its items' single-select
// values in one call.
type issueBoardPlacementQuery struct {
	Repository struct {
		Issue struct {
			Author struct {
				Login graphql.String
			}
			Labels struct {
				Nodes []labelNode
			} `graphql:"labels(first: 100)"`
			ProjectItems struct {
				Nodes []struct {
					Project struct {
						Number graphql.Int
					}
					FieldValues struct {
						Nodes []fieldValueNode
					} `graphql:"fieldValues(first: 20)"`
				}
			} `graphql:"projectItems(first: 10)"`
		} `graphql:"issue(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// IssueBoardPlacement reads whether the issue is on board projectNumber and,
// when it is, the item's Status and Priority. An issue on no board is a normal
// result (OnBoard false), not an error.
func IssueBoardPlacement(ctx context.Context, client *Client, owner, repo string, issueNumber, projectNumber int) (BoardPlacement, error) {
	var out BoardPlacement
	if client == nil {
		return out, fmt.Errorf("no github client")
	}
	num, err := checkedGraphQLInt("issue number", issueNumber)
	if err != nil {
		return out, err
	}
	var q issueBoardPlacementQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": num,
	}
	if err := client.query(ctx, &q, vars); err != nil {
		return out, fmt.Errorf("read board placement for %s/%s#%d: %w", owner, repo, issueNumber, err)
	}
	out.Author = string(q.Repository.Issue.Author.Login)
	for _, l := range q.Repository.Issue.Labels.Nodes {
		out.Labels = append(out.Labels, string(l.Name))
	}
	for _, item := range q.Repository.Issue.ProjectItems.Nodes {
		if int(item.Project.Number) != projectNumber {
			continue
		}
		out.OnBoard = true
		for _, fv := range item.FieldValues.Nodes {
			if fv.TypeName != "ProjectV2ItemFieldSingleSelectValue" {
				continue
			}
			switch string(fv.ProjectV2ItemFieldSingleSelect.Field.ProjectV2SingleSelectField.Name) {
			case "Status":
				out.Status = string(fv.ProjectV2ItemFieldSingleSelect.Name)
			case "Priority":
				out.Priority = string(fv.ProjectV2ItemFieldSingleSelect.Name)
			}
		}
	}
	return out, nil
}
