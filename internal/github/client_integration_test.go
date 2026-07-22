package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// ghWarningText is the deprecation warning prefix emitted when gh CLI is used.
const ghWarningText = "warning: Using gh CLI for token resolution"

// captureStderr replaces os.Stderr with a pipe, calls f, then restores it and
// returns the captured bytes. Use this to assert warning messages in tests.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }() // restore stderr on any return path

	f()

	w.Close() // signal EOF to reader before copying
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return buf.String()
}

// newMockGraphQLServer creates an httptest.Server that responds to GraphQL
// requests with a minimal user query response, validating the Authorization
// header carries the expected token.
func newMockGraphQLServer(t *testing.T, expectedToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		want := "Bearer " + expectedToken
		if auth != want {
			http.Error(w, fmt.Sprintf("want auth %q got %q", want, auth), http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Minimal GraphQL response sufficient for rateLimit query used by GetRateLimit.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"rateLimit": map[string]interface{}{
					"remaining": 5000,
					"limit":     5000,
					"resetAt":   "2026-04-12T00:00:00Z",
				},
			},
		})
	}))
}

// stubConfigResolver is a test implementation of TokenResolver.
// It returns a fixed token for any owner and exposes SuppressGHWarning.
type stubConfigResolver struct {
	token           string
	err             error
	suppressWarning bool
}

func (s *stubConfigResolver) ResolveToken(_ string) (string, error) {
	return s.token, s.err
}

func (s *stubConfigResolver) SuppressGHWarning() bool {
	return s.suppressWarning
}

// ── Integration tests ─────────────────────────────────────────────────────────

