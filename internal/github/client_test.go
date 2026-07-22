package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsRateLimited(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"regular error", errors.New("something broke"), false},
		{"rate limit error", errors.New("API rate limit exceeded"), true},
		{"abuse detection", errors.New("abuse detection mechanism"), true},
		{"secondary rate limit", errors.New("secondary rate limit"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimited(tt.err); got != tt.want {
				t.Errorf("isRateLimited(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		attempt int
		want    time.Duration
	}{
		{"nil error attempt 0", nil, 0, 1 * time.Second},
		{"nil error attempt 1", nil, 1, 2 * time.Second},
		{"nil error attempt 2", nil, 2, 4 * time.Second},
		{"with retry after", errors.New("retry after 60 seconds"), 0, 60 * time.Second},
		{"with retry after 5", errors.New("please retry after 5 seconds ok"), 0, 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryAfter(tt.err, tt.attempt); got != tt.want {
				t.Errorf("retryAfter(%v, %d) = %v, want %v", tt.err, tt.attempt, got, tt.want)
			}
		})
	}
}

func TestCapRateLimitWait(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero", 0, 500 * time.Millisecond},
		{"negative", -1 * time.Second, 500 * time.Millisecond},
		{"under cap", 10 * time.Second, 10 * time.Second},
		{"exact cap", maxRateLimitWait, maxRateLimitWait},
		{"over cap", 10 * time.Minute, maxRateLimitWait},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capRateLimitWait(tt.in); got != tt.want {
				t.Errorf("capRateLimitWait(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// rateLimitProbeServer returns an httptest server whose GraphQL response
// reports the given rateLimit values to any query. Fields match exactly what
// computeRateLimitBackoff's probe struct requests — shurcooL/graphql errors
// if the response carries fields the struct does not declare, so this mirrors
// the real GitHub behavior of only returning what the query selected.
func rateLimitProbeServer(t *testing.T, remaining int, resetAt time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"rateLimit": map[string]interface{}{
					"remaining": remaining,
					"resetAt":   resetAt.UTC().Format(time.RFC3339),
				},
			},
		})
	}))
}

func TestComputeRateLimitBackoff_ExplicitRetryAfterTakesPriority(t *testing.T) {
	// Even though the probe server would say remaining=0 with a 5m reset,
	// an explicit "retry after N seconds" hint in the error must win.
	srv := rateLimitProbeServer(t, 0, time.Now().Add(5*time.Minute))
	defer srv.Close()
	c := NewClientWithURL("test-token", srv.URL)

	got := c.computeRateLimitBackoff(context.Background(), errors.New("retry after 7 seconds"), 2)
	if got != 7*time.Second {
		t.Errorf("explicit hint should win: got %v, want 7s", got)
	}
}

func TestComputeRateLimitBackoff_UsesActualResetAt(t *testing.T) {
	// Probe says remaining=0 and reset is ~10s in the future → we should
	// wait approximately 10s (plus a small buffer), not the exponential
	// fallback of 2^attempt seconds.
	reset := time.Now().Add(10 * time.Second)
	srv := rateLimitProbeServer(t, 0, reset)
	defer srv.Close()
	c := NewClientWithURL("test-token", srv.URL)

	got := c.computeRateLimitBackoff(context.Background(), errors.New("API rate limit exceeded"), 0)
	// Expect the computed wait to be within a few seconds of 10s — not 1s (exponential).
	if got < 8*time.Second || got > 12*time.Second {
		t.Errorf("expected ~10s wait from actual resetAt, got %v", got)
	}
}

func TestComputeRateLimitBackoff_CapsFarFutureReset(t *testing.T) {
	// When remaining=0 and reset is 10 minutes away, the full wait is used
	// (not capped at maxRateLimitWait) so the pipeline can recover automatically.
	reset := time.Now().Add(10 * time.Minute)
	srv := rateLimitProbeServer(t, 0, reset)
	defer srv.Close()
	c := NewClientWithURL("test-token", srv.URL)

	got := c.computeRateLimitBackoff(context.Background(), errors.New("rate limit"), 0)
	// Should be ~10 minutes, not 30 seconds.
	if got <= maxRateLimitWait {
		t.Errorf("expected full reset wait (>%v) for fully-exhausted limit, got %v", maxRateLimitWait, got)
	}
	if got > maxFullExhaustionWait {
		t.Errorf("expected wait capped at maxFullExhaustionWait (%v), got %v", maxFullExhaustionWait, got)
	}
}

