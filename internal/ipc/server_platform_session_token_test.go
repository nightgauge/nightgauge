package ipc

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestPlatformSetSessionToken_ConstructsClientFromSessionAlone is #756: a
// daemon spawned with no platform_url, no api_key, and no license_key — the
// state a brand-new user is in the instant they sign in through the
// extension — must still accept the session token instead of answering
// "platform client not configured" forever. The signed-in session is itself
// proof a platform exists.
func TestPlatformSetSessionToken_ConstructsClientFromSessionAlone(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	if s.platformClient != nil {
		t.Fatal("test setup: server should start with no platform client")
	}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "jwt.session.token",
	}); err != nil {
		t.Fatalf("setSessionToken on an unconfigured daemon: %v", err)
	}

	if s.platformClient == nil {
		t.Fatal("a signed-in session did not construct the platform client")
	}
	if !s.platformClient.HasSessionToken() {
		t.Error("the constructed client did not receive the session token")
	}
}

// TestPlatformSetSessionToken_LazyClientUsesDefaultBaseURL locks in the other
// half of #756's acceptance criteria: the lazily-built client defaults to
// platform.DefaultConfig()'s base URL, because the session token carries no
// URL of its own and none was configured at startup.
func TestPlatformSetSessionToken_LazyClientUsesDefaultBaseURL(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "jwt.session.token",
	}); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}

	want := platform.DefaultConfig().BaseURL
	if want == "" {
		t.Fatal("platform.DefaultConfig().BaseURL is empty — the lazy path would build a client with no host")
	}
	if got := sessionOnlyPlatformConfig().BaseURL; got != want {
		t.Errorf("lazy-path base URL = %q, want the default %q", got, want)
	}
}

// TestPlatformSetSessionToken_EmptyTokenNoClientStaysUnconfigured is the
// other side of #756: a daemon that never had api_url/api_key/license_key at
// startup AND has no signed-in session (an empty token — sign-out, or the
// extension's startup sync finding nothing in SecretStorage) must still get
// no client at all, with an accurate "not configured" message rather than a
// spuriously constructed, unauthenticated one.
func TestPlatformSetSessionToken_EmptyTokenNoClientStaysUnconfigured(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	_, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
		Token: "",
	})
	if err == nil {
		t.Fatal("expected an error when no session and no platform config is present")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want it to say the platform is not configured (see #748's classification)", err.Error())
	}
	if s.platformClient != nil {
		t.Error("an empty token must not construct a platform client")
	}
}

// TestPlatformSetSessionToken_ConcurrentSignInBuildsOneClient exercises the
// exact ordering hazard #756 called out: IPC requests are dispatched one
// goroutine per call (see handleRequest), so nothing guarantees a single
// setSessionToken call lands before any other platform.* request reaches a
// cold daemon. Firing many setSessionToken calls at once must still leave
// exactly one platform client installed — never a second one built (and
// health-polling) after the first, and never two goroutines racing the
// nil check and each losing the assignment. Run with -race.
func TestPlatformSetSessionToken_ConcurrentSignInBuildsOneClient(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{
				Token: "jwt.session.token",
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: setSessionToken: %v", i, err)
		}
	}
	if s.platformClient == nil {
		t.Fatal("no platform client was constructed")
	}
	if !s.platformClient.HasSessionToken() {
		t.Error("the surviving client never received the session token")
	}
}
