package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLicenseService_Validate_Online(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/license/validate":
			// Camelcase shape matching the platform's live contract (#4159).
			jsonResponse(w, map[string]interface{}{
				"valid":        true,
				"status":       "active",
				"tier":         "pro",
				"expiresAt":    time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
				"machineBound": false,
				"machineCount": 0,
				"features": map[string]interface{}{
					"batchProcessing":        true,
					"concurrentPipelines":    3,
					"pipelineRunsPerDay":     nil, // null = unlimited
					"pipelineRunsPerMonth":   nil,
					"skillResolveRatePerMin": 100,
				},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "IB-TEST-TEST-TEST-TEST"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !info.Valid {
		t.Error("license should be valid")
	}
	if info.Tier != "pro" {
		t.Errorf("tier = %s, want pro", info.Tier)
	}
	// The local pipeline is uncapped (#134): the platform's per-tier feature caps
	// (concurrentPipelines/skillResolveRatePerMin/pipelineRunsPerDay) are no longer
	// mapped onto LicenseInfo — only validity, tier, and status are consumed.
	// #4156: status/machineBound/machineCount/expiresAt were previously parsed
	// off the platform response and then dropped — never stored on LicenseInfo.
	if info.Status != LicenseStatusActive {
		t.Errorf("status = %q, want %q", info.Status, LicenseStatusActive)
	}
	if info.MachineBound {
		t.Error("machineBound should be false")
	}
	if info.MachineCount != 0 {
		t.Errorf("machineCount = %d, want 0", info.MachineCount)
	}
	if info.ExpiresAt == nil {
		t.Error("expiresAt should be populated")
	}
}

// TestLicenseService_Validate_Rejected4xx is the #4158 regression guard: when the
// platform AUTHORITATIVELY rejects the key (4xx), the client must report the
// license as INVALID rather than silently degrading to a valid community license
// (which is what a connectivity failure legitimately does).
func TestLicenseService_Validate_Rejected4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/license/validate" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"LICENSE_REVOKED","message":"revoked"}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "IB-TEST-REVK-OKED-XXXX"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if info.Valid {
		t.Error("a platform 4xx rejection must NOT yield Valid=true (silent-degradation regression)")
	}
	if info.Tier == "community" {
		t.Error("a rejected license must not be reported as a valid community license")
	}
	// #4156: the platform's error.code (LICENSE_REVOKED) must parse into a
	// confirmed status so the caller can fail closed even if the server later
	// becomes unreachable.
	if info.Status != LicenseStatusRevoked {
		t.Errorf("status = %q, want %q", info.Status, LicenseStatusRevoked)
	}
	// Must not be cached — a transient/buggy 4xx must not pin the result for the TTL.
	svc.mu.RLock()
	cached := svc.cached
	svc.mu.RUnlock()
	if cached != nil {
		t.Error("rejected license must not be cached")
	}
}

// TestLicenseService_Validate_Rejected4xx_ExpiredCode: LICENSE_EXPIRED maps to
// LicenseStatusExpired (sibling of the REVOKED case above, #4156).
func TestLicenseService_Validate_Rejected4xx_ExpiredCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/license/validate" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"LICENSE_EXPIRED","message":"expired"}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "IB-TEST-EXPI-RED0-XXXX"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if info.Status != LicenseStatusExpired {
		t.Errorf("status = %q, want %q", info.Status, LicenseStatusExpired)
	}
}

// TestLicenseService_Validate_Rejected4xx_UnknownCode: a 4xx with no
// recognizable license error code leaves Status "" (unknown) rather than
// guessing — Valid=false already blocks regardless.
func TestLicenseService_Validate_Rejected4xx_UnknownCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/license/validate" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMIT_EXCEEDED","message":"slow down"}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "IB-TEST-RATE-LIMT-XXXX"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if info.Valid {
		t.Error("a 4xx rejection must not yield Valid=true")
	}
	if info.Status != "" {
		t.Errorf("status = %q, want \"\" (unrecognized code must not be guessed)", info.Status)
	}
}

