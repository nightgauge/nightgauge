package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

func newTestPlatformClientFor(t *testing.T, mockURL, apiKey string) *platform.Client {
	t.Helper()
	pc, err := platform.NewClient(platform.Config{BaseURL: mockURL, APIKey: apiKey})
	if err != nil {
		t.Fatalf("platform.NewClient: %v", err)
	}
	return pc
}

// TestAgentAcknowledgeCommand_HappyPath verifies the IPC handler routes to
// platform.CommandService.AcknowledgeAgentCommand and returns the runId.
func TestAgentAcknowledgeCommand_HappyPath(t *testing.T) {
	wantRunID := "run-abc-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		wantPath := "/v1/agents/agent-1/commands/cmd-1/ack"
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %s, want Bearer test-key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"runId":"` + wantRunID + `"}`))
	}))
	defer srv.Close()

	pc := newTestPlatformClientFor(t, srv.URL, "test-key")
	buf := &bytes.Buffer{}
	s := NewServer(nil, WithPlatformClient(pc))
	s.writer = buf

	params, _ := json.Marshal(AgentAcknowledgeCommandParams{AgentID: "agent-1", CommandID: "cmd-1"})
	result, err := s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err != nil {
		t.Fatalf("handleAgentAcknowledgeCommand: %v", err)
	}

	res, ok := result.(AgentAcknowledgeCommandResult)
	if !ok {
		t.Fatalf("result type = %T, want AgentAcknowledgeCommandResult", result)
	}
	if res.RunID != wantRunID {
		t.Errorf("RunID = %q, want %q", res.RunID, wantRunID)
	}
}

// TestAgentAcknowledgeCommand_NilPlatformClient verifies an error is returned
// when the platform client is not configured.
func TestAgentAcknowledgeCommand_NilPlatformClient(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewServer(nil)
	s.writer = buf
	// platformClient remains nil

	params, _ := json.Marshal(AgentAcknowledgeCommandParams{AgentID: "a", CommandID: "c"})
	_, err := s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err == nil {
		t.Fatal("expected error when platformClient is nil")
	}
	if !strings.Contains(err.Error(), "platform client not configured") {
		t.Errorf("error = %q, want 'platform client not configured'", err.Error())
	}
}

// TestAgentAcknowledgeCommand_MissingAgentID verifies an empty agentId returns an error.
func TestAgentAcknowledgeCommand_MissingAgentID(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewServer(nil)
	s.writer = buf

	params, _ := json.Marshal(AgentAcknowledgeCommandParams{AgentID: "", CommandID: "cmd-1"})
	_, err := s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for empty agentId")
	}
	if !strings.Contains(err.Error(), "agentId is required") {
		t.Errorf("error = %q, want 'agentId is required'", err.Error())
	}
}

// TestAgentAcknowledgeCommand_MissingCommandID verifies an empty commandId returns an error.
func TestAgentAcknowledgeCommand_MissingCommandID(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewServer(nil)
	s.writer = buf

	params, _ := json.Marshal(AgentAcknowledgeCommandParams{AgentID: "agent-1", CommandID: ""})
	_, err := s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for empty commandId")
	}
	if !strings.Contains(err.Error(), "commandId is required") {
		t.Errorf("error = %q, want 'commandId is required'", err.Error())
	}
}

// TestAgentAcknowledgeCommand_Registered verifies the method is reachable
// through the methods map.
func TestAgentAcknowledgeCommand_Registered(t *testing.T) {
	s := NewServer(nil)
	if _, ok := s.methods["agent.acknowledgeCommand"]; !ok {
		t.Fatal("agent.acknowledgeCommand not registered in methods map")
	}
}

// TestAgentAcknowledgeCommand_ServerError verifies that a non-200 platform
// response propagates as an error through the IPC handler.
func TestAgentAcknowledgeCommand_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"command not found"}`))
	}))
	defer srv.Close()

	pc := newTestPlatformClientFor(t, srv.URL, "key")
	buf := &bytes.Buffer{}
	s := NewServer(nil, WithPlatformClient(pc))
	s.writer = buf

	params, _ := json.Marshal(AgentAcknowledgeCommandParams{AgentID: "a", CommandID: "c"})
	_, err := s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for HTTP 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %q, want to contain '400'", err.Error())
	}
}
