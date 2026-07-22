package forge

import "errors"

// Sentinel errors returned (wrapped via %w) by forge adapters. Callers use
// errors.Is to react to a particular failure class without coupling to a
// specific forge's error text or HTTP status codes.
//
// Adapter implementations are responsible for translating their native
// errors (HTTP 404 → ErrNotFound, 401 → ErrUnauthorized, etc.) into these
// sentinels at the adapter boundary. Existing fmt.Errorf wrapping for
// non-sentinel cases is preserved.
var (
	// ErrNotFound indicates the requested resource (issue, PR, label, etc.)
	// does not exist or the caller cannot see it.
	ErrNotFound = errors.New("forge: resource not found")

	// ErrRateLimited indicates the caller has exceeded the forge's rate
	// limit. Callers should back off and retry per the forge's published
	// reset window.
	ErrRateLimited = errors.New("forge: rate limited")

	// ErrPermissionDenied indicates the token is valid but lacks permission
	// for the requested action (forbidden, scope mismatch).
	ErrPermissionDenied = errors.New("forge: permission denied")

	// ErrUnauthorized indicates the token is missing, expired, or revoked.
	ErrUnauthorized = errors.New("forge: unauthorized")

	// ErrUnsupported indicates the requested forge kind is recognised but
	// no adapter implementation is registered for it. Returned by
	// forge.New for unknown Kind values. Also used by adapter methods that
	// have not yet been implemented for a given forge — callers can use
	// errors.Is to fall back to an alternate code path.
	ErrUnsupported = errors.New("forge: unsupported forge kind")

	// ErrUnsupportedOnEdition indicates the requested feature exists in
	// the forge family but not in the licensed edition the caller has
	// access to. Examples: GitLab CE silently ignores
	// approvals_before_merge — adapters surface this as
	// ErrUnsupportedOnEdition so callers can choose to treat it as a
	// soft warning rather than a hard failure.
	ErrUnsupportedOnEdition = errors.New("forge: unsupported on this edition")
)
