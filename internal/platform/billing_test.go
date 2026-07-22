package platform

import (
	"context"
	"testing"
)

func TestBillingService_CreatePortalSession_Offline(t *testing.T) {
	// Create a client that starts in offline mode (default).
	cfg := DefaultConfig()
	cfg.BaseURL = "http://localhost:0" // unreachable
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	svc := NewBillingService(client)
	_, err = svc.CreatePortalSession(context.Background())
	if err == nil {
		t.Fatal("CreatePortalSession offline: expected error, got nil")
	}
	expected := "billing portal requires online platform connectivity"
	if err.Error() != expected {
		t.Errorf("CreatePortalSession offline: expected %q, got %q", expected, err.Error())
	}
}
