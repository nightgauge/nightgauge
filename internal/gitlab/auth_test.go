package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// newTestClient creates a *Client wired to the given test server.
// All requests are rewritten to target the test server URL.
func newTestClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	c := NewClient(srv.URL, token)
	return c
}

// newTestClientWithMethod creates a *Client wired to the test server with a
// specific resolved auth method.
func newTestClientWithMethod(t *testing.T, srv *httptest.Server, token string, m authMethod, deployUser string) *Client {
	t.Helper()
	c := NewClient(srv.URL, token, WithResolvedMethod(m, deployUser))
	return c
}

// --- PAT scope checking ---

func TestPATCheckTokenScopes_AllPresent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "" {
			http.Error(w, "missing PRIVATE-TOKEN", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scopes":     []string{"api", "read_repository", "read_user"},
			"expires_at": nil,
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, "test-pat-token")
	a := NewAuthAdapter(c)

	info, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes: %v", err)
	}
	if !info.Valid {
		t.Errorf("expected Valid=true, got false; missing=%v", info.MissingScopes)
	}
	if info.Login != "alice" {
		t.Errorf("Login = %q, want %q", info.Login, "alice")
	}
	if info.Resolution != "pat" {
		t.Errorf("Resolution = %q, want %q", info.Resolution, "pat")
	}
	if len(info.MissingScopes) != 0 {
		t.Errorf("MissingScopes = %v, want empty", info.MissingScopes)
	}
}

func TestPATCheckTokenScopes_MissingAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Missing "api" scope
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scopes": []string{"read_repository", "read_user"},
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, "test-pat-token")
	a := NewAuthAdapter(c)

	info, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes: %v", err)
	}
	if info.Valid {
		t.Error("expected Valid=false for missing scopes")
	}
	if len(info.MissingScopes) == 0 {
		t.Error("expected MissingScopes to be non-empty")
	}
	found := false
	for _, ms := range info.MissingScopes {
		if ms == "api" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("MissingScopes = %v, want to contain 'api'", info.MissingScopes)
	}
}

func TestPATCheckTokenScopes_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, "expired-token")
	a := NewAuthAdapter(c)

	_, err := a.CheckTokenScopes(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want to mention HTTP 401", err)
	}
}

// --- OAuth2 scope checking ---

func TestOAuth2CheckTokenScopes_Valid(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token/info", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing Bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scope": "api read_repository read_user",
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "bob"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClientWithMethod(t, srv, "oauth2-token", authMethodOAuth2, "")
	a := NewAuthAdapter(c)

	info, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes (OAuth2): %v", err)
	}
	if !info.Valid {
		t.Errorf("expected Valid=true, got false; missing=%v", info.MissingScopes)
	}
	if info.Login != "bob" {
		t.Errorf("Login = %q, want %q", info.Login, "bob")
	}
	if info.Resolution != "oauth2" {
		t.Errorf("Resolution = %q, want %q", info.Resolution, "oauth2")
	}
}

func TestOAuth2CheckTokenScopes_MissingScope(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scope": "read_user", // missing api and read_repository
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "bob"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClientWithMethod(t, srv, "oauth2-token", authMethodOAuth2, "")
	a := NewAuthAdapter(c)

	info, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes (OAuth2): %v", err)
	}
	if info.Valid {
		t.Error("expected Valid=false for missing scopes")
	}
	if len(info.MissingScopes) == 0 {
		t.Error("expected MissingScopes non-empty")
	}
}

// --- CI job token ---

func TestCIJobTokenGating_NotInCI(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("CI_JOB_TOKEN", "")

	if ciJobTokenAvailable() {
		t.Error("ciJobTokenAvailable() should return false when CI is not set")
	}
}

func TestCIJobTokenGating_InCI(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("CI_JOB_TOKEN", "glcit-token-xxx")

	if !ciJobTokenAvailable() {
		t.Error("ciJobTokenAvailable() should return true when CI=true and CI_JOB_TOKEN is set")
	}
}

func TestCIJobTokenCheckScopes_Valid(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("CI_JOB_TOKEN", "glcit-token-xxx")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("JOB-TOKEN") == "" {
			http.Error(w, "missing JOB-TOKEN", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "ci-runner"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClientWithMethod(t, srv, "glcit-token-xxx", authMethodCIJobToken, "")
	a := NewAuthAdapter(c)

	info, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes (CI job token): %v", err)
	}
	if !info.Valid {
		t.Errorf("expected Valid=true, got false")
	}
	if info.Resolution != "ci_job_token" {
		t.Errorf("Resolution = %q, want %q", info.Resolution, "ci_job_token")
	}
	if len(info.Scopes) != 1 || info.Scopes[0] != "ci_job_token" {
		t.Errorf("Scopes = %v, want [ci_job_token]", info.Scopes)
	}
}

func TestCIJobTokenCheckScopes_NotInCI_ReturnsError(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("CI_JOB_TOKEN", "")

	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()

	c := newTestClientWithMethod(t, srv, "", authMethodCIJobToken, "")
	a := NewAuthAdapter(c)

	_, err := a.CheckTokenScopes(context.Background())
	if err == nil {
		t.Fatal("expected error when CI_JOB_TOKEN is not available, got nil")
	}
	if !strings.Contains(err.Error(), "CI_JOB_TOKEN") {
		t.Errorf("error = %v, want mention of CI_JOB_TOKEN", err)
	}
}

// --- Deploy token ---

func TestDeployTokenCheckScopes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			http.Error(w, "missing Basic auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "deploy-user"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClientWithMethod(t, srv, "deploy-token-secret", authMethodDeployToken, "deploy-user")
	a := NewAuthAdapter(c)

	info, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes (deploy token): %v", err)
	}
	if !info.Valid {
		t.Errorf("expected Valid=true, got false")
	}
	if info.Resolution != "deploy_token" {
		t.Errorf("Resolution = %q, want %q", info.Resolution, "deploy_token")
	}
	if len(info.Scopes) != 1 || info.Scopes[0] != "deploy_token" {
		t.Errorf("Scopes = %v, want [deploy_token]", info.Scopes)
	}
}

