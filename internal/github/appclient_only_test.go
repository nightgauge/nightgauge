package github

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// errAppResolver is an App configuration that cannot be used.
type errAppResolver struct{ fakeAppResolver }

func (errAppResolver) ResolveGitHubApp(string) (*AppCredentials, error) {
	return nil, errors.New("github_auth.app.private_key_path: cannot read")
}

// #1570: a conversation write that only the App may carry is refused, never
// carried by the CI environment token, GITHUB_TOKEN, GH_TOKEN or a configured
// personal token, all of which the ordinary chain would have used.
func TestNewAppOnlyClient_NeverFallsBackToAnotherToken(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_TOKEN", "ghp_ci_environment")
	t.Setenv("GH_TOKEN", "ghp_gh_environment")
	key, pemBytes := testAppKey(t)
	cases := map[string]struct {
		cfg   TokenResolver
		mint  int // the token exchange's status; 0 when no exchange may happen
		calls int32
	}{
		"no configuration": {cfg: nil},
		"no App":           {cfg: fakeAppResolver{pat: "ghp_personal"}},
		"an unusable App":  {cfg: errAppResolver{fakeAppResolver{pat: "ghp_personal"}}},
		"an App that cannot mint": {cfg: fakeAppResolver{pat: "ghp_personal", creds: &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}},
			mint: http.StatusUnauthorized, calls: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			hits := new(int32)
			if tc.mint != 0 {
				hits = fakeMint(t, &key.PublicKey, tc.mint, time.Hour)
			}
			c, err := NewAppOnlyClient(context.Background(), tc.cfg, "acme")
			if !errors.Is(err, ErrAppUnavailable) || c != nil {
				t.Fatalf("client %v, err %v; want ErrAppUnavailable and no client", c, err)
			}
			if got := atomic.LoadInt32(hits); got != tc.calls {
				t.Errorf("%d token exchanges, want %d", got, tc.calls)
			}
		})
	}
	// The contrast: the ordinary chain takes the CI token for the same
	// configuration, which is exactly what a teammate's write must not do.
	if c, err := NewClientFromConfig(fakeAppResolver{pat: "ghp_personal"}, "acme", ""); err != nil || c.App() != nil {
		t.Fatalf("the ordinary chain should have resolved a personal client: %v", err)
	}
}

// With an App configured the client is the installation's, even on a CI host
// that also holds an environment token.
func TestNewAppOnlyClient_IsTheInstallation(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_TOKEN", "ghp_ci_environment")
	key, pemBytes := testAppKey(t)
	hits := fakeMint(t, &key.PublicKey, http.StatusCreated, time.Hour)
	cfg := fakeAppResolver{pat: "ghp_personal", creds: &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}}

	c, err := NewAppOnlyClient(context.Background(), cfg, "acme")
	if err != nil || c.App() == nil {
		t.Fatalf("client is not the App's: %v", err)
	}
	if c.identity != "app:42" {
		t.Errorf("identity %q, want the installation's", c.identity)
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Errorf("%d token exchanges, want 1", atomic.LoadInt32(hits))
	}
}
