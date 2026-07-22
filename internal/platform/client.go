// Package platform wraps the generated OpenAPI client with auth, health polling,
// and offline fallback for the nightgauge platform API.
package platform

import (
	"context"
	"fmt"
	"log"
	"net/http"
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

	// Auth
	apiKey     string
	licenseKey string
	agentID    string

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

	// Resolve the bearer token: an explicit API key (env NIGHTGAUGE_API_KEY)
	// wins, but fall back to the license key (config.yaml license_key) so a
	// provisioned license authenticates the pipeline without a separate
	// NIGHTGAUGE_API_KEY export. The platform's pipelineAuth accepts either
	// a JWT or a license key as the bearer (license keys carry no dots), so the
	// fallback is transparent to the server. Stored on apiKey so every raw-HTTP
	// push (analytics.go) uses the same resolved credential.
	bearer := cfg.APIKey
	if bearer == "" {
		bearer = cfg.LicenseKey
	}

	authEditor := func(ctx context.Context, req *http.Request) error {
		if bearer != "" {
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

	return &Client{
		api:          apiClient,
		base:         cfg.BaseURL,
		mode:         ModeOffline, // Start offline until first health check
		apiKey:       bearer,
		licenseKey:   cfg.LicenseKey,
		agentID:      cfg.AgentID,
		pollInterval: cfg.PollInterval,
	}, nil
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
