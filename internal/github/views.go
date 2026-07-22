package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shurcooL/graphql"
)

// View represents a GitHub project board view.
type View struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Layout string `json:"layout"` // "board", "table", or "roadmap"
}

// ViewService provides project view operations: list (GraphQL) and create (REST).
type ViewService struct {
	client        *Client
	owner         string
	ownerType     OwnerType
	projectNumber int
}

// NewViewService creates a ViewService for the given owner and project number.
// ownerType defaults to OwnerTypeOrg if not provided.
func NewViewService(client *Client, owner string, projectNumber int, ownerType ...OwnerType) *ViewService {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	return &ViewService{
		client:        client,
		owner:         owner,
		ownerType:     ot,
		projectNumber: projectNumber,
	}
}

// List returns all views for the project board via GraphQL.
func (s *ViewService) List(ctx context.Context) ([]*View, error) {
	vars := map[string]interface{}{
		"owner":         graphql.String(s.owner),
		"projectNumber": graphql.Int(s.projectNumber),
	}

	var nodes []viewNode
	if s.ownerType.IsUser() {
		var q userProjectV2ViewsQuery
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list views for user %s project %d: %w", s.owner, s.projectNumber, err)
		}
		nodes = q.User.ProjectV2.Views.Nodes
	} else {
		var q projectV2ViewsQuery
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list views for org %s project %d: %w", s.owner, s.projectNumber, err)
		}
		nodes = q.Organization.ProjectV2.Views.Nodes
	}

	views := make([]*View, 0, len(nodes))
	for _, n := range nodes {
		views = append(views, &View{
			ID:     fmt.Sprintf("%v", n.ID),
			Name:   string(n.Name),
			Layout: normalizeLayout(string(n.Layout)),
		})
	}
	return views, nil
}

// Create creates a new project view via the GitHub REST API.
// Idempotent: if a view with the same name already exists, it is returned
// without making a REST POST. query is an optional server-side filter string
// (e.g., "status:Ready").
//
// Required API header X-GitHub-Api-Version: 2026-03-10 is set automatically.
func (s *ViewService) Create(ctx context.Context, name, layout string, query *string) (*View, error) {
	// Idempotency: check for an existing view with this name.
	existing, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range existing {
		if v.Name == name {
			return v, nil
		}
	}

	// Build REST path based on owner type.
	var path string
	if s.ownerType.IsUser() {
		path = fmt.Sprintf("/users/%s/projectsV2/%d/views", s.owner, s.projectNumber)
	} else {
		path = fmt.Sprintf("/orgs/%s/projectsV2/%d/views", s.owner, s.projectNumber)
	}

	body := map[string]interface{}{
		"name":   name,
		"layout": layout,
	}
	if query != nil && *query != "" {
		body["filter"] = *query
	}

	respData, err := s.client.restPost(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("create view %q: %w", name, err)
	}

	var viewResp struct {
		ID     int    `json:"id"`
		NodeID string `json:"node_id"`
		Name   string `json:"name"`
		Layout string `json:"layout"`
	}
	if err := json.Unmarshal(respData, &viewResp); err != nil {
		return nil, fmt.Errorf("parse view create response: %w", err)
	}

	// Prefer node_id (GraphQL-compatible) over integer ID.
	id := viewResp.NodeID
	if id == "" {
		id = fmt.Sprintf("%d", viewResp.ID)
	}

	return &View{
		ID:     id,
		Name:   viewResp.Name,
		Layout: normalizeLayout(viewResp.Layout),
	}, nil
}

// normalizeLayout converts GitHub layout enum values to lowercase short names.
// Handles both GraphQL enum format (BOARD_LAYOUT) and REST format (board_layout, board).
func normalizeLayout(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, "_layout")
	return s
}
