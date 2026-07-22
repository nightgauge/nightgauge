package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Action Center agent registration — the daemon self-registers as a platform
// agent so its DecisionRequest mirror (attention_sync.go) carries a real
// `agent_id` (nightgauge/nightgauge#341). The Go binary's local
// `.nightgauge/attention/` store is the authoritative writer; the platform's
// `decision_requests` table has an FK `decision_requests_agent_id_agents_id_fk`
// → `agents.id`, so mirroring a request whose `agent_id` is an unregistered id
// (the daemon's machine id) throws and 500s the whole sweep. Registering here
// creates the `agents` row the FK requires, and returns the platform-assigned
// UUID the sync + command poller must use instead of the machine id.
//
// This rides the same license-key bearer auth and raw-HTTP shape the sync uses
// (see attention_sync.go): the platform's pipelineAuth accepts the license key
// (client.apiKey) as the bearer. POST /v1/agents/register upserts by machine_id
// per account, so calling it on every daemon start is idempotent (re-register =
// revival).

// AgentRegisterCapabilityResolve is the capability the daemon advertises so the
// platform relays `attention_resolve` commands to it (the Action Center bridge).
const AgentRegisterCapabilityResolve = "attention_resolve"

// ErrAgentNotFound is returned by Heartbeat when the platform reports the agent
// no longer exists (HTTP 404 — evicted after the 90s TTL, or the platform lost
// the row). The bridge treats it as a signal to re-register and swap in the new
// agent id rather than a transient transport error.
var ErrAgentNotFound = errors.New("agent registration: agent not found")

// AgentRegistration is the platform's 201 response to POST /v1/agents/register.
type AgentRegistration struct {
	AgentID     string `json:"agentId"`
	CommandsURL string `json:"commandsUrl"`
	TTLSeconds  int    `json:"ttl_seconds"`
}

// agentRegisterBody is the POST /v1/agents/register request body. The platform's
// RegisterAgentSchema (zod) requires `machine_id`; `agent_version` is optional
// (omitted when the build version is unknown) and `capabilities` is always sent.
type agentRegisterBody struct {
	MachineID    string   `json:"machine_id"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Capabilities []string `json:"capabilities"`
}

// AgentRegistrationService registers this daemon as a platform agent and keeps
// it alive with heartbeats. It holds no id itself — the caller (the serve
// bridge) owns the registered agent id and late-binds it onto the sync + poller.
type AgentRegistrationService struct {
	client       *Client
	agentVersion string
}

// NewAgentRegistrationService builds a registration service bound to the platform
// client. agentVersion is the build version reported to the platform (pass ""
// to omit it — e.g. an unknown/dev build).
func NewAgentRegistrationService(client *Client, agentVersion string) *AgentRegistrationService {
	return &AgentRegistrationService{client: client, agentVersion: agentVersion}
}

// RegisterAgent POSTs the daemon's machine id + capabilities to
// POST /v1/agents/register and returns the platform-assigned agent id + TTL.
// Idempotent server-side (upsert by machine_id per account). A non-201 response
// or a missing agentId is an error the caller retries with backoff.
func (s *AgentRegistrationService) RegisterAgent(ctx context.Context) (AgentRegistration, error) {
	if s == nil || s.client == nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: no platform client")
	}
	body := agentRegisterBody{
		MachineID:    ResolveMachineID(),
		AgentVersion: s.agentVersion,
		Capabilities: []string{AgentRegisterCapabilityResolve},
	}
	if body.MachineID == "" {
		return AgentRegistration{}, fmt.Errorf("agent registration: machine id unavailable")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.client.base+"/v1/agents/register", bytes.NewReader(data))
	if err != nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: POST: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return AgentRegistration{}, fmt.Errorf("agent registration: server returned %d", resp.StatusCode)
	}

	var parsed AgentRegistration
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: parse response: %w", err)
	}
	if parsed.AgentID == "" {
		return AgentRegistration{}, fmt.Errorf("agent registration: response missing agentId")
	}
	return parsed, nil
}

// Heartbeat PUTs /v1/agents/:agentId/heartbeat to keep the agent alive (the
// platform TTL is 90s; the bridge heartbeats every 30s). It returns
// ErrAgentNotFound on a 404 so the caller re-registers, and a generic error on
// any other non-2xx. Offline → nil (no-op; the sweep re-registers when online).
func (s *AgentRegistrationService) Heartbeat(ctx context.Context, agentID string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("agent heartbeat: no platform client")
	}
	if agentID == "" {
		return fmt.Errorf("agent heartbeat: agentId not set")
	}
	endpoint := s.client.base + "/v1/agents/" + url.PathEscape(agentID) + "/heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return fmt.Errorf("agent heartbeat: request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent heartbeat: PUT: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ErrAgentNotFound
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("agent heartbeat: server returned %d", resp.StatusCode)
	}
	return nil
}
