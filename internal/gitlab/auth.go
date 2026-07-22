package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// gitlabRequiredScopes are the PAT scopes needed for pipeline operations.
var gitlabRequiredScopes = []string{"api", "read_repository", "read_user"}

// authMethod identifies which credential type was resolved.
type authMethod string

const (
	authMethodPAT         authMethod = "pat"
	authMethodOAuth2      authMethod = "oauth2"
	authMethodCIJobToken  authMethod = "ci_job_token"
	authMethodDeployToken authMethod = "deploy_token"
)

// AuthAdapter implements forge.AuthService for GitLab. It wraps a *Client
// and dispatches CheckTokenScopes / Whoami per the resolved auth method.
type AuthAdapter struct {
	client     *Client
	method     authMethod
	deployUser string // username component for deploy-token Basic auth
}

// NewAuthAdapter constructs an AuthAdapter reading the resolved method from the
// client (set at construction time by NewClientFromConfig or WithResolvedMethod).
func NewAuthAdapter(client *Client) *AuthAdapter {
	m := client.resolvedMethod
	if m == "" {
		m = authMethodPAT
	}
	return &AuthAdapter{
		client:     client,
		method:     m,
		deployUser: client.deployUser,
	}
}

// ciJobTokenAvailable returns true when the environment indicates a GitLab CI
// context with an available job token.
func ciJobTokenAvailable() bool {
	return os.Getenv("CI") == "true" && os.Getenv("CI_JOB_TOKEN") != ""
}

// CheckTokenScopes validates the resolved credential has sufficient access for
// pipeline operations. The validation endpoint differs by auth method:
//   - PAT: GET /api/v4/personal_access_tokens/self
//   - OAuth2: GET /oauth/token/info
//   - CI job token: GET /api/v4/user (implicit project-level access)
//   - Deploy token: GET /api/v4/user (or fallback synthetic scopes)
func (a *AuthAdapter) CheckTokenScopes(ctx context.Context) (*forgetypes.TokenScopeInfo, error) {
	switch a.method {
	case authMethodOAuth2:
		return a.checkOAuth2Scopes(ctx)
	case authMethodCIJobToken:
		return a.checkCIJobTokenScopes(ctx)
	case authMethodDeployToken:
		return a.checkDeployTokenScopes(ctx)
	default:
		return a.checkPATScopes(ctx)
	}
}

// Whoami returns the actor associated with the active credential.
func (a *AuthAdapter) Whoami(ctx context.Context) (*forgetypes.Actor, error) {
	switch a.method {
	case authMethodOAuth2:
		return a.doWhoami(ctx, func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+a.client.token)
		})
	case authMethodCIJobToken:
		token := a.client.token
		if token == "" {
			token = os.Getenv("CI_JOB_TOKEN")
		}
		return a.doWhoami(ctx, func(req *http.Request) {
			req.Header.Set("JOB-TOKEN", token)
		})
	case authMethodDeployToken:
		encoded := base64.StdEncoding.EncodeToString(
			[]byte(a.deployUser + ":" + a.client.token),
		)
		return a.doWhoami(ctx, func(req *http.Request) {
			req.Header.Set("Authorization", "Basic "+encoded)
		})
	default:
		return a.doWhoami(ctx, func(req *http.Request) {
			req.Header.Set("PRIVATE-TOKEN", a.client.token)
		})
	}
}

// AuthStatus summarizes the active credential state.
type AuthStatus struct {
	Method      authMethod
	MaskedToken string
	Scopes      []string
	ExpiresAt   *time.Time
}

// Status returns the current authentication status with the token masked.
// It calls CheckTokenScopes internally to populate the scope list.
func (a *AuthAdapter) Status(ctx context.Context) (*AuthStatus, error) {
	info, err := a.CheckTokenScopes(ctx)
	if err != nil {
		return nil, err
	}
	return &AuthStatus{
		Method:      a.method,
		MaskedToken: maskToken(a.client.token),
		Scopes:      info.Scopes,
	}, nil
}

// Login initiates an OAuth2 device-code flow and stores the resulting tokens.
// VSCode IPC wiring for this flow is tracked in W4-3; the method is
// implemented here as the server-side but returns ErrUnsupported until wired.
func (a *AuthAdapter) Login(_ context.Context, _ string) error {
	return fmt.Errorf("gitlab: OAuth2 device-code Login not yet wired to IPC (tracked: W4-3)")
}

// RefreshOAuth2Token obtains a new access token via the stored refresh token.
// The full implementation awaits the W4-3 IPC wiring.
func (a *AuthAdapter) RefreshOAuth2Token(_ context.Context) error {
	if a.method != authMethodOAuth2 {
		return fmt.Errorf("gitlab: RefreshOAuth2Token called on non-OAuth2 adapter (method=%s)", a.method)
	}
	return fmt.Errorf("gitlab: OAuth2 token refresh not yet wired to IPC (tracked: W4-3)")
}

// --- PAT scope checking ---

