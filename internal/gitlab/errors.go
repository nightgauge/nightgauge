package gitlab

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// mapStatus translates a GitLab REST response status code into the
// corresponding forge sentinel error (wrapped via %w). The body snippet,
// when present, is included in the error message to ease debugging without
// exposing internal call stacks.
//
// Status codes that don't match a sentinel are returned as
// fmt.Errorf("gitlab %s: HTTP %d: %s", op, status, snippet) so callers
// still see the operation context. Returns nil when status is in the 2xx
// range.
func mapStatus(op string, status int, snippet string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("gitlab %s: %w (HTTP 401)", op, forge.ErrUnauthorized)
	case http.StatusForbidden:
		return fmt.Errorf("gitlab %s: %w (HTTP 403)", op, forge.ErrPermissionDenied)
	case http.StatusNotFound:
		return fmt.Errorf("gitlab %s: %w (HTTP 404)", op, forge.ErrNotFound)
	case http.StatusTooManyRequests:
		return fmt.Errorf("gitlab %s: %w (HTTP 429)", op, forge.ErrRateLimited)
	}
	if snippet == "" {
		return fmt.Errorf("gitlab %s: HTTP %d", op, status)
	}
	return fmt.Errorf("gitlab %s: HTTP %d: %s", op, status, truncateSnippet(snippet))
}

// truncateSnippet caps a response-body snippet to 200 characters so the
// error message stays log-friendly even when GitLab returns a large HTML
// error page.
func truncateSnippet(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// asEditionError wraps an underlying error chain with ErrUnsupportedOnEdition
// so callers can use errors.Is to react. Used when a CE deployment
// rejects EE-only fields like approvals_before_merge.
func asEditionError(op, field string, cause error) error {
	if cause == nil {
		return fmt.Errorf("gitlab %s: %s requires GitLab EE: %w", op, field, forge.ErrUnsupportedOnEdition)
	}
	if errors.Is(cause, forge.ErrUnsupportedOnEdition) {
		return cause
	}
	return fmt.Errorf("gitlab %s: %s requires GitLab EE: %w", op, field, forge.ErrUnsupportedOnEdition)
}
