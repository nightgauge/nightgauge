package github

import (
	"context"
	"fmt"

	"github.com/shurcooL/graphql"
)

// RepoRef is a minimal repository reference returned by project-linked repo queries.
type RepoRef struct {
	Owner string
	Name  string
}

// LinkedProjectRef is a ProjectV2 linked to a repository.
type LinkedProjectRef struct {
	ID     string
	Owner  string
	Number int
	Title  string
}

// FetchRepositoryLinkedProjects returns the ProjectV2 boards GitHub links to
// owner/name. Linkage is discovery input only; callers must not infer a default
// when more than one project is returned.
func FetchRepositoryLinkedProjects(ctx context.Context, client *Client, owner, name string) ([]LinkedProjectRef, error) {
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(name),
	}
	var q repositoryLinkedProjectsQuery
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("fetch repository linked projects: %w", err)
	}
	return linkedProjectsFromQuery(q, owner), nil
}

func linkedProjectsFromQuery(q repositoryLinkedProjectsQuery, owner string) []LinkedProjectRef {
	refs := make([]LinkedProjectRef, 0, len(q.Repository.ProjectsV2.Nodes))
	for _, node := range q.Repository.ProjectsV2.Nodes {
		refs = append(refs, LinkedProjectRef{
			ID:     fmt.Sprint(node.ID),
			Owner:  owner,
			Number: int(node.Number),
			Title:  string(node.Title),
		})
	}
	return refs
}

// FetchProjectLinkedRepos queries GitHub for all repositories linked to a ProjectV2.
// It supports both organization-owned and user-owned projects via ownerType.
// Results are capped at 100 — sufficient for any real workspace.
func FetchProjectLinkedRepos(ctx context.Context, client *Client, owner string, ownerType OwnerType, projectNumber int) ([]RepoRef, error) {
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"number": graphql.Int(projectNumber),
	}

	if ownerType.IsUser() {
		var q userProjectLinkedReposQuery
		if err := client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("fetch project linked repos (user): %w", err)
		}
		return refsFromUserQuery(q), nil
	}

	var q projectLinkedReposQuery
	if err := client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("fetch project linked repos (org): %w", err)
	}
	return refsFromOrgQuery(q), nil
}

func refsFromOrgQuery(q projectLinkedReposQuery) []RepoRef {
	nodes := q.Organization.ProjectV2.Repositories.Nodes
	refs := make([]RepoRef, 0, len(nodes))
	for _, n := range nodes {
		refs = append(refs, RepoRef{
			Owner: string(n.Owner.Login),
			Name:  string(n.Name),
		})
	}
	return refs
}

func refsFromUserQuery(q userProjectLinkedReposQuery) []RepoRef {
	nodes := q.User.ProjectV2.Repositories.Nodes
	refs := make([]RepoRef, 0, len(nodes))
	for _, n := range nodes {
		refs = append(refs, RepoRef{
			Owner: string(n.Owner.Login),
			Name:  string(n.Name),
		})
	}
	return refs
}
