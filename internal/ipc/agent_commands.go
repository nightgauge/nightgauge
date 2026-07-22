package ipc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// handleAgentAcknowledgeCommand handles the agent.acknowledgeCommand IPC method.
// It POSTs to /v1/agents/{agentId}/commands/{commandId}/ack and returns the
// runId assigned by the platform.
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
	if s.platformClient == nil {
		return nil, fmt.Errorf("agent.acknowledgeCommand: platform client not configured")
	}

	runID, err := platform.NewCommandService(s.platformClient).AcknowledgeAgentCommand(ctx, p.AgentID, p.CommandID)
	if err != nil {
		return nil, fmt.Errorf("agent.acknowledgeCommand: %w", err)
	}

	return AgentAcknowledgeCommandResult{RunID: runID}, nil
}
