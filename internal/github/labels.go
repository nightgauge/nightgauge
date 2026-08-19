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

// Rename renames a label in place, preserving every issue and pull-request
// association. GitHub's updateLabel mutation mutates the existing node, so the
// label keeps its ID and stays attached to every item that carries it. This is
// the only non-destructive way to change a label's name: a delete-then-create
// cycle produces a label with the same text but a new node ID, silently
// detaching it from every issue it was on.
//
// Idempotent by design so a partially-applied batch can be re-run: if oldName
// is absent but newName already exists, the existing label is returned without
// error. Passing oldName == newName is allowed and updates only color and
// description.
//
// description and color are optional — empty values leave the current value
// untouched rather than clearing it.
func (s *LabelService) Rename(ctx context.Context, oldName, newName, description, color string) (*Label, error) {
	if oldName == "" {
		return nil, fmt.Errorf("old label name is required")
	}
	if newName == "" {
		return nil, fmt.Errorf("new label name is required")
	}

	existing, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	var current, target *Label
	for _, l := range existing {
		switch l.Name {
		case oldName:
			current = l
		case newName:
			target = l
		}
	}

	// Already renamed by an earlier run — report success without mutating.
	if current == nil {
		if target != nil {
			return target, nil
		}
		return nil, fmt.Errorf("label %q not found in %s/%s", oldName, s.owner, s.repo)
	}

	// Renaming onto an occupied name would merge two label sets. GitHub rejects
	// it, and doing it by hand (relabel every issue, delete the old label) is a
	// different, lossy operation that this verb deliberately does not perform.
	if target != nil && oldName != newName {
		return nil, fmt.Errorf(
			"cannot rename %q to %q in %s/%s: a label named %q already exists; "+
				"merging two labels is not supported — relabel its issues first, then delete it",
			oldName, newName, s.owner, s.repo, newName)
	}

	name := graphql.String(newName)
	input := UpdateLabelInput{ID: graphql.ID(current.ID), Name: &name}
	if description != "" {
		d := graphql.String(description)
		input.Description = &d
	}
	if color != "" {
		c := graphql.String(color)
		input.Color = &c
	}

	var m updateLabelMutation
	if err := s.client.mutate(ctx, &m, map[string]interface{}{"input": input}); err != nil {
		return nil, fmt.Errorf("rename label %q to %q in %s/%s: %w", oldName, newName, s.owner, s.repo, err)
	}

	node := m.UpdateLabel.Label
	return &Label{
		ID:          idToString(node.ID),
		Name:        string(node.Name),
		Description: string(node.Description),
		Color:       string(node.Color),
	}, nil
}
