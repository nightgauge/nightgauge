package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// platform.status reports the workspace writes the daemon's latest agent
// registration was refused (#2372), and nothing once a later registration
// was refused none.
func TestPlatformStatus_ReportsTheLatestRegistrationsRefusals(t *testing.T) {
	pc := newTestPlatformClientFor(t, "http://127.0.0.1:1", "test-key")
	s := NewServer(nil, WithPlatformClient(pc))
	s.writer = &bytes.Buffer{}

	status := func() map[string]interface{} {
		t.Helper()
		result, err := s.methods["platform.status"](context.Background(), nil)
		if err != nil {
			t.Fatalf("platform.status: %v", err)
		}
		raw, _ := json.Marshal(result)
		var decoded map[string]interface{}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode platform.status: %v", err)
		}
		return decoded
	}

	if _, ok := status()["refusedWorkspaceWrites"]; ok {
		t.Fatal("platform.status reported refusals before any registration")
	}

	refused := []platform.RefusedWorkspaceWrite{{
		Workspace: "acme-platform", TeamID: "team-1", Code: "PERMISSION_DENIED",
		Permission: "workspace:update", Message: "the platform's own, unbounded message",
	}}
	s.SetRefusedWorkspaceWrites(refused)
	// The bounded fields and the operator's line; the raw message stays out.
	want := []interface{}{map[string]interface{}{
		"workspace": "acme-platform", "teamId": "team-1", "code": "PERMISSION_DENIED",
		"permission": "workspace:update", "description": refused[0].Describe(),
	}}
	if got := status()["refusedWorkspaceWrites"]; !reflect.DeepEqual(got, want) {
		t.Errorf("refusedWorkspaceWrites = %v, want %v", got, want)
	}

	// The caller's slice is copied: changing it later changes no status.
	refused[0].Workspace = "changed"
	if got := s.RefusedWorkspaceWrites()[0].Workspace; got != "acme-platform" {
		t.Errorf("the recorded refusal follows the caller's slice: %q", got)
	}

	s.SetRefusedWorkspaceWrites(nil)
	if _, ok := status()["refusedWorkspaceWrites"]; ok {
		t.Error("platform.status kept the refusals after a registration refused none")
	}
}
