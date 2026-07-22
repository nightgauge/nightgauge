package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSyncQueueSync_RequestContract verifies the PUT /v1/queue/sync request:
// method, path, bearer auth header, and JSON body shape.
func TestSyncQueueSync_RequestContract(t *testing.T) {
	type captured struct {
		method string
		path   string
		auth   string
		body   []byte
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- captured{r.Method, r.URL.Path, r.Header.Get("Authorization"), b}
		jsonResponse(w, map[string]interface{}{"items": []any{}})
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "key-abc"})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	payload := QueueSyncPayload{
		MachineID: "machine-1",
		Origin:    "local_cli",
		Items: []QueueSyncItem{
			{IssueNumber: 10, Position: 1, Priority: "high", Status: "processing", RepoFullName: "nightgauge/test", Title: "Working"},
		},
	}
	if err := svc.syncQueueSync(context.Background(), payload); err != nil {
		t.Fatalf("syncQueueSync: %v", err)
	}

	select {
	case c := <-got:
		if c.method != http.MethodPut {
			t.Errorf("method = %s, want PUT", c.method)
		}
		if c.path != "/v1/queue/sync" {
			t.Errorf("path = %s, want /v1/queue/sync", c.path)
		}
		if c.auth != "Bearer key-abc" {
			t.Errorf("auth = %q, want %q", c.auth, "Bearer key-abc")
		}
		var decoded QueueSyncPayload
		if err := json.Unmarshal(c.body, &decoded); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if decoded.MachineID != "machine-1" || decoded.Origin != "local_cli" {
			t.Errorf("machineId/origin = %q/%q", decoded.MachineID, decoded.Origin)
		}
		if len(decoded.Items) != 1 || decoded.Items[0].IssueNumber != 10 || decoded.Items[0].Status != "processing" {
			t.Errorf("items = %+v", decoded.Items)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the sync request")
	}
}

// TestSyncQueueSync_ServerError surfaces a non-2xx as an error.
func TestSyncQueueSync_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL, APIKey: "k"})
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	err := svc.syncQueueSync(context.Background(), QueueSyncPayload{MachineID: "m", Items: nil})
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
}

// TestSyncQueue_NoMachineIDNoOp verifies an empty machine id short-circuits
// before any goroutine/HTTP work.
func TestSyncQueue_NoMachineIDNoOp(t *testing.T) {
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL, APIKey: "k"})
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	svc.SyncQueue(context.Background(), QueueSyncPayload{MachineID: "", Items: []QueueSyncItem{{IssueNumber: 1}}})

	select {
	case <-hit:
		t.Fatal("server was hit despite empty machine id")
	case <-time.After(200 * time.Millisecond):
		// expected: no request
	}
}

// TestTelemetryServiceSyncQueue_StampsIdentity verifies the wrapper stamps the
// client's agent id and the local_cli origin onto the snapshot.
func TestTelemetryServiceSyncQueue_StampsIdentity(t *testing.T) {
	got := make(chan QueueSyncPayload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p QueueSyncPayload
		_ = json.Unmarshal(b, &p)
		got <- p
		jsonResponse(w, map[string]interface{}{"items": []any{}})
	}))
	defer srv.Close()

	c, _ := NewClient(Config{BaseURL: srv.URL, APIKey: "k", AgentID: "machine-xyz"})
	c.setMode(ModeOnline)
	svc := NewTelemetryService(c)

	svc.SyncQueue(context.Background(), []QueueSyncItem{{IssueNumber: 7, Position: 1, Status: "pending"}})

	select {
	case p := <-got:
		if p.MachineID != "machine-xyz" {
			t.Errorf("machineId = %q, want machine-xyz", p.MachineID)
		}
		if p.Origin != "local_cli" {
			t.Errorf("origin = %q, want local_cli", p.Origin)
		}
		if len(p.Items) != 1 || p.Items[0].IssueNumber != 7 {
			t.Errorf("items = %+v", p.Items)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the sync request")
	}
}

// TestNewClient_LicenseKeyFallback verifies that when no APIKey is set, the
// license key is used as the bearer (the auth-wiring fix).
func TestNewClient_LicenseKeyFallback(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// No APIKey — only a license key.
	c, err := NewClient(Config{BaseURL: srv.URL, LicenseKey: "lic-123"})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	_ = svc.syncQueueSync(context.Background(), QueueSyncPayload{MachineID: "m"})

	select {
	case auth := <-got:
		if auth != "Bearer lic-123" {
			t.Errorf("auth = %q, want %q (license key fallback)", auth, "Bearer lic-123")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}
}

// TestNewClient_APIKeyWinsOverLicense verifies the explicit API key takes
// precedence over the license key when both are set.
func TestNewClient_APIKeyWinsOverLicense(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://example.invalid", APIKey: "api-key", LicenseKey: "lic"})
	if err != nil {
		t.Fatal(err)
	}
	if c.apiKey != "api-key" {
		t.Errorf("resolved bearer = %q, want api-key", c.apiKey)
	}
}

// TestResolveMachineID_EnvOverride verifies the env var wins.
func TestResolveMachineID_EnvOverride(t *testing.T) {
	t.Setenv(machineIDEnv, "cloud-runner-7")
	if got := ResolveMachineID(); got != "cloud-runner-7" {
		t.Errorf("ResolveMachineID() = %q, want cloud-runner-7", got)
	}
}

// TestResolveMachineID_PersistsAndReuses verifies a UUID is minted once and
// reused on subsequent calls (stable per machine).
func TestResolveMachineID_PersistsAndReuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home resolution
	if v := os.Getenv(machineIDEnv); v != "" {
		t.Setenv(machineIDEnv, "")
	}

	first := ResolveMachineID()
	if first == "" {
		t.Fatal("ResolveMachineID() returned empty")
	}
	// File should now exist.
	if _, err := os.Stat(filepath.Join(home, machineIDFile)); err != nil {
		t.Fatalf("machine-id file not persisted: %v", err)
	}
	second := ResolveMachineID()
	if second != first {
		t.Errorf("machine id not stable: first=%q second=%q", first, second)
	}
}
