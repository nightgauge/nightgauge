package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// GitHub App installation tokens (#1955, ADR-014 decision 1).
//
// An installation carries its own rate-limit bucket (5,000 points/hr, scaling
// to 12,500), so pipeline traffic stops drawing on the maintainer's personal
// quota and becomes attributable to the App. The binary signs a short-lived
// JWT with the App's private key, exchanges it for an installation token
// (valid one hour), and re-mints before expiry.

// AppCredentials is one owner's GitHub App installation, fully resolved: the
// private key is already read from wherever the machine-tier config pointed.
// It is never logged and never rendered — String() names only the ids.
type AppCredentials struct {
	// AppID is the App's numeric id or client id, the JWT issuer.
	AppID string
	// InstallationID is the installation on the owner being addressed.
	InstallationID int64
	// PrivateKeyPEM is the App's private key (PKCS#1 or PKCS#8 PEM).
	PrivateKeyPEM []byte
	// Slug and BotUserID, when both are set, give the App's commit identity:
	// "<slug>[bot] <<bot-user-id>+<slug>[bot]@users.noreply.github.com>".
	Slug      string
	BotUserID int64
}

func (a *AppCredentials) String() string {
	if a == nil {
		return "<no app>"
	}
	return fmt.Sprintf("github-app %s (installation %d)", a.AppID, a.InstallationID)
}

// BotLogin is the login GitHub gives the App's bot user, or "" when the slug
// is not configured.
func (a *AppCredentials) BotLogin() string {
	if a == nil || a.Slug == "" {
		return ""
	}
	return a.Slug + "[bot]"
}

// CommitIdentity returns the name and email that attribute a commit to the
// App, and ok=false when the slug or bot user id is not configured.
func (a *AppCredentials) CommitIdentity() (name, email string, ok bool) {
	if a == nil || a.Slug == "" || a.BotUserID <= 0 {
		return "", "", false
	}
	login := a.BotLogin()
	return login, fmt.Sprintf("%d+%s@users.noreply.github.com", a.BotUserID, login), true
}

// AppResolver is optionally implemented by a TokenResolver whose config
// declares a GitHub App. ResolveGitHubApp returns (nil, nil) when no App is
// configured for owner, and an error when one is configured but unusable
// (the key cannot be read) — never a silent nil for a broken configuration.
type AppResolver interface {
	ResolveGitHubApp(owner string) (*AppCredentials, error)
}

// resolveApp returns the App configured for owner, if cfg declares one.
func resolveApp(cfg TokenResolver, owner string) (*AppCredentials, error) {
	ar, ok := cfg.(AppResolver)
	if !ok || cfg == nil {
		return nil, nil
	}
	return ar.ResolveGitHubApp(owner)
}

// githubAppAPIBase is the REST root the token exchange posts to. A var so
// tests can point it at an httptest server.
var githubAppAPIBase = "https://api.github.com"

// appTokenRefreshMargin is how long before expiry a cached token stops being
// handed out. Long enough that a request issued with it cannot outlive it.
const appTokenRefreshMargin = 10 * time.Minute

// appJWT signs the App-level JWT GitHub exchanges for an installation token:
// RS256, issued 60s in the past to absorb clock skew, valid nine minutes (the
// maximum is ten).
func appJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}{now.Add(-60 * time.Second).Unix(), now.Add(9 * time.Minute).Unix(), appID})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// parseAppPrivateKey reads the PEM GitHub issues (PKCS#1) or its PKCS#8
// conversion. Errors never quote the key material.
func parseAppPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(pemBytes))
	if block == nil {
		return nil, errors.New("github app private key: no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("github app private key: not an RSA key in PKCS#1 or PKCS#8 form")
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key: not an RSA key")
	}
	return k, nil
}

// cachedAppToken is the on-disk shape of a minted installation token.
// Permissions is what GitHub granted the token at mint time; only its hash,
// PermissionsHash, is written to the cache, so a later change to the
// installation's permissions (an org accepting a new one) can be detected and
// the token re-minted without the operator deleting the file (#2094).
type cachedAppToken struct {
	Token           string         `json:"token"`
	ExpiresAt       time.Time      `json:"expires_at"`
	Permissions     AppPermissions `json:"permissions,omitempty"`
	PermissionsHash string         `json:"permissions_hash"`
}

// cacheRecord is the entry written to disk: the token, its expiry and the
// hash of its permissions, never the permission map itself.
func (c *cachedAppToken) cacheRecord() cachedAppToken {
	return cachedAppToken{Token: c.Token, ExpiresAt: c.ExpiresAt, PermissionsHash: c.PermissionsHash}
}

// appTokenCachePath is where installation's token is cached between
// processes: under the machine state root, one file per installation, mode
// 0600. Every CLI invocation would otherwise mint a fresh token.
func appTokenCachePath(installationID int64) (string, error) {
	root, err := layout.StateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "github-app-token-"+strconv.FormatInt(installationID, 10)+".json"), nil
}

// appTokenMu serialises minting within one process so concurrent clients for
// the same installation share one exchange.
var appTokenMu sync.Mutex

