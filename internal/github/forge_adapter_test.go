package github

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// TestForgeAdapter_SatisfiesForgeClient is a runtime echo of the
// compile-time assert in forge_adapter.go. It documents the expected
// type-shape and serves as a discoverable reference for future
// adapters porting from another forge.
func TestForgeAdapter_SatisfiesForgeClient(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	var fc forge.ForgeClient = adapter
	if fc == nil {
		t.Fatal("ForgeAdapter is not assignable to forge.ForgeClient")
	}
}

// TestForgeAdapter_LazyServiceConstruction confirms the lazy/cached
// service construction returns the same instance on repeated access —
// callers can hold onto a sub-service across calls without surprise.
func TestForgeAdapter_LazyServiceConstruction(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	if adapter.Issues() != adapter.Issues() {
		t.Error("Issues() should return the same instance on repeated access")
	}
	if adapter.PRs() != adapter.PRs() {
		t.Error("PRs() should return the same instance on repeated access")
	}
	if adapter.Project() != adapter.Project() {
		t.Error("Project() should return the same instance on repeated access")
	}
	if adapter.Board() != adapter.Board() {
		t.Error("Board() should return the same instance on repeated access")
	}
	if adapter.CI() != adapter.CI() {
		t.Error("CI() should return the same instance on repeated access")
	}
	if adapter.Rulesets() != adapter.Rulesets() {
		t.Error("Rulesets() should return the same instance on repeated access")
	}
}

// TestClient_Forge confirms the *Client.Forge() convenience accessor
// returns a usable ForgeClient.
func TestClient_Forge(t *testing.T) {
	client := NewClientWithToken("fake")

	var fc forge.ForgeClient = client.Forge("nightgauge", 1, OwnerTypeOrg)
	if fc == nil {
		t.Fatal("Client.Forge() returned nil")
	}
	if fc.Issues() == nil {
		t.Error("Forge().Issues() returned nil")
	}
}

// TestForgeAdapter_AuthIsClient documents the design choice (ADR-006)
// that *Client itself satisfies forge.AuthService directly, without a
// wrapper struct. Future contributors who try to add a wrapper will see
// this test fail and find the rationale in the ADR.
func TestForgeAdapter_AuthIsClient(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	auth := adapter.Auth()
	if auth == nil {
		t.Fatal("Auth() returned nil")
	}
	// ADR-006: AuthService is satisfied by *Client itself, so the
	// returned value should be the same client instance (interface
	// equality of underlying pointer).
	if asClient, ok := auth.(*Client); !ok || asClient != client {
		t.Error("Auth() should return the wrapping *Client itself; ADR-006 documents this choice")
	}
}
