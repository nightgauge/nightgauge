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
	// value at construction. Read them through bearer(); never directly.
	credMu       sync.RWMutex
	sessionToken string
	staticAPIKey string
	licenseKey   string

	agentID string

	// Health polling
	pollInterval time.Duration
	pollCancel   context.CancelFunc

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
		base:         cfg.BaseURL,
		mode:         ModeOffline, // Start offline until first health check
		staticAPIKey: cfg.APIKey,
		licenseKey:   cfg.LicenseKey,
		agentID:      cfg.AgentID,
		pollInterval: cfg.PollInterval,
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
//  3. the license key (config license_key / NIGHTGAUGE_LICENSE_KEY).
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

// IsOnline returns true if the platform is reachable.
func (c *Client) IsOnline() bool {
	return c.Mode() == ModeOnline
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

// StartHealthPolling begins periodic health checks in the background.
func (c *Client) StartHealthPolling(ctx context.Context) {
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

// StopHealthPolling stops the background health poller.
func (c *Client) StopHealthPolling() {
	if c.pollCancel != nil {
		c.pollCancel()
	}
}

// checkHealth performs a single health check and updates connectivity mode.
func (c *Client) checkHealth(ctx context.Context) {
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

// FetchCommands retrieves pending remote commands by delegating to CommandService.
// This satisfies the CommandFetcher interface for use with CommandPoller.
func (c *Client) FetchCommands(ctx context.Context) ([]PendingCommand, error) {
	return NewCommandService(c).PollCommands(ctx)
}
