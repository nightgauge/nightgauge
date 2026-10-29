package github

import (
	"context"

	"github.com/shurcooL/graphql"
)

// projectV2Result is the common shape extracted from both org and user
// queries, over the board document's item node N.
type projectV2Result[N boardItemNode] struct {
	ID    graphql.ID
	Title graphql.String
	Items struct {
		PageInfo pageInfo
		Nodes    []N
	}
}

// queryProjectItems runs the appropriate org or user GraphQL query and returns
// the common projectV2Result. Used by BoardService for unfiltered item fetches.
// N chooses the document: projectItemNode or leanProjectItemNode.
func queryProjectItems[N boardItemNode](ctx context.Context, client *Client, ownerType OwnerType, vars map[string]interface{}) (*projectV2Result[N], error) {
	if ownerType.IsUser() {
		var q userProjectV2Query[N]
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, err
		}
		return &projectV2Result[N]{
			ID:    q.User.ProjectV2.ID,
			Title: q.User.ProjectV2.Title,
			Items: q.User.ProjectV2.Items,
		}, nil
	}
	var q projectV2Query[N]
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, err
	}
	return &projectV2Result[N]{
		ID:    q.Organization.ProjectV2.ID,
		Title: q.Organization.ProjectV2.Title,
		Items: q.Organization.ProjectV2.Items,
	}, nil
}

// queryProjectItemsFiltered runs the appropriate org or user GraphQL query
// with server-side status filtering and returns the common projectV2Result.
// N chooses the document, as for queryProjectItems.
func queryProjectItemsFiltered[N boardItemNode](ctx context.Context, client *Client, ownerType OwnerType, vars map[string]interface{}) (*projectV2Result[N], error) {
	if ownerType.IsUser() {
		var q userProjectV2FilteredQuery[N]
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, err
		}
		return &projectV2Result[N]{
			ID:    q.User.ProjectV2.ID,
			Title: q.User.ProjectV2.Title,
			Items: q.User.ProjectV2.Items,
		}, nil
	}
	var q projectV2FilteredQuery[N]
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, err
	}
	return &projectV2Result[N]{
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

// queryProjectUpdatedAt runs the 1-point change probe against the appropriate
// org or user query and returns the project's raw RFC3339 updatedAt string.
func queryProjectUpdatedAt(ctx context.Context, client *Client, ownerType OwnerType, vars map[string]interface{}) (string, error) {
	if ownerType.IsUser() {
		var q userProjectV2UpdatedAtQuery
		if err := client.query(ctx, &q, vars); err != nil {
			return "", err
		}
		return string(q.User.ProjectV2.UpdatedAt), nil
	}
	var q projectV2UpdatedAtQuery
	if err := client.query(ctx, &q, vars); err != nil {
		return "", err
	}
	return string(q.Organization.ProjectV2.UpdatedAt), nil
}