func TestComputeRateLimitBackoff_CapsBeyondMaxFullExhaustion(t *testing.T) {
	// A reset far in the future (>75 min) must be capped at maxFullExhaustionWait.
	srv := rateLimitProbeServer(t, 0, time.Now().Add(2*time.Hour))
	defer srv.Close()
	c := NewClientWithURL("test-token", srv.URL)

	got := c.computeRateLimitBackoff(context.Background(), errors.New("rate limit"), 0)
	if got != maxFullExhaustionWait {
		t.Errorf("expected cap at maxFullExhaustionWait (%v), got %v", maxFullExhaustionWait, got)
	}
}

func TestComputeRateLimitBackoff_ShortWaitWhenLimitAlreadyCleared(t *testing.T) {
	// Probe says remaining > 0 → the limit cleared between the failing
	// call and our probe. Return a small pause, not the exponential wait.
	srv := rateLimitProbeServer(t, 42, time.Now().Add(1*time.Minute))
	defer srv.Close()
	c := NewClientWithURL("test-token", srv.URL)

	got := c.computeRateLimitBackoff(context.Background(), errors.New("rate limit"), 3)
	if got != 500*time.Millisecond {
		t.Errorf("expected 500ms short-wait when remaining>0, got %v", got)
	}
}

func TestComputeRateLimitBackoff_FallsBackWhenProbeFails(t *testing.T) {
	// Point the client at an immediately-closed server so the probe errors
	// out; we should fall through to exponential backoff (2^attempt).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // close immediately so dials fail
	c := NewClientWithURL("test-token", srv.URL)

	got := c.computeRateLimitBackoff(context.Background(), errors.New("rate limit"), 2)
	if got != 4*time.Second {
		t.Errorf("expected 4s exponential fallback (attempt=2), got %v", got)
	}
}

func TestNewClientWithToken(t *testing.T) {
	client := NewClientWithToken("test-token")
	if client == nil {
		t.Fatal("NewClientWithToken returned nil")
	}
	if client.gql == nil {
		t.Error("GraphQL client is nil")
	}
	if client.limiter == nil {
		t.Error("Rate limiter is nil")
	}
}

func TestNewClientRequiresToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	_, err := NewClient()
	if err == nil {
		t.Error("NewClient should fail without GITHUB_TOKEN")
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		s, substr string
		want      bool
	}{
		{"hello world", "world", true},
		{"hello world", "xyz", false},
		{"rate limit", "rate", true},
		{"", "x", false},
		{"x", "", true},
	}
	for _, tt := range tests {
		if got := contains(tt.s, tt.substr); got != tt.want {
			t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}

// ── NewClientFromConfig tests (#2663) ─────────────────────────────────────────

// stubTokenResolver satisfies the TokenResolver interface for tests.
type stubTokenResolver struct {
	token           string
	err             error
	suppressWarning bool
}

func (s *stubTokenResolver) ResolveToken(_ string) (string, error) {
	return s.token, s.err
}

func (s *stubTokenResolver) SuppressGHWarning() bool {
	return s.suppressWarning
}

// stubUserResolver adds a configured github_user to stubTokenResolver so the
// gh CLI fallback resolves a user-scoped token rather than the active account.
type stubUserResolver struct {
	stubTokenResolver
	githubUser string
}

func (s *stubUserResolver) ResolveGitHubUser() string { return s.githubUser }

// stubOwnerUserResolver implements the owner-parameterized resolver so the
// token chain can scope the gh CLI user to a specific (target) owner — the
// cross-org case (Acme-Community → acmebot) from #4068.
type stubOwnerUserResolver struct {
	stubTokenResolver
	usersByOwner map[string]string
}

func (s *stubOwnerUserResolver) ResolveGitHubUserForOwner(owner string) string {
	return s.usersByOwner[owner]
}

// TestResolveTokenChain_GitHubUserWinsOverAmbientEnv verifies the #4068 core
// fix: when a github_user is configured, the github_user-scoped token is
// authoritative over an ambient GITHUB_TOKEN env var (which would otherwise be
// the wrong user). Without the fix, the ambient env token would win.
func TestResolveTokenChain_GitHubUserWinsOverAmbientEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_ambient_wrong_user")
	resolver := &stubUserResolver{githubUser: "acmebot"}

	origBare := execGHAuthToken
	origUser := execGHAuthTokenForUser
	execGHAuthToken = func() (string, error) {
		t.Fatal("bare execGHAuthToken must not be called when github_user is configured")
		return "", nil
	}
	var gotUser string
	execGHAuthTokenForUser = func(user string) (string, error) {
		gotUser = user
		return "ghp_acmebot_scoped", nil
	}
	defer func() { execGHAuthToken = origBare; execGHAuthTokenForUser = origUser }()

	tok, err := ResolveTokenChain(resolver, "Acme-Community")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_acmebot_scoped" {
		t.Errorf("token = %q, want ghp_acmebot_scoped (github_user-scoped, NOT ambient env)", tok)
	}
	if gotUser != "acmebot" {
		t.Errorf("resolved user = %q, want acmebot", gotUser)
	}
}

