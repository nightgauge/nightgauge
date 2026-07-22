package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// AuthService is the forge-agnostic surface for authentication / token
// inspection. Adapters report whichever scope/permission model their forge
// exposes through TokenScopeInfo.
type AuthService interface {
	CheckTokenScopes(ctx context.Context) (*forgetypes.TokenScopeInfo, error)

	// Whoami returns the actor associated with the active token. Mirrors
	// `gh api user --jq .login` — used by repo-init to discover the
	// authenticated user for personal-project lookup. Adapters that
	// cannot identify the caller return an error wrapping ErrUnsupported.
	Whoami(ctx context.Context) (*forgetypes.Actor, error)
}
