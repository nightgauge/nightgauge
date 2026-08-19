package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestBearer_Precedence pins the credential order the hosted API forces on us:
// the signed-in user's JWT outranks an explicit API key, which outranks the
// license key. A license key identifies an account and 401s on every
// user-scoped route, so it can only ever be the last resort (#742).
func TestBearer_Precedence(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		session string
		want    string
	}{
		{
			name: "license key only",
			cfg:  Config{BaseURL: "http://example.invalid", LicenseKey: "lic"},
			want: "lic",
		},
		{
			name: "api key beats license key",
			cfg:  Config{BaseURL: "http://example.invalid", APIKey: "api", LicenseKey: "lic"},
			want: "api",
		},
		{
			name:    "session token beats api key and license key",
			cfg:     Config{BaseURL: "http://example.invalid", APIKey: "api", LicenseKey: "lic"},
			session: "jwt.header.payload",
			want:    "jwt.header.payload",
		},
		{
			name:    "session token with no other credential",
			cfg:     Config{BaseURL: "http://example.invalid"},
			session: "jwt.header.payload",
			want:    "jwt.header.payload",
		},
		{
			name: "no credential at all",
			cfg:  Config{BaseURL: "http://example.invalid"},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if tc.session != "" {
				c.SetSessionToken(tc.session)
			}
			if got := c.bearer(); got != tc.want {
				t.Errorf("bearer() = %q, want %q", got, tc.want)
			}
			if got, want := c.HasSessionToken(), tc.session != ""; got != want {
				t.Errorf("HasSessionToken() = %v, want %v", got, want)
			}
		})
	}
}

// TestSetSessionToken_ClearsBackToLicenseKey verifies sign-out semantics: an
// empty token removes the JWT and the client falls back exactly as a
// never-signed-in headless run would, rather than going credential-less.
func TestSetSessionToken_ClearsBackToLicenseKey(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://example.invalid", LicenseKey: "lic"})
	if err != nil {
		t.Fatal(err)
	}

	c.SetSessionToken("jwt.a.b")
	if got := c.bearer(); got != "jwt.a.b" {
		t.Fatalf("bearer() = %q, want the session token", got)
	}

	c.SetSessionToken("")
	if got := c.bearer(); got != "lic" {
		t.Errorf("after clear, bearer() = %q, want the license key", got)
	}
	if c.HasSessionToken() {
		t.Error("HasSessionToken() = true after clear")
	}
}

// TestSetSessionToken_TrimsWhitespace: a whitespace-only push is a clear, not a
// credential made of spaces.
func TestSetSessionToken_TrimsWhitespace(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://example.invalid", LicenseKey: "lic"})
	if err != nil {
		t.Fatal(err)
	}

	c.SetSessionToken("  jwt.a.b\n")
	if got := c.bearer(); got != "jwt.a.b" {
		t.Errorf("bearer() = %q, want the trimmed token", got)
	}

	c.SetSessionToken("   ")
	if got := c.bearer(); got != "lic" {
		t.Errorf("whitespace-only push should clear; bearer() = %q", got)
	}
}

// TestSetSessionToken_ReachesRawHTTPPaths covers the analytics/compliance style
// callers, which build their own *http.Request instead of going through the
// generated client. Before #742 they read a bearer frozen at construction.
func TestSetSessionToken_ReachesRawHTTPPaths(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, LicenseKey: "lic-123"})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	_ = svc.syncQueueSync(context.Background(), QueueSyncPayload{MachineID: "m"})
	if auth := receive(t, got); auth != "Bearer lic-123" {
		t.Fatalf("auth = %q, want the license key before sign-in", auth)
	}

	// The user signs in: every later request must carry the JWT.
	c.SetSessionToken("jwt.session.token")
	_ = svc.syncQueueSync(context.Background(), QueueSyncPayload{MachineID: "m"})
	if auth := receive(t, got); auth != "Bearer jwt.session.token" {
		t.Fatalf("auth = %q, want the session token after sign-in", auth)
	}

	// Refresh rotates the token — the daemon must follow, or the session dies
	// one access-token lifetime after sign-in.
	c.SetSessionToken("jwt.rotated.token")
	_ = svc.syncQueueSync(context.Background(), QueueSyncPayload{MachineID: "m"})
	if auth := receive(t, got); auth != "Bearer jwt.rotated.token" {
		t.Fatalf("auth = %q, want the rotated token", auth)
	}

	// Sign-out clears it and the license key takes over again.
	c.SetSessionToken("")
	_ = svc.syncQueueSync(context.Background(), QueueSyncPayload{MachineID: "m"})
	if auth := receive(t, got); auth != "Bearer lic-123" {
		t.Fatalf("auth = %q, want the license key after sign-out", auth)
	}
}

// TestSetSessionToken_ReachesGeneratedClient covers the other half: the
// oapi-codegen client authenticates through a request-editor closure that used
// to capture a bearer resolved once in NewClient.
func TestSetSessionToken_ReachesGeneratedClient(t *testing.T) {
	got := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, LicenseKey: "lic-123"})
	if err != nil {
		t.Fatal(err)
	}

	_, _ = c.API().GetHealthWithResponse(context.Background())
	if auth := receive(t, got); auth != "Bearer lic-123" {
		t.Fatalf("auth = %q, want the license key before sign-in", auth)
	}

	c.SetSessionToken("jwt.session.token")
	_, _ = c.API().GetHealthWithResponse(context.Background())
	if auth := receive(t, got); auth != "Bearer jwt.session.token" {
		t.Fatalf("auth = %q, want the session token after sign-in", auth)
	}
}

// TestSetSessionToken_Concurrent is a -race probe: the credential is swapped
// from the IPC handler goroutine while request goroutines read it.
func TestSetSessionToken_Concurrent(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://example.invalid", LicenseKey: "lic"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = c.bearer()
				_ = c.HasSessionToken()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if j%2 == 0 {
					c.SetSessionToken("jwt.rotated")
				} else {
					c.SetSessionToken("")
				}
			}
		}(i)
	}
	wg.Wait()
}

func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
		return ""
	}
}
