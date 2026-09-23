package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// callerConditionalKey marks a request whose conditional headers the caller
// owns (condGet). The transport's in-memory ETag layer steps aside for it, so
// the caller sees the server's real 304 rather than a replayed 200.
type callerConditionalKey struct{}

// condResponse is one conditional GET's outcome.
type condResponse struct {
	// Status is the HTTP status the server answered: 200, 304, or an error
	// status the caller classifies. 304 is only ever reported with Payload set.
	Status int
	// Payload is the caller's reduced form of the body — freshly reduced on a
	// 200, read back from the store on a 304.
	Payload json.RawMessage
	// Next is the absolute URL of the next page (Link rel="next"), "" at the end.
	Next string
	// NotModified is true when the server answered 304 and nothing was billed.
	NotModified bool
	// ETag is the validator the payload is current under ("" when the server
	// sent none). A caller that derives something from several reads can key
	// it by their ETags.
	ETag string
	// Body is the raw body of a non-2xx answer, for error classification.
	Body []byte
}

// condGet issues a GET that revalidates against the client's
// ConditionalStore. target is an API path ("/repos/o/r") or an absolute URL on
// the API host (a Link header's next page). reduce turns a 200 body into the
// value worth keeping; its JSON is what the store holds and what a later 304
// hands back, so a 2.5 MB page can be kept as the few fields it maps to.
//
// schema names the reducer's output shape ("board-items/v2"). It is part of the
// store key, so changing a reducer's shape and bumping its tag makes every old
// payload a miss rather than something decoded into the wrong struct; each
// reducer carries its own tag, so one change invalidates only its own entries.
//
// A 304 costs no rate limit (GitHub does not count a conditional request it
// answers 304 when it carries an Authorization header), so a read repeated
// with nothing changed is free — across daemon restarts too, when the store is
// disk-backed.
func (c *Client) condGet(ctx context.Context, target, schema string, reduce func(body []byte) (any, error)) (*condResponse, error) {
	url := target
	if strings.HasPrefix(target, "/") {
		url = c.restBaseURL() + target
	}
	c.mu.Lock()
	store, identity := c.cond, c.identity
	c.mu.Unlock()

	storeKey := schema + " " + url
	var hdr http.Header
	cached, haveCached := store.get(identity, storeKey)
	if haveCached {
		hdr = http.Header{"If-None-Match": []string{cached.ETag}}
	}
	ctx = context.WithValue(ctx, callerConditionalKey{}, true)
	body, status, respHdr, err := c.restDoURL(ctx, http.MethodGet, url, target, nil, hdr)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusNotModified && haveCached:
		return &condResponse{Status: status, Payload: cached.Payload, Next: cached.Next, NotModified: true, ETag: cached.ETag}, nil
	case status == http.StatusNotModified:
		// We sent no validator, so a 304 is a server we do not understand.
		return nil, fmt.Errorf("REST GET %s: 304 without a conditional request", target)
	case status >= 200 && status < 300:
		reduced, err := reduce(body)
		if err != nil {
			return nil, fmt.Errorf("REST GET %s: decode: %w", target, err)
		}
		payload, err := json.Marshal(reduced)
		if err != nil {
			return nil, fmt.Errorf("REST GET %s: encode reduced payload: %w", target, err)
		}
		next := nextLink(respHdr.Get("Link"))
		etag := respHdr.Get("ETag")
		if etag != "" {
			store.put(identity, storeKey, condEntry{ETag: etag, Next: next, Payload: payload, StoredAt: time.Now().UTC()})
		}
		return &condResponse{Status: status, Payload: payload, Next: next, ETag: etag}, nil
	default:
		return &condResponse{Status: status, Body: body}, nil
	}
}

// nextLink extracts the rel="next" target from an RFC 8288 Link header.
func nextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		target := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, p := range segs[1:] {
			if strings.ReplaceAll(strings.TrimSpace(p), " ", "") == `rel="next"` {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

// condGetAll walks a paginated list with condGet, page by page, and returns
// every page's reduced payload in order plus whether any page changed.
//
// Consistency: pages are addressed by cursor, and a cursor names a position
// in the list as it stood when the previous page was served. When a page
// comes back 200 (the list changed) the walk is repeated once from the first
// page — conditionally, so an unchanged repeat costs nothing — and repeated
// again, up to maxWalks, while it keeps finding changes. A walk in which every
// page answered 304 is, by construction, a set of pages that all describe the
// same server state as the stored ones, so it is accepted as consistent.
func (c *Client) condGetAll(ctx context.Context, first, schema string, reduce func([]byte) (any, error)) (pages []json.RawMessage, changed bool, err error) {
	const maxWalks = 3
	const maxPages = 50
	for walk := 0; walk < maxWalks; walk++ {
		pages = pages[:0]
		walkChanged := false
		next := first
		for n := 0; next != ""; n++ {
			if n >= maxPages {
				return nil, false, fmt.Errorf("REST GET %s: more than %d pages", first, maxPages)
			}
			resp, err := c.condGet(ctx, next, schema, reduce)
			if err != nil {
				return nil, false, err
			}
			if resp.Status != http.StatusOK && resp.Status != http.StatusNotModified {
				return nil, false, &restStatusError{Target: next, Status: resp.Status, Body: resp.Body}
			}
			if !resp.NotModified {
				walkChanged = true
			}
			pages = append(pages, resp.Payload)
			next = resp.Next
		}
		changed = changed || walkChanged
		// A one-page list is consistent with itself; only a multi-page walk
		// that saw a change is re-walked.
		if !walkChanged || len(pages) <= 1 || walk == maxWalks-1 {
			return pages, changed, nil
		}
		// Something changed during this walk: re-walk to confirm the pages
		// agree with each other. Unchanged pages answer 304 for free.
	}
	return pages, changed, nil
}

// restStatusError is a non-2xx REST answer, kept typed so a caller can
// branch on the status (404 → fall back to GraphQL) without string matching.
type restStatusError struct {
	Target string
	Status int
	Body   []byte
}

func (e *restStatusError) Error() string {
	return fmt.Sprintf("REST GET %s: status %d: %s", e.Target, e.Status, restErrorSummary(e.Body))
}
