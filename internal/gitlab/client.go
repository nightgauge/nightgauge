package gitlab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// Version is the application version embedded in the User-Agent header.
// Set this at startup via ldflags or direct assignment; defaults to "dev".
var Version = "dev"

// ErrRateLimitGated is returned when the SharedRateLimitTracker reports a fresh
// reading whose remaining quota is below the configured floor and sleeping
// until the reset window would exceed defaultRateLimitSleepCapSecs.
var ErrRateLimitGated = errors.New("gitlab rate limit gated by SharedRateLimitTracker")

// rateLimitFloorEnv is the env var that overrides the default gate threshold.
const rateLimitFloorEnv = "NIGHTGAUGE_GITLAB_RATELIMIT_FLOOR"

// defaultRateLimitFloor is the threshold below which the gate trips.
// GitLab instances tend to have tighter budgets than GitHub, so 50 is used
// instead of GitHub's 100.
const defaultRateLimitFloor = 50

// rateLimitFloor reads the gate threshold lazily.
func rateLimitFloor() int {
	v := os.Getenv(rateLimitFloorEnv)
	if v == "" {
		return defaultRateLimitFloor
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return defaultRateLimitFloor
	}
	return n
}

// DefaultBaseURL is the GitLab.com REST root. Adapter callers point at
// self-hosted instances by passing the alternate base URL to NewClient.
const DefaultBaseURL = "https://gitlab.com"

const apiPrefix = "/api/v4"

// buildUserAgent constructs the User-Agent string for a given base URL.
// Format: Nightgauge/<version> (gitlab; <instance-host>)
func buildUserAgent(baseURL string) string {
	host := "gitlab.com"
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	v := Version
	if v == "" {
		v = "dev"
	}
	return "Nightgauge/" + v + " (gitlab; " + host + ")"
}

// defaultTimeout caps each REST call. Generous enough for slow self-hosted
// instances; short enough that a hung instance does not stall the pipeline.
const defaultTimeout = 30 * time.Second

// Client is the HTTP handle the GitLab adapter uses for every REST call.
// Construction defaults can be overridden via ClientOption — most notably
// WithHTTPClient for CA/proxy transport built by BuildTransport.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	userAgent  string

	// authHeaderName / authHeaderValue replace the default PRIVATE-TOKEN
	// injection when an alternate auth method is in use (OAuth2 Bearer,
	// JOB-TOKEN for CI, or Basic for deploy tokens). Set by WithAuthHeader.
	authHeaderName  string
	authHeaderValue string

	// resolvedMethod records which auth method was selected at construction
	// time so that AuthAdapter can dispatch the correct validation endpoint.
	resolvedMethod authMethod

	// deployUser holds the username component for deploy-token Basic auth.
	deployUser string

	editionMu    sync.Mutex
	editionCache *editionProbe

	// trackerMu guards tracker and trackerInstance.
	trackerMu       sync.Mutex
	tracker         *SharedRateLimitTracker
	trackerInstance string

	// gateLogger is used for the one-line "gated" decision log.
	// nil → use the default log package.
	gateLogger func(format string, args ...interface{})

	// repoIDMu guards repoIDCache.
	repoIDMu    sync.Mutex
	repoIDCache map[string]*repoIDEntry
}

// repoIDEntry is one cached RepositoryID result.
type repoIDEntry struct {
	numericID int
	globalID  string
	checkedAt time.Time
}

// ClientOption configures a Client at construction time.
type ClientOption func(*Client)

