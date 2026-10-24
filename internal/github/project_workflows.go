package github

import (
	"context"
	"fmt"

	"github.com/shurcooL/graphql"
)

// ProjectWorkflow is one built-in workflow of a ProjectV2 board and whether it
// is enabled. GitHub exposes no mutation to change either, so the pipeline can
// only read this and report drift against project.builtin_workflows (#2499).
type ProjectWorkflow struct {
	Name    string
	Enabled bool
}

type projectWorkflowNode struct {
	Name    string
	Enabled bool
}

// ListProjectWorkflows returns the built-in workflows of project number under
// owner. A board that does not exist is an error, not an empty list.
func ListProjectWorkflows(ctx context.Context, client *Client, owner string, ownerType OwnerType, number int) ([]ProjectWorkflow, error) {
	vars := map[string]interface{}{
		"owner":         graphql.String(owner),
		"projectNumber": graphql.Int(number),
	}
	var nodes []projectWorkflowNode
	if ownerType.IsUser() {
		var q struct {
			User struct {
				ProjectV2 struct {
					Workflows struct {
						Nodes []projectWorkflowNode
					} `graphql:"workflows(first: 50)"`
				} `graphql:"projectV2(number: $projectNumber)"`
			} `graphql:"user(login: $owner)"`
		}
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list workflows on project %d: %w", number, err)
		}
		nodes = q.User.ProjectV2.Workflows.Nodes
	} else {
		var q struct {
			Organization struct {
				ProjectV2 struct {
					Workflows struct {
						Nodes []projectWorkflowNode
					} `graphql:"workflows(first: 50)"`
				} `graphql:"projectV2(number: $projectNumber)"`
			} `graphql:"organization(login: $owner)"`
		}
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list workflows on project %d: %w", number, err)
		}
		nodes = q.Organization.ProjectV2.Workflows.Nodes
	}
	out := make([]ProjectWorkflow, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, ProjectWorkflow{Name: n.Name, Enabled: n.Enabled})
	}
	return out, nil
}