type patSelfResponse struct {
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

func (a *AuthAdapter) checkPATScopes(ctx context.Context) (*forgetypes.TokenScopeInfo, error) {
	endpoint := a.client.baseURL + "/api/v4/personal_access_tokens/self"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: build PAT request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", a.client.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.client.userAgent)

	resp, err := a.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: PAT scope check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("gitlab auth: PAT is expired or revoked (HTTP 401)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab auth: PAT scope check returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: read PAT response: %w", err)
	}
	var pat patSelfResponse
	if err := json.Unmarshal(body, &pat); err != nil {
		return nil, fmt.Errorf("gitlab auth: decode PAT response: %w", err)
	}

	missing := computeMissingGitLabScopes(pat.Scopes, gitlabRequiredScopes)

	login := ""
	if actor, err := a.Whoami(ctx); err == nil {
		login = actor.Login
	}

	return &forgetypes.TokenScopeInfo{
		Scopes:        pat.Scopes,
		Login:         login,
		Resolution:    "pat",
		MissingScopes: missing,
		Valid:         len(missing) == 0,
	}, nil
}

// --- OAuth2 scope checking ---

type oauth2TokenInfoResponse struct {
	Scope string `json:"scope"`
}

func (a *AuthAdapter) checkOAuth2Scopes(ctx context.Context) (*forgetypes.TokenScopeInfo, error) {
	endpoint := a.client.baseURL + "/oauth/token/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: build OAuth2 request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.client.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.client.userAgent)

	resp, err := a.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: OAuth2 scope check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("gitlab auth: OAuth2 token is expired or revoked (HTTP 401)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab auth: OAuth2 scope check returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: read OAuth2 response: %w", err)
	}
	var info oauth2TokenInfoResponse
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("gitlab auth: decode OAuth2 response: %w", err)
	}

	// OAuth2 token/info returns space-separated scope string.
	scopes := strings.Fields(info.Scope)
	missing := computeMissingGitLabScopes(scopes, gitlabRequiredScopes)

	login := ""
	if actor, err := a.Whoami(ctx); err == nil {
		login = actor.Login
	}

	return &forgetypes.TokenScopeInfo{
		Scopes:        scopes,
		Login:         login,
		Resolution:    "oauth2",
		MissingScopes: missing,
		Valid:         len(missing) == 0,
	}, nil
}

// --- CI job token scope checking ---

func (a *AuthAdapter) checkCIJobTokenScopes(ctx context.Context) (*forgetypes.TokenScopeInfo, error) {
	if !ciJobTokenAvailable() {
		return nil, fmt.Errorf("gitlab auth: ci_job_token requires CI=true and CI_JOB_TOKEN to be set")
	}

	// CI job tokens have implicit project-level access; verify reachability via /api/v4/user.
	actor, err := a.Whoami(ctx)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: CI job token validation: %w", err)
	}

	// CI job tokens have no named scopes; use a synthetic marker.
	scopes := []string{"ci_job_token"}
	return &forgetypes.TokenScopeInfo{
		Scopes:        scopes,
		Login:         actor.Login,
		Resolution:    "ci_job_token",
		MissingScopes: nil,
		Valid:         true,
	}, nil
}

// --- Deploy token scope checking ---

func (a *AuthAdapter) checkDeployTokenScopes(ctx context.Context) (*forgetypes.TokenScopeInfo, error) {
	// Deploy tokens may return 403 for /api/v4/user — treat that as valid
	// with the deploy_token synthetic scope.
	login := a.deployUser
	actor, err := a.Whoami(ctx)
	if err == nil {
		login = actor.Login
	}

	scopes := []string{"deploy_token"}
	return &forgetypes.TokenScopeInfo{
		Scopes:        scopes,
		Login:         login,
		Resolution:    "deploy_token",
		MissingScopes: nil,
		Valid:         true,
	}, nil
}

// --- Shared Whoami helper ---

// doWhoami issues GET /api/v4/user with the auth header set by setAuth.
func (a *AuthAdapter) doWhoami(ctx context.Context, setAuth func(*http.Request)) (*forgetypes.Actor, error) {
	endpoint := a.client.baseURL + "/api/v4/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: build whoami request: %w", err)
	}
	setAuth(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.client.userAgent)

	resp, err := a.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: whoami: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("gitlab auth: credential is expired or revoked (HTTP 401)")
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("gitlab auth: credential lacks /api/v4/user access (HTTP 403)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab auth: whoami returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gitlab auth: read whoami response: %w", err)
	}

	var u struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("gitlab auth: decode whoami response: %w", err)
	}
	return &forgetypes.Actor{Login: u.Username}, nil
}

// --- Helpers ---

// computeMissingGitLabScopes returns required scopes absent from actual.
func computeMissingGitLabScopes(actual, required []string) []string {
	have := make(map[string]bool, len(actual))
	for _, s := range actual {
		have[s] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// maskToken returns the token with all but the last 4 characters replaced with *.
func maskToken(token string) string {
	if len(token) <= 4 {
		return strings.Repeat("*", len(token))
	}
	return strings.Repeat("*", len(token)-4) + token[len(token)-4:]
}

// instanceHost extracts the hostname from the client's base URL.
// Used to derive keyring keys for per-instance OAuth2 token storage.
func (a *AuthAdapter) instanceHost() string {
	u := a.client.baseURL
	if idx := strings.Index(u, "://"); idx >= 0 {
		u = u[idx+3:]
	}
	if idx := strings.IndexByte(u, '/'); idx >= 0 {
		u = u[:idx]
	}
	return u
}
