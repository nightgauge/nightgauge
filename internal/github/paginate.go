package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// restListPerPage is GitHub's per_page ceiling for REST list endpoints.
const restListPerPage = 100

// maxRESTListPages bounds a paginated read. At 100 items per page this is
// 10,000 items — far beyond any real commit's check-runs or statuses — and it
// exists only so a forge that keeps answering with a next link cannot hold the
// caller forever. Reaching it is an error, never a silent truncation: a
// truncated list is exactly the defect pagination is here to prevent (#1681).
const maxRESTListPages = 100

// withPerPage returns rawURL with per_page=100 added when the caller has not
// set per_page itself. GitHub's default page size is 30.
func withPerPage(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", rawURL, err)
	}
	q := u.Query()
	if q.Get("per_page") == "" {
		q.Set("per_page", fmt.Sprintf("%d", restListPerPage))
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// nextPageURL returns the rel="next" target of a GitHub Link header, or "" on
// the last page. The header has the RFC 8288 shape
//
//	<https://api.github.com/...&page=2>; rel="next", <...&page=5>; rel="last"
func nextPageURL(link string) string {
	for _, part := range strings.Split(link, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		target := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, param := range segments[1:] {
			param = strings.TrimSpace(param)
			if !strings.HasPrefix(param, "rel=") {
				continue
			}
			for _, rel := range strings.Fields(strings.Trim(strings.TrimPrefix(param, "rel="), `"`)) {
				if rel == "next" {
					return strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
				}
			}
		}
	}
	return ""
}

// getAllPages GETs rawURL with per_page=100 and then every rel="next" page,
// handing each 200 body to decodePage in order.
//
// A non-200 on any page is passed to onStatus with the response and its body.
// onStatus returning a non-nil error aborts the read with that error; returning
// nil ends the read cleanly with whatever was already decoded (a caller that
// treats 404 as "nothing configured" uses this).
func (s *CIService) getAllPages(ctx context.Context, rawURL string, onStatus func(*http.Response, []byte) error, decodePage func(io.Reader) error) error {
	next, err := withPerPage(rawURL)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for page := 1; next != ""; page++ {
		if page > maxRESTListPages {
			return fmt.Errorf("paginated read of %s exceeded %d pages", rawURL, maxRESTListPages)
		}
		if seen[next] {
			return fmt.Errorf("paginated read of %s: next link repeats %s", rawURL, next)
		}
		seen[next] = true

		link, done, err := s.getOnePage(ctx, next, onStatus, decodePage)
		if err != nil || done {
			return err
		}
		next = nextPageURL(link)
	}
	return nil
}

// getOnePage fetches one page. done reports that onStatus accepted a non-200
// as the end of the read.
func (s *CIService) getOnePage(ctx context.Context, pageURL string, onStatus func(*http.Response, []byte) error, decodePage func(io.Reader) error) (link string, done bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if statusErr := onStatus(resp, body); statusErr != nil {
			return "", false, statusErr
		}
		return "", true, nil
	}
	if err := decodePage(resp.Body); err != nil {
		return "", false, err
	}
	return resp.Header.Get("Link"), false, nil
}
