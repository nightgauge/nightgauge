package platform

import (
	"context"
	"testing"
)

func TestTeamService_GetMembers_Offline(t *testing.T) {
	// Create a client that starts in offline mode (default).
	cfg := DefaultConfig()
	cfg.BaseURL = "http://localhost:0" // unreachable
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewTeamService(client)
	members, err := svc.GetMembers(context.Background())
	if err != nil {
		t.Fatalf("GetMembers offline: unexpected error: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("GetMembers offline: expected empty slice, got %d members", len(members))
	}
}
