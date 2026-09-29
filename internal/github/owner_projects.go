package github

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OwnerProject is one ProjectV2 board an owner has, as the authenticated
// identity sees it.
type OwnerProject struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Closed bool   `json:"closed"`
}

// OwnerProjects is an owner's visible boards. Login is the owner's login as
// GitHub returned it (not as configured), for building links.
type OwnerProjects struct {
	Login    string
	Projects []OwnerProject
}

// ownerProjectsLimit bounds the listing: it exists to name candidates for a
// wrong project number, not to take a census.
const ownerProjectsLimit = 100

// ListOwnerProjects lists up to 100 of owner's ProjectV2 boards visible to
// this client, ordered by number. ownerType selects the organization or user
// root. A GraphQL error (the owner does not exist, or the identity may not
// list its projects) is returned with GitHub's message.
func ListOwnerProjects(ctx context.Context, c *Client, owner string, ownerType OwnerType) (OwnerProjects, error) {
	root := "organization"
	if ownerType == OwnerTypeUser {
		root = "user"
	}
	query := fmt.Sprintf(`query($owner: String!) {
  %s(login: $owner) {
    login
    projectsV2(first: %d, orderBy: {field: NUMBER, direction: ASC}) {
      nodes { number title closed }
    }
  }
}`, root, ownerProjectsLimit)
	raw, err := c.queryRaw(ctx, query, map[string]interface{}{"owner": owner})
	if err != nil {
		return OwnerProjects{}, fmt.Errorf("list projects of %s: %w", owner, err)
	}
	type ownerNode struct {
		Login      string `json:"login"`
		ProjectsV2 struct {
			Nodes []OwnerProject `json:"nodes"`
		} `json:"projectsV2"`
	}
	var env struct {
		Data struct {
			Organization *ownerNode `json:"organization"`
			User         *ownerNode `json:"user"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return OwnerProjects{}, fmt.Errorf("list projects of %s: decode: %w", owner, err)
	}
	node := env.Data.Organization
	if ownerType == OwnerTypeUser {
		node = env.Data.User
	}
	if len(env.Errors) > 0 || node == nil {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, "owner not found")
		}
		return OwnerProjects{}, fmt.Errorf("list projects of %s: %s", owner, strings.Join(msgs, "; "))
	}
	out := OwnerProjects{Login: node.Login, Projects: node.ProjectsV2.Nodes}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].Number < out.Projects[j].Number })
	return out, nil
}
