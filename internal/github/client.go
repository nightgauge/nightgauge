// Package github provides a typed GitHub GraphQL client with rate limiting
// and automatic retry for the nightgauge CLI.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shurcooL/graphql"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

// ErrRateLimitGated is returned by query/mutate/REST helpers when the
// SharedRateLimitTracker reports a fresh reading whose remaining quota is
// below the configured floor and the rate-limit window has not yet reset.
//
// Callers should treat this as a signal to back off (not retry immediately).
// It is distinct from rate-limit errors returned by the GitHub API: those are
// reactive (we already tried), while this is proactive (we declined to try).
var ErrRateLimitGated = errors.New("github rate limit gated by SharedRateLimitTracker")

// rateLimitFloorEnv is the env var that overrides the default gate threshold.
// Set to a non-negative integer (decimal) to override; invalid values fall
// back to the default.
const rateLimitFloorEnv = "NIGHTGAUGE_GITHUB_RATELIMIT_FLOOR"

// defaultRateLimitFloor is the threshold below which the tracker gate trips
// when no env override is provided. 100 leaves enough budget for a handful
// of operations after gating begins (graceful degradation).
const defaultRateLimitFloor = 100

// rateLimitFloor reads the gate threshold lazily — cheap on every call and
// avoids needing process restarts to retune. Negative or unparseable values
// fall back to the default.
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

// Client wraps the GitHub GraphQL API with rate limiting and retry logic.
type Client struct {
	gql        *graphql.Client
	http       *http.Client
	limiter    *rate.Limiter
	mu         sync.Mutex
	graphqlURL string

	// tracker, when non-nil, is consulted before every query/mutate. If a
	// fresh entry exists with remaining < floor, calls return ErrRateLimitGated
	// without dispatching. Headers from every HTTP response are also fed back
	// into the tracker so freshness stays current without a separate probe.
	tracker     *SharedRateLimitTracker
	trackerUser string

	// rateLimitWaitOnGate flips the pre-call gate from fail-fast to
	// wait-for-reset. When false (default), a fresh below-floor reading makes
	// query/mutate/REST return ErrRateLimitGated immediately (the right
	// behavior for dispatch-preflight callers that should skip and try other
	// work). When true, the gate instead WAITS for the reset window — bounded
	// by maxFullExhaustionWait AND the caller's context — then proceeds. Set on
	// in-flight / recovery clients (board move-status, revert-status, pr ops)
	// so a quota dip pauses-and-continues instead of leaving an issue stuck.
	// The per-caller context timeout is the real control: a long-ctx recovery
	// op waits the reset out; a short-ctx call bails fast. Issue #3976.
	rateLimitWaitOnGate bool

	// gateLogger is used for the one-line "gated" decision log. Tests
	// override this to capture log output deterministically. nil → use the
	// default log package.
	gateLogger func(format string, args ...interface{})
}

// TokenResolver is the interface for config-based token resolution.
// Accepts any type that has a ResolveToken method matching this signature.
type TokenResolver interface {
	ResolveToken(owner string) (string, error)
	// SuppressGHWarning returns true when the user has opted out of the
	// deprecation warning emitted on gh CLI fallback.
	SuppressGHWarning() bool
}

// GitHubUserResolver is optionally implemented by a TokenResolver to expose the
// configured gh CLI user (config `github_user` / `github_auth.users[owner]`).
// When present, the gh CLI fallback resolves the token for THAT user via
// `gh auth token --user <user>` rather than whichever account happens to be
// active. This keeps token resolution deterministic and tied to configuration —
// we never silently use the currently logged-in gh account for API calls.
type GitHubUserResolver interface {
	ResolveGitHubUser() string
}

// OwnerGitHubUserResolver is the owner-parameterized form of GitHubUserResolver
// (config implements it via ResolveGitHubUserForOwner). When a resolver exposes
// it, token resolution scopes the gh CLI user to the PASSED owner so a cross-org
// target (e.g. Acme-Community → acmebot) resolves the right
// identity even when the workspace root owner differs (#4068). Preferred over
// the zero-arg GitHubUserResolver when both are implemented.
type OwnerGitHubUserResolver interface {
	ResolveGitHubUserForOwner(owner string) string
}

