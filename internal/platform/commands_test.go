package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- AcknowledgeAgentCommand tests ---

func TestCommandService_AcknowledgeAgentCommand_Success(t *testing.T) {
	wantRunID := "run-xyz-789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		wantPath := "/v1/agents/agent-42/commands/cmd-99/ack"
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %s, want Bearer secret", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"runId":"` + wantRunID + `"}`))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "secret"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	runID, err := svc.AcknowledgeAgentCommand(context.Background(), "agent-42", "cmd-99")
	if err != nil {
		t.Fatalf("AcknowledgeAgentCommand: %v", err)
	}
	if runID != wantRunID {
		t.Errorf("runID = %q, want %q", runID, wantRunID)
	}
}

func TestCommandService_AcknowledgeAgentCommand_EmptyAgentID(t *testing.T) {
	cfg := Config{BaseURL: "http://unused"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	_, err = svc.AcknowledgeAgentCommand(context.Background(), "", "cmd-1")
	if err == nil {
		t.Fatal("expected error for empty agentId")
	}
	if !strings.Contains(err.Error(), "agentId is required") {
		t.Errorf("error = %q, want 'agentId is required'", err.Error())
	}
}

func TestCommandService_AcknowledgeAgentCommand_EmptyCommandID(t *testing.T) {
	cfg := Config{BaseURL: "http://unused"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	_, err = svc.AcknowledgeAgentCommand(context.Background(), "agent-1", "")
	if err == nil {
		t.Fatal("expected error for empty commandId")
	}
	if !strings.Contains(err.Error(), "commandId is required") {
		t.Errorf("error = %q, want 'commandId is required'", err.Error())
	}
}

func TestCommandService_AcknowledgeAgentCommand_NonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"command not found"}`))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	_, err = svc.AcknowledgeAgentCommand(context.Background(), "agent-1", "cmd-1")
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %q, want to contain '404'", err.Error())
	}
}

func TestCommandService_AcknowledgeAgentCommand_NoAPIKey(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"runId":"run-no-key"}`))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL} // no APIKey
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(c)
	runID, err := svc.AcknowledgeAgentCommand(context.Background(), "agent-1", "cmd-1")
	if err != nil {
		t.Fatalf("AcknowledgeAgentCommand without API key: %v", err)
	}
	if runID != "run-no-key" {
		t.Errorf("runID = %q, want 'run-no-key'", runID)
	}
	if capturedAuth != "" {
		t.Errorf("Authorization = %q, want empty when no APIKey", capturedAuth)
	}
}

// RejectAgentCommand posts {outcome: "rejected", detail} (#1656) with detail
// cut to the hosted service's 2000-byte bound on a UTF-8 boundary, and reads
// no runId from the response.
func TestCommandService_RejectAgentCommand(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/agent-1/commands/cmd-1/ack" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	detail := strings.Repeat("a", 1999) + "é" + strings.Repeat("b", 50)
	if err := NewCommandService(c).RejectAgentCommand(context.Background(), "agent-1", "cmd-1", detail); err != nil {
		t.Fatalf("RejectAgentCommand: %v", err)
	}
	if body["outcome"] != "rejected" {
		t.Errorf("outcome = %q", body["outcome"])
	}
	if got := body["detail"]; len(got) > AgentCommandAckDetailMax || got != strings.Repeat("a", 1999) {
		t.Errorf("detail is %d bytes, want the 1999 bytes before the cut rune", len(got))
	}
}
