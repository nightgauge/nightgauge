package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestRegisterAgent_Success verifies the 201 parse and the request body/auth the
// platform's RegisterAgentSchema expects (#341).
func TestRegisterAgent_Success(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid") // deterministic + hermetic (no home-dir read)

	var gotBody []byte
	var gotAuth, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"9c1f0f2e-1111-4222-8333-444455556666","commandsUrl":"/v1/agents/9c1f0f2e-1111-4222-8333-444455556666/commands","ttl_seconds":90}`))
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3")
	info, err := reg.RegisterAgent(context.Background())
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	if info.AgentID != "9c1f0f2e-1111-4222-8333-444455556666" {
		t.Errorf("agentId = %q", info.AgentID)
	}
	if info.CommandsURL != "/v1/agents/9c1f0f2e-1111-4222-8333-444455556666/commands" {
		t.Errorf("commandsUrl = %q", info.CommandsURL)
	}
	if info.TTLSeconds != 90 {
		t.Errorf("ttl_seconds = %d, want 90", info.TTLSeconds)
	}

	if gotMethod != http.MethodPost || gotPath != "/v1/agents/register" {
		t.Errorf("request = %s %s, want POST /v1/agents/register", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}

	var body agentRegisterBody
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal register body: %v", err)
	}
	if body.MachineID != "test-machine-uuid" {
		t.Errorf("machine_id = %q, want the resolved machine id", body.MachineID)
	}
	if body.AgentVersion != "1.2.3" {
		t.Errorf("agent_version = %q, want 1.2.3", body.AgentVersion)
	}
	if len(body.Capabilities) != 1 || body.Capabilities[0] != AgentRegisterCapabilityResolve {
		t.Errorf("capabilities = %v, want [%s]", body.Capabilities, AgentRegisterCapabilityResolve)
	}
}

// TestRegisterAgent_OmitsAgentVersionWhenEmpty verifies a dev/unknown build omits
// agent_version (the field is omitempty; the platform schema treats it optional).
func TestRegisterAgent_OmitsAgentVersionWhenEmpty(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"a","commandsUrl":"/v1/agents/a/commands","ttl_seconds":90}`))
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "")
	if _, err := reg.RegisterAgent(context.Background()); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("unmarshal raw body: %v", err)
	}
	if _, present := raw["agent_version"]; present {
		t.Errorf("agent_version present with empty version — must be omitted")
	}
}

// TestRegisterAgent_NonCreatedError verifies a non-201 response is an error the
// caller retries (no agentId returned).
func TestRegisterAgent_NonCreatedError(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusInternalServerError, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":"ERR"}`))
		}))
		reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")
		info, err := reg.RegisterAgent(context.Background())
		srv.Close()
		if err == nil {
			t.Errorf("status %d: expected error, got nil (agentId=%q)", status, info.AgentID)
		}
		if info.AgentID != "" {
			t.Errorf("status %d: agentId = %q, want empty on error", status, info.AgentID)
		}
	}
}

// TestRegisterAgent_MissingAgentIDError verifies a 201 whose body has no agentId
// is an error (defensive — never treat a malformed 201 as registered).
func TestRegisterAgent_MissingAgentIDError(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"commandsUrl":"/x","ttl_seconds":90}`))
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")
	if _, err := reg.RegisterAgent(context.Background()); err == nil {
		t.Fatalf("expected error for 201 missing agentId, got nil")
	}
}

// TestAgentHeartbeat_OKAndNotFound verifies a 2xx heartbeat is nil and a 404
// heartbeat returns the sentinel ErrAgentNotFound (the re-register signal).
func TestAgentHeartbeat_OKAndNotFound(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	var status atomic.Int32
	status.Store(http.StatusOK)
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		code := int(status.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte(`{"ttl_seconds":90}`))
		} else {
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
		}
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")

	if err := reg.Heartbeat(context.Background(), "agent-xyz"); err != nil {
		t.Fatalf("heartbeat 200: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/agents/agent-xyz/heartbeat" {
		t.Errorf("request = %s %s, want PUT /v1/agents/agent-xyz/heartbeat", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}

	status.Store(http.StatusNotFound)
	err := reg.Heartbeat(context.Background(), "agent-xyz")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("heartbeat 404 err = %v, want ErrAgentNotFound", err)
	}
}

// TestAgentRegistration_ReRegisterOn404 exercises the exact primitives the serve
// bridge composes for TTL-eviction recovery: a heartbeat 404 signals eviction,
// a fresh RegisterAgent yields a new agent id, and the new id heartbeats OK.
func TestAgentRegistration_ReRegisterOn404(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	// registerCount drives a distinct agent id per registration so we can prove
	// the id actually swaps.
	var registerCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/agents/register" && r.Method == http.MethodPost:
			n := registerCount.Add(1)
			w.WriteHeader(http.StatusCreated)
			id := "agent-old"
			if n >= 2 {
				id = "agent-new"
			}
			_, _ = w.Write([]byte(`{"agentId":"` + id + `","commandsUrl":"/v1/agents/` + id + `/commands","ttl_seconds":90}`))
		case r.URL.Path == "/v1/agents/agent-old/heartbeat" && r.Method == http.MethodPut:
			// The old agent has been evicted.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
		case r.URL.Path == "/v1/agents/agent-new/heartbeat" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ttl_seconds":90}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")
	ctx := context.Background()

	first, err := reg.RegisterAgent(ctx)
	if err != nil || first.AgentID != "agent-old" {
		t.Fatalf("first register = (%q, %v), want agent-old", first.AgentID, err)
	}

	// Heartbeat the old id → 404 → re-register.
	if !errors.Is(reg.Heartbeat(ctx, first.AgentID), ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound heartbeating evicted agent")
	}
	second, err := reg.RegisterAgent(ctx)
	if err != nil || second.AgentID != "agent-new" {
		t.Fatalf("re-register = (%q, %v), want agent-new", second.AgentID, err)
	}
	// The new id heartbeats OK.
	if err := reg.Heartbeat(ctx, second.AgentID); err != nil {
		t.Fatalf("heartbeat new agent: %v", err)
	}
}