// appInstallationToken returns a valid installation token for creds, from the
// cache when it has more than appTokenRefreshMargin left, else freshly minted.
func appInstallationToken(ctx context.Context, creds *AppCredentials, now func() time.Time) (*oauth2.Token, error) {
	appTokenMu.Lock()
	defer appTokenMu.Unlock()

	path, pathErr := appTokenCachePath(creds.InstallationID)
	if pathErr == nil {
		if data, err := os.ReadFile(path); err == nil {
			var c cachedAppToken
			// An entry without a permissions hash predates #2094 and cannot be
			// compared with the installation, so it is re-minted.
			if json.Unmarshal(data, &c) == nil && c.Token != "" && c.PermissionsHash != "" &&
				c.ExpiresAt.Sub(now()) > appTokenRefreshMargin {
				return &oauth2.Token{AccessToken: c.Token, Expiry: c.ExpiresAt.Add(-appTokenRefreshMargin)}, nil
			}
		}
	}

	tok, err := mintInstallationToken(ctx, creds, now())
	if err != nil {
		return nil, err
	}
	if pathErr == nil {
		if data, mErr := json.Marshal(tok.cacheRecord()); mErr == nil {
			_ = writeFileAtomic0600(path, data)
		}
	}
	return &oauth2.Token{AccessToken: tok.Token, Expiry: tok.ExpiresAt.Add(-appTokenRefreshMargin)}, nil
}

// mintInstallationToken exchanges an App JWT for an installation token.
func mintInstallationToken(ctx context.Context, creds *AppCredentials, now time.Time) (*cachedAppToken, error) {
	key, err := parseAppPrivateKey(creds.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	jwt, err := appJWT(creds.AppID, key, now)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", githubAppAPIBase, creds.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mint installation token for %s: %w", creds, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, fmt.Errorf("mint installation token for %s: HTTP %d: %s", creds, resp.StatusCode, e.Message)
	}
	var out cachedAppToken
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return nil, fmt.Errorf("mint installation token for %s: response carried no token", creds)
	}
	out.PermissionsHash = PermissionsHash(out.Permissions)
	return &out, nil
}

func writeFileAtomic0600(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".github-app-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// appTokenSource is an oauth2.TokenSource over appInstallationToken, so a
// long-lived client (the serve daemon) re-mints on expiry instead of failing
// an hour in.
type appTokenSource struct {
	creds *AppCredentials
}

func (s appTokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return appInstallationToken(ctx, s.creds, time.Now)
}

// appReuseSource hands out one installation token until it expires, like
// oauth2.ReuseTokenSource, and can be told to drop it: a token minted before
// the installation's permissions changed keeps the old permissions for its
// whole life, so after SyncAppTokenPermissions discards the cached copy the
// in-process copy must go too.
type appReuseSource struct {
	mu  sync.Mutex
	tok *oauth2.Token
	src oauth2.TokenSource
}

func newAppReuseSource(first *oauth2.Token, creds *AppCredentials) *appReuseSource {
	return &appReuseSource{tok: first, src: appTokenSource{creds: creds}}
}

func (s *appReuseSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok.Valid() {
		return s.tok, nil
	}
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.tok = t
	return t, nil
}

func (s *appReuseSource) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok = nil
}

// PermissionsHash is a stable digest of a permission map (name=level lines,
// sorted), truncated to 16 hex characters. An empty or nil map has a hash
// too, so "minted with no permissions" is distinguishable from "unknown".
func PermissionsHash(p AppPermissions) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, p[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// SyncAppTokenPermissions compares the permissions hash stored with creds'
// cached installation token against current, the installation's permissions
// now (GET /app/installations/{id}). On a mismatch the cached token is
// discarded, so the next request re-mints one carrying the new permissions,
// and invalidated is true. No cache file is not an error.
func SyncAppTokenPermissions(creds *AppCredentials, current AppPermissions) (invalidated bool, err error) {
	if creds == nil {
		return false, nil
	}
	appTokenMu.Lock()
	defer appTokenMu.Unlock()
	path, err := appTokenCachePath(creds.InstallationID)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var c cachedAppToken
	if json.Unmarshal(data, &c) == nil && c.PermissionsHash == PermissionsHash(current) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// AppTokenExpiry reports when the cached installation token for owner's App
// stops being handed out, for callers (the env refresher) that must act
// before it does. ok=false when no App is configured or no token is cached.
func AppTokenExpiry(cfg TokenResolver, owner string) (time.Time, bool) {
	creds, err := resolveApp(cfg, owner)
	if err != nil || creds == nil {
		return time.Time{}, false
	}
	path, err := appTokenCachePath(creds.InstallationID)
	if err != nil {
		return time.Time{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	var c cachedAppToken
	if json.Unmarshal(data, &c) != nil || c.ExpiresAt.IsZero() {
		return time.Time{}, false
	}
	return c.ExpiresAt.Add(-appTokenRefreshMargin), true
}

// ResolveApp exposes the configured App for owner to callers outside this
// package (doctor, the commit-identity export).
func ResolveApp(cfg TokenResolver, owner string) (*AppCredentials, error) {
	return resolveApp(cfg, owner)
}
