// Package platform wraps the generated OpenAPI client with auth, health polling,
// and offline fallback for the nightgauge platform API.
package platform

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// ConnectivityMode indicates the binary's connection status to the platform.
type ConnectivityMode string

const (
	ModeOnline   ConnectivityMode = "online"
	ModeDegraded ConnectivityMode = "degraded"
	ModeOffline  ConnectivityMode = "offline"
)

// Client wraps the generated OpenAPI client with auth injection,
// health polling, and connectivity awareness.
type Client struct {
	api  *api.ClientWithResponses
	base string

	mu   sync.RWMutex
	mode ConnectivityMode

	// Auth. The credential is read on every request from an arbitrary
	// goroutine (health poller, analytics push, IPC handlers) and the session
	// token is swapped at runtime by platform.setSessionToken (#742), so the
	// three sources live behind credMu instead of being collapsed into one
	// value at construction. Read them through bearer() or sessionBearer();
	// never directly.
	credMu       sync.RWMutex
	sessionToken string
	staticAPIKey string
	licenseKey   string

	agentID string

	// Health polling
	pollInterval time.Duration
	pollCancel   context.CancelFunc
	// onDemandHealth marks a client that never polls (Config.OnDemandHealth):
	// IsOnline checks on demand instead, at most once per pollInterval.
	// lastCheck is when the latest health check ran (UnixNano), and
	// onDemandMu keeps concurrent IsOnline calls from checking together.
	onDemandHealth bool
	lastCheck      atomic.Int64
	onDemandMu     sync.Mutex

	// Callbacks
	onModeChange func(old, new ConnectivityMode)
}

// Config holds platform client configuration.
type Config struct {
	BaseURL      string
	APIKey       string
	LicenseKey   string
	AgentID      string
	PollInterval time.Duration
	// OnDemandHealth makes a client that sends nothing on its own: it runs no
	// health poller (StartHealthPolling is a no-op), and IsOnline checks the
	// platform when asked, at most once per PollInterval. The daemon builds
	// its clients this way while the user has not opted in to the cloud, so
	// the only requests they make are the ones the user's own actions ask for.
	OnDemandHealth bool
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		BaseURL:      "https://api.nightgauge.dev",
		PollInterval: 60 * time.Second,
	}
}

// NewClient creates a platform client with auth and health polling.
func NewClient(cfg Config) (*Client, error) {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 60 * time.Second
	}

	c := &Client{
		base:           cfg.BaseURL,
		mode:           ModeOffline, // Start offline until first health check
		staticAPIKey:   cfg.APIKey,
		licenseKey:     cfg.LicenseKey,
		agentID:        cfg.AgentID,
		pollInterval:   cfg.PollInterval,
		onDemandHealth: cfg.OnDemandHealth,
	}

	// The editor closes over the client rather than over a bearer resolved once
	// here: a session token pushed after construction has to reach the
	// generated api client too, not only the raw-HTTP paths (#742).
	authEditor := func(_ context.Context, req *http.Request) error {
		if bearer := c.bearer(); bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return nil
	}

	apiClient, err := api.NewClientWithResponses(
		cfg.BaseURL,
		api.WithRequestEditorFn(authEditor),
	)
	if err != nil {
		return nil, fmt.Errorf("create platform client: %w", err)
	}
	c.api = apiClient

	return c, nil
}

// bearer returns the credential to present on the next request.
//
// Precedence, highest first:
//
//  1. the signed-in user's session JWT, pushed at runtime over IPC
//     (platform.setSessionToken) and re-pushed on every token refresh;
//  2. an explicit API key (env NIGHTGAUGE_API_KEY);
//  3. the license key, as resolved by internal/keychain for the CLI and the
//     daemon: NIGHTGAUGE_LICENSE_KEY, then the OS keychain entry, then
//     platform.license_key in the machine-tier config file.
//
// The session token has to win. Several hosted routes — /v1/analytics/health,
// /v1/analytics/trends, /v1/analytics/cost, /v1/audit/reports — authorise a
// *user*, not an account, and answer 401 to a license key no matter how valid
// it is. The license key stays as the fallback so headless CLI runs, which have
// no session at all, keep working on the routes that do accept one.
//
// The platform's pipelineAuth accepts either a JWT or a license key as the
// bearer (license keys carry no dots), so the fallback is transparent.
func (c *Client) bearer() string {
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	if c.sessionToken != "" {
		return c.sessionToken
	}
	if c.staticAPIKey != "" {
		return c.staticAPIKey
	}
	return c.licenseKey
}