// configuredGitHubUser returns the configured gh CLI user for the given owner.
// When the resolver is owner-aware (OwnerGitHubUserResolver) its result is
// authoritative for THIS owner: an empty string means "no identity configured
// for this owner" and we must NOT fall back to the zero-arg resolver, which
// resolves the WORKSPACE owner's user and would leak that identity onto an
// unmapped cross-org target (#4068). The zero-arg GitHubUserResolver is consulted
// only for resolvers that do not implement the owner-aware form. A nil resolver
// (or one implementing neither) yields "".
func configuredGitHubUser(cfg TokenResolver, owner string) string {
	if ur, ok := cfg.(OwnerGitHubUserResolver); ok {
		return ur.ResolveGitHubUserForOwner(owner)
	}
	if ur, ok := cfg.(GitHubUserResolver); ok {
		return ur.ResolveGitHubUser()
	}
	return ""
}

// execGHAuthToken obtains a token from the default gh CLI user.
// Replaced in tests to avoid spawning real processes.
var execGHAuthToken = func() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token: empty output")
	}
	return tok, nil
}

// execGHAuthTokenForUser obtains a token from gh CLI for a specific user.
// Replaced in tests to avoid spawning real processes.
//
// The child process runs with ambient GH_TOKEN/GITHUB_TOKEN stripped from its
// environment. gh's token resolution order is GH_TOKEN > GITHUB_TOKEN > keyring,
// so an ambient (wrong-user) env token would otherwise shadow the keyring entry
// and `gh auth token --user acmebot` would return the ambient token —
// silently the wrong identity. Stripping the env forces gh to read THAT user's
// keyring entry, which is the whole point of scoping by --user (#4068).
var execGHAuthTokenForUser = func(user string) (string, error) {
	cmd := exec.Command("gh", "auth", "token", "--user", user)
	cmd.Env = envWithout(os.Environ(), "GH_TOKEN", "GITHUB_TOKEN")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token --user %s: %w", user, err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token --user %s: empty output", user)
	}
	return tok, nil
}

