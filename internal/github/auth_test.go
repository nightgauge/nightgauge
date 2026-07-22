package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseScopes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", []string{}},
		{"single", "repo", []string{"repo"}},
		{"multiple", "repo, project, read:org", []string{"repo", "project", "read:org"}},
		{"no spaces", "repo,project,read:org", []string{"repo", "project", "read:org"}},
		{"extra spaces", "  repo ,  project ", []string{"repo", "project"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseScopes(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseScopes(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestComputeMissingScopes(t *testing.T) {
	tests := []struct {
		name     string
		actual   []string
		required []string
		want     []string
	}{
		{
			name:     "all present",
			actual:   []string{"repo", "project", "read:org"},
			required: []string{"repo", "project", "read:org"},
			want:     nil,
		},
		{
			name:     "missing project and read:org",
			actual:   []string{"repo"},
			required: []string{"repo", "project", "read:org"},
			want:     []string{"project", "read:org"},
		},
		{
			name:     "empty actual",
			actual:   []string{},
			required: []string{"repo"},
			want:     []string{"repo"},
		},
		{
			name:     "empty required",
			actual:   []string{"repo"},
			required: []string{},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeMissingScopes(tt.actual, tt.required)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("computeMissingScopes(%v, %v) = %v, want %v", tt.actual, tt.required, got, tt.want)
			}
		})
	}
}

// newTestServer creates a test HTTP server with the provided handlers and
// returns a GitHub client wired to it.
func newTestServerWithHandlers(t *testing.T, mux *http.ServeMux) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Build a client that points REST calls at the test server.
	c := NewClientWithToken("test-token")
	// Override the http client transport to redirect to the test server.
	c.http.Transport = &rewriteTransport{base: srv.URL}
	return c, srv
}

// rewriteTransport rewrites the host of every request to a fixed base URL.
type rewriteTransport struct {
	base string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = req.URL.Host
	// Replace the host with the test server base.
	base := t.base[len("http://"):]
	req2.URL.Host = base
	return http.DefaultTransport.RoundTrip(req2)
}

func TestCheckTokenScopes_AllPresent(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/rate_limit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, project, read:org")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resources":{}}`))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
	})
	mux.HandleFunc("/user/orgs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "nightgauge"}})
	})

	c, _ := newTestServerWithHandlers(t, mux)
	info, err := c.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes: %v", err)
	}

	if !info.Valid {
		t.Errorf("expected Valid=true, got false; missing=%v", info.MissingScopes)
	}
	if info.Login != "octocat" {
		t.Errorf("Login = %q, want %q", info.Login, "octocat")
	}
	if len(info.OrgMemberships) != 1 || info.OrgMemberships[0] != "nightgauge" {
		t.Errorf("OrgMemberships = %v, want [nightgauge]", info.OrgMemberships)
	}
}

func TestCheckTokenScopes_MissingProject(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rate_limit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, read:org")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
	})
	mux.HandleFunc("/user/orgs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]map[string]string{})
	})

	c, _ := newTestServerWithHandlers(t, mux)
	info, err := c.CheckTokenScopes(context.Background())
	if err != nil {
		t.Fatalf("CheckTokenScopes: %v", err)
	}

	if info.Valid {
		t.Error("expected Valid=false")
	}
	if len(info.MissingScopes) != 1 || info.MissingScopes[0] != "project" {
		t.Errorf("MissingScopes = %v, want [project]", info.MissingScopes)
	}
}

func TestCheckTokenScopes_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rate_limit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	c, _ := newTestServerWithHandlers(t, mux)
	_, err := c.CheckTokenScopes(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}
