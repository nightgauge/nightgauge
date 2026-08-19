package ipc

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// newSessionTokenServer builds an IPC server whose platform client carries a
// license key, mirroring how the extension spawns the daemon today.
func newSessionTokenServer(t *testing.T, baseURL string) (*Server, *platform.Client) {
	t.Helper()
	pc, err := platform.NewClient(platform.Config{BaseURL: baseURL, LicenseKey: "lic-123"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	s := NewServer(nil, WithPlatformClient(pc))
	s.writer = &bytes.Buffer{}
	return s, pc
}

// TestPlatformSetSessionToken_InstallsAndClears is the whole point of #742: the
// daemon has to be able to learn the signed-in user's JWT after it was spawned,
// and to forget it on sign-out.
func TestPlatformSetSessionToken_InstallsAndClears(t *testing.T) {
	s, pc := newSessionTokenServer(t, "http://example.invalid")

	if pc.HasSessionToken() {
		t.Fatal("client started with a session token")
	}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "jwt.session.token",
	}); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}
	if !pc.HasSessionToken() {
		t.Error("session token was not installed on the platform client")
	}

	// Sign-out pushes an empty token.
	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "",
	}); err != nil {
		t.Fatalf("setSessionToken(clear): %v", err)
	}
	if pc.HasSessionToken() {
		t.Error("session token survived the clearing push")
	}
}

// TestPlatformSetSessionToken_ChangesTheWireCredential asserts the swap is
// observable on the wire, not merely in a field — a refreshed token must reach
// the platform on the very next request.
func TestPlatformSetSessionToken_ChangesTheWireCredential(t *testing.T) {
	got := make(chan string, 4)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer mock.Close()

	s, pc := newSessionTokenServer(t, mock.URL)

	if _, err := callHandler(t, s, "platform.healthCheck", nil); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
	if auth := <-got; auth != "Bearer lic-123" {
		t.Fatalf("auth = %q, want the license key before sign-in", auth)
	}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "jwt.first",
	}); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}
	if _, err := callHandler(t, s, "platform.healthCheck", nil); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
	if auth := <-got; auth != "Bearer jwt.first" {
		t.Fatalf("auth = %q, want the session token", auth)
	}

	// The refresh path pushes again; the daemon must not keep the stale token.
	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "jwt.refreshed",
	}); err != nil {
		t.Fatalf("setSessionToken(refresh): %v", err)
	}
	if _, err := callHandler(t, s, "platform.healthCheck", nil); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
	if auth := <-got; auth != "Bearer jwt.refreshed" {
		t.Fatalf("auth = %q, want the refreshed token", auth)
	}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{}); err != nil {
		t.Fatalf("setSessionToken(clear): %v", err)
	}
	if _, err := callHandler(t, s, "platform.healthCheck", nil); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
	if auth := <-got; auth != "Bearer lic-123" {
		t.Fatalf("auth = %q, want the license-key fallback after sign-out", auth)
	}
	if pc.HasSessionToken() {
		t.Error("session token survived sign-out")
	}
}

// TestPlatformSetSessionToken_NoPlatformClient keeps the nil guard honest: a
// community build with no platform configured must answer with an error rather
// than panic when the extension pushes a token at it.
func TestPlatformSetSessionToken_NoPlatformClient(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	_, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "jwt.session.token",
	})
	if err == nil {
		t.Fatal("expected an error when no platform client is configured")
	}
}