// --- Priority resolution ---

func TestPriorityResolution_ExplicitTokenOverridesEnv(t *testing.T) {
	// Put a different value in the env var — the explicit arg should win.
	t.Setenv("GITLAB_TEST_TOKEN", "env-token")

	mux := http.NewServeMux()
	var receivedToken string
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scopes": []string{"api", "read_repository", "read_user"},
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Explicit token arg takes precedence.
	c := newTestClient(t, srv, "explicit-token")
	a := NewAuthAdapter(c)

	_, err := a.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes: %v", err)
	}
	if receivedToken != "explicit-token" {
		t.Errorf("server received token %q, want explicit-token", receivedToken)
	}
}

// --- Whoami ---

func TestWhoami_PAT(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "carol"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, "pat-token")
	a := NewAuthAdapter(c)

	actor, err := a.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if actor.Login != "carol" {
		t.Errorf("Login = %q, want %q", actor.Login, "carol")
	}
}

func TestWhoami_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, "bad-token")
	a := NewAuthAdapter(c)

	_, err := a.Whoami(context.Background())
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

// --- Credential masking ---

func TestCredentialNeverLogged(t *testing.T) {
	secret := "super-secret-token-xyz"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // force error path
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, secret)
	a := NewAuthAdapter(c)

	_, err := a.CheckTokenScopes(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if strings.Contains(err.Error(), secret) {
		t.Errorf("error message contains raw token — credential leaked: %v", err)
	}
}

func TestMaskToken(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"", ""},
		{"abc", "***"},
		{"abcd", "****"},
		{"abcdefgh", "****efgh"},
		{"glpat-xxxx-yyyy", "***********yyyy"},
	}
	for _, tt := range tests {
		got := maskToken(tt.token)
		if got != tt.want {
			t.Errorf("maskToken(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}

func TestComputeMissingGitLabScopes(t *testing.T) {
	tests := []struct {
		name     string
		actual   []string
		required []string
		want     []string
	}{
		{
			name:     "all present",
			actual:   []string{"api", "read_repository", "read_user"},
			required: []string{"api", "read_repository", "read_user"},
			want:     nil,
		},
		{
			name:     "missing api",
			actual:   []string{"read_repository", "read_user"},
			required: []string{"api", "read_repository", "read_user"},
			want:     []string{"api"},
		},
		{
			name:     "all missing",
			actual:   []string{},
			required: []string{"api", "read_repository"},
			want:     []string{"api", "read_repository"},
		},
		{
			name:     "empty required",
			actual:   []string{"api"},
			required: []string{},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeMissingGitLabScopes(tt.actual, tt.required)
			if len(got) != len(tt.want) {
				t.Errorf("computeMissingGitLabScopes(%v, %v) = %v, want %v", tt.actual, tt.required, got, tt.want)
				return
			}
			for i, g := range got {
				if g != tt.want[i] {
					t.Errorf("computeMissingGitLabScopes(%v, %v)[%d] = %q, want %q", tt.actual, tt.required, i, g, tt.want[i])
				}
			}
		})
	}
}

// TestNewClientFromConfig_CIJobToken verifies that NewClientFromConfig with
// auth_method=ci_job_token reads CI_JOB_TOKEN and sets JOB-TOKEN header.
func TestNewClientFromConfig_CIJobToken(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("CI_JOB_TOKEN", "glcit-test-token")

	var receivedHeader string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("JOB-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "ci-user"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	entry := &struct {
		AuthMethod string
		TokenEnv   string
		BaseURL    string
		CABundle   string
		Proxy      string
	}{
		AuthMethod: "ci_job_token",
		BaseURL:    srv.URL,
	}
	_ = entry
	// Directly test the resolved method path rather than going through
	// ForgeConfigEntry (which would require a full config.ForgeConfigEntry).
	c := NewClient(srv.URL, os.Getenv("CI_JOB_TOKEN"),
		WithAuthHeader("JOB-TOKEN", os.Getenv("CI_JOB_TOKEN")),
		WithResolvedMethod(authMethodCIJobToken, ""),
	)
	a := NewAuthAdapter(c)

	if a.method != authMethodCIJobToken {
		t.Errorf("method = %q, want %q", a.method, authMethodCIJobToken)
	}

	actor, err := a.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if actor.Login != "ci-user" {
		t.Errorf("Login = %q, want ci-user", actor.Login)
	}
	if receivedHeader != "glcit-test-token" {
		t.Errorf("JOB-TOKEN header = %q, want glcit-test-token", receivedHeader)
	}
}

// TestStatus_ReturnsMaskedToken verifies that Status() masks the token.
func TestStatus_ReturnsMaskedToken(t *testing.T) {
	secret := "glpat-abcdefghijklmnop"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"scopes": []string{"api", "read_repository", "read_user"},
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv, secret)
	a := NewAuthAdapter(c)

	status, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(status.MaskedToken, secret) {
		t.Errorf("MaskedToken contains raw secret: %q", status.MaskedToken)
	}
	if !strings.HasSuffix(status.MaskedToken, secret[len(secret)-4:]) {
		t.Errorf("MaskedToken = %q, expected last 4 chars of token to be visible", status.MaskedToken)
	}
	if status.Method != authMethodPAT {
		t.Errorf("Method = %q, want %q", status.Method, authMethodPAT)
	}
}
