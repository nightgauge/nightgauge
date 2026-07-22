package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
)

func TestNewClient_DefaultsBaseURL(t *testing.T) {
	c := NewClient("", "tok")
	if c.baseURL != strings.TrimRight(DefaultBaseURL, "/") {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c := NewClient("https://gitlab.example.com/", "tok")
	if c.baseURL != "https://gitlab.example.com" {
		t.Errorf("baseURL = %q, want trimmed", c.baseURL)
	}
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	c := NewClient("", "tok", WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient: custom client not applied")
	}
}

func TestNewClient_WithUserAgent(t *testing.T) {
	c := NewClient("", "tok", WithUserAgent("custom/1.0"))
	if c.userAgent != "custom/1.0" {
		t.Errorf("userAgent = %q, want custom/1.0", c.userAgent)
	}
}

func TestProjectPath_URLEncodes(t *testing.T) {
	got := projectPath("nightgauge", "nightgauge")
	if got != "nightgauge%2Fnightgauge" {
		t.Errorf("projectPath = %q, want nightgauge%%2Fnightgauge", got)
	}
}

func TestDo_SendsPrivateTokenHeader(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "secret-token")
	var out struct {
		OK bool `json:"ok"`
	}
	_, err := c.do(context.Background(), "GET", srv.URL+"/api/v4/anything", nil, &out, "test")
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotToken != "secret-token" {
		t.Errorf("PRIVATE-TOKEN = %q, want secret-token", gotToken)
	}
	if !out.OK {
		t.Error("expected decoded body")
	}
}

func TestDo_PropagatesSentinelOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"404 Not Found"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok")
	_, err := c.do(context.Background(), "GET", srv.URL+"/api/v4/missing", nil, nil, "get")
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestParseLinkHeader_ExtractsNextAndLast(t *testing.T) {
	h := `<https://gitlab.example.com/api/v4/projects/1/issues?page=2>; rel="next", <https://gitlab.example.com/api/v4/projects/1/issues?page=10>; rel="last", <https://gitlab.example.com/api/v4/projects/1/issues?page=1>; rel="first"`
	links := parseLinkHeader(h)
	if links.Next == nil || !strings.Contains(links.Next.String(), "page=2") {
		t.Errorf("Next missing or wrong: %v", links.Next)
	}
	if links.Last == nil || !strings.Contains(links.Last.String(), "page=10") {
		t.Errorf("Last missing or wrong: %v", links.Last)
	}
	if links.First == nil {
		t.Error("First missing")
	}
}

func TestParseLinkHeader_HandlesEmpty(t *testing.T) {
	links := parseLinkHeader("")
	if links.Next != nil || links.Prev != nil {
		t.Error("expected empty struct for empty header")
	}
}

func TestParseLinkHeader_SkipsMalformed(t *testing.T) {
	// Missing < > brackets should be skipped silently.
	links := parseLinkHeader(`https://example.com; rel="next"`)
	if links.Next != nil {
		t.Error("expected next to be nil for malformed entry")
	}
}

func TestPagination_WalksMultiplePages(t *testing.T) {
	// Mock a 3-page result set; page 1 → page 2 → page 3 (no next).
	var pageCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCalls++
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		switch page {
		case "1":
			selfURL := "http://" + r.Host + r.URL.Path
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=2>; rel="next", <%s?page=3>; rel="last"`, selfURL, selfURL))
			_, _ = w.Write([]byte(`[{"iid":1}]`))
		case "2":
			selfURL := "http://" + r.Host + r.URL.Path
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=3>; rel="next", <%s?page=3>; rel="last"`, selfURL, selfURL))
			_, _ = w.Write([]byte(`[{"iid":2}]`))
		case "3":
			_, _ = w.Write([]byte(`[{"iid":3}]`))
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok")
	svc := NewIssueService(c)

	// Use a project name so the URL encoding includes the path; the test
	// server ignores the project in its handler, so any owner/repo works.
	all, err := svc.ListIssues(context.Background(), "o", "r", nil)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 issues across pages, got %d", len(all))
	}
	if pageCalls != 3 {
		t.Errorf("expected 3 page calls, got %d", pageCalls)
	}
}