// TestNewClientFromConfig_GitHubUserWinsOverAmbientEnv verifies the same
// env-precedence fix through the client constructor.
func TestNewClientFromConfig_GitHubUserWinsOverAmbientEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_ambient_wrong_user")
	resolver := &stubUserResolver{githubUser: "acmebot"}

	origBare := execGHAuthToken
	origUser := execGHAuthTokenForUser
	execGHAuthToken = func() (string, error) {
		t.Fatal("bare execGHAuthToken must not be called when github_user is configured")
		return "", nil
	}
	var gotUser string
	execGHAuthTokenForUser = func(user string) (string, error) {
		gotUser = user
		return "ghp_acmebot_scoped", nil
	}
	defer func() { execGHAuthToken = origBare; execGHAuthTokenForUser = origUser }()

	c, err := NewClientFromConfig(resolver, "Acme-Community", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if gotUser != "acmebot" {
		t.Errorf("resolved user = %q, want acmebot", gotUser)
	}
}

// TestResolveTokenChain_OwnerScopedUser verifies the owner-parameterized
// resolver scopes the gh CLI user to the PASSED owner, so a cross-org target
// resolves the correct identity (#4068).
func TestResolveTokenChain_OwnerScopedUser(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_ambient")
	resolver := &stubOwnerUserResolver{
		usersByOwner: map[string]string{
			"nightgauge":            "octocat",
			"Acme-Community": "acmebot",
		},
	}

	origBare := execGHAuthToken
	origUser := execGHAuthTokenForUser
	execGHAuthToken = func() (string, error) {
		t.Fatal("bare execGHAuthToken must not be called when a per-owner github_user is configured")
		return "", nil
	}
	var gotUser string
	execGHAuthTokenForUser = func(user string) (string, error) {
		gotUser = user
		return "ghp_scoped_" + user, nil
	}
	defer func() { execGHAuthToken = origBare; execGHAuthTokenForUser = origUser }()

	tok, err := ResolveTokenChain(resolver, "Acme-Community")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUser != "acmebot" {
		t.Errorf("resolved user for Acme-Community = %q, want acmebot", gotUser)
	}
	if tok != "ghp_scoped_acmebot" {
		t.Errorf("token = %q, want ghp_scoped_acmebot", tok)
	}
}

// TestEnvWithout_StripsTokenVars verifies the helper used to strip ambient
// GH_TOKEN/GITHUB_TOKEN from the gh child env removes exactly the named keys
// (so `gh auth token --user` reads the keyring, not the shadowing env token)
// and preserves everything else, including values containing '='.
func TestEnvWithout_StripsTokenVars(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GH_TOKEN=ghp_ambient",
		"GITHUB_TOKEN=ghp_ambient",
		"HOME=/home/u",
		"WEIRD=a=b=c",        // value with '=' must survive intact
		"GH_TOKENISH=keepme", // prefix match must NOT be stripped
	}
	got := envWithout(in, "GH_TOKEN", "GITHUB_TOKEN")

	has := func(want string) bool {
		for _, kv := range got {
			if kv == want {
				return true
			}
		}
		return false
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			t.Errorf("envWithout left a stripped key in env: %q", kv)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/u", "WEIRD=a=b=c", "GH_TOKENISH=keepme"} {
		if !has(want) {
			t.Errorf("envWithout dropped a non-target entry %q; got %v", want, got)
		}
	}
}

