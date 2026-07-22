package integrationprobe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// placeholderRE matches `:identifier` path segments like `:id`, `:teamId`.
var placeholderRE = regexp.MustCompile(`:[A-Za-z][A-Za-z0-9_]*`)

// placeholderSentinel is substituted into placeholder path segments before
// the request is issued. The probe is interested in route existence, not
// response content, so any non-empty value works.
const placeholderSentinel = "probe"

// bodyPreviewLimit caps the size of the captured response body to keep
// the report small. Stub-detection is performed against this trimmed value.
const bodyPreviewLimit = 256

// ResolvePath substitutes `:placeholder` segments with the sentinel so the
// path can be requested. Exposed for tests and for callers that want the
// resolved path before a probe runs.
func ResolvePath(path string) string {
	return placeholderRE.ReplaceAllString(path, placeholderSentinel)
}

// authHeader returns the (header, value) pair to set for a given auth mode.
// Empty header name signals "no header to set".
func authHeader(authMode, token string) (string, string) {
	if token == "" {
		return "", ""
	}
	switch authMode {
	case AuthModeJWT:
		return "Authorization", "Bearer " + token
	case AuthModeLicense:
		return "X-License-Key", token
	}
	return "", ""
}

// Probe walks every entry in the manifest, issues an HTTP request via
// client, and categorizes the response into one of the six categories.
// Categorization is purely status-code + body-shape based — no schema
// inference, no LLM calls.
func Probe(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	authMode string,
	token string,
	manifest *EndpointManifest,
) (*ProbeReport, error) {
	if client == nil {
		return nil, fmt.Errorf("nil http client")
	}
	if manifest == nil {
		return nil, fmt.Errorf("nil manifest")
	}
	baseURL = strings.TrimRight(baseURL, "/")

	report := &ProbeReport{
		V:           1,
		BaseURL:     baseURL,
		AuthMode:    authMode,
		Categories:  map[string]int{},
		Results:     []ProbeResult{},
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, c := range AllCategories {
		report.Categories[c] = 0
	}

	groupNames := make([]string, 0, len(manifest.Groups))
	for g := range manifest.Groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	transportErrors := 0
	for _, group := range groupNames {
		for _, entry := range manifest.Groups[group] {
			result := probeOne(ctx, client, baseURL, group, entry, authMode, token)
			if result.Error != "" {
				transportErrors++
			}
			report.Categories[result.Category]++
			report.Results = append(report.Results, result)
		}
	}

	if len(report.Results) > 0 && transportErrors == len(report.Results) {
		report.Unreachable = true
	}

	return report, nil
}

// probeOne issues a single request and returns its categorized result.
// Caller-supplied entry.AuthMode (if set) overrides the global authMode.
func probeOne(
	ctx context.Context,
	client *http.Client,
	baseURL, group string,
	entry EndpointEntry,
	authMode, token string,
) ProbeResult {
	resolved := ResolvePath(entry.Path)
	url := baseURL + resolved
	effectiveAuth := authMode
	if entry.AuthMode != "" {
		effectiveAuth = entry.AuthMode
	}

	result := ProbeResult{
		Group:        group,
		Method:       strings.ToUpper(entry.Method),
		Path:         entry.Path,
		ResolvedPath: resolved,
	}

	req, err := http.NewRequestWithContext(ctx, result.Method, url, nil)
	if err != nil {
		result.Category = CategoryBroken
		result.Error = "build_request: " + err.Error()
		return result
	}
	if h, v := authHeader(effectiveAuth, token); h != "" {
		req.Header.Set(h, v)
	}

	start := time.Now()
	resp, err := client.Do(req)
	result.DurationMs = int(time.Since(start) / time.Millisecond)
	if err != nil {
		result.Category = CategoryBroken
		result.Error = "transport_error: " + err.Error()
		return result
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, bodyPreviewLimit+1))
	if len(body) > bodyPreviewLimit {
		body = body[:bodyPreviewLimit]
	}
	result.StatusCode = resp.StatusCode
	result.BodyPreview = string(body)
	result.Category = Categorize(resp.StatusCode, body)
	return result
}

// Categorize maps an HTTP response to one of the six probe categories.
// Pure rules — no schema awareness, no LLM. See ADR-002.
func Categorize(statusCode int, body []byte) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		if isStubBody(body) {
			return CategoryStub
		}
		return CategoryWorking
	case statusCode == 401:
		return CategoryAuthRequired
	case statusCode == 403:
		return CategoryAuthMismatch
	case statusCode == 404:
		return CategoryNotFound
	case statusCode >= 500 && statusCode < 600:
		return CategoryBroken
	default:
		// 3xx, 4xx (other than 401/403/404), and any other unusual status.
		return CategoryBroken
	}
}

// isStubBody returns true when the body is empty, very short, or a literal
// JSON empty value. Cheap and deterministic — see ADR-002.
func isStubBody(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) < 4 {
		return true
	}
	switch trimmed {
	case "{}", "[]", "null":
		return true
	}
	return false
}