// TestLicenseService_CommunityInfo_NoExpiry: community tier has no expiry —
// ExpiresAt must be nil, mirroring the uncapped community contract
// (expiresAt: null). Pre-#4156 this field was a non-nil "+365 days" hack.
func TestLicenseService_CommunityInfo_NoExpiry(t *testing.T) {
	srv := httptest.NewServer(healthyHandler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL} // No license key → community
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ExpiresAt != nil {
		t.Errorf("community ExpiresAt = %v, want nil", info.ExpiresAt)
	}
	if info.Status != LicenseStatusActive {
		t.Errorf("community status = %q, want %q", info.Status, LicenseStatusActive)
	}
}

func TestLicenseService_Validate_Offline_Cached(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Leave mode as offline

	svc := NewLicenseService(c)

	// Pre-populate cache
	svc.cached = &LicenseInfo{
		Valid:    true,
		Tier:     "pro",
		CachedAt: time.Now().Add(-1 * time.Hour), // Within grace period
	}

	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Tier != "pro" {
		t.Errorf("offline cached tier = %s, want pro", info.Tier)
	}
}

func TestLicenseService_Validate_Offline_NoCacheGracePeriodExpired(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewLicenseService(c)

	// Stale cache beyond grace period
	svc.cached = &LicenseInfo{
		Valid:    true,
		Tier:     "pro",
		CachedAt: time.Now().Add(-8 * 24 * time.Hour), // Past 7-day grace
	}

	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Tier != "community" {
		t.Errorf("expired offline tier = %s, want community", info.Tier)
	}
}

func TestLicenseService_NoLicenseKey(t *testing.T) {
	srv := httptest.NewServer(healthyHandler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL} // No license key
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Tier != "community" {
		t.Errorf("no-key tier = %s, want community", info.Tier)
	}
}

// TestCommunityLicenseInfo asserts the uncapped community contract (#134): the
// free/local tier is always Valid → allowed, tier "community", no expiry, and
// carries no numeric feature caps (the whole LicenseFeatures notion was removed).
func TestCommunityLicenseInfo(t *testing.T) {
	info := CommunityLicenseInfo()
	if !info.Valid {
		t.Error("community license must be valid (allowed)")
	}
	if info.Tier != "community" {
		t.Errorf("tier = %q, want community", info.Tier)
	}
	if info.Status != LicenseStatusActive {
		t.Errorf("status = %q, want %q", info.Status, LicenseStatusActive)
	}
	if info.ExpiresAt != nil {
		t.Errorf("community ExpiresAt = %v, want nil (no expiry)", info.ExpiresAt)
	}
}

