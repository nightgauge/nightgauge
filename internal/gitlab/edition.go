package gitlab

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// Edition tags a GitLab instance as Community or Enterprise. EditionUnknown
// is the zero value and is returned when probing fails in a way callers
// should retry (network failure, 401 — possibly insufficient token scope).
type Edition string

const (
	EditionUnknown Edition = ""
	EditionCE      Edition = "ce"
	EditionEE      Edition = "ee"
)

// editionProbe holds the cached result of GET /api/v4/license. The probe is
// performed on the first Edition() call and reused thereafter via sync.Once.
type editionProbe struct {
	once    sync.Once
	edition Edition
	err     error
}

// Edition returns the cached edition for this client, performing a single
// GET /api/v4/license probe on the first call. Subsequent calls return the
// cached value without an HTTP round-trip.
//
// Mapping:
//   - 200 → EditionEE (license endpoint exists and returned a license body).
//   - 403 / 404 → EditionCE (CE deliberately omits this endpoint).
//   - 401 → EditionUnknown + err (token may be missing the `api` scope).
//   - 5xx / network error → EditionUnknown + err.
//
// The license body itself is intentionally not retained; only the edition
// classification is cached.
func (c *Client) Edition(ctx context.Context) Edition {
	ed, _ := c.editionWithError(ctx)
	return ed
}

// editionWithError exposes the underlying probe error for tests / future
// callers that want to surface 401 detection. Public callers use Edition()
// which discards the error.
func (c *Client) editionWithError(ctx context.Context) (Edition, error) {
	c.editionMu.Lock()
	probe := c.editionCache
	if probe == nil {
		probe = &editionProbe{}
		c.editionCache = probe
	}
	c.editionMu.Unlock()

	probe.once.Do(func() {
		probe.edition, probe.err = c.probeEdition(ctx)
	})
	return probe.edition, probe.err
}

// probeEdition issues the actual GET /api/v4/license request. Used by
// editionWithError under sync.Once; callers should not invoke directly.
func (c *Client) probeEdition(ctx context.Context) (Edition, error) {
	full := c.buildURL("/license", nil)
	_, err := c.do(ctx, http.MethodGet, full, nil, nil, "license probe")
	if err == nil {
		return EditionEE, nil
	}
	switch {
	case errors.Is(err, forge.ErrNotFound), errors.Is(err, forge.ErrPermissionDenied):
		// CE deliberately does not expose /license. 403 happens on
		// instances where admins have hidden the endpoint from non-admin
		// users — both indicate "no EE-only behaviour available here".
		return EditionCE, nil
	case errors.Is(err, forge.ErrUnauthorized):
		// 401 most often means the token lacks the `api` scope. We can't
		// distinguish CE from "EE behind insufficient scope" so return
		// Unknown and let the caller retry once auth is fixed.
		return EditionUnknown, err
	default:
		return EditionUnknown, err
	}
}