// TestExecGHAuthTokenForUser_StripsAmbientEnv verifies the REAL
// execGHAuthTokenForUser implementation invokes gh with ambient
// GH_TOKEN/GITHUB_TOKEN stripped from the child environment. It substitutes a
// fake "gh" on PATH that echoes whether the token vars are present, so the test
// asserts the env-strip without needing a real gh keyring (#4068).
func TestExecGHAuthTokenForUser_StripsAmbientEnv(t *testing.T) {
	dir := t.TempDir()
	fakeGH := filepath.Join(dir, "gh")
	// The fake gh prints "STRIPPED" when neither token var is set in its env,
	// else "LEAKED:<value>". execGHAuthTokenForUser trims and returns stdout.
	script := "#!/bin/sh\n" +
		"if [ -n \"$GH_TOKEN\" ] || [ -n \"$GITHUB_TOKEN\" ]; then\n" +
		"  printf 'LEAKED'\n" +
		"else\n" +
		"  printf 'STRIPPED'\n" +
		"fi\n"
	if err := os.WriteFile(fakeGH, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("GH_TOKEN", "ghp_ambient_wrong")
	t.Setenv("GITHUB_TOKEN", "ghp_ambient_wrong")

	tok, err := execGHAuthTokenForUser("acmebot")
	if err != nil {
		t.Fatalf("execGHAuthTokenForUser: %v", err)
	}
	if tok != "STRIPPED" {
		t.Errorf("gh child saw ambient token vars (env-strip failed): got %q, want STRIPPED", tok)
	}
}

// TestResolveTokenChain_GitHubUserScopesGHFallback verifies that when the
// resolver exposes a configured github_user and no config/env token is
// available, the gh CLI fallback resolves the token for THAT user
// (gh auth token --user) and never the ambient active account (#3700).
func TestResolveTokenChain_GitHubUserScopesGHFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubUserResolver{githubUser: "octocat"}

	origBare := execGHAuthToken
	origUser := execGHAuthTokenForUser
	execGHAuthToken = func() (string, error) {
		t.Fatal("bare execGHAuthToken (active account) must not be called when github_user is configured")
		return "", nil
	}
	var gotUser string
	execGHAuthTokenForUser = func(user string) (string, error) {
		gotUser = user
		return "ghp_user_scoped", nil
	}
	defer func() { execGHAuthToken = origBare; execGHAuthTokenForUser = origUser }()

	tok, err := ResolveTokenChain(resolver, "nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_user_scoped" {
		t.Errorf("token = %q, want %q", tok, "ghp_user_scoped")
	}
	if gotUser != "octocat" {
		t.Errorf("resolved user = %q, want %q", gotUser, "octocat")
	}
}

// TestNewClientFromConfig_GitHubUserScopesGHFallback verifies the same
// user-scoped behavior through the client constructor (#3700).
func TestNewClientFromConfig_GitHubUserScopesGHFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubUserResolver{githubUser: "octocat"}

	origBare := execGHAuthToken
	origUser := execGHAuthTokenForUser
	execGHAuthToken = func() (string, error) {
		t.Fatal("bare execGHAuthToken (active account) must not be called when github_user is configured")
		return "", nil
	}
	var gotUser string
	execGHAuthTokenForUser = func(user string) (string, error) {
		gotUser = user
		return "ghp_user_scoped", nil
	}
	defer func() { execGHAuthToken = origBare; execGHAuthTokenForUser = origUser }()

	c, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if gotUser != "octocat" {
		t.Errorf("resolved user = %q, want %q", gotUser, "octocat")
	}
}