func TestDoRaw_ReturnsBodyAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<http://x/api/v4/y?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`hello`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok")
	body, headers, err := c.doRaw(context.Background(), "GET", srv.URL+"/api/v4/x", nil, "test")
	if err != nil {
		t.Fatalf("doRaw: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", string(body))
	}
	if headers.Get("Link") == "" {
		t.Error("expected Link header to be returned")
	}
}

func TestReadSnippet(t *testing.T) {
	r := strings.NewReader("short body")
	got, err := readSnippet(r)
	if err != nil && err != io.EOF {
		t.Fatalf("readSnippet: %v", err)
	}
	if got != "short body" {
		t.Errorf("snippet = %q", got)
	}
}

// TestDoRaw_PropagatesSentinelOn404 verifies doRaw maps non-2xx into the
// canonical forge sentinel chain — symmetric with TestDo_PropagatesSentinelOn404
// for the do() variant.
func TestDoRaw_PropagatesSentinelOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok")
	_, _, err := c.doRaw(context.Background(), "GET", srv.URL+"/api/v4/x", nil, "test")
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("doRaw 404: err = %v, want ErrNotFound chain", err)
	}
}

// TestDoRaw_SendsAuthAndUserAgentHeaders pins the wire-level contract for
// doRaw — every request must carry the PRIVATE-TOKEN and User-Agent headers
// or rate-limit attribution and audit logs break on the GitLab side.
func TestDoRaw_SendsAuthAndUserAgentHeaders(t *testing.T) {
	var gotToken, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok-value", WithUserAgent("test-ua/1.0"))
	if _, _, err := c.doRaw(context.Background(), "GET", srv.URL+"/api/v4/x", nil, "test"); err != nil {
		t.Fatalf("doRaw: %v", err)
	}
	if gotToken != "tok-value" {
		t.Errorf("PRIVATE-TOKEN = %q, want tok-value", gotToken)
	}
	if gotUA != "test-ua/1.0" {
		t.Errorf("User-Agent = %q, want test-ua/1.0", gotUA)
	}
}

// TestRateLimitFloor_DefaultsAndOverride exercises the env-var override path
// through rateLimitFloor(). The env var is the only branch without explicit
// coverage; the default path is exercised implicitly by every gated request.
func TestRateLimitFloor_DefaultsAndOverride(t *testing.T) {
	// Default — env var unset.
	t.Setenv(rateLimitFloorEnv, "")
	if got := rateLimitFloor(); got != defaultRateLimitFloor {
		t.Errorf("default rateLimitFloor = %d, want %d", got, defaultRateLimitFloor)
	}

	// Override — well-formed integer.
	t.Setenv(rateLimitFloorEnv, "200")
	if got := rateLimitFloor(); got != 200 {
		t.Errorf("override rateLimitFloor = %d, want 200", got)
	}

	// Malformed — falls back to default rather than panicking.
	t.Setenv(rateLimitFloorEnv, "not-a-number")
	if got := rateLimitFloor(); got != defaultRateLimitFloor {
		t.Errorf("malformed rateLimitFloor = %d, want default %d", got, defaultRateLimitFloor)
	}

	// Negative — also falls back to default.
	t.Setenv(rateLimitFloorEnv, "-5")
	if got := rateLimitFloor(); got != defaultRateLimitFloor {
		t.Errorf("negative rateLimitFloor = %d, want default %d", got, defaultRateLimitFloor)
	}
}

// TestProjectPath_HandlesDotsAndSpecials verifies projectPath URL-encodes the
// owner/repo separator while leaving safe characters intact.
func TestProjectPath_HandlesDotsAndSpecials(t *testing.T) {
	cases := []struct {
		owner, repo, want string
	}{
		{"nightgauge", "nightgauge", "nightgauge%2Fnightgauge"},
		{"o", "r-with-dashes", "o%2Fr-with-dashes"},
		{"sub.group", "repo.name", "sub.group%2Frepo.name"},
	}
	for _, tc := range cases {
		if got := projectPath(tc.owner, tc.repo); got != tc.want {
			t.Errorf("projectPath(%q,%q) = %q, want %q", tc.owner, tc.repo, got, tc.want)
		}
	}
}

// --- NewClientFromConfig and NewClientWithHTTP tests ---