// sessionBearer is bearer() for a request that only a signed-in user may
// make: the session token, or "" with no fallback when none is installed.
func (c *Client) sessionBearer() string {
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	return c.sessionToken
}

// SetSessionToken installs the signed-in user's JWT as the credential for every
// subsequent request, on the generated api client and the raw-HTTP paths alike.
// An empty (or whitespace-only) token clears it, which is what sign-out does:
// the client then falls back to the API key or license key, behaving exactly as
// a process that was never signed in.
//
// Safe to call concurrently with in-flight requests. A request whose
// Authorization header has already been written keeps the credential it was
// built with; the next one picks up the new value.
func (c *Client) SetSessionToken(token string) {
	token = strings.TrimSpace(token)
	c.credMu.Lock()
	c.sessionToken = token
	c.credMu.Unlock()
}

// HasSessionToken reports whether a user-scoped JWT is currently installed.
func (c *Client) HasSessionToken() bool {
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	return c.sessionToken != ""
}

// AgentID returns the machine/agent identifier this client reports to the
// platform (empty when unset). Used by queue-sync to scope a machine's snapshot.
func (c *Client) AgentID() string {
	return c.agentID
}

// Mode returns the current connectivity mode.
func (c *Client) Mode() ConnectivityMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

// IsOnline returns true if the platform is reachable. A client built with
// OnDemandHealth has no poller keeping its mode current, so it checks here
// when its last check is older than the poll interval: the request the caller
// is about to make is the user's own, and the check goes with it.
func (c *Client) IsOnline() bool {
	if c.onDemandHealth {
		c.checkIfStale()
	}
	return c.Mode() == ModeOnline
}

// checkIfStale runs a health check when the latest one is older than the
// poll interval (or none has run). Callers that arrive while a check is in
// flight wait for it and use its answer.
func (c *Client) checkIfStale() {
	c.onDemandMu.Lock()
	defer c.onDemandMu.Unlock()
	if last := c.lastCheck.Load(); last != 0 && time.Since(time.Unix(0, last)) < c.pollInterval {
		return
	}
	c.checkHealth(context.Background())
}

// OnModeChange registers a callback for connectivity changes.
func (c *Client) OnModeChange(fn func(old, new ConnectivityMode)) {
	c.onModeChange = fn
}

// setMode updates connectivity and fires the callback.
func (c *Client) setMode(m ConnectivityMode) {
	c.mu.Lock()
	old := c.mode
	c.mode = m
	c.mu.Unlock()

	if old != m && c.onModeChange != nil {
		c.onModeChange(old, m)
	}
}

// StartHealthPolling begins periodic health checks in the background. A
// client built with OnDemandHealth never polls, so for it this does nothing.
func (c *Client) StartHealthPolling(ctx context.Context) {
	if c.onDemandHealth {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	c.pollCancel = cancel

	// Run an initial check immediately
	c.checkHealth(ctx)

	go func() {
		ticker := time.NewTicker(c.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.checkHealth(ctx)
			}
		}
	}()
}

// ProbeHealth runs one health check now, in the caller's goroutine, and
// reports whether the platform answered online. A client built on demand
// starts offline and polls in the background (#2398), so an account action
// that gates on IsOnline probes first rather than reporting the platform
// unreachable before its first check has run.
func (c *Client) ProbeHealth(ctx context.Context) bool {
	c.checkHealth(ctx)
	return c.IsOnline()
}

// StopHealthPolling stops the background health poller.
func (c *Client) StopHealthPolling() {
	if c.pollCancel != nil {
		c.pollCancel()
	}
}

// checkHealth performs a single health check and updates connectivity mode.
func (c *Client) checkHealth(ctx context.Context) {
	c.lastCheck.Store(time.Now().UnixNano())
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := c.api.GetHealthWithResponse(checkCtx)
	if err != nil {
		log.Printf("platform health check failed: %v", err)
		c.setMode(ModeOffline)
		return
	}

	if resp.JSON200 == nil {
		c.setMode(ModeOffline)
		return
	}

	switch resp.JSON200.Status {
	case "ok":
		c.setMode(ModeOnline)
	case "degraded":
		c.setMode(ModeDegraded)
	default:
		c.setMode(ModeOffline)
	}
}

// API returns the underlying generated client for direct access.
func (c *Client) API() *api.ClientWithResponses {
	return c.api
}
