package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// CommandResult holds the outcome of executing a remote command.
type CommandResult struct {
	Status     string `json:"status"`           // "success" or "failure"
	Output     string `json:"output,omitempty"` // stdout/result text
	Error      string `json:"error,omitempty"`  // error message on failure
	DurationMs int64  `json:"duration_ms"`      // execution duration in milliseconds
}

// PendingCommand represents a remote command pushed from the platform to a
// running CLI agent. Payload is kept as raw JSON so callers can unmarshal to
// concrete types without this package depending on command-specific structs.
type PendingCommand struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

// CommandService queries the platform for pending remote commands.
// The commands endpoint (GET /v1/commands/pending) is not in the OpenAPI spec,
// so this service makes raw HTTP requests following the same pattern as
// BillingService and AnalyticsService.
//
// Note: the issue body mentions /api/v1/commands/pending, but all existing raw
// HTTP calls in this package use /v1/... without the /api prefix. This service
// follows the established pattern; verify against the platform spec if the path
// needs adjustment.
type CommandService struct {
	client *Client
}

// NewCommandService creates a command service backed by the given client.
func NewCommandService(client *Client) *CommandService {
	return &CommandService{client: client}
}

// PollCommands fetches pending remote commands for the configured agent from
// GET /v1/commands/pending?agentId={agentId}.
//
// Returns (nil, nil) when the client is offline — commands are a real-time
// concern and an empty result is the correct offline fallback.
//
// Returns an error if agentID is empty or if the request fails.
func (s *CommandService) PollCommands(ctx context.Context) ([]PendingCommand, error) {
	if !s.client.IsOnline() {
		return nil, nil
	}

	if s.client.agentID == "" {
		return nil, fmt.Errorf("poll commands: agentId not configured")
	}

	endpoint := s.client.base + "/v1/commands/pending?agentId=" + url.QueryEscape(s.client.agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("poll commands: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll commands: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("poll commands: read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("poll commands: unauthorized (check apiKey)")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("poll commands: server returned %d", resp.StatusCode)
	}

	var commands []PendingCommand
	if err := json.Unmarshal(body, &commands); err != nil {
		return nil, fmt.Errorf("poll commands: parse response: %w", err)
	}

	return commands, nil
}

// AcknowledgeCommand posts the execution result of a remote command back to the platform.
// Returns an error if the platform is offline (acknowledgement requires live API).
func (s *CommandService) AcknowledgeCommand(ctx context.Context, cmdID string, result CommandResult) error {
	if !s.client.IsOnline() {
		return fmt.Errorf("command acknowledgement requires online platform connectivity")
	}

	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal command result: %w", err)
	}

	reqURL := s.client.base + "/v1/commands/" + url.PathEscape(cmdID) + "/ack"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create acknowledge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Inject auth header using the same pattern as the generated client.
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("acknowledge request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read acknowledge response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("acknowledge command failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// AcknowledgeAgentCommand POSTs to /v1/agents/{agentId}/commands/{commandId}/ack
// to signal receipt of a trigger command. Returns the runId assigned by the platform.
func (s *CommandService) AcknowledgeAgentCommand(ctx context.Context, agentId, commandId string) (string, error) {
	if agentId == "" {
		return "", fmt.Errorf("acknowledge agent command: agentId is required")
	}
	if commandId == "" {
		return "", fmt.Errorf("acknowledge agent command: commandId is required")
	}

	body, err := json.Marshal(struct{}{})
	if err != nil {
		return "", fmt.Errorf("acknowledge agent command: marshal body: %w", err)
	}

	reqURL := s.client.base + "/v1/agents/" + url.PathEscape(agentId) + "/commands/" + url.PathEscape(commandId) + "/ack"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("acknowledge agent command: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("acknowledge agent command: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("acknowledge agent command: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("acknowledge agent command: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("acknowledge agent command: parse response: %w", err)
	}

	return result.RunID, nil
}

// CommandFetcher abstracts the source of pending commands for the poller.
type CommandFetcher interface {
	FetchCommands(ctx context.Context) ([]PendingCommand, error)
}

// CommandExecutor handles dispatched commands.
type CommandExecutor interface {
	Execute(ctx context.Context, cmd PendingCommand) error
}

// NoOpCommandExecutor is a placeholder executor that silently succeeds.
type NoOpCommandExecutor struct{}

// Execute does nothing and returns nil.
func (n *NoOpCommandExecutor) Execute(_ context.Context, _ PendingCommand) error {
	return nil
}

// CommandPollerConfig holds tuning parameters for the polling loop.
type CommandPollerConfig struct {
	PollInterval time.Duration
	MaxBackoff   time.Duration
}

// DefaultCommandPollerConfig returns sensible defaults (5s poll, 60s max backoff).
func DefaultCommandPollerConfig() CommandPollerConfig {
	return CommandPollerConfig{
		PollInterval: 5 * time.Second,
		MaxBackoff:   60 * time.Second,
	}
}

// CommandPoller periodically fetches pending commands and dispatches them.
type CommandPoller struct {
	fetcher  CommandFetcher
	executor CommandExecutor
	cfg      CommandPollerConfig
}

// NewCommandPoller creates a poller that fetches commands from fetcher and
// dispatches them to executor.
func NewCommandPoller(fetcher CommandFetcher, executor CommandExecutor, cfg CommandPollerConfig) *CommandPoller {
	return &CommandPoller{fetcher: fetcher, executor: executor, cfg: cfg}
}

// Start begins the polling loop in a background goroutine. Cancel ctx to stop.
func (p *CommandPoller) Start(ctx context.Context) {
	go p.run(ctx)
}

func (p *CommandPoller) run(ctx context.Context) {
	backoff := p.cfg.PollInterval

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cmds, err := p.fetcher.FetchCommands(ctx)
		if err != nil {
			// Exponential backoff on error, capped at MaxBackoff.
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > p.cfg.MaxBackoff {
				backoff = p.cfg.MaxBackoff
			}
			continue
		}

		// Reset backoff on success.
		backoff = p.cfg.PollInterval

		for _, cmd := range cmds {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = p.executor.Execute(ctx, cmd)
		}

		select {
		case <-time.After(p.cfg.PollInterval):
		case <-ctx.Done():
			return
		}
	}
}