// WithHTTPClient swaps the default *http.Client for a caller-supplied one.
// Used by #3353 to inject a transport with custom CA / proxy support.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithUserAgent overrides the default User-Agent header.
func WithUserAgent(ua string) ClientOption {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithAuthHeader overrides the HTTP authentication header injected by do /
// doGraphQL / doRaw. Use this when the auth mechanism is not PRIVATE-TOKEN
// (e.g. "Authorization" + "Bearer <token>" for OAuth2, "JOB-TOKEN" + token
// for CI job tokens, "Authorization" + "Basic <encoded>" for deploy tokens).
func WithAuthHeader(name, value string) ClientOption {
	return func(c *Client) {
		if name != "" {
			c.authHeaderName = name
			c.authHeaderValue = value
		}
	}
}

// WithResolvedMethod records the authentication method that was selected at
// construction time. AuthAdapter reads this field to dispatch the correct
// validation endpoint in CheckTokenScopes and Whoami.
func WithResolvedMethod(m authMethod, deployUser string) ClientOption {
	return func(c *Client) {
		c.resolvedMethod = m
		c.deployUser = deployUser
	}
}

// NewClient builds a GitLab REST client targeting the given base URL with
// PRIVATE-TOKEN authentication. Empty baseURL falls back to DefaultBaseURL.
// Auth header defaults to PRIVATE-TOKEN; use WithAuthHeader to override.
func NewClient(baseURL, token string, opts ...ClientOption) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	c := &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		token:           token,
		userAgent:       buildUserAgent(baseURL),
		authHeaderName:  "PRIVATE-TOKEN",
		authHeaderValue: token,
		resolvedMethod:  authMethodPAT,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewClientWithHTTP constructs a Client using a pre-built *http.Client.
// Mirrors internal/github's NewClientWithURL pattern for test injection and
// transport customization — use BuildTransport to produce the http.Client.
func NewClientWithHTTP(baseURL, token string, h *http.Client) *Client {
	return NewClient(baseURL, token, WithHTTPClient(h))
}

// NewClientFromConfig constructs a Client from a ForgeConfigEntry.
// configDir is the directory the config file was loaded from (for CA bundle
// path resolution). token overrides entry.TokenEnv when non-empty.
//
// Auth method priority (entry.AuthMethod):
//   - "ci_job_token": requires CI=true and CI_JOB_TOKEN env; uses JOB-TOKEN header
//   - "oauth2":       uses Authorization: Bearer; token must be supplied via token arg
//   - "deploy_token": uses Authorization: Basic; username derived from <TokenEnv>_USER
//   - "pat" / "token" / "" (default): uses PRIVATE-TOKEN header
func NewClientFromConfig(entry *config.ForgeConfigEntry, configDir, token string) (*Client, error) {
	baseURL := entry.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	// Validate base URL is parseable before proceeding.
	if u, err := url.Parse(baseURL); err != nil || u.Host == "" {
		return nil, fmt.Errorf("gitlab: malformed base_url %q: must be a valid URL with host", baseURL)
	}

	httpClient, err := BuildTransport(*entry, configDir)
	if err != nil {
		return nil, fmt.Errorf("gitlab: build transport: %w", err)
	}

	switch entry.AuthMethod {
	case "ci_job_token":
		if !ciJobTokenAvailable() {
			return nil, fmt.Errorf("gitlab: auth_method=ci_job_token requires CI=true and CI_JOB_TOKEN to be set")
		}
		ciToken := os.Getenv("CI_JOB_TOKEN")
		return NewClient(baseURL, ciToken,
			WithHTTPClient(httpClient),
			WithAuthHeader("JOB-TOKEN", ciToken),
			WithResolvedMethod(authMethodCIJobToken, ""),
		), nil

	case "oauth2":
		if token == "" && entry.TokenEnv != "" {
			token = os.Getenv(entry.TokenEnv)
		}
		if token == "" {
			return nil, fmt.Errorf("gitlab: auth_method=oauth2 requires a token (set token_env or pass token directly)")
		}
		return NewClient(baseURL, token,
			WithHTTPClient(httpClient),
			WithAuthHeader("Authorization", "Bearer "+token),
			WithResolvedMethod(authMethodOAuth2, ""),
		), nil

	case "deploy_token":
		if token == "" && entry.TokenEnv != "" {
			token = os.Getenv(entry.TokenEnv)
		}
		if token == "" {
			return nil, fmt.Errorf("gitlab: auth_method=deploy_token requires a token (set token_env or pass token directly)")
		}
		// Username is read from <TOKEN_ENV>_USER, falling back to empty string.
		deployUser := ""
		if entry.TokenEnv != "" {
			deployUser = os.Getenv(entry.TokenEnv + "_USER")
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(deployUser + ":" + token))
		return NewClient(baseURL, token,
			WithHTTPClient(httpClient),
			WithAuthHeader("Authorization", "Basic "+encoded),
			WithResolvedMethod(authMethodDeployToken, deployUser),
		), nil

	default: // "pat", "token", ""
		if token == "" && entry.TokenEnv != "" {
			token = os.Getenv(entry.TokenEnv)
		}
		if token == "" {
			return nil, fmt.Errorf("gitlab: no token available for forge (set token_env or pass token directly)")
		}
		return NewClient(baseURL, token,
			WithHTTPClient(httpClient),
			WithResolvedMethod(authMethodPAT, ""),
		), nil
	}
}

// WithRateLimitTracker attaches a SharedRateLimitTracker to the client. Every
// REST and GraphQL request will consult the tracker before dispatching; headers
// from every response are fed back into the tracker. The instance key
// partitions entries — typically the GitLab hostname.
//
// Returns the client for fluent chaining.
func (c *Client) WithRateLimitTracker(tracker *SharedRateLimitTracker, instance string) *Client {
	c.trackerMu.Lock()
	defer c.trackerMu.Unlock()
	c.tracker = tracker
	c.trackerInstance = instance
	if tracker != nil {
		c.installRateLimitTransport()
	}
	return c
}

// RateLimitTracker returns the attached SharedRateLimitTracker, or nil.
func (c *Client) RateLimitTracker() *SharedRateLimitTracker {
	c.trackerMu.Lock()
	defer c.trackerMu.Unlock()
	return c.tracker
}

// RateLimitTrackerUser returns the instance key used by this client's tracker.
func (c *Client) RateLimitTrackerUser() string {
	c.trackerMu.Lock()
	defer c.trackerMu.Unlock()
	return c.trackerInstance
}

// installRateLimitTransport wraps the underlying http.Client's Transport with
// rateLimitHeaderTransport. Must be called with trackerMu held.
func (c *Client) installRateLimitTransport() {
	base := c.httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	// Avoid double-wrapping.
	if _, already := base.(*rateLimitHeaderTransport); already {
		return
	}
	c.httpClient.Transport = &rateLimitHeaderTransport{
		base:   base,
		client: c,
	}
}

// rateLimitHeaderTransport intercepts HTTP responses to extract GitLab's
// RateLimit-* headers and feed them into the client's tracker.
type rateLimitHeaderTransport struct {
	base   http.RoundTripper
	client *Client
}

func (t *rateLimitHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// GitLab uses RateLimit-Remaining (no X- prefix on CE ≥16.x).
	remaining := resp.Header.Get("RateLimit-Remaining")
	if remaining == "" {
		// Also check X-RateLimit-* for older instances or GitLab SaaS variants.
		remaining = resp.Header.Get("X-RateLimit-Remaining")
	}
	if remaining == "" {
		return resp, err
	}
	limit := resp.Header.Get("RateLimit-Limit")
	if limit == "" {
		limit = resp.Header.Get("X-RateLimit-Limit")
	}
	reset := resp.Header.Get("RateLimit-Reset")
	if reset == "" {
		reset = resp.Header.Get("X-RateLimit-Reset")
	}

	c := t.client
	if c == nil {
		return resp, err
	}
	c.trackerMu.Lock()
	tracker := c.tracker
	instance := c.trackerInstance
	c.trackerMu.Unlock()
	if tracker == nil {
		return resp, err
	}
	// Best-effort: a tracker write failure must never break a request.
	_, _ = tracker.SetFromHeaders(instance, remaining, limit, reset)
	return resp, err
}

// checkRateLimitGate consults the attached tracker and, when a fresh reading
// shows remaining quota below the floor and the reset window has not elapsed,
// sleeps until reset (capped at defaultRateLimitSleepCapSecs). If sleep would
// exceed the cap, returns ErrRateLimitGated.
func (c *Client) checkRateLimitGate(ctx context.Context) error {
	c.trackerMu.Lock()
	tracker := c.tracker
	instance := c.trackerInstance
	logger := c.gateLogger
	c.trackerMu.Unlock()
	if tracker == nil {
		return nil
	}
	entry, fresh, err := tracker.Get(instance)
	if err != nil || entry == nil || !fresh {
		return nil
	}
	floor := rateLimitFloor()
	if entry.Remaining >= floor {
		return nil
	}
	now := time.Now().Unix()
	if entry.ResetAt <= 0 || entry.ResetAt <= now {
		return nil
	}
	sleepSecs := entry.ResetAt - now
	if logger == nil {
		logger = log.Printf
	}
	logger("gitlab: rate limit gate tripped (remaining=%d floor=%d sleep_secs=%d instance=%q)",
		entry.Remaining, floor, sleepSecs, instance)

	if sleepSecs > defaultRateLimitSleepCapSecs {
		return fmt.Errorf("%w: remaining=%d floor=%d reset_in=%ds exceeds cap",
			ErrRateLimitGated, entry.Remaining, floor, sleepSecs)
	}

	select {
	case <-time.After(time.Duration(sleepSecs) * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// graphqlResponse is the top-level shape of a GitLab GraphQL response.
type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// doGraphQL issues a POST to <baseURL>/api/graphql with the given query and
// variables. Returns the raw `data` JSON on success, or an error when the
// response contains errors and no data.
func (c *Client) doGraphQL(ctx context.Context, query string, variables map[string]interface{}) ([]byte, error) {
	if err := c.checkRateLimitGate(ctx); err != nil {
		return nil, err
	}

	endpoint := c.baseURL + "/api/graphql"

	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("gitlab graphql: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gitlab graphql: build request: %w", err)
	}
	if c.authHeaderName != "" && c.authHeaderValue != "" {
		req.Header.Set(c.authHeaderName, c.authHeaderValue)
	} else if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab graphql: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gitlab graphql: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Cap the error body to avoid leaking internals (stack traces, auth hints).
		snippet := string(respBody)
		if len(snippet) > 512 {
			snippet = snippet[:512] + "…"
		}
		return nil, fmt.Errorf("gitlab graphql: status %d: %s", resp.StatusCode, snippet)
	}

	var gqlResp graphqlResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return nil, fmt.Errorf("gitlab graphql: decode response: %w", err)
	}

	if len(gqlResp.Errors) > 0 && len(gqlResp.Data) == 0 {
		msgs := make([]string, 0, len(gqlResp.Errors))
		for _, e := range gqlResp.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("gitlab graphql: errors: %s", strings.Join(msgs, "; "))
	}

	return gqlResp.Data, nil
}

// projectPath URL-encodes "owner/repo" as the GitLab :id path segment
// per the project-path-as-id convention.
func projectPath(owner, repo string) string {
	return url.PathEscape(owner + "/" + repo)
}

// buildURL composes a fully-qualified REST URL from a path under /api/v4
// with optional query parameters. The path argument starts at "/projects/…".
func (c *Client) buildURL(path string, query url.Values) string {
	base := c.baseURL + apiPrefix + path
	if len(query) == 0 {
		return base
	}
	if strings.Contains(path, "?") {
		return base + "&" + query.Encode()
	}
	return base + "?" + query.Encode()
}

// do issues an HTTP request and decodes a JSON response into out (when
// non-nil). It applies authentication, JSON content negotiation, and
// status → forge sentinel mapping. The returned *http.Response is the raw
// response, useful for callers that need to inspect Link headers
// post-decode.
func (c *Client) do(ctx context.Context, method, fullURL string, body any, out any, op string) (*http.Response, error) {
	if err := c.checkRateLimitGate(ctx); err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("gitlab %s: encode body: %w", op, err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("gitlab %s: build request: %w", op, err)
	}
	if c.authHeaderName != "" && c.authHeaderValue != "" {
		req.Header.Set(c.authHeaderName, c.authHeaderValue)
	} else if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab %s: %w", op, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := readSnippet(resp.Body)
		_ = resp.Body.Close()
		return resp, mapStatus(op, resp.StatusCode, snippet)
	}

	if out != nil {
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(out); err != nil && err != io.EOF {
			_ = resp.Body.Close()
			return resp, fmt.Errorf("gitlab %s: decode: %w", op, err)
		}
	}
	_ = resp.Body.Close()
	return resp, nil
}

