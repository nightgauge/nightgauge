package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// License status values — mirrors TS LicenseStatus in
// packages/nightgauge-vscode/src/platform/types.ts. Kept as plain
// strings (not a Go-side enum type) so they round-trip through JSON and the
// IPC layer without conversion.
const (
	LicenseStatusActive    = "active"
	LicenseStatusExpired   = "expired"
	LicenseStatusRevoked   = "revoked"
	LicenseStatusSuspended = "suspended"
)

// LicenseInfo holds cached license validation results.
//
// JSON tags are REQUIRED here (#4156): this struct is serialized directly
// over the Go↔TS stdio IPC channel (internal/ipc/server.go's "platform.license"
// / "platform.validateLicense" handlers return it as-is, encoded by plain
// encoding/json with no case conversion). Before #4156 these fields had no
// tags at all, so the wire format was PascalCase ("Tier", "Valid", ...) while
// every TS consumer (LicensePreflight, activateLicense) read lowercase
// camelCase fields — meaning `.valid`/`.tier` were silently `undefined` at
// runtime. Tags below fix the wire format AND add the fields the platform
// already sends (status/machineBound/machineCount, #4159) that were
// previously read from the HTTP response and then dropped on the floor.
type LicenseInfo struct {
	Valid bool   `json:"valid"`
	Tier  string `json:"tier"`
	// Status is one of LicenseStatusActive/Expired/Revoked/Suspended, or ""
	// when unknown (e.g. a generic 4xx with no parseable error code).
	Status       string     `json:"status,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	ExpiresSoon  bool       `json:"expiresSoon,omitempty"`
	MachineBound bool       `json:"machineBound,omitempty"`
	MachineCount int        `json:"machineCount,omitempty"`
	// CachedAt is internal cache bookkeeping — never exposed over the wire.
	CachedAt time.Time `json:"-"`
}

// CommunityLicenseInfo returns the free-tier license info used when no license
// key is configured or the platform is unreachable. The local pipeline is
// uncapped (see VISION.md §7): community is always Valid → allowed, with no
// feature caps or numeric limits. Community tier has no expiry (ExpiresAt stays
// nil).
func CommunityLicenseInfo() *LicenseInfo {
	return &LicenseInfo{
		Valid:    true,
		Tier:     "community",
		Status:   LicenseStatusActive,
		CachedAt: time.Now(),
	}
}

const (
	licenseCacheTTL    = 24 * time.Hour
	licenseGracePeriod = 7 * 24 * time.Hour // 7-day grace after expiry
)

// LicenseService validates and caches license information.
type LicenseService struct {
	client *Client
	mu     sync.RWMutex
	cached *LicenseInfo
}

// NewLicenseService creates a license validation service.
func NewLicenseService(client *Client) *LicenseService {
	return &LicenseService{client: client}
}

// Validate checks the license, returning cached data if fresh.
func (s *LicenseService) Validate(ctx context.Context) (*LicenseInfo, error) {
	// Check cache first
	s.mu.RLock()
	if s.cached != nil && s.isCacheValid() {
		info := *s.cached
		s.mu.RUnlock()
		return &info, nil
	}
	s.mu.RUnlock()

	// If offline, use cached (even if stale) or fall back to community
	if !s.client.IsOnline() {
		return s.offlineFallback(), nil
	}

	// Validate with platform
	licenseKey := s.client.licenseKey
	if licenseKey == "" {
		return s.communityInfo(), nil
	}

	resp, err := s.client.api.LicenseValidateWithResponse(ctx, api.LicenseValidateJSONRequestBody{
		Key: licenseKey,
	})
	if err != nil {
		// Genuine transport/connectivity failure → grace fallback.
		return s.offlineFallback(), nil
	}
	// (#4158) A 4xx is the platform AUTHORITATIVELY rejecting the license
	// (invalid / expired / revoked / over machine-limit). It must NOT be masked
	// as a valid community license the way a connectivity failure is — surface it
	// as invalid so enforcement can block instead of silently degrading.
	if resp.HTTPResponse != nil {
		if code := resp.HTTPResponse.StatusCode; code >= 400 && code < 500 {
			return s.rejectedInfo(code, resp.Body), nil
		}
	}
	if resp.JSON200 == nil {
		// 5xx / unexpected (platform trouble, not a rejection) → grace fallback.
		return s.offlineFallback(), nil
	}

	info := licenseInfoFromBody(resp.JSON200)
	info.CachedAt = time.Now()

	s.mu.Lock()
	s.cached = info
	s.mu.Unlock()

	return info, nil
}

// licenseInfoFromBody maps the platform's LicenseValidateBody (#4159 —
// camelCase valid/status/tier/expiresAt/expiresSoon/machineBound/machineCount)
// onto the internal LicenseInfo shape. Status defaults to "active" when the
// platform reports Valid=true without an explicit status (older platform
// versions), and is left "" (unknown) when Valid=false without one — the
// caller still enforces on Valid, it just can't distinguish WHY. The platform's
// per-tier feature caps are intentionally NOT mapped: the local pipeline is
// uncapped (VISION.md §7) and enforces no numeric limits.
func licenseInfoFromBody(body *api.LicenseValidateBody) *LicenseInfo {
	info := &LicenseInfo{
		Valid: body.Valid,
		Tier:  string(body.Tier),
	}
	if body.Status != nil {
		info.Status = *body.Status
	} else if body.Valid {
		info.Status = LicenseStatusActive
	}
	if body.ExpiresAt != nil {
		expiresAt := *body.ExpiresAt
		info.ExpiresAt = &expiresAt
	}
	if body.ExpiresSoon != nil {
		info.ExpiresSoon = *body.ExpiresSoon
	}
	if body.MachineBound != nil {
		info.MachineBound = *body.MachineBound
	}
	if body.MachineCount != nil {
		info.MachineCount = *body.MachineCount
	}
	return info
}

// ValidateKey validates an ARBITRARY license key against the platform without
// touching the session cache or the configured session key. It backs the
// extension's "Activate License" flow: the user enters a key, we verify it with
// the platform BEFORE persisting it. Unlike Validate(), it never reads or writes
// s.cached and never substitutes the community key — an empty key, a 4xx, or an
// offline/transport failure all report Valid=false so the UI can tell the user
// the key wasn't accepted (rather than silently degrading to community).
func (s *LicenseService) ValidateKey(ctx context.Context, key string) (*LicenseInfo, error) {
	if key == "" {
		return s.rejectedInfo(401, nil), nil
	}
	// Can't verify offline — report not-valid so the UI prompts a retry instead
	// of accepting an unverified key.
	if !s.client.IsOnline() {
		return invalidInfo(), nil
	}

	resp, err := s.client.api.LicenseValidateWithResponse(ctx, api.LicenseValidateJSONRequestBody{
		Key: key,
	})
	if err != nil {
		return invalidInfo(), nil
	}
	if resp.HTTPResponse != nil {
		if code := resp.HTTPResponse.StatusCode; code >= 400 && code < 500 {
			return s.rejectedInfo(code, resp.Body), nil
		}
	}
	if resp.JSON200 == nil {
		return invalidInfo(), nil
	}

	info := licenseInfoFromBody(resp.JSON200)
	info.CachedAt = time.Now()
	return info, nil
}

// ConfiguredKey returns the license key the client was constructed with (the
// session key), or "" when none is configured.
func (s *LicenseService) ConfiguredKey() string {
	return s.client.licenseKey
}

// invalidInfo is a non-cached "not valid" result for ValidateKey when the key
// could not be affirmatively verified (offline / transport / unexpected). It is
// deliberately distinct from communityInfo (which is a VALID community license).
// Status is left "" — the caller couldn't reach the platform, so there is no
// confirmed status to report (distinct from rejectedInfo's parsed codes).
func invalidInfo() *LicenseInfo {
	return &LicenseInfo{Valid: false, Tier: "", CachedAt: time.Now()}
}

// TrialResult is the outcome of starting a free trial — the issued key plus the
// trial terms. Returned over IPC to the extension's "Start Free Trial" command;
// camelCase json tags drive the generated TS client field names.
type TrialResult struct {
	LicenseKey   string    `json:"licenseKey"`
	Tier         string    `json:"tier"`
	Trial        bool      `json:"trial"`
	ExpiresAt    time.Time `json:"expiresAt"`
	RunAllowance int       `json:"runAllowance"`
}

// TrialErrorCode classifies a StartTrial failure so the command layer can show a
// precise message instead of a generic error.
type TrialErrorCode string

const (
	// TrialNotEligible — the account already holds a license (the once-per-account 409).
	TrialNotEligible TrialErrorCode = "NOT_ELIGIBLE"
	// TrialUnauthorized — missing/expired device-flow token (401).
	TrialUnauthorized TrialErrorCode = "UNAUTHORIZED"
	// TrialUnavailable — transport failure or unexpected status.
	TrialUnavailable TrialErrorCode = "UNAVAILABLE"
)

// TrialError is a typed StartTrial failure carrying a TrialErrorCode.
type TrialError struct {
	Code    TrialErrorCode
	Message string
}

func (e *TrialError) Error() string { return e.Message }

// StartTrial issues a 14-day Pro free trial for the signed-in user. The trial
// endpoint is JWT-only (device-flow auth), so the caller passes a fresh access
// token; it is applied as a PER-CALL bearer override, leaving the client's
// session bearer (the license key) untouched for every other call.
func (s *LicenseService) StartTrial(ctx context.Context, accessToken string) (*TrialResult, error) {
	if accessToken == "" {
		return nil, &TrialError{Code: TrialUnauthorized, Message: "sign in required to start a trial"}
	}

	resp, err := s.client.api.LicenseTrialWithResponse(ctx, bearerEditor(accessToken))
	if err != nil {
		return nil, &TrialError{Code: TrialUnavailable, Message: fmt.Sprintf("trial request failed: %v", err)}
	}

	if resp.JSON201 != nil {
		b := resp.JSON201
		return &TrialResult{
			LicenseKey:   b.LicenseKey,
			Tier:         string(b.Tier),
			Trial:        b.Trial,
			ExpiresAt:    b.ExpiresAt,
			RunAllowance: b.RunAllowance,
		}, nil
	}

	code := 0
	if resp.HTTPResponse != nil {
		code = resp.HTTPResponse.StatusCode
	}
	switch code {
	case http.StatusUnauthorized:
		return nil, &TrialError{Code: TrialUnauthorized, Message: "your session has expired — sign in again"}
	case http.StatusConflict:
		return nil, &TrialError{Code: TrialNotEligible, Message: "this account already has a license and is not eligible for a free trial"}
	default:
		return nil, &TrialError{Code: TrialUnavailable, Message: fmt.Sprintf("trial request returned HTTP %d", code)}
	}
}

// bearerEditor returns a per-request editor that sets the Authorization bearer,
// overriding the client's default (session) credential for a single call.
func bearerEditor(token string) api.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// CurrentTier returns the current license tier (from cache or community default).
func (s *LicenseService) CurrentTier() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cached != nil && s.cached.Valid {
		return s.cached.Tier
	}
	return "community"
}

// isCacheValid checks if the cached license is within TTL.
func (s *LicenseService) isCacheValid() bool {
	if s.cached == nil {
		return false
	}
	return time.Since(s.cached.CachedAt) < licenseCacheTTL
}

// offlineFallback returns cached license (within grace period) or community tier.
func (s *LicenseService) offlineFallback() *LicenseInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.cached != nil && time.Since(s.cached.CachedAt) < licenseGracePeriod {
		info := *s.cached
		return &info
	}
	return s.communityInfo()
}

// communityInfo returns a free-tier license info (always allowed, uncapped).
func (s *LicenseService) communityInfo() *LicenseInfo {
	return CommunityLicenseInfo()
}

// licenseErrorBody is the generic {"error":{"code":...}} shape the platform
// uses for 4xx license-validate rejections (matches TS ApiLicenseErrorCode in
// platform/types.ts: LICENSE_EXPIRED / LICENSE_REVOKED / LICENSE_INVALID /
// LICENSE_TIER_EXCEEDED / LICENSE_MACHINE_LIMIT).
type licenseErrorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// statusFromErrorCode maps a platform ApiLicenseErrorCode to a LicenseStatus.
// Only REVOKED/EXPIRED map to a specific status — the others (INVALID,
// TIER_EXCEEDED, MACHINE_LIMIT) aren't lifecycle states of the license itself,
// so they're left "" (unknown); Valid=false already blocks regardless.
func statusFromErrorCode(code string) string {
	switch code {
	case "LICENSE_REVOKED":
		return LicenseStatusRevoked
	case "LICENSE_EXPIRED":
		return LicenseStatusExpired
	default:
		return ""
	}
}

// rejectedInfo is returned when the platform AUTHORITATIVELY rejects the license
// with a 4xx (invalid / expired / revoked / over-limit). It reports the license
// as NOT valid so enforcement blocks, rather than silently degrading to a valid
// community license the way a connectivity failure does (#4158). It is
// deliberately NOT cached — a transient/buggy 4xx must not pin the result for the
// cache TTL — and is distinct from offlineFallback (grace) which applies only to
// genuine connectivity failures (err != nil / 5xx).
//
// body is the raw response body (when available) — parsed for a
// license-specific error code (#4156) so the caller can distinguish a
// CONFIRMED revoked/suspended license (which must fail closed even when the
// server later becomes unreachable) from a generic validation/rate-limit 4xx.
func (s *LicenseService) rejectedInfo(statusCode int, body []byte) *LicenseInfo {
	fmt.Fprintf(os.Stderr, "Warning: platform rejected license (HTTP %d) — treating as invalid, not community\n", statusCode)
	status := ""
	if len(body) > 0 {
		var parsed licenseErrorBody
		if err := json.Unmarshal(body, &parsed); err == nil {
			status = statusFromErrorCode(parsed.Error.Code)
		}
	}
	return &LicenseInfo{
		Valid:    false,
		Tier:     "",
		Status:   status,
		CachedAt: time.Now(),
	}
}

// String returns a human-readable summary.
func (info *LicenseInfo) String() string {
	if !info.Valid {
		return "invalid license"
	}
	expiry := "no expiry"
	if info.ExpiresAt != nil {
		expiry = "expires " + info.ExpiresAt.Format("2006-01-02")
	}
	return fmt.Sprintf("%s tier (%s)", info.Tier, expiry)
}
