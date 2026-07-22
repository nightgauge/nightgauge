package integrationprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCategorize_StatusCodes(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want string
	}{
		{"working", 200, `{"x":1}`, CategoryWorking},
		{"working_201", 201, `{"id":"abc"}`, CategoryWorking},
		{"stub_empty", 200, ``, CategoryStub},
		{"stub_object", 200, `{}`, CategoryStub},
		{"stub_array", 200, `[]`, CategoryStub},
		{"stub_null", 200, `null`, CategoryStub},
		{"stub_short", 200, ` {} `, CategoryStub},
		{"auth_required", 401, `{"error":"unauthorized"}`, CategoryAuthRequired},
		{"auth_mismatch", 403, `{"error":"forbidden"}`, CategoryAuthMismatch},
		{"not_found", 404, `not found`, CategoryNotFound},
		{"broken_500", 500, `oops`, CategoryBroken},
		{"broken_503", 503, ``, CategoryBroken},
		{"broken_3xx", 301, ``, CategoryBroken},
		{"broken_400", 400, ``, CategoryBroken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Categorize(tc.code, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("Categorize(%d, %q) = %s, want %s", tc.code, tc.body, got, tc.want)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/v1/issues/:id", "/v1/issues/probe"},
		{"/v1/team/:teamId/members/:memberId", "/v1/team/probe/members/probe"},
		{"/v1/health", "/v1/health"},
		{"/v1/queue/:id/items", "/v1/queue/probe/items"},
	}
	for _, tc := range cases {
		got := ResolvePath(tc.in)
		if got != tc.want {
			t.Fatalf("ResolvePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuthHeader(t *testing.T) {
	cases := []struct {
		mode, token, wantHeader, wantValue string
	}{
		{AuthModeJWT, "abc", "Authorization", "Bearer abc"},
		{AuthModeLicense, "lic-1", "X-License-Key", "lic-1"},
		{AuthModeNone, "abc", "", ""},
		{AuthModeJWT, "", "", ""},
	}
	for _, tc := range cases {
		h, v := authHeader(tc.mode, tc.token)
		if h != tc.wantHeader || v != tc.wantValue {
			t.Fatalf("authHeader(%q, %q) = (%q, %q), want (%q, %q)",
				tc.mode, tc.token, h, v, tc.wantHeader, tc.wantValue)
		}
	}
}

// stubServer returns a server that maps a path to a (status, body) pair.
func stubServer(t *testing.T, routes map[string]struct {
	status int
	body   string
}) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, resp := range routes {
		resp := resp
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(resp.status)
			_, _ = w.Write([]byte(resp.body))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestProbe_AllSixCategories(t *testing.T) {
	srv := stubServer(t, map[string]struct {
		status int
		body   string
	}{
		"/v1/working":        {200, `{"ok":true}`},
		"/v1/stub":           {200, `{}`},
		"/v1/auth-req":       {401, `unauthorized`},
		"/v1/auth-mis":       {403, `forbidden`},
		"/v1/missing":        {404, `not found`},
		"/v1/broken":         {500, `boom`},
		"/v1/redirect":       {301, ``},
		"/v1/probe-id/probe": {200, `{"ok":true}`}, // for placeholder substitution
	})

	manifest := &EndpointManifest{
		Version: 1,
		Groups: map[string][]EndpointEntry{
			"GROUP": {
				{Method: "GET", Path: "/v1/working"},
				{Method: "GET", Path: "/v1/stub"},
				{Method: "GET", Path: "/v1/auth-req"},
				{Method: "GET", Path: "/v1/auth-mis"},
				{Method: "GET", Path: "/v1/missing"},
				{Method: "GET", Path: "/v1/broken"},
				{Method: "GET", Path: "/v1/redirect"},
				{Method: "GET", Path: "/v1/probe-id/:id"},
			},
		},
	}

	report, err := Probe(context.Background(), &http.Client{Timeout: 5 * time.Second},
		srv.URL, AuthModeNone, "", manifest)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if report.V != 1 {
		t.Errorf("schema version V = %d, want 1", report.V)
	}
	wantCounts := map[string]int{
		CategoryWorking:      2, // /v1/working + /v1/probe-id/probe
		CategoryStub:         1,
		CategoryAuthRequired: 1,
		CategoryAuthMismatch: 1,
		CategoryNotFound:     1,
		CategoryBroken:       2, // /v1/broken (500) + /v1/redirect (301)
	}
	for cat, want := range wantCounts {
		if report.Categories[cat] != want {
			t.Errorf("category %s = %d, want %d (results=%+v)",
				cat, report.Categories[cat], want, report.Results)
		}
	}
	if report.Unreachable {
		t.Errorf("Unreachable = true, want false (server is up)")
	}

	// Path resolution recorded in result.
	for _, r := range report.Results {
		if r.Path == "/v1/probe-id/:id" && r.ResolvedPath != "/v1/probe-id/probe" {
			t.Errorf("ResolvedPath = %q, want /v1/probe-id/probe", r.ResolvedPath)
		}
	}
}

func TestProbe_Unreachable(t *testing.T) {
	manifest := &EndpointManifest{
		Version: 1,
		Groups: map[string][]EndpointEntry{
			"G": {{Method: "GET", Path: "/x"}},
		},
	}
	// Use a non-routable address; rely on the client timeout to fail fast.
	client := &http.Client{Timeout: 100 * time.Millisecond}
	report, err := Probe(context.Background(), client, "http://127.0.0.1:1", AuthModeNone, "", manifest)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !report.Unreachable {
		t.Errorf("Unreachable = false, want true (every result transport-errored): %+v", report.Results)
	}
	if got := report.Categories[CategoryBroken]; got != 1 {
		t.Errorf("Broken count = %d, want 1", got)
	}
}

func TestProbe_AuthHeaderInjection(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		token      string
		wantHeader string
		wantValue  string
	}{
		{"jwt", AuthModeJWT, "tok-jwt", "Authorization", "Bearer tok-jwt"},
		{"license", AuthModeLicense, "lic-1", "X-License-Key", "lic-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seenHeader, seenValue string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if v := r.Header.Get("Authorization"); v != "" {
					seenHeader = "Authorization"
					seenValue = v
				}
				if v := r.Header.Get("X-License-Key"); v != "" {
					seenHeader = "X-License-Key"
					seenValue = v
				}
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer srv.Close()

			manifest := &EndpointManifest{
				Version: 1,
				Groups: map[string][]EndpointEntry{
					"G": {{Method: "GET", Path: "/x"}},
				},
			}
			_, err := Probe(context.Background(), &http.Client{Timeout: 2 * time.Second},
				srv.URL, tc.mode, tc.token, manifest)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if seenHeader != tc.wantHeader || seenValue != tc.wantValue {
				t.Errorf("server saw header %q=%q, want %q=%q",
					seenHeader, seenValue, tc.wantHeader, tc.wantValue)
			}
		})
	}
}

func TestProbe_PerEntryAuthOverride(t *testing.T) {
	// HEALTH endpoints carry auth_mode: none and must NOT receive the global token.
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	manifest := &EndpointManifest{
		Version: 1,
		Groups: map[string][]EndpointEntry{
			"HEALTH": {{Method: "GET", Path: "/health", AuthMode: AuthModeNone}},
		},
	}
	_, err := Probe(context.Background(), &http.Client{Timeout: 2 * time.Second},
		srv.URL, AuthModeJWT, "should-not-leak", manifest)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if sawAuth != "" {
		t.Errorf("Authorization header leaked to AuthModeNone endpoint: %q", sawAuth)
	}
}

func TestDefaultManifest_LoadsAndCovers8Groups(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("manifest version = %d, want 1", m.Version)
	}
	wantGroups := []string{"AUTH", "PIPELINES", "QUEUE", "GITHUB", "TEAM", "ANALYTICS", "ADMIN", "HEALTH"}
	for _, g := range wantGroups {
		entries, ok := m.Groups[g]
		if !ok {
			t.Errorf("missing group %q in default manifest", g)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("group %q is empty", g)
		}
	}
}

func TestLoadManifest_ExternalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	yaml := `version: 1
groups:
  X:
    - { method: GET, path: /x }
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Groups["X"]) != 1 || m.Groups["X"][0].Path != "/x" {
		t.Errorf("loaded manifest = %+v, want X→[/x]", m.Groups)
	}
}

func TestLoadManifest_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	yaml := `version: 1
unknown_top_level: 42
groups:
  X:
    - { method: GET, path: /x }
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "unknown_top_level") {
		t.Errorf("error %v does not mention unknown_top_level", err)
	}
}

func TestLoadManifest_EmptyManifestRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(path, []byte("version: 1\ngroups: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("expected error for empty manifest")
	}
}

func TestLoadManifest_MissingVersionRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	yaml := `groups:
  X:
    - { method: GET, path: /x }
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("expected error for missing version")
	}
}
