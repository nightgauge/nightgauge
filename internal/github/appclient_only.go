package github

import (
	"context"
	"errors"
	"fmt"
)

// ErrAppUnavailable is a write only the GitHub App installation may carry,
// refused because the App cannot carry it: none is configured for the owner,
// its configuration is unusable, or it could not mint a token. Nothing was
// sent to GitHub, with that identity or any other.
var ErrAppUnavailable = errors.New("no usable GitHub App installation")

// NewAppOnlyClient returns a client authenticated as owner's GitHub App
// installation, and never as anything else.
//
// NewClientFromConfig's chain puts a CI environment token ahead of the App
// and personal tokens after it, and falls through to them when the App is
// missing or broken. This consults none of them: no CI token, no GITHUB_TOKEN
// or GH_TOKEN, no configured token and no gh CLI account. When the App cannot
// be used it returns an error wrapping ErrAppUnavailable, and the caller
// writes nothing. A conversation turn posted by a teammate files issues
// through it (#1570), so that write never carries the host operator's
// personal identity, or anyone else's.
func NewAppOnlyClient(ctx context.Context, cfg TokenResolver, owner string) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: no configuration names a GitHub App for %s", ErrAppUnavailable, owner)
	}
	creds, err := resolveApp(cfg, owner)
	if err != nil {
		return nil, fmt.Errorf("%w: github_auth.app is configured for %s but unusable: %v", ErrAppUnavailable, owner, err)
	}
	if creds == nil {
		return nil, fmt.Errorf("%w: no GitHub App installation is configured for %s", ErrAppUnavailable, owner)
	}
	c, err := newAppClient(ctx, creds)
	if err != nil {
		return nil, fmt.Errorf("%w: %s could not mint a token: %v", ErrAppUnavailable, creds, err)
	}
	return c, nil
}
