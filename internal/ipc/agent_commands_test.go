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

// ackBodyRecorder serves the ack route and keeps each request's JSON body.
func ackBodyRecorder(t *testing.T, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("ack body is not JSON: %v", err)
		}
		*bodies = append(*bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runId":"run-1"}`))
	}))
}

// A refused remote run request is acked {outcome: "rejected", detail} (#1656),
// and an ordinary ack still POSTs {} and returns the runId.
func TestAgentAcknowledgeCommand_RejectedCarriesOutcomeAndDetail(t *testing.T) {
	var bodies []map[string]any
	srv := ackBodyRecorder(t, &bodies)
	defer srv.Close()
	s := NewServer(nil, WithPlatformClient(newTestPlatformClientFor(t, srv.URL, "k")))
	s.writer = &bytes.Buffer{}

	params, _ := json.Marshal(AgentAcknowledgeCommandParams{
		AgentID: "agent-1", CommandID: "cmd-1",
		Outcome: "rejected", Detail: "model x is not in this machine's opencode catalog",
	})
	res, err := s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err != nil {
		t.Fatalf("rejected ack: %v", err)
	}
	if got := res.(AgentAcknowledgeCommandResult).RunID; got != "" {
		t.Errorf("a rejected ack returned runId %q; no run starts", got)
	}
	params, _ = json.Marshal(AgentAcknowledgeCommandParams{AgentID: "agent-1", CommandID: "cmd-2"})
	res, err = s.handleAgentAcknowledgeCommand(context.Background(), params)
	if err != nil || res.(AgentAcknowledgeCommandResult).RunID != "run-1" {
		t.Fatalf("ordinary ack: %v %v", res, err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d acks posted, want 2", len(bodies))
	}
	if bodies[0]["outcome"] != "rejected" || bodies[0]["detail"] != "model x is not in this machine's opencode catalog" {
		t.Errorf("rejected ack body = %v", bodies[0])
	}
	if len(bodies[1]) != 0 {
		t.Errorf("ordinary ack body = %v, want {}", bodies[1])
	}
}

func TestAgentAcknowledgeCommand_OutcomeIsClosed(t *testing.T) {
	var bodies []map[string]any
	srv := ackBodyRecorder(t, &bodies)
	defer srv.Close()
	s := NewServer(nil, WithPlatformClient(newTestPlatformClientFor(t, srv.URL, "k")))
	s.writer = &bytes.Buffer{}
	for _, p := range []AgentAcknowledgeCommandParams{
		{AgentID: "a", CommandID: "c", Outcome: "applied"},
		{AgentID: "a", CommandID: "c", Outcome: "rejected"},
	} {
		params, _ := json.Marshal(p)
		if _, err := s.handleAgentAcknowledgeCommand(context.Background(), params); err == nil {
			t.Errorf("%+v was accepted", p)
		}
	}
	if len(bodies) != 0 {
		t.Errorf("%d acks posted for refused params", len(bodies))
	}
}
