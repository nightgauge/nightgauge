package github

import (
	"context"

	"github.com/shurcooL/graphql"
)

// projectV2Result is the common shape extracted from both org and user queries.
type projectV2Result struct {
	ID    graphql.ID
	Title graphql.String
	Items struct {
		PageInfo pageInfo
		Nodes    []projectItemNode
	}
}

// queryProjectItems runs the appropriate org or user GraphQL query and returns
// the common projectV2Result. Used by BoardService for unfiltered item fetches.
func queryProjectItems(ctx context.Context, client *Client, ownerType OwnerType, vars map[string]interface{}) (*projectV2Result, error) {
	if ownerType.IsUser() {
		var q userProjectV2Query
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, err
		}
		return &projectV2Result{
			ID:    q.User.ProjectV2.ID,
			Title: q.User.ProjectV2.Title,
			Items: q.User.ProjectV2.Items,
		}, nil
	}
	var q projectV2Query
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, err
	}
	return &projectV2Result{
		ID:    q.Organization.ProjectV2.ID,
		Title: q.Organization.ProjectV2.Title,
		Items: q.Organization.ProjectV2.Items,
	}, nil
}

// queryProjectItemsFiltered runs the appropriate org or user GraphQL query
// with server-side status filtering and returns the common projectV2Result.
func queryProjectItemsFiltered(ctx context.Context, client *Client, ownerType OwnerType, vars map[string]interface{}) (*projectV2Result, error) {
	if ownerType.IsUser() {
		var q userProjectV2FilteredQuery
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, err
		}
		return &projectV2Result{
			ID:    q.User.ProjectV2.ID,
			Title: q.User.ProjectV2.Title,
			Items: q.User.ProjectV2.Items,
		}, nil
	}
	var q projectV2FilteredQuery
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, err
	}
	return &projectV2Result{
		ID:    q.Organization.ProjectV2.ID,
		Title: q.Organization.ProjectV2.Title,
		Items: q.Organization.ProjectV2.Items,
	}, nil
}

// projectFieldsResult is the common shape from field introspection queries.
type projectFieldsResult struct {
	ID     graphql.String
	Fields []projectFieldFullNode
}

// queryProjectFieldsFull runs the appropriate org or user GraphQL query for
// full project field introspection.
func queryProjectFieldsFull(ctx context.Context, client *Client, ownerType OwnerType, vars map[string]interface{}) (*projectFieldsResult, error) {
	if ownerType.IsUser() {
		var q userProjectFieldsFullQuery
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, err
		}
		return &projectFieldsResult{
			ID:     q.User.ProjectV2.ID,
			Fields: q.User.ProjectV2.Fields.Nodes,
		}, nil
	}
	var q projectFieldsFullQuery
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, err
	}
	return &projectFieldsResult{
		ID:     q.Organization.ProjectV2.ID,
		Fields: q.Organization.ProjectV2.Fields.Nodes,
	}, nil
}
