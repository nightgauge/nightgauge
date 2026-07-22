package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommandService_AcknowledgeCommand_Success(t *testing.T) {
	var (
		capturedMethod string
		capturedPath   string
		capturedBody   CommandResult
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	svc := NewCommandService(client)
	result := CommandResult{
		Status:     "success",
		Output:     "build passed",
		DurationMs: 1234,
	}

	if err := svc.AcknowledgeCommand(context.Background(), "cmd-abc-123", result); err != nil {
		t.Fatalf("AcknowledgeCommand: unexpected error: %v", err)
	}

	if capturedMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", capturedMethod)
	}
	if capturedPath != "/v1/commands/cmd-abc-123/ack" {
		t.Errorf("path = %s, want /v1/commands/cmd-abc-123/ack", capturedPath)
	}
	if capturedBody.Status != "success" {
		t.Errorf("body.status = %s, want success", capturedBody.Status)
	}
	if capturedBody.Output != "build passed" {
		t.Errorf("body.output = %s, want 'build passed'", capturedBody.Output)
	}
	if capturedBody.DurationMs != 1234 {
		t.Errorf("body.duration_ms = %d, want 1234", capturedBody.DurationMs)
	}
}

func TestCommandService_AcknowledgeCommand_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	svc := NewCommandService(client)
	result := CommandResult{Status: "failure", Error: "build failed", DurationMs: 500}

	err = svc.AcknowledgeCommand(context.Background(), "cmd-xyz-789", result)
	if err == nil {
		t.Fatal("AcknowledgeCommand: expected error on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to contain '500'", err.Error())
	}
	if !strings.Contains(err.Error(), "internal server error") {
		t.Errorf("error = %q, want it to contain response body", err.Error())
	}
}

func TestCommandService_AcknowledgeCommand_WithAPIKey(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "my-api-key"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	svc := NewCommandService(client)
	result := CommandResult{Status: "success", DurationMs: 100}

	if err := svc.AcknowledgeCommand(context.Background(), "cmd-auth-test", result); err != nil {
		t.Fatalf("AcknowledgeCommand: unexpected error: %v", err)
	}
	if capturedAuth != "Bearer my-api-key" {
		t.Errorf("Authorization = %q, want 'Bearer my-api-key'", capturedAuth)
	}
}

func TestCommandService_AcknowledgeCommand_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srvURL := srv.URL
	srv.Close() // Close before making the request

	cfg := Config{BaseURL: srvURL}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.setMode(ModeOnline)

	svc := NewCommandService(client)
	result := CommandResult{Status: "success", DurationMs: 50}

	err = svc.AcknowledgeCommand(context.Background(), "cmd-net-error", result)
	if err == nil {
		t.Fatal("AcknowledgeCommand: expected error for closed server, got nil")
	}
}

func TestCommandService_AcknowledgeCommand_Offline(t *testing.T) {
	// Client starts offline by default (no health check run).
	cfg := DefaultConfig()
	cfg.BaseURL = "http://localhost:0" // unreachable
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewCommandService(client)
	result := CommandResult{Status: "success", DurationMs: 100}

	err = svc.AcknowledgeCommand(context.Background(), "cmd-offline-001", result)
	if err == nil {
		t.Fatal("AcknowledgeCommand offline: expected error, got nil")
	}
	expected := "command acknowledgement requires online platform connectivity"
	if err.Error() != expected {
		t.Errorf("AcknowledgeCommand offline: got %q, want %q", err.Error(), expected)
	}
}