// TestResolveTokenChain_NoGitHubUserUsesBareGH verifies the bare gh fallback
// is still used when no github_user is configured (zero-config single-account
// path remains intact).
func TestResolveTokenChain_NoGitHubUserUsesBareGH(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubTokenResolver{token: ""}

	origBare := execGHAuthToken
	origUser := execGHAuthTokenForUser
	execGHAuthToken = func() (string, error) { return "ghp_bare", nil }
	execGHAuthTokenForUser = func(string) (string, error) {
		t.Fatal("execGHAuthTokenForUser must not be called when no github_user is configured")
		return "", nil
	}
	defer func() { execGHAuthToken = origBare; execGHAuthTokenForUser = origUser }()

	tok, err := ResolveTokenChain(resolver, "nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_bare" {
		t.Errorf("token = %q, want %q", tok, "ghp_bare")
	}
}

func TestNewClientFromConfig_CLITokenTakesPriority(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_envtoken")
	resolver := &stubTokenResolver{token: "ghp_configtoken"}
	c, err := NewClientFromConfig(resolver, "nightgauge", "ghp_clitoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClientFromConfig_ConfigTokenUsedWhenNoCLI(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubTokenResolver{token: "ghp_configtoken"}
	c, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClientFromConfig_EnvVarFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_envfallback")
	resolver := &stubTokenResolver{token: ""}
	c, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClientFromConfig_NilResolverFallsBackToEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_nilresolver")
	c, err := NewClientFromConfig(nil, "nightgauge", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClientFromConfig_NoTokenReturnsError(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubTokenResolver{token: ""}

	// Mock gh CLI to fail so the test is deterministic regardless of local gh config.
	origExec := execGHAuthToken
	execGHAuthToken = func() (string, error) {
		return "", fmt.Errorf("gh: command not found")
	}
	defer func() { execGHAuthToken = origExec }()

	_, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err == nil {
		t.Error("expected error when no token is available, got nil")
	}
}

// TestNewClientFromConfig_SuppressWarning verifies that when suppressWarning is
// true on the resolver, the deprecation warning is not emitted on gh CLI fallback.
// Note: stderr capture happens in client_integration_test.go; this test just
// verifies that the client is still created successfully.
func TestNewClientFromConfig_SuppressWarning(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubTokenResolver{token: "", suppressWarning: true}

	// Mock gh CLI to return a token so the function succeeds.
	origExec := execGHAuthToken
	execGHAuthToken = func() (string, error) {
		return "ghp_from_gh_suppressed", nil
	}
	defer func() { execGHAuthToken = origExec }()

	c, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err != nil {
		t.Fatalf("unexpected error with suppress=true: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

// ── ResolveTokenChain tests (#2733) ────────────────────────────────────────────

func TestResolveTokenChain_ConfigTokenWins(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_envtoken")
	resolver := &stubTokenResolver{token: "ghp_configtoken"}
	tok, err := ResolveTokenChain(resolver, "nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_configtoken" {
		t.Errorf("token = %q, want %q", tok, "ghp_configtoken")
	}
}

func TestResolveTokenChain_EnvFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_envfallback")
	resolver := &stubTokenResolver{token: ""}
	tok, err := ResolveTokenChain(resolver, "nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_envfallback" {
		t.Errorf("token = %q, want %q", tok, "ghp_envfallback")
	}
}

func TestResolveTokenChain_NilResolver(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_nilresolver")
	tok, err := ResolveTokenChain(nil, "nightgauge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_nilresolver" {
		t.Errorf("token = %q, want %q", tok, "ghp_nilresolver")
	}
}

func TestResolveTokenChain_NoTokenReturnsError(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	resolver := &stubTokenResolver{token: ""}
	origExec := execGHAuthToken
	execGHAuthToken = func() (string, error) {
		return "", fmt.Errorf("gh: command not found")
	}
	defer func() { execGHAuthToken = origExec }()

	_, err := ResolveTokenChain(resolver, "nightgauge")
	if err == nil {
		t.Error("expected error when no token is available, got nil")
	}
}

func TestNewClientFromConfig_ConfigErrorFallsBackToEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_aftererror")
	resolver := &stubTokenResolver{err: errors.New("env var not set")}
	c, err := NewClientFromConfig(resolver, "nightgauge", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client after config error fallback")
	}
}

// stubOwnerAndUserResolver implements BOTH the owner-aware and the zero-arg
// user resolvers, mirroring *config.Config: ResolveGitHubUserForOwner(owner)
// consults a per-owner map, while ResolveGitHubUser() returns the workspace
// owner's user. Used to verify the #4068 fix that an unmapped cross-org owner
// does NOT fall back to the workspace user.
type stubOwnerAndUserResolver struct {
	stubTokenResolver
	workspaceUser string
	usersByOwner  map[string]string
}

func (s *stubOwnerAndUserResolver) ResolveGitHubUser() string { return s.workspaceUser }
func (s *stubOwnerAndUserResolver) ResolveGitHubUserForOwner(owner string) string {
	return s.usersByOwner[owner]
}

// TestResolveTokenChain_UnmappedOwnerDoesNotLeakWorkspaceUser verifies the
// #4068 review fix: for a multi-identity config where the workspace owner has a
// github_user but a TARGET owner has no mapping, token resolution must NOT fall
// back to the workspace user's scoped token — it must use the ambient
// GITHUB_TOKEN env (the env-first path for an owner that declared no identity),
// matching maybeExportGitHubToken's owner-aware gate. Before the fix, the
// zero-arg fallback leaked the workspace user onto the unmapped owner.
func TestResolveTokenChain_UnmappedOwnerDoesNotLeakWorkspaceUser(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_env_token")
	resolver := &stubOwnerAndUserResolver{
		workspaceUser: "octocat",
		usersByOwner:  map[string]string{"Acme-Community": "acmebot"},
	}

	origUser := execGHAuthTokenForUser
	execGHAuthTokenForUser = func(user string) (string, error) {
		t.Fatalf("execGHAuthTokenForUser(%q) must NOT be called for an unmapped owner — env token should win", user)
		return "", nil
	}
	defer func() { execGHAuthTokenForUser = origUser }()

	tok, err := ResolveTokenChain(resolver, "UnmappedOrg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghp_env_token" {
		t.Errorf("token = %q, want ghp_env_token (ambient env for an owner with no configured identity)", tok)
	}
}