// envWithout returns a copy of env with every entry whose name matches one of
// keys removed (case-sensitive, matching os/exec semantics on POSIX). Used to
// strip ambient GH_TOKEN/GITHUB_TOKEN before shelling out to gh so the child
// reads the keyring entry for a specific --user rather than the ambient token.
func envWithout(env []string, keys ...string) []string {
	if len(keys) == 0 {
		return env
	}
	drop := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if _, skip := drop[name]; skip {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// NewClient creates a GitHub GraphQL client using the GITHUB_TOKEN env var.
func NewClient() (*Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}
	return NewClientWithToken(token), nil
}

// NewClientFromConfig creates a GitHub GraphQL client using the resolution
// priority chain:
//  1. cliToken (--token flag) if non-empty
//  2. cfg.ResolveToken(owner) — per-project or per-org config token
//     3a. When a github_user is configured: the github_user-scoped token
//     (gh auth token --user, ambient env stripped) — authoritative over
//     ambient GITHUB_TOKEN so a repo declaring only github_user acts as that
//     identity, never the ambient (wrong-user) env token (#4068).
//     3b. When NO github_user is configured: GITHUB_TOKEN env var, then the
//     default gh account — the single-identity / CI path is unchanged.
//
// A deprecation warning is emitted to stderr when the gh CLI fallback is used.
// Suppress it by setting github_auth.suppress_gh_warning: true in config.yaml.
//
// Never logs the token value; only env:VAR_NAME references appear in logs.
func NewClientFromConfig(cfg TokenResolver, owner string, cliToken string) (*Client, error) {
	// 1. CLI flag override.
	if cliToken != "" {
		return NewClientWithToken(cliToken), nil
	}

	// 2. Config-based token (per-project or per-org).
	if cfg != nil {
		tok, err := cfg.ResolveToken(owner)
		if err != nil {
			// Log as warning only — fall through to next tier.
			fmt.Fprintf(os.Stderr, "warning: config token resolution failed: %v\n", err)
		} else if tok != "" {
			return NewClientWithToken(tok), nil
		}
	}

	// 3a. Configured github_user → the user-scoped token is authoritative over
	// ambient GITHUB_TOKEN. The repo declared a specific identity; we must act
	// as it regardless of whatever ambient token the runner injected.
	if user := configuredGitHubUser(cfg, owner); user != "" {
		warnGHFallback(cfg)
		tok, err := execGHAuthTokenForUser(user)
		if err != nil {
			return nil, fmt.Errorf("no GitHub token available for configured github_user %q (tried config and gh auth token --user; ambient GITHUB_TOKEN is intentionally NOT used for a configured identity): %w", user, err)
		}
		return NewClientWithToken(tok), nil
	}

	// 3b. No github_user configured — ambient GITHUB_TOKEN env var first.
	if envTok := os.Getenv("GITHUB_TOKEN"); envTok != "" {
		return NewClientWithToken(envTok), nil
	}

	// 3c. No github_user, no env token — fall back to the default gh account.
	warnGHFallback(cfg)
	tok, err := execGHAuthToken()
	if err != nil {
		return nil, fmt.Errorf("no GitHub token available (tried config, GITHUB_TOKEN env, and gh CLI): %w", err)
	}
	return NewClientWithToken(tok), nil
}

// warnGHFallback emits the gh CLI deprecation warning to stderr unless the
// resolver opted out via github_auth.suppress_gh_warning.
func warnGHFallback(cfg TokenResolver) {
	if cfg != nil && cfg.SuppressGHWarning() {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: Using gh CLI for token resolution — "+
		"configure github_auth.token in config.yaml for reliable multi-org support\n")
}

// ResolveTokenChain resolves the GitHub token using the same priority chain
// as NewClientFromConfig (skipping the CLI --token flag tier):
//  1. cfg.ResolveToken(owner) — per-project or per-org config token
//     2a. When a github_user is configured: the github_user-scoped token
//     (gh auth token --user, ambient env stripped) — authoritative over the
//     ambient GITHUB_TOKEN env var (#4068).
//     2b. When NO github_user is configured: GITHUB_TOKEN env var, then the
//     default gh account.
//
// The returned token can be fingerprinted without creating a client.
// Returns empty string and error if no token is found.
func ResolveTokenChain(cfg TokenResolver, owner string) (string, error) {
	if cfg != nil {
		tok, err := cfg.ResolveToken(owner)
		if err == nil && tok != "" {
			return tok, nil
		}
	}
	// Configured github_user → user-scoped token wins over ambient env so the
	// configured per-repo identity is authoritative (gh auth token --user runs
	// with ambient GH_TOKEN/GITHUB_TOKEN stripped). Repos with no github_user
	// keep the ambient-env-first single-identity / CI path below.
	if user := configuredGitHubUser(cfg, owner); user != "" {
		return execGHAuthTokenForUser(user)
	}
	if envTok := os.Getenv("GITHUB_TOKEN"); envTok != "" {
		return envTok, nil
	}
	return execGHAuthToken()
}

// NewClientWithToken creates a GitHub GraphQL client with the given token.
func NewClientWithToken(token string) *Client {
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(context.Background(), src)

	c := &Client{
		http:       httpClient,
		limiter:    rate.NewLimiter(rate.Every(time.Second), 5), // 5 req/s
		graphqlURL: "https://api.github.com/graphql",
	}
	c.installHeaderInterceptor()
	c.gql = graphql.NewClient(c.graphqlURL, c.http)
	return c
}

// NewClientWithURL creates a GitHub GraphQL client pointing at the given URL.
// Use in tests only to inject a mock server URL.
func NewClientWithURL(token, graphqlURL string) *Client {
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(context.Background(), src)
	c := &Client{
		http:       httpClient,
		limiter:    rate.NewLimiter(rate.Every(time.Second), 5),
		graphqlURL: graphqlURL,
	}
	c.installHeaderInterceptor()
	c.gql = graphql.NewClient(c.graphqlURL, c.http)
	return c
}

// WithRateLimitTracker attaches a SharedRateLimitTracker to the client so that
// (a) every query/mutate consults it before dispatching, returning
// ErrRateLimitGated when remaining < floor and we're inside the reset window,
// and (b) X-RateLimit-* headers from every HTTP response are fed back into
// the tracker. The user key partitions tracker entries — pass the same
// identifier across processes that share the same GitHub token.
//
// Returns the client for fluent chaining. Pass a nil tracker to detach.
func (c *Client) WithRateLimitTracker(tracker *SharedRateLimitTracker, user string) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tracker = tracker
	c.trackerUser = user
	return c
}