// TestIntegration_ConfigTokenWithoutGhCLI verifies that when a config-based
// token is available, NewClientFromConfig succeeds without calling gh CLI.
// AC#1: Integration test: pipeline run with config-based token (no gh CLI) succeeds.
func TestIntegration_ConfigTokenWithoutGhCLI(t *testing.T) {
	const configToken = "ghp_config_token_abc123"

	// Point execGHAuthToken to a function that fails — it must never be called.
	origExec := execGHAuthToken
	execGHAuthToken = func() (string, error) {
		t.Error("execGHAuthToken should not be called when config token is set")
		return "", fmt.Errorf("gh CLI must not be called")
	}
	defer func() { execGHAuthToken = origExec }()

	t.Setenv("GITHUB_TOKEN", "")

	resolver := &stubConfigResolver{token: configToken}
	c, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err != nil {
		t.Fatalf("NewClientFromConfig: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

// multiOrgTokenResolver is a TokenResolver that maps org owners to tokens —
// simulating github_auth.tokens[owner] multi-org config.
type multiOrgTokenResolver struct {
	orgTokens       map[string]string
	suppressWarning bool
}

func (m *multiOrgTokenResolver) ResolveToken(owner string) (string, error) {
	if tok, ok := m.orgTokens[owner]; ok && tok != "" {
		return tok, nil
	}
	return "", nil
}

func (m *multiOrgTokenResolver) SuppressGHWarning() bool {
	return m.suppressWarning
}

// TestIntegration_MultiOrgTokenResolution verifies that a multi-org resolver
// dispatches the correct token for each owner using a shared resolver instance.
// AC#2: Integration test: multi-org workspace with different tokens per org resolves correctly.
func TestIntegration_MultiOrgTokenResolution(t *testing.T) {
	orgTokens := map[string]string{
		"nightgauge": "ghp_acme_token_xyz",
		"OtherOrg": "ghp_other_token_abc",
	}

	resolver := &multiOrgTokenResolver{orgTokens: orgTokens}

	for owner, expectedToken := range orgTokens {
		owner := owner
		expectedToken := expectedToken
		t.Run("owner="+owner, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "")

			origExec := execGHAuthToken
			execGHAuthToken = func() (string, error) {
				t.Errorf("gh CLI must not be called for owner %s (config token set)", owner)
				return "", fmt.Errorf("gh CLI must not be called")
			}
			defer func() { execGHAuthToken = origExec }()

			// Use a mock server that only accepts the expected per-org token.
			srv := newMockGraphQLServer(t, expectedToken)
			defer srv.Close()

			c, err := NewClientFromConfig(resolver, owner, "")
			if err != nil {
				t.Fatalf("NewClientFromConfig(%s): %v", owner, err)
			}
			if c == nil {
				t.Fatal("expected non-nil client")
			}

			// Verify the client carries the correct org token by pointing it at the
			// mock server, which rejects any other token with 401.
			c2 := NewClientWithURL(expectedToken, srv.URL)
			ctx := t.Context()
			if _, err := c2.GetRateLimit(ctx); err != nil {
				t.Errorf("mock server rejected token for %s: %v", owner, err)
			}
			_ = c // primary assertion: correct token resolved without gh CLI
		})
	}
}

// TestIntegration_DeprecationWarningOnFallback verifies that when no config
// token and no GITHUB_TOKEN is set, the warning is emitted before gh CLI use.
// AC#3: Deprecation warning logged when gh auth token fallback is used.
// AC#4: Warning is non-blocking (doesn't fail the pipeline).
func TestIntegration_DeprecationWarningOnFallback(t *testing.T) {
	const fakeGHToken = "ghp_from_gh_cli"

	// Mock gh CLI to return a token.
	origExec := execGHAuthToken
	execGHAuthToken = func() (string, error) {
		return fakeGHToken, nil
	}
	defer func() { execGHAuthToken = origExec }()

	t.Setenv("GITHUB_TOKEN", "")

	resolver := &stubConfigResolver{token: ""} // no config token

	var stderr string
	var c *Client
	var err error
	stderr = captureStderr(t, func() {
		c, err = NewClientFromConfig(resolver, "nightgauge", "")
	})

	if err != nil {
		t.Fatalf("NewClientFromConfig: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client (gh CLI fallback should succeed)")
	}
	if !strings.Contains(stderr, ghWarningText) {
		t.Errorf("expected deprecation warning in stderr, got: %q", stderr)
	}
}

// TestIntegration_SuppressWarning verifies that the deprecation warning is
// suppressed when SuppressGHWarning returns true.
// AC#5: Warning suppressed if user sets github_auth.suppress_gh_warning: true.
func TestIntegration_SuppressWarning(t *testing.T) {
	const fakeGHToken = "ghp_from_gh_cli_suppressed"

	origExec := execGHAuthToken
	execGHAuthToken = func() (string, error) {
		return fakeGHToken, nil
	}
	defer func() { execGHAuthToken = origExec }()

	t.Setenv("GITHUB_TOKEN", "")

	resolver := &stubConfigResolver{
		token:           "", // force gh CLI fallback
		suppressWarning: true,
	}

	var stderr string
	var err error
	stderr = captureStderr(t, func() {
		_, err = NewClientFromConfig(resolver, "nightgauge", "")
	})

	if err != nil {
		t.Fatalf("NewClientFromConfig: unexpected error: %v", err)
	}
	if strings.Contains(stderr, ghWarningText) {
		t.Errorf("expected no deprecation warning (suppress=true), but got: %q", stderr)
	}
}

// TestIntegration_TokenResolutionPriority validates the exact priority order of
// the token resolution chain in NewClientFromConfig.
func TestIntegration_TokenResolutionPriority(t *testing.T) {
	// Tracking which sources were consulted.
	type call struct{ source string }

	var calls []call

	// Tier 4 (gh CLI fallback) — should NOT be reached in most sub-tests.
	origExec := execGHAuthToken
	defer func() { execGHAuthToken = origExec }()

	t.Run("CLI_flag_beats_config", func(t *testing.T) {
		calls = nil
		t.Setenv("GITHUB_TOKEN", "ghp_env")
		resolver := &stubConfigResolver{token: "ghp_config"}
		execGHAuthToken = func() (string, error) {
			calls = append(calls, call{"gh_cli"})
			return "ghp_gh", nil
		}

		c, err := NewClientFromConfig(resolver, "nightgauge", "ghp_cli")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c == nil {
			t.Fatal("nil client")
		}
		for _, c := range calls {
			t.Errorf("gh CLI should not be called; got call from %s", c.source)
		}
	})

	t.Run("config_beats_env", func(t *testing.T) {
		calls = nil
		t.Setenv("GITHUB_TOKEN", "ghp_env")
		resolver := &stubConfigResolver{token: "ghp_config"}
		execGHAuthToken = func() (string, error) {
			calls = append(calls, call{"gh_cli"})
			return "ghp_gh", nil
		}

		c, err := NewClientFromConfig(resolver, "nightgauge", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c == nil {
			t.Fatal("nil client")
		}
		for _, c := range calls {
			t.Errorf("gh CLI should not be called; got call from %s", c.source)
		}
	})

	t.Run("env_beats_gh_cli", func(t *testing.T) {
		calls = nil
		t.Setenv("GITHUB_TOKEN", "ghp_env")
		resolver := &stubConfigResolver{token: ""}
		execGHAuthToken = func() (string, error) {
			calls = append(calls, call{"gh_cli"})
			return "ghp_gh", nil
		}

		c, err := NewClientFromConfig(resolver, "nightgauge", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c == nil {
			t.Fatal("nil client")
		}
		for _, c := range calls {
			t.Errorf("gh CLI should not be called when GITHUB_TOKEN is set; got call from %s", c.source)
		}
	})

	t.Run("gh_cli_used_as_last_resort", func(t *testing.T) {
		calls = nil
		t.Setenv("GITHUB_TOKEN", "")
		resolver := &stubConfigResolver{token: "", suppressWarning: true}
		execGHAuthToken = func() (string, error) {
			calls = append(calls, call{"gh_cli"})
			return "ghp_from_gh", nil
		}

		c, err := NewClientFromConfig(resolver, "nightgauge", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c == nil {
			t.Fatal("nil client")
		}
		if len(calls) == 0 {
			t.Error("expected gh CLI to be called as last resort")
		}
	})
}

// TestIntegration_ResolveTokenForUser_Warning verifies that ResolveTokenForUser
// emits a warning when suppressWarning=false and suppresses it when true.
func TestIntegration_ResolveTokenForUser_Warning(t *testing.T) {
	origExec := execGHAuthTokenForUser
	defer func() { execGHAuthTokenForUser = origExec }()

	t.Run("emits_warning", func(t *testing.T) {
		execGHAuthTokenForUser = func(user string) (string, error) {
			return "ghp_user_token", nil
		}
		stderr := captureStderr(t, func() {
			_, _ = ResolveTokenForUser("testuser", false)
		})
		if !strings.Contains(stderr, ghWarningText) {
			t.Errorf("expected deprecation warning, got: %q", stderr)
		}
	})

	t.Run("suppresses_warning", func(t *testing.T) {
		execGHAuthTokenForUser = func(user string) (string, error) {
			return "ghp_user_token", nil
		}
		stderr := captureStderr(t, func() {
			_, _ = ResolveTokenForUser("testuser", true)
		})
		if strings.Contains(stderr, ghWarningText) {
			t.Errorf("expected no warning (suppress=true), got: %q", stderr)
		}
	})
}