func TestNewClientWithHTTP_UsesProvidedClient(t *testing.T) {
	custom := &http.Client{}
	c := NewClientWithHTTP("https://gitlab.example.com", "tok", custom)
	if c.httpClient != custom {
		t.Error("NewClientWithHTTP: custom http.Client not applied")
	}
	if c.baseURL != "https://gitlab.example.com" {
		t.Errorf("baseURL = %q, want https://gitlab.example.com", c.baseURL)
	}
}

func TestNewClientFromConfig_DefaultBaseURL(t *testing.T) {
	entry := &config.ForgeConfigEntry{
		Kind: "gitlab",
	}
	c, err := NewClientFromConfig(entry, "", "test-token")
	if err != nil {
		t.Fatalf("NewClientFromConfig empty BaseURL: %v", err)
	}
	if c.baseURL != strings.TrimRight(DefaultBaseURL, "/") {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
}

func TestNewClientFromConfig_CustomBaseURL(t *testing.T) {
	entry := &config.ForgeConfigEntry{
		Kind:    "gitlab",
		BaseURL: "https://gitlab.corp.example.com",
	}
	c, err := NewClientFromConfig(entry, "", "test-token")
	if err != nil {
		t.Fatalf("NewClientFromConfig custom BaseURL: %v", err)
	}
	if c.baseURL != "https://gitlab.corp.example.com" {
		t.Errorf("baseURL = %q, want https://gitlab.corp.example.com", c.baseURL)
	}
}

func TestNewClientFromConfig_UserAgent(t *testing.T) {
	entry := &config.ForgeConfigEntry{
		Kind:    "gitlab",
		BaseURL: "https://gitlab.corp.example.com",
	}
	c, err := NewClientFromConfig(entry, "", "test-token")
	if err != nil {
		t.Fatalf("NewClientFromConfig UserAgent: %v", err)
	}
	// Format: Nightgauge/<version> (gitlab; <host>)
	if !strings.HasPrefix(c.userAgent, "Nightgauge/") {
		t.Errorf("userAgent = %q, want prefix Nightgauge/", c.userAgent)
	}
	if !strings.Contains(c.userAgent, "(gitlab;") {
		t.Errorf("userAgent = %q, want (gitlab; ...) suffix", c.userAgent)
	}
	if !strings.Contains(c.userAgent, "gitlab.corp.example.com") {
		t.Errorf("userAgent = %q, want instance host gitlab.corp.example.com", c.userAgent)
	}
}

func TestNewClientFromConfig_MalformedBaseURL(t *testing.T) {
	entry := &config.ForgeConfigEntry{
		Kind:    "gitlab",
		BaseURL: "://bad",
	}
	_, err := NewClientFromConfig(entry, "", "test-token")
	if err == nil {
		t.Fatal("expected error for malformed base URL, got nil")
	}
}

func TestNewClientFromConfig_TokenFromEnv(t *testing.T) {
	t.Setenv("TEST_GITLAB_TOKEN_3353", "env-token-value")
	entry := &config.ForgeConfigEntry{
		Kind:     "gitlab",
		BaseURL:  "https://gitlab.example.com",
		TokenEnv: "TEST_GITLAB_TOKEN_3353",
	}
	c, err := NewClientFromConfig(entry, "", "")
	if err != nil {
		t.Fatalf("NewClientFromConfig token from env: %v", err)
	}
	if c.token != "env-token-value" {
		t.Errorf("token = %q, want env-token-value", c.token)
	}
}

func TestNewClientFromConfig_NoToken_ReturnsError(t *testing.T) {
	entry := &config.ForgeConfigEntry{
		Kind:    "gitlab",
		BaseURL: "https://gitlab.example.com",
	}
	_, err := NewClientFromConfig(entry, "", "")
	if err == nil {
		t.Fatal("expected error when no token available, got nil")
	}
}

func TestBuildUserAgent_Format(t *testing.T) {
	// Save and restore Version.
	orig := Version
	defer func() { Version = orig }()

	Version = "1.2.3"
	got := buildUserAgent("https://my-gitlab.example.com")
	want := "Nightgauge/1.2.3 (gitlab; my-gitlab.example.com)"
	if got != want {
		t.Errorf("buildUserAgent = %q, want %q", got, want)
	}
}

func TestBuildUserAgent_EmptyVersionFallback(t *testing.T) {
	orig := Version
	defer func() { Version = orig }()

	Version = ""
	got := buildUserAgent("https://gitlab.com")
	if !strings.Contains(got, "/dev ") {
		t.Errorf("buildUserAgent with empty Version = %q, want /dev fallback", got)
	}
}

func TestBuildUserAgent_DefaultHostOnBadURL(t *testing.T) {
	got := buildUserAgent("://garbage")
	if !strings.Contains(got, "gitlab.com") {
		t.Errorf("buildUserAgent bad URL = %q, want gitlab.com fallback host", got)
	}
}

// --- RepositoryID tests ---

// stubProjectServer returns a test server that responds to /api/v4/projects/{slug}
// with the given numeric ID and optional global_id.
func stubProjectServer(t *testing.T, numID int, globalID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"id": numID}
		if globalID != "" {
			resp["global_id"] = globalID
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestRepositoryID_ResolvesAndCaches(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":        99,
			"global_id": "gid://gitlab/Project/99",
		})
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok")
	ctx := context.Background()

	numID, gid, err := c.RepositoryID(ctx, "group/repo")
	if err != nil {
		t.Fatalf("RepositoryID first call: %v", err)
	}
	if numID != 99 {
		t.Errorf("numericID = %d, want 99", numID)
	}
	if gid != "gid://gitlab/Project/99" {
		t.Errorf("globalID = %q, want gid://gitlab/Project/99", gid)
	}
	if requestCount != 1 {
		t.Errorf("expected 1 request, got %d", requestCount)
	}

	// Second call should hit the cache — no additional request.
	numID2, gid2, err2 := c.RepositoryID(ctx, "group/repo")
	if err2 != nil {
		t.Fatalf("RepositoryID second call: %v", err2)
	}
	if numID2 != numID || gid2 != gid {
		t.Errorf("cache returned different values: %d/%q vs %d/%q", numID2, gid2, numID, gid)
	}
	if requestCount != 1 {
		t.Errorf("expected cache hit (still 1 request), got %d requests", requestCount)
	}
}

