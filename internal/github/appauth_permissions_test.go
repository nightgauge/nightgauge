package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// fakeMintWithPermissions is fakeMint whose tokens carry perms, as GitHub's
// access_tokens response does. Each mint returns a distinct token.
func fakeMintWithPermissions(t *testing.T, perms *AppPermissions) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       "ghs_mint_" + string(rune('0'+n)),
			"expires_at":  time.Now().Add(time.Hour).UTC(),
			"permissions": *perms,
		})
	}))
	t.Cleanup(srv.Close)
	old := githubAppAPIBase
	githubAppAPIBase = srv.URL
	t.Cleanup(func() { githubAppAPIBase = old })
	t.Setenv(layout.EnvStateHome, t.TempDir())
	return &hits
}

// #2094: a token minted before the org accepted a new permission keeps the old
// permissions for its whole life. Once the installation's permissions differ
// from the hash stored with the cached token, the token is re-minted; the
// operator never deletes github-app-token-*.json by hand.
func TestAppTokenCacheInvalidatesOnPermissionChange(t *testing.T) {
	_, pemBytes := testAppKey(t)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}
	perms := AppPermissions{"issues": "write", "contents": "write"}
	hits := fakeMintWithPermissions(t, &perms)

	first, err := appInstallationToken(context.Background(), creds, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := appTokenCachePath(42)
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"permissions_hash":"`+PermissionsHash(perms)+`"`) {
		t.Fatalf("cache must store the permissions hash: %s", raw)
	}
	if strings.Contains(string(raw), `"issues"`) {
		t.Errorf("cache must store the hash, not the permission map: %s", raw)
	}

	// Unchanged permissions: the cached token stays.
	if inv, err := SyncAppTokenPermissions(creds, AppPermissions{"contents": "write", "issues": "write"}); err != nil || inv {
		t.Fatalf("same permissions must not invalidate: inv=%v err=%v", inv, err)
	}
	if tok, _ := appInstallationToken(context.Background(), creds, time.Now); tok.AccessToken != first.AccessToken || *hits != 1 {
		t.Fatalf("unchanged permissions re-minted: hits=%d", *hits)
	}

	// The org accepts organization_projects: the installation now differs.
	perms = AppPermissions{"issues": "write", "contents": "write", "organization_projects": "write"}
	inv, err := SyncAppTokenPermissions(creds, perms)
	if err != nil || !inv {
		t.Fatalf("changed permissions must invalidate: inv=%v err=%v", inv, err)
	}
	second, err := appInstallationToken(context.Background(), creds, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if *hits != 2 || second.AccessToken == first.AccessToken {
		t.Errorf("a cached token with an old permissions hash must be re-minted: hits=%d tokens %q -> %q",
			*hits, first.AccessToken, second.AccessToken)
	}
	if inv, _ := SyncAppTokenPermissions(creds, perms); inv {
		t.Error("the re-minted token carries the new hash; a second sync must not invalidate it")
	}
}

// A cache entry written before permission hashes existed cannot be compared,
// so it is re-minted rather than trusted.
func TestAppTokenCache_EntryWithoutHashIsReminted(t *testing.T) {
	_, pemBytes := testAppKey(t)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}
	perms := AppPermissions{"issues": "write"}
	hits := fakeMintWithPermissions(t, &perms)
	path, _ := appTokenCachePath(42)
	legacy, _ := json.Marshal(map[string]any{"token": "ghs_legacy", "expires_at": time.Now().Add(time.Hour)})
	if err := writeFileAtomic0600(path, legacy); err != nil {
		t.Fatal(err)
	}
	tok, err := appInstallationToken(context.Background(), creds, time.Now)
	if err != nil || tok.AccessToken == "ghs_legacy" || *hits != 1 {
		t.Errorf("legacy entry reused: tok=%v hits=%d err=%v", tok, *hits, err)
	}
}

// ResetAppToken drops the in-memory copy, so the client's next request reads
// the (re-minted) token instead of the one it was built with.
func TestAppReuseSource_ResetDropsTheInMemoryToken(t *testing.T) {
	_, pemBytes := testAppKey(t)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}
	perms := AppPermissions{"issues": "write"}
	hits := fakeMintWithPermissions(t, &perms)
	first, err := appInstallationToken(context.Background(), creds, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	src := newAppReuseSource(first, creds)
	c := &Client{appSrc: src}
	if tok, _ := src.Token(); tok.AccessToken != first.AccessToken {
		t.Fatal("reuse source must hand out the first token")
	}
	perms = AppPermissions{"issues": "write", "organization_projects": "write"}
	if inv, _ := SyncAppTokenPermissions(creds, perms); !inv {
		t.Fatal("sync must invalidate")
	}
	c.ResetAppToken()
	tok, err := src.Token()
	if err != nil || tok.AccessToken == first.AccessToken || *hits != 2 {
		t.Errorf("after reset the source must re-mint: tok=%v hits=%d err=%v", tok, *hits, err)
	}
	(*Client)(nil).ResetAppToken() // nil-safe
	NewClientWithToken("ghp_x").ResetAppToken()
}

func TestAppInspector_ReadsAppAndInstallationWithTheJWT(t *testing.T) {
	_, pemBytes := testAppKey(t)
	creds := &AppCredentials{AppID: "7", InstallationID: 42, PrivateKeyPEM: pemBytes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Errorf("%s without an App JWT", r.URL.Path)
		}
		switch r.URL.Path {
		case "/app":
			_, _ = w.Write([]byte(`{"id":7,"slug":"ng-pipeline","owner":{"login":"acme","type":"Organization"},"permissions":{"issues":"write"}}`))
		case "/app/installations/42":
			_, _ = w.Write([]byte(`{"id":42,"account":{"login":"acme","type":"Organization"},"permissions":{"issues":"read"},"suspended_at":"2026-09-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	in := NewAppInspectorWithBase(creds, srv.URL)
	app, err := in.App(context.Background())
	if err != nil || app.Slug != "ng-pipeline" || app.Owner.Type != "Organization" || app.Permissions["issues"] != "write" {
		t.Fatalf("app = %+v (%v)", app, err)
	}
	inst, err := in.Installation(context.Background())
	if err != nil || inst.Account.Login != "acme" || inst.Permissions["issues"] != "read" || !inst.Suspended() {
		t.Fatalf("installation = %+v (%v)", inst, err)
	}

	missing := NewAppInspectorWithBase(&AppCredentials{AppID: "7", InstallationID: 9, PrivateKeyPEM: pemBytes}, srv.URL)
	_, err = missing.Installation(context.Background())
	var apiErr *AppAPIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || strings.Contains(err.Error(), "ey") {
		t.Errorf("a 404 must be an AppAPIError that carries no JWT: %v", err)
	}
}

func TestListOwnerProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Query, "user(login") {
			_, _ = w.Write([]byte(`{"data":{"user":null},"errors":[{"message":"Could not resolve to a User with the login of 'x'."}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"organization":{"login":"Acme","projectsV2":{"nodes":[{"number":2,"title":"B"},{"number":1,"title":"A","closed":true}]}}}}`))
	}))
	defer srv.Close()
	c := NewClientWithURL("ghp_x", srv.URL)
	got, err := ListOwnerProjects(context.Background(), c, "acme", OwnerTypeOrg)
	if err != nil || got.Login != "Acme" || len(got.Projects) != 2 || got.Projects[0].Number != 1 || !got.Projects[0].Closed {
		t.Fatalf("projects = %+v (%v)", got, err)
	}
	if _, err := ListOwnerProjects(context.Background(), c, "x", OwnerTypeUser); err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Errorf("want the GraphQL message, got %v", err)
	}
}
