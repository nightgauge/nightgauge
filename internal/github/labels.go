package github

import (
	"context"
	"fmt"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/shurcooL/graphql"
)

func idToString(id graphql.ID) string {
	return fmt.Sprintf("%v", id)
}

// Label is an alias for forgetypes.Label — the canonical, forge-agnostic
// shape lives in internal/forge/types so future GitLab adapters share the
// same struct without an import cycle.
type Label = forgetypes.Label

// LabelService provides label CRUD operations via GraphQL.
type LabelService struct {
	client *Client
	owner  string
	repo   string
}

// NewLabelService creates a LabelService for the given owner/repo.
func NewLabelService(client *Client, owner, repo string) *LabelService {
	return &LabelService{
		client: client,
		owner:  owner,
		repo:   repo,
	}
}

// List returns all labels for the repository (first 100).
func (s *LabelService) List(ctx context.Context) ([]*Label, error) {
	var q listLabelsQuery
	vars := map[string]interface{}{
		"owner": graphql.String(s.owner),
		"name":  graphql.String(s.repo),
	}
	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("list labels for %s/%s: %w", s.owner, s.repo, err)
	}

	labels := make([]*Label, 0, len(q.Repository.Labels.Nodes))
	for _, n := range q.Repository.Labels.Nodes {
		labels = append(labels, &Label{
			ID:          idToString(n.ID),
			Name:        string(n.Name),
			Description: string(n.Description),
			Color:       string(n.Color),
		})
	}
	return labels, nil
}

// Create creates a label in the repository. Idempotent: if a label with the
// same name already exists, it is returned without creating a duplicate.
// color should be a hex string without the leading "#" (e.g., "d73a4a").
func (s *LabelService) Create(ctx context.Context, name, description, color string) (*Label, error) {
	if color == "" {
		color = "cccccc"
	}

	// Idempotency check: return existing label if name matches.
	existing, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range existing {
		if l.Name == name {
			return l, nil
		}
	}

	// Fetch repository node ID required by the createLabel mutation.
	repoID, err := s.client.GetRepositoryID(ctx, s.owner, s.repo)
	if err != nil {
		return nil, fmt.Errorf("get repository ID for %s/%s: %w", s.owner, s.repo, err)
	}

	var m createLabelMutation
	input := map[string]interface{}{
		"input": CreateLabelInput{
			RepositoryID: graphql.ID(repoID),
			Name:         graphql.String(name),
			Description:  graphql.String(description),
			Color:        graphql.String(color),
		},
	}
	if err := s.client.mutate(ctx, &m, input); err != nil {
		return nil, fmt.Errorf("create label %q in %s/%s: %w", name, s.owner, s.repo, err)
	}

	node := m.CreateLabel.Label
	return &Label{
		ID:          idToString(node.ID),
		Name:        string(node.Name),
		Description: string(node.Description),
		Color:       string(node.Color),
	}, nil
}

// Delete deletes a label by its node ID.
func (s *LabelService) Delete(ctx context.Context, labelID string) error {
	var m deleteLabelMutation
	input := map[string]interface{}{
		"input": DeleteLabelInput{
			ID: graphql.ID(labelID),
		},
	}
	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("delete label %q: %w", labelID, err)
	}
	return nil
}
