package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testProfile() ExecutionProfile {
	return ExecutionProfile{
		Adapter: "codex", AdapterDisplayName: "Codex", AdapterSource: "config",
		PerformanceMode: "frontier", PerformanceModeSource: "file",
		Effort: "high", EffortSource: "config",
	}
}

// captureServer records the last request body and answers like the platform.
func captureServer(t *testing.T, status int, resp string) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var body []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		contentType = r.Header.Get("Content-Type")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &contentType
}

const registered = `{"agentId":"a","commandsUrl":"/v1/agents/a/commands","ttl_seconds":90}`

func TestRegisterAgent_CarriesTheExecutionProfile(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")
	for _, conversation := range []bool{false, true} {
		srv, body, _ := captureServer(t, http.StatusCreated, registered)
		reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3").
			WithExecutionProfile(func() (ExecutionProfile, bool, error) { return testProfile(), conversation, nil })
		if _, err := reg.RegisterAgent(context.Background()); err != nil {
			t.Fatalf("RegisterAgent: %v", err)
		}
		var got agentRegisterBody
		if err := json.Unmarshal(*body, &got); err != nil {
			t.Fatal(err)
		}
		if got.ExecutionProfile == nil || *got.ExecutionProfile != testProfile() {
			t.Errorf("execution_profile = %+v, want %+v", got.ExecutionProfile, testProfile())
		}
		want := []string{AgentRegisterCapabilityResolve}
		if conversation {
			want = append(want, AgentCapabilityConversation)
		}
		if len(got.Capabilities) != len(want) || got.Capabilities[len(want)-1] != want[len(want)-1] {
			t.Errorf("conversation=%v: capabilities = %v, want %v", conversation, got.Capabilities, want)
		}
	}
}

// A profile source that fails, or returns something outside the vocabulary,
// costs nothing: the body is exactly the pre-#1567 one, and conversation is
// never claimed on the back of a profile that was not sent.
func TestRegisterAgent_OmitsAProfileThatDidNotResolveOrValidate(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")
	leaky := testProfile()
	leaky.Adapter = "/Users/someone/work"
	sources := map[string]ProfileFunc{
		"error":   func() (ExecutionProfile, bool, error) { return ExecutionProfile{}, true, errors.New("no config") },
		"invalid": func() (ExecutionProfile, bool, error) { return leaky, true, nil },
	}
	for name, fn := range sources {
		srv, body, _ := captureServer(t, http.StatusCreated, registered)
		reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "").WithExecutionProfile(fn)
		if _, err := reg.RegisterAgent(context.Background()); err != nil {
			t.Fatalf("%s: RegisterAgent: %v", name, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(*body, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["execution_profile"]; ok {
			t.Errorf("%s: execution_profile sent: %s", name, *body)
		}
		if caps := raw["capabilities"].([]any); len(caps) != 1 {
			t.Errorf("%s: capabilities = %v, want only %s", name, caps, AgentRegisterCapabilityResolve)
		}
	}
}

// The heartbeat re-resolves the profile on every beat, so a change reaches the
// platform within one interval; without a source the beat stays bodiless.
func TestAgentHeartbeat_CarriesTheCurrentProfile(t *testing.T) {
	srv, body, contentType := captureServer(t, http.StatusOK, `{}`)

	if err := NewAgentRegistrationService(onlineClient(t, srv.URL), "").Heartbeat(context.Background(), "agent-1"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if len(*body) != 0 {
		t.Errorf("heartbeat without a profile source sent a body: %s", *body)
	}

	current := testProfile()
	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "").
		WithExecutionProfile(func() (ExecutionProfile, bool, error) { return current, false, nil })
	for _, adapter := range []string{"codex", "opencode"} {
		current.Adapter = adapter
		if err := reg.Heartbeat(context.Background(), "agent-1"); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		var got agentHeartbeatBody
		if err := json.Unmarshal(*body, &got); err != nil {
			t.Fatalf("heartbeat body %q: %v", *body, err)
		}
		if got.ExecutionProfile == nil || got.ExecutionProfile.Adapter != adapter {
			t.Errorf("heartbeat profile = %+v, want adapter %s", got.ExecutionProfile, adapter)
		}
		if *contentType != "application/json" {
			t.Errorf("Content-Type = %q", *contentType)
		}
	}
}

func TestExecutionProfile_ValidateRejectsAnythingButShortTokens(t *testing.T) {
	if err := testProfile().Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	mutations := map[string]func(*ExecutionProfile){
		"path adapter":      func(p *ExecutionProfile) { p.Adapter = "/tmp/x" },
		"secret adapter":    func(p *ExecutionProfile) { p.Adapter = "sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"empty adapter":     func(p *ExecutionProfile) { p.Adapter = "" },
		"display free text": func(p *ExecutionProfile) { p.AdapterDisplayName = "Codex <script>" },
		"unknown source":    func(p *ExecutionProfile) { p.AdapterSource = "workspace-root" },
		"mode with slash":   func(p *ExecutionProfile) { p.PerformanceMode = "a/b" },
		"effort uppercase":  func(p *ExecutionProfile) { p.Effort = "HIGH" },
		"effort source":     func(p *ExecutionProfile) { p.EffortSource = "" },
	}
	for name, mutate := range mutations {
		p := testProfile()
		mutate(&p)
		if p.Validate() == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}
