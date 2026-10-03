package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The daemon's instance id (#2395) is one random UUID v4 per process: every
// service built in the process carries the same one, and it is within the
// platform's bound.
func TestInstanceID_IsOneUUIDPerProcess(t *testing.T) {
	id := processInstanceID()
	if id != processInstanceID() {
		t.Fatal("processInstanceID changed within one process")
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 4 {
		t.Fatalf("instance id %q is not a UUID v4 (err %v)", id, err)
	}
	if wireInstanceID(id) != id {
		t.Fatalf("instance id %q fails the platform's bound", id)
	}
	a := NewAgentRegistrationService(nil, "")
	b := NewAgentRegistrationService(nil, "")
	if a.instanceID != id || b.instanceID != id {
		t.Errorf("services carry %q and %q, want the process's %q", a.instanceID, b.instanceID, id)
	}
}

// What leaves the machine is bounded at the wire: 1–64 of [A-Za-z0-9_-].
func TestWireInstanceID_KeepsOnlyTheBound(t *testing.T) {
	for _, ok := range []string{"a", "0f8b2c1e-7d4a-4b9e-9c3f-2a1b3c4d5e6f", "A_b-9", strings.Repeat("x", 64)} {
		if got := wireInstanceID(ok); got != ok {
			t.Errorf("wireInstanceID(%q) = %q, want it kept", ok, got)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 65), "has space", "/Users/someone", "a.b", "é"} {
		if got := wireInstanceID(bad); got != "" {
			t.Errorf("wireInstanceID(%q) = %q, want it dropped", bad, got)
		}
	}
}

// The registration carries the instance id top-level, beside the profile and
// never inside it.
func TestRegisterAgent_CarriesTheInstanceID(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")
	srv, body, _ := captureServer(t, http.StatusCreated, registered)
	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3").
		WithExecutionProfile(func() (ExecutionProfile, bool, error) { return testProfile(), false, nil })
	if _, err := reg.RegisterAgent(context.Background()); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(*body, &raw); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := json.Unmarshal(raw["instance_id"], &got); err != nil || got != processInstanceID() {
		t.Errorf("instance_id = %s, want %q", raw["instance_id"], processInstanceID())
	}
	var profile map[string]any
	if err := json.Unmarshal(raw["execution_profile"], &profile); err != nil {
		t.Fatalf("execution_profile: %v", err)
	}
	if _, inside := profile["instance_id"]; inside {
		t.Errorf("instance_id sent inside execution_profile: %s", raw["execution_profile"])
	}
}

// Every beat carries the instance id, with or without a profile: a beat is how
// the platform knows the instance is alive.
func TestHeartbeat_CarriesTheInstanceIDOnEveryBeat(t *testing.T) {
	srv, body, contentType := captureServer(t, http.StatusOK, `{}`)
	bare := NewAgentRegistrationService(onlineClient(t, srv.URL), "")
	withProfile := NewAgentRegistrationService(onlineClient(t, srv.URL), "").
		WithExecutionProfile(func() (ExecutionProfile, bool, error) { return testProfile(), false, nil })

	for name, reg := range map[string]*AgentRegistrationService{"no profile": bare, "profile": withProfile} {
		for beat := 0; beat < 2; beat++ {
			if err := reg.Heartbeat(context.Background(), "agent-1"); err != nil {
				t.Fatalf("%s: Heartbeat: %v", name, err)
			}
			var got agentHeartbeatBody
			if err := json.Unmarshal(*body, &got); err != nil {
				t.Fatalf("%s: heartbeat body %q: %v", name, *body, err)
			}
			if got.InstanceID != processInstanceID() {
				t.Errorf("%s beat %d: instance_id = %q, want %q", name, beat, got.InstanceID, processInstanceID())
			}
			if *contentType != "application/json" {
				t.Errorf("%s: Content-Type = %q", name, *contentType)
			}
		}
	}
	if err := bare.Heartbeat(context.Background(), "agent-1"); err != nil {
		t.Fatal(err)
	}
	if want := `{"instance_id":"` + processInstanceID() + `"}`; string(*body) != want {
		t.Errorf("beat without a profile = %s, want %s", *body, want)
	}
}

// An instance id outside the bound is never sent: the registration leaves the
// field out, and a beat with nothing else to carry stays bodiless.
func TestInstanceID_OutOfBoundsIsNotSent(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")
	srv, body, _ := captureServer(t, http.StatusCreated, registered)
	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "")
	reg.instanceID = wireInstanceID("/Users/someone/work")
	if _, err := reg.RegisterAgent(context.Background()); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if strings.Contains(string(*body), "instance_id") {
		t.Errorf("registration sent an out-of-bounds instance id: %s", *body)
	}

	beatSrv, beatBody, _ := captureServer(t, http.StatusOK, `{}`)
	beat := NewAgentRegistrationService(onlineClient(t, beatSrv.URL), "")
	beat.instanceID = wireInstanceID("")
	if err := beat.Heartbeat(context.Background(), "agent-1"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if len(*beatBody) != 0 {
		t.Errorf("beat with nothing to carry sent a body: %s", *beatBody)
	}
}
