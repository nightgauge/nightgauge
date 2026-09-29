package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Reading a GitHub App's declared permissions and one installation's granted
// permissions, authenticated with the App JWT (#2094). Doctor compares the two
// against what the pipeline needs, so "the App lacks Organization → Projects"
// and "the org has not accepted it yet" are separate, named diagnoses rather
// than one opaque "Could not resolve to a ProjectV2".
//
// The JWT is built here and sent only in the Authorization header. It never
// appears in an error, a log line or a returned value.

// AppPermissions maps a GitHub App permission name ("issues",
// "organization_projects") to its level ("read", "write" or "admin").
type AppPermissions map[string]string

// AppAccount is the owner of an App or the account an installation is on.
type AppAccount struct {
	Login string `json:"login"`
	Type  string `json:"type"` // "Organization" or "User"
}

// AppInfo is the part of GET /app that doctor reads: the App's identity and
// the permissions it declares (requests of every installation).
type AppInfo struct {
	ID          int64          `json:"id"`
	Slug        string         `json:"slug"`
	Owner       AppAccount     `json:"owner"`
	Permissions AppPermissions `json:"permissions"`
}

// AppInstallation is the part of GET /app/installations/{id} that doctor
// reads: where the App is installed, what that installation has accepted, and
// whether it is suspended.
type AppInstallation struct {
	ID          int64          `json:"id"`
	Account     AppAccount     `json:"account"`
	TargetType  string         `json:"target_type"`
	Permissions AppPermissions `json:"permissions"`
	SuspendedAt *time.Time     `json:"suspended_at"`
	SuspendedBy *AppAccount    `json:"suspended_by"`
}

// Suspended reports whether the installation is suspended.
func (i *AppInstallation) Suspended() bool { return i != nil && i.SuspendedAt != nil }

// AppAPIError is a non-2xx answer from an App-authenticated endpoint. It
// carries GitHub's message, never the request's credentials.
type AppAPIError struct {
	Endpoint string // "GET /app"
	Status   int
	Message  string
}

func (e *AppAPIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Endpoint, e.Status, e.Message)
}

// AppInspector reads App metadata with a freshly signed JWT per request.
type AppInspector struct {
	creds *AppCredentials
	base  string
	http  *http.Client
	now   func() time.Time
}

// NewAppInspector reads creds' App from api.github.com.
func NewAppInspector(creds *AppCredentials) *AppInspector {
	return NewAppInspectorWithBase(creds, githubAppAPIBase)
}

// NewAppInspectorWithBase reads from base instead of api.github.com; tests
// point it at an httptest server.
func NewAppInspectorWithBase(creds *AppCredentials, base string) *AppInspector {
	return &AppInspector{creds: creds, base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 20 * time.Second}, now: time.Now}
}

// App is GET /app: the App's slug, owner and declared permissions.
func (a *AppInspector) App(ctx context.Context) (*AppInfo, error) {
	var out AppInfo
	if err := a.get(ctx, "/app", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Installation is GET /app/installations/{id}: the installation's account,
// granted permissions and suspension.
func (a *AppInspector) Installation(ctx context.Context) (*AppInstallation, error) {
	var out AppInstallation
	if err := a.get(ctx, fmt.Sprintf("/app/installations/%d", a.creds.InstallationID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *AppInspector) get(ctx context.Context, path string, into any) error {
	endpoint := "GET " + path
	if a == nil || a.creds == nil {
		return fmt.Errorf("%s: no GitHub App configured", endpoint)
	}
	key, err := parseAppPrivateKey(a.creds.PrivateKeyPEM)
	if err != nil {
		return err
	}
	jwt, err := appJWT(a.creds.AppID, key, a.now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.http.Do(req)
	if err != nil {
		// The transport error names the URL, never the header.
		return fmt.Errorf("%s for %s: %w", endpoint, a.creds, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		return &AppAPIError{Endpoint: endpoint, Status: resp.StatusCode, Message: e.Message}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%s: decode response: %w", endpoint, err)
	}
	return nil
}
