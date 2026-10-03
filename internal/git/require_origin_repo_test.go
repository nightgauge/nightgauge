package git

import (
	"strings"
	"testing"
)

// TestRequireOriginRepo pins the guard the epic commands apply before they act
// on a checkout (#2388): only a checkout whose origin is the named repository
// passes, matched case-insensitively, and a refusal names what the checkout
// is instead.
func TestRequireOriginRepo(t *testing.T) {
	svc, dir := setupTestRepo(t)
	if err := svc.RequireOriginRepo("example-org/app"); err == nil || !strings.Contains(err.Error(), "origin is unknown") {
		t.Fatalf("checkout with no origin: err = %v, want an unknown-origin refusal", err)
	}

	gitExecTest(t, dir, "remote", "add", "origin", "https://github.com/Example-Org/App.git")
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.RequireOriginRepo("example-org/app"); err != nil {
		t.Errorf("checkout of the named repository refused: %v", err)
	}
	err = svc.RequireOriginRepo("example-org/platform")
	if err == nil || !strings.Contains(err.Error(), "a checkout of Example-Org/App, not example-org/platform") {
		t.Errorf("checkout of another repository: err = %v, want a refusal naming Example-Org/App", err)
	}
}
