package blogwatch

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// MaxFetchBytes bounds any single fetched document.
const MaxFetchBytes = 8 << 20

// CheckAllowed reports why raw may not be fetched, or nil. The scheme must be
// https and the host an exact, case-insensitive member of allowedHosts.
func CheckAllowed(raw string, allowedHosts []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("not an absolute URL: %q", raw)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not https", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range allowedHosts {
		if strings.ToLower(strings.TrimSpace(h)) == host {
			return nil
		}
	}
	return fmt.Errorf("host %q is not in blog_watch.allowed_hosts", host)
}

// Fetcher fetches allowlisted documents. Client is injectable for tests.
type Fetcher struct {
	Client       *http.Client
	AllowedHosts []string
}

// Fetch returns the body of raw after the allowlist check passes. No network
// I/O happens for a refused URL.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (string, error) {
	if err := CheckAllowed(raw, f.AllowedHosts); err != nil {
		return "", err
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "nightgauge-blog-watch")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// A redirect off the allowlist is refused too.
	if err := CheckAllowed(resp.Request.URL.String(), f.AllowedHosts); err != nil {
		return "", fmt.Errorf("redirected: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", raw, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

var (
	locRE  = regexp.MustCompile(`(?is)<(?:loc|link)>\s*([^<\s]+)\s*</(?:loc|link)>`)
	hrefRE = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
)

// ExtractPostURLs returns the de-duplicated, sorted, normalised post URLs in
// doc: <loc>, <link>, and href= values that are absolute https URLs on the
// index's host and start with prefix.
func ExtractPostURLs(doc, indexURL, prefix string) []string {
	base, err := url.Parse(indexURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		n, ok := Normalize(html.UnescapeString(strings.TrimSpace(raw)))
		if !ok || seen[n] {
			return
		}
		u, _ := url.Parse(n)
		if !strings.EqualFold(u.Hostname(), base.Hostname()) || !strings.HasPrefix(n, strings.TrimSuffix(prefix, "/")+"/") {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, m := range locRE.FindAllStringSubmatch(doc, -1) {
		add(m[1])
	}
	for _, m := range hrefRE.FindAllStringSubmatch(doc, -1) {
		add(m[1])
	}
	sort.Strings(out)
	return out
}

// Normalize drops the query, fragment and trailing slash of an absolute https
// URL and lowercases its host.
func Normalize(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u.String(), true
}

// ValidateSources splits sources into those whose index is allowlisted and
// skipped entries for the rest.
func ValidateSources(sources []Source, allowedHosts []string) ([]Source, []Skipped) {
	var ok []Source
	var skipped []Skipped
	for _, s := range sources {
		if err := CheckAllowed(s.IndexURL, allowedHosts); err != nil {
			skipped = append(skipped, Skipped{Source: s.Name, Reason: err.Error()})
			continue
		}
		if s.PostURLPrefix == "" {
			skipped = append(skipped, Skipped{Source: s.Name, Reason: "post_url_prefix is empty"})
			continue
		}
		ok = append(ok, s)
	}
	return ok, skipped
}

// Detect fetches every allowlisted source and returns the posts not in the
// log. A source that fails or yields no post URL is reported as skipped.
func Detect(ctx context.Context, f *Fetcher, sources []Source, log []Decision) DetectResult {
	valid, skipped := ValidateSources(sources, f.AllowedHosts)
	res := DetectResult{New: []Post{}, Skipped: skipped, KnownCount: len(log)}
	if res.Skipped == nil {
		res.Skipped = []Skipped{}
	}
	recorded := RecordedURLs(log)
	for _, s := range valid {
		doc, err := f.Fetch(ctx, s.IndexURL)
		if err != nil {
			res.Skipped = append(res.Skipped, Skipped{Source: s.Name, Reason: err.Error()})
			continue
		}
		urls := ExtractPostURLs(doc, s.IndexURL, s.PostURLPrefix)
		if len(urls) == 0 {
			res.Skipped = append(res.Skipped, Skipped{Source: s.Name, Reason: "index yielded no post URL under " + s.PostURLPrefix})
			continue
		}
		for _, u := range New(urls, recorded) {
			res.New = append(res.New, Post{URL: u, Source: s.Name})
			recorded[u] = true
		}
	}
	return res
}

var (
	scriptRE = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	tagRE    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRE  = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankRE  = regexp.MustCompile(`\n\s*\n+`)
)

// HTMLToText strips scripts, styles and tags and collapses whitespace.
func HTMLToText(doc string) string {
	s := scriptRE.ReplaceAllString(doc, " ")
	s = tagRE.ReplaceAllString(s, "\n")
	s = html.UnescapeString(s)
	s = spaceRE.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	return strings.TrimSpace(blankRE.ReplaceAllString(s, "\n\n"))
}
