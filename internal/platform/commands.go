package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// PendingCommand represents a remote command pushed from the platform to a
// running CLI agent. Payload is kept as raw JSON so callers can unmarshal to
// concrete types without this package depending on command-specific structs.
type PendingCommand struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
	// Frame is the SSE `data:` JSON exactly as the platform sent it, kept so
	// a command this daemon does not execute can be relayed whole (#2335).
	Frame json.RawMessage `json:"-"`
}

// CommandService acknowledges agent commands the hosted service delivers on
// the SSE stream GET /v1/agents/{agentId}/commands.
type CommandService struct {
	client *Client
}

// NewCommandService creates a command service backed by the given client.
func NewCommandService(client *Client) *CommandService {
	return &CommandService{client: client}
}

// AcknowledgeAgentCommand POSTs to /v1/agents/{agentId}/commands/{commandId}/ack
// to signal receipt of a trigger command. Returns the runId assigned by the platform.
func (s *CommandService) AcknowledgeAgentCommand(ctx context.Context, agentId, commandId string) (string, error) {
	return s.ackAgentCommand(ctx, agentId, commandId, struct{}{}, true)
}

// AgentCommandRejectedOutcome is the ack outcome that refuses a command.
const AgentCommandRejectedOutcome = "rejected"

// AgentCommandAppliedOutcome is the ack outcome for a command the agent
// carried out (#2334). It and AgentCommandRejectedOutcome are how an ack tells
// an applied verb from one that found nothing to act on.
const AgentCommandAppliedOutcome = "applied"

// AgentCommandAlreadyResolvedOutcome is the ack outcome for a verb that found
// its run already in the state it asks for (#2341): a pause of a run that is
// already paused, a resume of one that is not. The platform keeps the status
// the verb set, where a rejected pause or resume makes it restore the run's
// earlier status.
const AgentCommandAlreadyResolvedOutcome = "already_resolved"

// AgentCommandAckDetailMax bounds a rejected ack's detail, the hosted
// service's limit on the field.
const AgentCommandAckDetailMax = 2000

// RejectAgentCommand acks a command as refused, with detail as the reason the
// requester sees (#1656): {outcome: "rejected", detail}. The command is
// consumed and nothing runs. detail is cut to AgentCommandAckDetailMax bytes
// on a UTF-8 boundary.
func (s *CommandService) RejectAgentCommand(ctx context.Context, agentId, commandId, detail string) error {
	body := struct {
		Outcome string `json:"outcome"`
		Detail  string `json:"detail"`
	}{AgentCommandRejectedOutcome, truncateAckDetail(detail)}
	_, err := s.ackAgentCommand(ctx, agentId, commandId, body, false)
	return err
}

// ApplyAgentCommand acks a command the agent carried out: {outcome:
// "applied"}, with detail when one is given (#2334). It starts no run, so no
// runId is read back.
func (s *CommandService) ApplyAgentCommand(ctx context.Context, agentId, commandId, detail string) error {
	return s.ackWithOutcome(ctx, agentId, commandId, AgentCommandAppliedOutcome, detail)
}

// AlreadyResolvedAgentCommand acks a verb whose run was already in the state
// it asks for: {outcome: "already_resolved"}, with detail when one is given
// (#2341). It starts no run, so no runId is read back.
func (s *CommandService) AlreadyResolvedAgentCommand(ctx context.Context, agentId, commandId, detail string) error {
	return s.ackWithOutcome(ctx, agentId, commandId, AgentCommandAlreadyResolvedOutcome, detail)
}

// ackWithOutcome posts {outcome, detail?} for an ack that starts no run.
func (s *CommandService) ackWithOutcome(ctx context.Context, agentId, commandId, outcome, detail string) error {
	body := struct {
		Outcome string `json:"outcome"`
		Detail  string `json:"detail,omitempty"`
	}{outcome, truncateAckDetail(detail)}
	_, err := s.ackAgentCommand(ctx, agentId, commandId, body, false)
	return err
}

func truncateAckDetail(detail string) string {
	if len(detail) <= AgentCommandAckDetailMax {
		return detail
	}
	cut := AgentCommandAckDetailMax
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut]
}

// ackAgentCommand POSTs payload as the ack body. wantRunID parses the
// runId an accepting ack returns; a rejected ack starts no run, so its
// response body is not read for one.
func (s *CommandService) ackAgentCommand(ctx context.Context, agentId, commandId string, payload any, wantRunID bool) (string, error) {
	if agentId == "" {
		return "", fmt.Errorf("acknowledge agent command: agentId is required")
	}
	if commandId == "" {
		return "", fmt.Errorf("acknowledge agent command: commandId is required")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("acknowledge agent command: marshal body: %w", err)
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsAckCommand,
		PathArgs: []string{agentId, commandId},
		Body:     body,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
	})
	if err != nil {
		return "", fmt.Errorf("acknowledge agent command: create request: %w", err)
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
	if !wantRunID {
		return "", nil
	}

	var result struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("acknowledge agent command: parse response: %w", err)
	}

	return result.RunID, nil
}
