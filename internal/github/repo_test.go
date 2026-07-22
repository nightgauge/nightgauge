package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// permServer returns an httptest server that answers the collaborator-permission
// REST endpoint with the given status and permission level. A REST client built
// with NewClientWithURL(token, srv.URL+"/graphql") routes REST calls here (the
// base URL is graphqlURL with the /graphql suffix stripped).
func permServer(t *testing.T, status int, permission string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if permission != "" {
			_, _ = w.Write([]byte(`{"permission":"` + permission + `"}`))
		} else {
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return NewClientWithURL("test-token", srv.URL+"/graphql")
}

func TestHasRepoWriteAccess(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		permission string
		wantWrite  bool
		wantErr    bool
	}{
		{"admin grants write", 200, "admin", true, false},
		{"write grants write", 200, "write", true, false},
		{"read denies write", 200, "read", false, false},
		{"none denies write", 200, "none", false, false},
		// 404 = not a collaborator → CONFIRMED no write, NOT an error (#4068).
		{"404 not-a-collaborator is confirmed no-write", 404, "", false, false},
		// Other non-2xx → error so the caller can treat it as infra/visibility.
		{"500 is an error", 500, "", false, true},
		{"403 is an error", 403, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := permServer(t, tc.status, tc.permission)
			got, err := c.HasRepoWriteAccess(context.Background(), "bot", "owner", "repo")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.wantWrite {
				t.Errorf("HasRepoWriteAccess = %v, want %v", got, tc.wantWrite)
			}
		})
	}
}

func TestHasRepoAdminAccess(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		permission string
		wantAdmin  bool
		wantErr    bool
	}{
		{"admin grants admin", 200, "admin", true, false},
		{"write does not grant admin", 200, "write", false, false},
		{"read does not grant admin", 200, "read", false, false},
		{"404 not-a-collaborator is confirmed no-admin", 404, "", false, false},
		{"500 is an error", 500, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := permServer(t, tc.status, tc.permission)
			got, err := c.HasRepoAdminAccess(context.Background(), "bot", "owner", "repo")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.wantAdmin {
				t.Errorf("HasRepoAdminAccess = %v, want %v", got, tc.wantAdmin)
			}
		})
	}
}