// readSnippet reads up to 1 KiB from r so the error message can include
// a recognisable hint of what GitLab said. Caller is responsible for
// closing r.
func readSnippet(r io.Reader) (string, error) {
	buf := make([]byte, 1024)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	return string(buf[:n]), nil
}

// doRaw issues a request and returns the response body bytes plus the
// response headers. Used by the page iterator which needs both the
// decoded body and the Link header for next-page traversal.
func (c *Client) doRaw(ctx context.Context, method, fullURL string, body any, op string) ([]byte, http.Header, error) {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("gitlab %s: encode body: %w", op, err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab %s: build request: %w", op, err)
	}
	if c.authHeaderName != "" && c.authHeaderValue != "" {
		req.Header.Set(c.authHeaderName, c.authHeaderValue)
	} else if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab %s: %w", op, err)
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := ""
		if readErr == nil {
			snippet = string(bodyBytes)
		}
		return nil, resp.Header, mapStatus(op, resp.StatusCode, snippet)
	}
	if readErr != nil {
		return nil, resp.Header, fmt.Errorf("gitlab %s: read body: %w", op, readErr)
	}
	return bodyBytes, resp.Header, nil
}

// redactCredentials replaces the userinfo component of a URL with a placeholder
// so that connection error messages never surface credentials in logs.
func redactCredentials(rawURL string) string {
	if !strings.Contains(rawURL, "@") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return "<credentials-redacted>"
	}
	u.User = url.User("<credentials-redacted>")
	return u.String()
}