// WithRateLimitWait flips the pre-call rate-limit gate from fail-fast to
// wait-for-reset on this client (see rateLimitWaitOnGate). Use it on clients
// that run in-flight / recovery operations which must eventually succeed —
// board move-status, revert-status, PR create/merge — so a quota dip pauses and
// continues rather than hard-failing and leaving an issue stuck. Dispatch /
// preflight clients should NOT enable it (they want to skip and try other
// work). The wait is always bounded by the caller's context. Issue #3976.
//
// Returns the client for fluent chaining.
func (c *Client) WithRateLimitWait() *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rateLimitWaitOnGate = true
	return c
}

// RateLimitTracker returns the attached SharedRateLimitTracker, or nil when
// none is attached. Exported for tests that need to verify wiring across
// package boundaries (e.g. internal/ipc verifying server / resolver wired
// the tracker per Issue #3417).
func (c *Client) RateLimitTracker() *SharedRateLimitTracker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tracker
}

// RateLimitTrackerUser returns the user key under which this client writes to
// the SharedRateLimitTracker. Empty when no tracker is attached. Exported for
// tests that verify the resolved identity is propagated correctly.
func (c *Client) RateLimitTrackerUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.trackerUser
}

// installHeaderInterceptor wraps the underlying http.Client's Transport with a
// RoundTripper that captures GitHub rate-limit response headers and feeds them
// into the tracker (when one is attached). Idempotent — re-wrapping is safe.
func (c *Client) installHeaderInterceptor() {
	if c.http == nil {
		return
	}
	base := c.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	// Avoid double-wrapping if installHeaderInterceptor is called twice.
	if _, already := base.(*rateLimitHeaderTransport); already {
		return
	}
	c.http.Transport = &rateLimitHeaderTransport{
		base:   base,
		client: c,
	}
}

// rateLimitHeaderTransport intercepts HTTP responses to extract GitHub's
// X-RateLimit-* headers and feed them into the client's tracker. It does NOT
// short-circuit requests — gating happens in query/mutate before the call is
// dispatched, not at the transport layer (which would race with concurrent
// in-flight calls).
type rateLimitHeaderTransport struct {
	base   http.RoundTripper
	client *Client
}

func (t *rateLimitHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// Only act on GitHub API responses. Header names are case-insensitive
	// per RFC 7230; net/http canonicalizes on Get.
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining == "" {
		return resp, err
	}
	limit := resp.Header.Get("X-RateLimit-Limit")
	reset := resp.Header.Get("X-RateLimit-Reset")

	c := t.client
	if c == nil {
		return resp, err
	}
	c.mu.Lock()
	tracker := c.tracker
	user := c.trackerUser
	c.mu.Unlock()
	if tracker == nil {
		return resp, err
	}
	// Best-effort: a tracker write failure must never break a request.
	_, _ = tracker.SetFromHeaders(user, remaining, limit, reset)
	return resp, err
}

// rateLimitResetWait reports whether the attached tracker shows a fresh
// below-floor reading inside an un-elapsed reset window, and if so how long
// until that window resets. Returns (0, false) when not gated: no tracker, a
// missing/stale/unreadable entry, enough quota, or a reset that already passed
// (the next API response will refresh us, so gating would be pointlessly
// pessimistic). The one-line "rate limit gated" decision is logged here so the
// fail-fast and wait paths share identical operator-visible output.
func (c *Client) rateLimitResetWait() (time.Duration, bool) {
	c.mu.Lock()
	tracker := c.tracker
	user := c.trackerUser
	logger := c.gateLogger
	c.mu.Unlock()
	if tracker == nil {
		return 0, false
	}
	entry, fresh, err := tracker.Get(user)
	if err != nil || entry == nil || !fresh {
		return 0, false
	}
	floor := rateLimitFloor()
	if entry.Remaining >= floor {
		return 0, false
	}
	now := time.Now().Unix()
	if entry.ResetAt > 0 && entry.ResetAt <= now {
		return 0, false
	}
	resetIn := time.Duration(entry.ResetAt-now) * time.Second
	if logger == nil {
		logger = log.Printf
	}
	logger("github: rate limit gated (remaining=%d floor=%d reset_in=%s user=%q)",
		entry.Remaining, floor, resetIn, user)
	return resetIn, true
}

