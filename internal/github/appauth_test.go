package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

func testAppKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// fakeMint stands in for POST /app/installations/{id}/access_tokens. It
// verifies the JWT's signature against pub and counts exchanges.
func fakeMint(t *testing.T, pub *rsa.PublicKey, status int, lifetime time.Duration) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Fatalf("not a JWT: %q", jwt)
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
			t.Errorf("JWT signature does not verify: %v", err)
		}
		if status != http.StatusCreated {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation", "expires_at": time.Now().Add(lifetime).UTC()})
	}))
	t.Cleanup(srv.Close)
	old := githubAppAPIBase
	githubAppAPIBase = srv.URL
	t.Cleanup(func() { githubAppAPIBase = old })
	t.Setenv(layout.EnvStateHome, t.TempDir())
	return &hits
}

func TestAppJWT_ClaimsAndSignature(t *testing.T) {
	key, _ := testAppKey(t)
	now := time.Unix(1_800_000_000, 0)
	jwt, err := appJWT("12345", key, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		IAT, EXP int64
		ISS      string
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.ISS != "12345" || c.IAT != now.Unix()-60 || c.EXP-c.IAT > 600 {
		t.Errorf("claims = %+v: want iss 12345, iat backdated 60s, lifetime within GitHub's 10 minutes", c)
	}
}

func TestParseAppPrivateKey_AcceptsPKCS8AndNeverQuotesTheKey(t *testing.T) {
	key, pkcs1 := testAppKey(t)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	if _, err := parseAppPrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Errorf("PKCS#8: %v", err)
	}
	broken := append([]byte{}, pkcs1...)
	broken[40] ^= 0xff
	_, err := parseAppPrivateKey(broken)
	if err == nil || strings.Contains(err.Error(), string(pkcs1[30:60])) {
		t.Errorf("a corrupt key must fail without quoting it: %v", err)
	}
}

// One exchange serves every process until the token nears expiry: the cache
// is what keeps a CLI invocation from minting per call.
func TestAppInstallationToken_MintsOnceAndCaches0600(t *testing.T) {
	key, pemBytes := testAppKey(t)
	hits := fakeMint(t, &key.PublicKey, http.StatusCreated, time.Hour)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}

	for i := 0; i < 3; i++ {
		tok, err := appInstallationToken(context.Background(), creds, time.Now)
		if err != nil || tok.AccessToken != "ghs_installation" {
			t.Fatalf("call %d: tok=%v err=%v", i, tok, err)
		}
	}
	if *hits != 1 {
		t.Errorf("exchanges = %d, want 1 (cache unused)", *hits)
	}
	path, _ := appTokenCachePath(42)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %v (%v), want 0600", info, err)
	}
}

func TestAppInstallationToken_RemintsInsideTheRefreshMargin(t *testing.T) {
	key, pemBytes := testAppKey(t)
	hits := fakeMint(t, &key.PublicKey, http.StatusCreated, appTokenRefreshMargin-time.Minute)
	creds := &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}
	for i := 0; i < 2; i++ {
		if _, err := appInstallationToken(context.Background(), creds, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	if *hits != 2 {
		t.Errorf("exchanges = %d, want 2: a token inside the margin must not be reused", *hits)
	}
}

type fakeAppResolver struct {
	pat   string
	creds *AppCredentials
}

func (f fakeAppResolver) ResolveToken(string) (string, error) { return f.pat, nil }
func (f fakeAppResolver) SuppressGHWarning() bool             { return true }
func (f fakeAppResolver) ResolveGitHubApp(string) (*AppCredentials, error) {
	return f.creds, nil
}

// #1955 AC2: a configured App is preferred over a configured PAT.
func TestResolveTokenChain_PrefersTheAppOverAPAT(t *testing.T) {
	t.Setenv("CI", "")
	key, pemBytes := testAppKey(t)
	fakeMint(t, &key.PublicKey, http.StatusCreated, time.Hour)
	cfg := fakeAppResolver{pat: "ghp_personal", creds: &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}}

	tok, err := ResolveTokenChain(cfg, "nightgauge")
	if err != nil || tok != "ghs_installation" {
		t.Errorf("token = %q (%v), want the installation token", tok, err)
	}
	c, err := NewClientFromConfig(cfg, "nightgauge", "")
	if err != nil || c.App() == nil {
		t.Fatalf("client is not the App's: %v", err)
	}
	if c.trackerUser != "app:42" || c.identity != "app:42" {
		t.Errorf("tracker slot %q / identity %q must be the installation's, not the user's", c.trackerUser, c.identity)
	}
}

// A broken App must not take the personal chain down with it.
func TestResolveTokenChain_FallsBackToThePATWhenTheAppCannotMint(t *testing.T) {
	t.Setenv("CI", "")
	key, pemBytes := testAppKey(t)
	fakeMint(t, &key.PublicKey, http.StatusUnauthorized, time.Hour)
	cfg := fakeAppResolver{pat: "ghp_personal", creds: &AppCredentials{AppID: "1", InstallationID: 42, PrivateKeyPEM: pemBytes}}

	if tok, err := ResolveTokenChain(cfg, "nightgauge"); err != nil || tok != "ghp_personal" {
		t.Errorf("token = %q (%v), want the PAT", tok, err)
	}
	c, err := NewClientFromConfig(cfg, "nightgauge", "")
	if err != nil || c.App() != nil {
		t.Errorf("client should be the PAT's: app=%v err=%v", c.App(), err)
	}
}

// An installation token has no GET /user; the client answers with the bot
// login instead of failing doctor and forge auth.
func TestAppClient_WhoamiIsTheBotWithoutCallingGetUser(t *testing.T) {
	c := NewClientWithToken("ghs_x")
	c.app = &AppCredentials{AppID: "1", InstallationID: 42, Slug: "nightgauge-pipeline", BotUserID: 99}
	actor, err := c.Whoami(context.Background())
	if err != nil || actor.Login != "nightgauge-pipeline[bot]" {
		t.Errorf("whoami = %+v (%v)", actor, err)
	}
	name, email, ok := c.app.CommitIdentity()
	if !ok || name != "nightgauge-pipeline[bot]" || email != "99+nightgauge-pipeline[bot]@users.noreply.github.com" {
		t.Errorf("commit identity = %q <%q> ok=%v", name, email, ok)
	}
}
