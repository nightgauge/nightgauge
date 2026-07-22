// server_ratelimit_wiring_test.go verifies that the IPC server wires the
// SharedRateLimitTracker into every client construction path it owns
// (Issue #3417). Without this, the proactive rate-limit gate is dead code
// in production and per-repo / per-user clients silently discard the
// X-RateLimit-* response headers that should refresh the shared file.

package ipc

import (
	"path/filepath"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

func TestServer_DefaultClientWiredToTracker(t *testing.T) {
	defaultClient := gh.NewClientWithToken("default-token")
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))

	s := NewServer(defaultClient, WithRateLimitTracker(tracker))

	if got := s.client.RateLimitTracker(); got != tracker {
		t.Fatalf("default client tracker = %v, want %v", got, tracker)
	}
	// Default client uses empty user key (collapses to "default" in tracker).
	if got, want := s.client.RateLimitTrackerUser(), ""; got != want {
		t.Errorf("default client tracker user = %q, want %q", got, want)
	}
}

func TestServer_NilDefaultClientIsSafe(t *testing.T) {
	// NewServer(nil) is a supported pattern (e.g. server_platform_auth_test.go).
	// Wiring must not panic when the default client is nil.
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewServer(nil) panicked: %v", r)
		}
	}()
	s := NewServer(nil, WithRateLimitTracker(tracker))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestServer_ClientForUserAttachesTracker(t *testing.T) {
	// Per-user clients constructed by clientForUser must feed the tracker
	// keyed by the user's name so multi-identity workspaces share quota
	// observations correctly.
	defaultClient := gh.NewClientWithToken("default-token")
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))

	// Test factory returns a synthetic client without invoking the gh CLI.
	factory := func(user string, _ bool) (*gh.Client, error) {
		return gh.NewClientWithToken("synthetic-" + user + "-token"), nil
	}

	s := NewServer(
		defaultClient,
		WithRateLimitTracker(tracker),
		WithUserClientFactory(factory),
	)

	c, err := s.clientForUser("alice")
	if err != nil {
		t.Fatalf("clientForUser(alice): %v", err)
	}
	if got := c.RateLimitTracker(); got != tracker {
		t.Fatalf("alice client tracker = %v, want %v", got, tracker)
	}
	if got, want := c.RateLimitTrackerUser(), "alice"; got != want {
		t.Errorf("alice client tracker user = %q, want %q", got, want)
	}

	// Cache hit on second call returns the same tracker-attached client.
	c2, err := s.clientForUser("alice")
	if err != nil {
		t.Fatalf("clientForUser(alice) second call: %v", err)
	}
	if c2 != c {
		t.Error("expected cache hit (same pointer) on second clientForUser call")
	}
}

func TestServer_ClientForUserEmptyReturnsDefault(t *testing.T) {
	// Existing contract: empty user returns the default client unchanged.
	defaultClient := gh.NewClientWithToken("default-token")
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))

	s := NewServer(defaultClient, WithRateLimitTracker(tracker))
	c, err := s.clientForUser("")
	if err != nil {
		t.Fatalf("clientForUser(\"\"): %v", err)
	}
	if c != s.client {
		t.Error("expected default client for empty user")
	}
	// And the default client is the tracker-wired one (verified separately).
	if c.RateLimitTracker() != tracker {
		t.Error("expected default client to carry tracker")
	}
}

func TestServer_ResolverReceivesTracker(t *testing.T) {
	// The server's resolver must be constructed with the same tracker so
	// per-repo clients also feed the shared file.
	defaultClient := gh.NewClientWithToken("default-token")
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))

	s := NewServer(defaultClient, WithRateLimitTracker(tracker))
	if s.resolver == nil {
		t.Fatal("server has no resolver")
	}
	if s.resolver.tracker != tracker {
		t.Errorf("resolver tracker = %v, want %v", s.resolver.tracker, tracker)
	}
}
