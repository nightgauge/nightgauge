package contractrollout

import (
	"context"
	"fmt"
	"strings"
)

// LabelClient is one repository's labels. Narrow on purpose, so the label
// primitive is testable without a forge.
type LabelClient interface {
	List(ctx context.Context) ([]Label, error)
	Create(ctx context.Context, l Label) error
}

// LabelResult is what provisioning did in one repository.
type LabelResult struct {
	// Created are the labels that were missing and were created.
	Created []string `json:"created,omitempty"`
	// Present are the labels that already matched.
	Present []string `json:"present,omitempty"`
	// Drift are labels that exist with another color or description. They
	// are reported, not repainted: a label's color and description may be
	// another contract's, and changing them is a decision, not a rollout.
	Drift []string `json:"drift,omitempty"`
}

// ProvisionLabels creates every wanted label the repository lacks. Names
// match case-insensitively, as the forge matches them. With write false
// nothing is created and Created lists what would be.
func ProvisionLabels(ctx context.Context, c LabelClient, want []Label, write bool) (LabelResult, error) {
	var res LabelResult
	if len(want) == 0 {
		return res, nil
	}
	have, err := c.List(ctx)
	if err != nil {
		return res, fmt.Errorf("list labels: %w", err)
	}
	byName := make(map[string]Label, len(have))
	for _, l := range have {
		byName[strings.ToLower(l.Name)] = l
	}
	for _, w := range want {
		cur, ok := byName[strings.ToLower(w.Name)]
		switch {
		case !ok:
			if write {
				if err := c.Create(ctx, w); err != nil {
					return res, fmt.Errorf("create label %q: %w", w.Name, err)
				}
			}
			res.Created = append(res.Created, w.Name)
		case !strings.EqualFold(cur.Color, w.Color) || cur.Description != w.Description:
			res.Drift = append(res.Drift, fmt.Sprintf("%s (has color %s, description %q)", w.Name, cur.Color, cur.Description))
		default:
			res.Present = append(res.Present, w.Name)
		}
	}
	return res, nil
}
