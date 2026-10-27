package github

// #2578: the serve daemon caches a forge.Router per board for its whole life.
// The router used to carry the App installation token as a string, so every
// client it built after that token expired sent a dead credential and every
// sweep failed with 401 until a restart. These tests pin the source path.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/layout"
	"golang.org/x/oauth2"
)

type tokenSourceRoundTrip func(*http.Request) (*http.Response, error)

func (f tokenSourceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// authRecorder is a REST server that records each request's Authorization
// header and answers status. http.DefaultTransport is redirected to it for the
// test, which is where an oauth2 client with no base transport sends.
type authRecorder struct {
	mu     sync.Mutex
	auth   []string
	status int
}

func (a *authRecorder) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auth...)
}

func newAuthRecorder(t *testing.T) *authRecorder {
	t.Helper()
	t.Setenv(layout.EnvStateHome, t.TempDir())
	rec := &authRecorder{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.auth = append(rec.auth, r.Header.Get("Authorization"))
		status := rec.status
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"full_name":"acme/web","name":"web","owner":{"login":"acme"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = tokenSourceRoundTrip(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host, r2.Host = target.Scheme, target.Host, target.Host
		return prev.RoundTrip(r2)
	})
	return rec
}

// sequenceSource hands out ghs_refreshed_N on each call: the re-mint.
type sequenceSource struct {
	mu sync.Mutex
	n  int
}

func (s *sequenceSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return &oauth2.Token{AccessToken: "ghs_refreshed_" + string(rune('0'+s.n)), Expiry: time.Now().Add(time.Hour)}, nil
}

func TestForgeAdapter_AppTokenSourceRefreshesAfterExpiry(t *testing.T) {
	rec := newAuthRecorder(t)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, Slug: "nightgauge-pipeline"}
	first := &oauth2.Token{AccessToken: "ghs_first", Expiry: time.Now().Add(time.Hour)}
	src := &appReuseSource{tok: first, src: &sequenceSource{}, creds: creds}

	fc, err := forge.New(forge.Config{Kind: forge.KindGitHub, Token: "ghs_first", TokenSource: src, Owner: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	c := fc.(*ForgeAdapter).client
	if c.App() != creds || c.identity != "app:42" || c.trackerUser != "app:42" {
		t.Errorf("source-built client lost the installation's identity: app=%v identity=%q slot=%q", c.App(), c.identity, c.trackerUser)
	}
	if got, want := c.ledgerIdentity.Load(), appLedgerIdentity(creds); got != want {
		t.Errorf("ledger identity = %q, want %q", got, want)
	}

	ctx := context.Background()
	if _, err := fc.Repo().RepoMetadata(ctx, "acme", "web"); err != nil {
		t.Fatal(err)
	}
	first.Expiry = time.Now().Add(-time.Minute) // the installation token dies
	if _, err := fc.Repo().RepoMetadata(ctx, "acme", "web"); err != nil {
		t.Fatal(err)
	}
	got := rec.seen()
	if len(got) != 2 || got[0] != "Bearer ghs_first" || got[1] != "Bearer ghs_refreshed_1" {
		t.Fatalf("Authorization across expiry = %q, want [Bearer ghs_first Bearer ghs_refreshed_1] — a client that keeps sending the first token is the #2578 401", got)
	}
}

// The string path is what the router used to hand the adapter: it keeps
// sending the token it was built with, alive or not. Pinned so the test above
// is known to distinguish the two paths.
func TestForgeAdapter_TokenStringIsFrozen(t *testing.T) {
	rec := newAuthRecorder(t)
	fc, err := forge.New(forge.Config{Kind: forge.KindGitHub, Token: "ghs_first", Owner: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := fc.Repo().RepoMetadata(context.Background(), "acme", "web"); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.seen(); len(got) != 2 || got[1] != "Bearer ghs_first" {
		t.Fatalf("Authorization = %q, want the static token twice", got)
	}
}

func TestResolveTokenSource_AppYieldsARefreshingSource(t *testing.T) {
	t.Setenv("CI", "")
	key, pemBytes := testAppKey(t)
	fakeMint(t, &key.PublicKey, http.StatusCreated, time.Hour)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}

	src, tok, err := ResolveTokenSource(fakeAppResolver{pat: "ghp_personal", creds: creds}, "acme")
	if err != nil || tok != "ghs_installation" {
		t.Fatalf("token = %q (%v), want the installation token", tok, err)
	}
	reuse, ok := src.(*appReuseSource)
	if !ok || reuse.creds != creds {
		t.Fatalf("source = %T, want the App's refreshing source", src)
	}
	if got, _ := src.Token(); got.AccessToken != "ghs_installation" {
		t.Errorf("source's first token = %q, want the one the chain resolved", got.AccessToken)
	}

	// A personal token is long-lived: no source, the string is the credential.
	src, tok, err = ResolveTokenSource(fakeAppResolver{pat: "ghp_personal"}, "acme")
	if err != nil || src != nil || tok != "ghp_personal" {
		t.Errorf("PAT tier = (%v, %q, %v), want (nil, ghp_personal, nil)", src, tok, err)
	}
}

// A REST 401 is forge.ErrUnauthorized, so the sweep skips the repo instead of
// trusting every other producer's empty answer.
func TestRESTStatusErrors_401IsUnauthorized(t *testing.T) {
	rec := newAuthRecorder(t)
	rec.status = http.StatusUnauthorized
	fc, err := forge.New(forge.Config{Kind: forge.KindGitHub, Token: "ghs_dead", Owner: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fc.Repo().RepoMetadata(context.Background(), "acme", "web")
	if !errors.Is(err, forge.ErrUnauthorized) {
		t.Fatalf("repo view on 401 = %v, want forge.ErrUnauthorized", err)
	}

	if err := restStatusErr(http.StatusUnauthorized, nil); !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("restStatusErr(401) = %v, want forge.ErrUnauthorized", err)
	}
	if err := (&restStatusError{Status: http.StatusUnauthorized}); !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("restStatusError(401) = %v, want forge.ErrUnauthorized", err)
	}
	// 403 also answers a secondary rate limit; it is not a dead credential.
	if err := restStatusErr(http.StatusForbidden, nil); errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("restStatusErr(403) must not be forge.ErrUnauthorized: %v", err)
	}
	body := []byte(`{"message":"Not Found"}`)
	if got := restStatusErr(http.StatusNotFound, body).Error(); got != "REST 404: "+restErrorSummary(body) {
		t.Errorf("message = %q, want the unchanged REST <status>: <summary> form", got)
	}
}