func TestRepositoryID_CacheExpiry(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":        77,
			"global_id": "gid://gitlab/Project/77",
		})
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "tok")
	// Pre-seed cache with an expired entry.
	c.repoIDMu.Lock()
	c.repoIDCache = map[string]*repoIDEntry{
		"g/r": {
			numericID: 1,
			globalID:  "gid://gitlab/Project/1",
			checkedAt: time.Now().Add(-20 * time.Second), // older than 15s TTL
		},
	}
	c.repoIDMu.Unlock()

	numID, _, err := c.RepositoryID(context.Background(), "g/r")
	if err != nil {
		t.Fatalf("RepositoryID after cache expiry: %v", err)
	}
	if numID != 77 {
		t.Errorf("numericID = %d, want 77 (fresh from server)", numID)
	}
	if requestCount != 1 {
		t.Errorf("expected 1 request on cache miss, got %d", requestCount)
	}
}

func TestRepositoryID_CredentialRedaction(t *testing.T) {
	// Create a client whose baseURL contains credentials.
	// The server is unreachable so the connect error path is exercised.
	// Use a real TCP addr that will refuse the connection.
	c := NewClient("https://user:secret@127.0.0.1:19999", "tok")

	_, _, err := c.RepositoryID(context.Background(), "group/repo")
	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error message leaks credentials: %v", err)
	}
}

func TestRedactCredentials_NoCredentials(t *testing.T) {
	in := "https://gitlab.example.com/api/v4/projects/foo"
	if got := redactCredentials(in); got != in {
		t.Errorf("redactCredentials(no-creds) = %q, want unchanged %q", got, in)
	}
}

func TestRedactCredentials_WithCredentials(t *testing.T) {
	in := "https://user:pass@gitlab.example.com/api/v4/projects/foo"
	got := redactCredentials(in)
	if strings.Contains(got, "pass") {
		t.Errorf("redactCredentials: password not redacted in %q", got)
	}
	if !strings.Contains(got, "gitlab.example.com") {
		t.Errorf("redactCredentials: host lost from %q", got)
	}
}
