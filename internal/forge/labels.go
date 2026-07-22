package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// LabelService is the forge-agnostic surface for repository label CRUD.
type LabelService interface {
	List(ctx context.Context) ([]*forgetypes.Label, error)
	Create(ctx context.Context, name, description, color string) (*forgetypes.Label, error)
	Delete(ctx context.Context, labelID string) error
}