// TestLicenseService_ValidateKey_UsesPassedKeyAndDoesNotCache asserts the core
// "Activate License" contract: ValidateKey verifies the ENTERED key (not the
// configured session key), and never writes the session cache.
func TestLicenseService_ValidateKey_UsesPassedKeyAndDoesNotCache(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case "/v1/license/validate":
			var body struct {
				Key string `json:"key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotKey = body.Key
			jsonResponse(w, map[string]interface{}{
				"valid": true, "status": "active", "tier": "pro",
				"machineBound": false, "machineCount": 0,
				"features": map[string]interface{}{
					"concurrentPipelines":    2,
					"pipelineRunsPerDay":     nil,
					"skillResolveRatePerMin": 60,
				},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "CONFIGURED-SESSION-KEY"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewLicenseService(c)

	info, err := svc.ValidateKey(context.Background(), "ENTERED-KEY-TO-CHECK")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Valid || info.Tier != "pro" {
		t.Errorf("want valid pro, got valid=%v tier=%s", info.Valid, info.Tier)
	}
	if gotKey != "ENTERED-KEY-TO-CHECK" {
		t.Errorf("platform received key %q, want the entered key (not the session key)", gotKey)
	}
	svc.mu.RLock()
	cached := svc.cached
	svc.mu.RUnlock()
	if cached != nil {
		t.Error("ValidateKey must not write the session cache")
	}
	if svc.ConfiguredKey() != "CONFIGURED-SESSION-KEY" {
		t.Errorf("ConfiguredKey() = %q, want the unchanged session key", svc.ConfiguredKey())
	}
}

// TestLicenseService_ValidateKey_Rejected4xx: a platform 4xx on an entered key is
// surfaced as invalid (so the UI reports "not accepted"), not silently degraded.
func TestLicenseService_ValidateKey_Rejected4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case "/v1/license/validate":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"LICENSE_REVOKED","message":"revoked"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "SESSION-KEY"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewLicenseService(c)

	info, err := svc.ValidateKey(context.Background(), "BAD-ENTERED-KEY")
	if err != nil {
		t.Fatal(err)
	}
	if info.Valid {
		t.Error("a 4xx-rejected entered key must yield Valid=false")
	}
}

// TestLicenseService_ValidateKey_EmptyInvalid: an empty key is rejected without
// any network call (no server needed).
func TestLicenseService_ValidateKey_EmptyInvalid(t *testing.T) {
	cfg := Config{BaseURL: "http://127.0.0.1:0", LicenseKey: "SESSION-KEY"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLicenseService(c)
	info, err := svc.ValidateKey(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Valid {
		t.Error("empty key must be invalid")
	}
}

// TestLicenseService_StartTrial_Success asserts the core trial contract: the
// device-flow JWT (NOT the session license key) is sent as the bearer, and the
// 201 body maps into TrialResult.
func TestLicenseService_StartTrial_Success(t *testing.T) {
	var gotAuth string
	expiry := time.Now().Add(14 * 24 * time.Hour).UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case "/v1/license/trial":
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"license_key":   "IB-TRIAL-AAAA-BBBB-CCCC",
				"tier":          "pro",
				"trial":         true,
				"expires_at":    expiry.Format(time.RFC3339),
				"run_allowance": 50,
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "SESSION-LICENSE-KEY"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewLicenseService(c)

	res, err := svc.StartTrial(context.Background(), "JWT-ACCESS-TOKEN")
	if err != nil {
		t.Fatalf("StartTrial returned error: %v", err)
	}
	if gotAuth != "Bearer JWT-ACCESS-TOKEN" {
		t.Errorf("trial request Authorization = %q, want the device-flow JWT (not the session license key)", gotAuth)
	}
	if res.LicenseKey != "IB-TRIAL-AAAA-BBBB-CCCC" {
		t.Errorf("license key = %q", res.LicenseKey)
	}
	if res.Tier != "pro" || !res.Trial || res.RunAllowance != 50 {
		t.Errorf("unexpected result: %+v", res)
	}
	if !res.ExpiresAt.Equal(expiry) {
		t.Errorf("expiresAt = %v, want %v", res.ExpiresAt, expiry)
	}
}

// TestLicenseService_StartTrial_NotEligible: the once-per-account 409 maps to a
// typed TrialError(NOT_ELIGIBLE) so the UI can show a precise message.
func TestLicenseService_StartTrial_NotEligible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case "/v1/license/trial":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"TRIAL_NOT_ELIGIBLE","message":"already has a license"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, LicenseKey: "SESSION-KEY"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewLicenseService(c)

	_, err = svc.StartTrial(context.Background(), "JWT")
	te, ok := err.(*TrialError)
	if !ok {
		t.Fatalf("want *TrialError, got %T (%v)", err, err)
	}
	if te.Code != TrialNotEligible {
		t.Errorf("code = %q, want NOT_ELIGIBLE", te.Code)
	}
}

// TestLicenseService_StartTrial_EmptyToken: no token short-circuits to a typed
// UNAUTHORIZED error without any network call.
func TestLicenseService_StartTrial_EmptyToken(t *testing.T) {
	cfg := Config{BaseURL: "http://127.0.0.1:0", LicenseKey: "SESSION-KEY"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLicenseService(c)

	_, err = svc.StartTrial(context.Background(), "")
	te, ok := err.(*TrialError)
	if !ok {
		t.Fatalf("want *TrialError, got %T", err)
	}
	if te.Code != TrialUnauthorized {
		t.Errorf("code = %q, want UNAUTHORIZED", te.Code)
	}
}
