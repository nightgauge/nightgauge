package ipc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// handleAgentAcknowledgeCommand handles the agent.acknowledgeCommand IPC method.
// It POSTs to /v1/agents/{agentId}/commands/{commandId}/ack and returns the
// runId assigned by the platform. With outcome "rejected" it refuses the
// command instead, with detail as the reason (#1656), and returns no runId.
func (s *Server) handleAgentAcknowledgeCommand(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p AgentAcknowledgeCommandParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("agent.acknowledgeCommand: parse params: %w", err)
	}
	if p.AgentID == "" {
		return nil, fmt.Errorf("agent.acknowledgeCommand: agentId is required")
	}
	if p.CommandID == "" {
		return nil, fmt.Errorf("agent.acknowledgeCommand: commandId is required")
	}
	if s.getPlatformClient() == nil {
		return nil, fmt.Errorf("agent.acknowledgeCommand: platform client not configured")
	}

	svc := platform.NewCommandService(s.getPlatformClient())
	switch p.Outcome {
	case "":
	case platform.AgentCommandRejectedOutcome:
		// A refusal (#1656): the command is consumed with the reason and no
		// run is started, so there is no runId to return.
		if p.Detail == "" {
			return nil, fmt.Errorf("agent.acknowledgeCommand: a rejected ack needs a detail")
		}
		if err := svc.RejectAgentCommand(ctx, p.AgentID, p.CommandID, p.Detail); err != nil {
			return nil, fmt.Errorf("agent.acknowledgeCommand: %w", err)
		}
		return AgentAcknowledgeCommandResult{}, nil
	default:
		return nil, fmt.Errorf("agent.acknowledgeCommand: unknown outcome %q", p.Outcome)
	}

	runID, err := svc.AcknowledgeAgentCommand(ctx, p.AgentID, p.CommandID)
	if err != nil {
		return nil, fmt.Errorf("agent.acknowledgeCommand: %w", err)
	}

	return AgentAcknowledgeCommandResult{RunID: runID}, nil
}