// RepositoryID resolves a GitLab project path slug (e.g. "group/repo") to its
// numeric project ID and GraphQL global ID. Results are cached for 15s to
// match SharedTrackerMinCheckIntervalSecs.
//
// Resolution strategy:
//  1. Return from cache when the entry is younger than 15 s.
//  2. REST GET /api/v4/projects/{encoded-slug} — parse id and global_id.
//  3. When global_id is absent (GitLab CE < 15), fall back to a GraphQL query.
//
// Connection errors include the resolved URL with credentials redacted.
func (c *Client) RepositoryID(ctx context.Context, slug string) (numericID int, globalID string, err error) {
	c.repoIDMu.Lock()
	if c.repoIDCache == nil {
		c.repoIDCache = make(map[string]*repoIDEntry)
	}
	if e, ok := c.repoIDCache[slug]; ok {
		if time.Since(e.checkedAt) < SharedTrackerMinCheckIntervalSecs*time.Second {
			n, g := e.numericID, e.globalID
			c.repoIDMu.Unlock()
			return n, g, nil
		}
	}
	c.repoIDMu.Unlock()

	encodedSlug := url.PathEscape(slug)
	restURL := c.baseURL + apiPrefix + "/projects/" + encodedSlug

	var proj struct {
		ID       int    `json:"id"`
		GlobalID string `json:"global_id"`
	}
	if _, apiErr := c.do(ctx, "GET", restURL, nil, &proj, "repository-id"); apiErr != nil {
		safeURL := redactCredentials(restURL)
		return 0, "", fmt.Errorf("gitlab: resolve project ID for %q via %s: %w", slug, safeURL, apiErr)
	}

	numericID = proj.ID
	globalID = proj.GlobalID

	// GitLab REST API on older CE versions omits global_id; fall back to GraphQL.
	// GitLab GraphQL is at /api/graphql (singular), not /graphql — common gotcha.
	if globalID == "" && numericID > 0 {
		const gqlQuery = `query($path: ID!) { project(fullPath: $path) { id } }`
		data, gqlErr := c.doGraphQL(ctx, gqlQuery, map[string]interface{}{"path": slug})
		if gqlErr == nil {
			var gqlResp struct {
				Project struct {
					ID string `json:"id"`
				} `json:"project"`
			}
			if jsonErr := json.Unmarshal(data, &gqlResp); jsonErr == nil {
				globalID = gqlResp.Project.ID
			}
		}
	}

	c.repoIDMu.Lock()
	c.repoIDCache[slug] = &repoIDEntry{
		numericID: numericID,
		globalID:  globalID,
		checkedAt: time.Now(),
	}
	c.repoIDMu.Unlock()

	return numericID, globalID, nil
}