// waitRateLimitGate is the pre-call rate-limit gate used by query/mutate/REST.
//
// Default (fail-fast): when the tracker shows a fresh below-floor reading it
// returns ErrRateLimitGated immediately — the right behavior for dispatch /
// preflight callers that should skip and try other work.
//
// With WithRateLimitWait enabled: instead of failing it WAITS for the reset
// window — bounded by maxFullExhaustionWait AND the caller's context — then
// returns nil so the call proceeds (the response path absorbs any residual
// 429). A context that expires mid-wait returns ctx.Err(), so a short-ctx call
// bails fast while a long-ctx recovery op waits the reset out. Issue #3976.
func (c *Client) waitRateLimitGate(ctx context.Context) error {
	resetIn, gated := c.rateLimitResetWait()
	if !gated {
		return nil
	}
	c.mu.Lock()
	wait := c.rateLimitWaitOnGate
	user := c.trackerUser
	logger := c.gateLogger
	c.mu.Unlock()
	if !wait {
		return fmt.Errorf("%w: floor=%d reset_in=%s", ErrRateLimitGated, rateLimitFloor(), resetIn)
	}
	sleep := resetIn + 500*time.Millisecond
	if sleep > maxFullExhaustionWait {
		sleep = maxFullExhaustionWait
	}
	if logger == nil {
		logger = log.Printf
	}
	logger("github: rate limit gated — waiting %s for reset before retrying (user=%q)",
		sleep.Round(time.Second), user)
	select {
	case <-time.After(sleep):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResolveTokenForUser returns the GitHub token for the given gh CLI user.
// Calls: gh auth token --user <user>
// Returns error if gh is not installed or user is not authenticated.
// A deprecation warning is emitted to stderr unless suppressWarning is true.
func ResolveTokenForUser(user string, suppressWarning bool) (string, error) {
	if !suppressWarning {
		fmt.Fprintf(os.Stderr, "warning: Using gh CLI for token resolution — "+
			"configure github_auth.token in config.yaml for reliable multi-org support\n")
	}
	return execGHAuthTokenForUser(user)
}

// NewClientForUser creates a GitHub GraphQL client for the given gh CLI user.
// Falls back to NewClient() (GITHUB_TOKEN env var) if user is empty.
// A deprecation warning is emitted to stderr unless suppressWarning is true.
func NewClientForUser(user string, suppressWarning bool) (*Client, error) {
	if user == "" {
		return NewClient()
	}
	token, err := ResolveTokenForUser(user, suppressWarning)
	if err != nil {
		return nil, err
	}
	return NewClientWithToken(token), nil
}

// ValidateTokenScopes checks the GitHub token for the minimum required scopes
// (repo, project, read:org). It is non-blocking: failures are logged as warnings
// and nil is always returned so the caller is never blocked.
//
// The token value is never logged; only "provided token" appears in messages.
func ValidateTokenScopes(ctx context.Context, token string) {
	if token == "" {
		return
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: token scope validation: could not build request: %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: token scope validation: request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintln(os.Stderr, "warning: GitHub token appears to be invalid (401 Unauthorized)")
		return
	}

	scopeHeader := resp.Header.Get("X-OAuth-Scopes")
	if scopeHeader == "" {
		// Fine-grained PATs don't return this header; skip validation silently.
		return
	}

	requiredScopes := []string{"repo", "project", "read:org"}
	missing := make([]string, 0, len(requiredScopes))
	for _, required := range requiredScopes {
		found := false
		for _, scope := range strings.Split(scopeHeader, ",") {
			if strings.TrimSpace(scope) == required {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "warning: GitHub token may be missing required scopes: %s (have: %s)\n",
			strings.Join(missing, ", "), scopeHeader)
	}
}

// maxRetries is the maximum number of retry attempts for rate-limited requests.
const maxRetries = 3

// maxRateLimitWait caps the wait for transient / secondary rate limit errors
// where the limit still has quota remaining but a burst was rejected.
const maxRateLimitWait = 30 * time.Second

// maxFullExhaustionWait caps the wait when the primary rate limit is fully
// exhausted (remaining = 0). GitHub's hourly limit resets within 60 minutes;
// 75 minutes absorbs any clock skew or delayed reset.
const maxFullExhaustionWait = 75 * time.Minute

// Query executes a GraphQL query with rate limiting and retry on rate limit errors.
func (c *Client) Query(ctx context.Context, q interface{}, variables map[string]interface{}) error {
	return c.query(ctx, q, variables)
}

// Mutate executes a GraphQL mutation with rate limiting and retry.
func (c *Client) Mutate(ctx context.Context, m interface{}, input map[string]interface{}) error {
	return c.mutate(ctx, m, input)
}

// query executes a GraphQL query with rate limiting and retry on rate limit errors.
func (c *Client) query(ctx context.Context, q interface{}, variables map[string]interface{}) error {
	if err := c.waitRateLimitGate(ctx); err != nil {
		return err
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		err := c.gql.Query(ctx, q, variables)
		if err == nil {
			return nil
		}

		if !isRateLimited(err) || attempt == maxRetries {
			return err
		}

		backoff := c.computeRateLimitBackoff(ctx, err, attempt)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// mutate executes a GraphQL mutation with rate limiting and retry.
func (c *Client) mutate(ctx context.Context, m interface{}, input map[string]interface{}) error {
	if err := c.waitRateLimitGate(ctx); err != nil {
		return err
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		err := c.gql.Mutate(ctx, m, input)
		if err == nil {
			return nil
		}

		if !isRateLimited(err) || attempt == maxRetries {
			return err
		}

		backoff := c.computeRateLimitBackoff(ctx, err, attempt)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// queryRaw executes a raw GraphQL query string (for aliases and other features
// not supported by the shurcooL struct-based API). Returns the raw JSON body.
func (c *Client) queryRaw(ctx context.Context, query string, variables map[string]interface{}) ([]byte, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter: %w", err)
		}

		payload := map[string]interface{}{
			"query":     query,
			"variables": variables,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal query: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", c.graphqlURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			if !isRateLimited(err) || attempt == maxRetries {
				return nil, err
			}
			backoff := c.computeRateLimitBackoff(ctx, err, attempt)
			select {
			case <-time.After(backoff):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			if attempt < maxRetries {
				backoff := c.computeRateLimitBackoff(ctx, fmt.Errorf("status %d", resp.StatusCode), attempt)
				select {
				case <-time.After(backoff):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("graphql request failed with status %d: %s", resp.StatusCode, string(respBody))
		}

		return respBody, nil
	}
	return nil, fmt.Errorf("exhausted retries")
}

// restPost makes an authenticated REST POST request to the GitHub API.
// path must begin with "/" (e.g., "/orgs/nightgauge/projectsV2/1/views").
func (c *Client) restPost(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.restDo(ctx, http.MethodPost, path, body)
}

// restGet makes an authenticated REST GET request to the GitHub API.
// path must begin with "/" (e.g., "/repos/owner/repo").
func (c *Client) restGet(ctx context.Context, path string) ([]byte, error) {
	return c.restDo(ctx, http.MethodGet, path, nil)
}

// restPatch makes an authenticated REST PATCH request to the GitHub API.
// path must begin with "/" (e.g., "/repos/owner/repo").
func (c *Client) restPatch(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.restDo(ctx, http.MethodPatch, path, body)
}

// restDo performs an authenticated REST request and returns the raw body on
// 2xx, or an error on any non-2xx / transport failure. External contract is
// unchanged — a non-2xx is a fmt.Errorf error, exactly as every existing caller
// expects. Callers that need to distinguish a specific HTTP status (e.g. a 404
// meaning "absent") should use restDoStatus directly.
func (c *Client) restDo(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	respBody, status, err := c.restDoStatus(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("REST %s %s: status %d: %s", method, path, status, string(respBody))
	}
	return respBody, nil
}

// restDoStatus is the core authenticated REST request with the SAME rate-limit
// handling as the GraphQL path (Issue #3976): a pre-call gate (wait-for-reset
// when the client has WithRateLimitWait, else fail-fast) plus a retry loop that
// waits out a 403/429 rate-limit response. It returns the response body AND the
// HTTP status code; a non-2xx response is NOT an error here (the status is
// returned for the caller to classify — e.g. a 404 on the collaborator endpoint
// means "not a collaborator", a confirmed denial rather than an infra failure).
// A non-nil error is returned only for transport-level failures (request build,
// network, body read, context cancellation, exhausted retries).
//
// Before REST rate-limit handling existed, a quota dip failed repo-settings / PR
// / board REST calls instantly, contributing to the recurring "GraphQL/REST
// problems" cascade. The X-GitHub-Api-Version header is always set; Content-Type
// only when there is a body.
func (c *Client) restDoStatus(ctx context.Context, method, path string, body interface{}) ([]byte, int, error) {
	// Derive REST base URL from graphqlURL (strip /graphql suffix if present).
	baseURL := strings.TrimSuffix(c.graphqlURL, "/graphql")
	url := baseURL + path

	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal REST %s body: %w", method, err)
		}
	}

	if err := c.waitRateLimitGate(ctx); err != nil {
		return nil, 0, err
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		var reqBody io.Reader
		if data != nil {
			reqBody = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, 0, fmt.Errorf("create REST %s request: %w", method, err)
		}
		if data != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("REST %s %s: %w", method, path, err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, 0, fmt.Errorf("read REST %s response body: %w", method, readErr)
		}

		// 403/429 with a rate-limit signal → wait out the reset and retry
		// rather than surfacing a hard failure. computeRateLimitBackoff uses
		// the just-recorded X-RateLimit-* headers (fed to the tracker by the
		// response interceptor) / a GraphQL probe to size the wait.
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			restBodyLooksRateLimited(respBody) && attempt < maxRetries {
			// Honor GitHub's Retry-After header when present (folded into the
			// error so computeRateLimitBackoff's "retry after N" parser uses
			// it); otherwise it probes / falls back to the tracker reset.
			rlErr := fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				rlErr = fmt.Errorf("status %d: retry after %s seconds: %s", resp.StatusCode, ra, string(respBody))
			}
			backoff := c.computeRateLimitBackoff(ctx, rlErr, attempt)
			select {
			case <-time.After(backoff):
				continue
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}

		return respBody, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("REST %s %s: exhausted retries", method, path)
}

// restBodyLooksRateLimited reports whether a GitHub REST error body carries a
// rate-limit signal ("API rate limit exceeded", secondary rate limit, abuse
// detection) — distinguishing a throttled 403 from a genuine permission 403.
func restBodyLooksRateLimited(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "rate limit") ||
		strings.Contains(s, "secondary rate") ||
		strings.Contains(s, "abuse detection")
}

// GetRepositoryID fetches the node ID for a repository (needed for mutations).
func (c *Client) GetRepositoryID(ctx context.Context, owner, name string) (string, error) {
	var q struct {
		Repository struct {
			ID graphql.ID
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(name),
	}
	if err := c.query(ctx, &q, vars); err != nil {
		return "", fmt.Errorf("get repository ID for %s/%s: %w", owner, name, err)
	}
	return fmt.Sprintf("%v", q.Repository.ID), nil
}

// RateLimitInfo holds the current GitHub API rate limit state.
type RateLimitInfo struct {
	Remaining int   `json:"remaining"`
	Limit     int   `json:"limit"`
	ResetAt   int64 `json:"resetAt"` // Unix timestamp
}

// GetRateLimit checks the current GitHub GraphQL API rate limit without
// consuming a rate-limited request (uses the rateLimit query).
func (c *Client) GetRateLimit(ctx context.Context) (*RateLimitInfo, error) {
	var q struct {
		RateLimit struct {
			Remaining graphql.Int
			Limit     graphql.Int
			ResetAt   graphql.String
		}
	}
	if err := c.query(ctx, &q, nil); err != nil {
		return nil, fmt.Errorf("get rate limit: %w", err)
	}

	resetTime, _ := time.Parse(time.RFC3339, string(q.RateLimit.ResetAt))
	return &RateLimitInfo{
		Remaining: int(q.RateLimit.Remaining),
		Limit:     int(q.RateLimit.Limit),
		ResetAt:   resetTime.Unix(),
	}, nil
}

// isRateLimited checks if an error is a GitHub rate limit error.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "rate limit") || contains(msg, "abuse detection") || contains(msg, "secondary rate")
}

// contains is a simple substring check to avoid importing strings.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// retryAfter calculates backoff duration. It checks for Retry-After header
// pattern in error messages, falling back to exponential backoff.
//
// This function is package-local and free of Client state for test
// simplicity; production retry paths use Client.computeRateLimitBackoff which
// additionally consults GitHub's rateLimit query for an accurate reset time.
func retryAfter(err error, attempt int) time.Duration {
	if d, ok := parseRetryAfter(err); ok {
		return d
	}
	// Exponential backoff: 1s, 2s, 4s
	return time.Duration(1<<uint(attempt)) * time.Second
}

// parseRetryAfter extracts a wait duration from a "retry after N seconds"
// hint in an error message, if present.
func parseRetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	for i := 0; i < len(msg)-6; i++ {
		if msg[i:i+6] == "after " {
			end := i + 6
			for end < len(msg) && msg[end] >= '0' && msg[end] <= '9' {
				end++
			}
			if end > i+6 {
				if secs, err := strconv.Atoi(msg[i+6 : end]); err == nil {
					return time.Duration(secs) * time.Second, true
				}
			}
		}
	}
	return 0, false
}

// computeRateLimitBackoff returns how long the retry loop should wait before
// its next attempt. Preference order:
//  1. Explicit "retry after N seconds" hint in the error (honored as-is,
//     capped at maxRateLimitWait).
//  2. Actual resetAt from GitHub's free rateLimit GraphQL query. When
//     remaining == 0 (fully exhausted), the full reset time is used so the
//     pipeline recovers rather than failing after 90s of futile retries.
//     When remaining > 0 (transient burst limit), capped at maxRateLimitWait.
//  3. Cached reset time from the SharedRateLimitTracker (probe also rate-limited).
//  4. Exponential fallback (1s / 2s / 4s).
//
// The rateLimit probe uses the underlying gql client directly so it does not
// recurse through the retry loop, and it uses a short context timeout so a
// slow probe doesn't multiply waiting time.
func (c *Client) computeRateLimitBackoff(ctx context.Context, err error, attempt int) time.Duration {
	if d, ok := parseRetryAfter(err); ok {
		return capRateLimitWait(d)
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var q struct {
		RateLimit struct {
			Remaining graphql.Int
			ResetAt   graphql.String
		}
	}
	if probeErr := c.gql.Query(probeCtx, &q, nil); probeErr == nil {
		if int(q.RateLimit.Remaining) > 0 {
			// The limit already cleared between the failing call and our probe;
			// a tiny pause is enough for the local limiter to catch up.
			return 500 * time.Millisecond
		}
		if reset, parseErr := time.Parse(time.RFC3339, string(q.RateLimit.ResetAt)); parseErr == nil {
			until := time.Until(reset) + 500*time.Millisecond
			if until > 0 {
				// Fully exhausted (remaining=0): wait until the actual reset so
				// the pipeline recovers automatically instead of failing after
				// 3 × 30s of futile retries.
				log.Printf("github: rate limit fully exhausted (user=%q), waiting %s until reset",
					c.trackerUser, until.Round(time.Second))
				return capFullExhaustionWait(until)
			}
		}
	} else if c.tracker != nil {
		// Probe also failed (rate limited). Fall back to the tracker's cached
		// ResetAt so we still wait for the actual reset rather than using the
		// short exponential fallback.
		if entry, _, trackerErr := c.tracker.Get(c.trackerUser); trackerErr == nil && entry != nil && entry.ResetAt > 0 {
			until := time.Duration(entry.ResetAt-time.Now().Unix())*time.Second + 500*time.Millisecond
			if until > 0 {
				log.Printf("github: rate limit fully exhausted, probe failed — using cached reset (user=%q), waiting %s",
					c.trackerUser, until.Round(time.Second))
				return capFullExhaustionWait(until)
			}
		}
	}

	// Exponential fallback: 1s, 2s, 4s.
	return time.Duration(1<<uint(attempt)) * time.Second
}

// capRateLimitWait bounds a wait duration to maxRateLimitWait for transient
// burst / secondary rate limit errors where quota is not fully exhausted.
func capRateLimitWait(d time.Duration) time.Duration {
	if d <= 0 {
		return 500 * time.Millisecond
	}
	if d > maxRateLimitWait {
		return maxRateLimitWait
	}
	return d
}

// capFullExhaustionWait bounds a wait to maxFullExhaustionWait for full quota
// exhaustion (remaining = 0). Unlike capRateLimitWait, this allows waiting the
// full hourly reset window so the pipeline can recover automatically.
func capFullExhaustionWait(d time.Duration) time.Duration {
	if d <= 0 {
		return 500 * time.Millisecond
	}
	if d > maxFullExhaustionWait {
		return maxFullExhaustionWait
	}
	return d
}