// linkHeader is a parsed Link header from a paginated GitLab response. The
// fields are nil when the corresponding rel is absent.
type linkHeader struct {
	Next  *url.URL
	Prev  *url.URL
	First *url.URL
	Last  *url.URL
}

// parseLinkHeader implements the subset of RFC 5988 link-header parsing
// GitLab uses for pagination. Format example:
//
//	<https://.../issues?page=2>; rel="next", <https://.../issues?page=10>; rel="last"
//
// Unknown rels are ignored. Malformed entries are silently skipped — the
// pagination contract is "walk while next is set".
func parseLinkHeader(h string) linkHeader {
	out := linkHeader{}
	if h == "" {
		return out
	}
	// Split on commas at the top level. RFC 5988 allows commas inside
	// quoted parameters, but GitLab never emits those, so a simple split
	// is safe here.
	for _, raw := range strings.Split(h, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// Extract <URL>; params...
		end := strings.Index(raw, ">")
		if !strings.HasPrefix(raw, "<") || end < 0 {
			continue
		}
		urlStr := raw[1:end]
		paramsStr := raw[end+1:]
		_, params, err := mime.ParseMediaType("dummy" + paramsStr)
		if err != nil {
			continue
		}
		rel := params["rel"]
		u, err := url.Parse(urlStr)
		if err != nil {
			continue
		}
		switch rel {
		case "next":
			out.Next = u
		case "prev":
			out.Prev = u
		case "first":
			out.First = u
		case "last":
			out.Last = u
		}
	}
	return out
}
