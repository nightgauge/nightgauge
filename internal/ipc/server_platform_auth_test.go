package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformapi "github.com/nightgauge/nightgauge/api/generated/go/platform"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// newTestPlatformServer creates a minimal IPC server with a platform client
// pointing at the given httptest.Server. Only the platform.auth* handlers are
// exercised; the gh.Client is nil (other handlers must not be called).
func newTestPlatformServer(t *testing.T, mockURL string) *Server {
	t.Helper()

	pc, err := platform.NewClient(platform.Config{
		BaseURL: mockURL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// NewServer(nil) is safe — registerMethods() creates gh services with
	// the nil client but never dereferences it during registration.
	s := NewServer(nil, WithPlatformClient(pc))
	s.writer = &bytes.Buffer{}
	return s
}

// callHandler invokes an IPC handler by method name with JSON-marshaled params.
func callHandler(t *testing.T, s *Server, method string, params interface{}) (interface{}, error) {
	t.Helper()

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw = b
	}

	handler, ok := s.methods[method]
	if !ok {
		t.Fatalf("method %q not registered", method)
	}
	return handler(context.Background(), raw)
}

// --- platform.authDeviceCode ---

func TestPlatformAuthDeviceCode_Success(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device-code" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"device_code":"abc123","expires_in":900,"interval":5,"user_code":"ABCD-EFGH","verification_uri":"https://example.com/device"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	result, err := callHandler(t, s, "platform.authDeviceCode", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got platformapi.AuthDeviceCodeResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.DeviceCode != "abc123" {
		t.Errorf("DeviceCode = %q, want %q", got.DeviceCode, "abc123")
	}
	if got.UserCode != "ABCD-EFGH" {
		t.Errorf("UserCode = %q, want %q", got.UserCode, "ABCD-EFGH")
	}
	if got.ExpiresIn != 900 {
		t.Errorf("ExpiresIn = %d, want 900", got.ExpiresIn)
	}
}

// TestPlatformAuthDeviceCode_BuildsClientOnDemand pins #2398: sign-in is an
// explicit account action, so a daemon with no platform client (platform.enabled
// off, so a stored license key gave it none at startup) builds one on demand,
// at the endpoint it was told, instead of answering "not configured".
func TestPlatformAuthDeviceCode_BuildsClientOnDemand(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device-code" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"device_code":"abc123","expires_in":900,"interval":5,"user_code":"ABCD-EFGH","verification_uri":"https://example.com/device"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := NewServer(nil, WithPlatformEndpoint(mock.URL))
	s.writer = &bytes.Buffer{}
	if s.getPlatformClient() != nil {
		t.Fatal("precondition: the server must start with no platform client")
	}

	result, err := callHandler(t, s, "platform.authDeviceCode", nil)
	if err != nil {
		t.Fatalf("authDeviceCode with no client: %v — an explicit sign-in must build one", err)
	}
	b, _ := json.Marshal(result)
	var got platformapi.AuthDeviceCodeResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.DeviceCode != "abc123" {
		t.Errorf("DeviceCode = %q, want abc123 from the configured endpoint", got.DeviceCode)
	}
	if s.getPlatformClient() == nil {
		t.Error("the client built for the sign-in was not attached")
	}
}

// TestPlatformValidateLicense_OnDemandClientIsOnline pins the other half of
// #2398's account actions: Activate License verifies a key with no client at
// startup. A client built on demand starts offline and validation answers
// "not valid" offline, so the handler must probe before validating.
func TestPlatformValidateLicense_OnDemandClientIsOnline(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/v1/license/validate":
			fmt.Fprint(w, `{"valid":true,"status":"active","tier":"pro","expiresAt":"2027-01-01T00:00:00Z","expiresSoon":false,"machineBound":false,"machineCount":1,"features":{"batchProcessing":true,"concurrentPipelines":3,"pipelineRunsPerDay":50,"pipelineRunsPerMonth":null,"skillResolveRatePerMin":60}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()

	s := NewServer(nil, WithPlatformEndpoint(mock.URL))
	s.writer = &bytes.Buffer{}

	result, err := callHandler(t, s, "platform.validateLicense", PlatformValidateLicenseParams{
		LicenseKey: "lic_entered_by_the_user",
		MachineID:  "machine-1",
	})
	if err != nil {
		t.Fatalf("validateLicense with no client: %v", err)
	}
	info, ok := result.(*platform.LicenseInfo)
	if !ok {
		t.Fatalf("result is %T, want *platform.LicenseInfo", result)
	}
	if !info.Valid || info.Tier != "pro" {
		t.Errorf("license = valid %v tier %q, want a valid pro license — the on-demand client validated while still offline", info.Valid, info.Tier)
	}
}

func TestPlatformAuthDeviceCode_Non200(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Return plain text to avoid oapi-codegen JSON parse errors.
		w.WriteHeader(500)
		fmt.Fprint(w, "internal server error")
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authDeviceCode", nil)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "authDeviceCode") {
		t.Fatalf("expected error prefixed with 'authDeviceCode', got: %v", err)
	}
}

// --- platform.authDeviceToken ---

func TestPlatformAuthDeviceToken_Success(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device-token" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"access_token":"at_xxx","refresh_token":"rt_xxx","expires_in":3600,"status":"authorized","token_type":"bearer"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	result, err := callHandler(t, s, "platform.authDeviceToken", PlatformAuthDeviceTokenParams{
		DeviceCode: "abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result type = %T, want map[string]interface{}", result)
	}
	if m["access_token"] != "at_xxx" {
		t.Errorf("access_token = %v, want %q", m["access_token"], "at_xxx")
	}
	if m["status"] != "authorized" {
		t.Errorf("status = %v, want %q", m["status"], "authorized")
	}
}

func TestPlatformAuthDeviceToken_Pending(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/device-token" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"status":"authorization_pending"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	result, err := callHandler(t, s, "platform.authDeviceToken", PlatformAuthDeviceTokenParams{
		DeviceCode: "abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result type = %T, want map[string]interface{}", result)
	}
	if m["status"] != "authorization_pending" {
		t.Errorf("status = %v, want %q", m["status"], "authorization_pending")
	}
}

func TestPlatformAuthDeviceToken_MissingDeviceCode(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authDeviceToken", PlatformAuthDeviceTokenParams{
		DeviceCode: "",
	})
	if err == nil || !strings.Contains(err.Error(), "deviceCode is required") {
		t.Fatalf("expected 'deviceCode is required', got: %v", err)
	}
}

func TestPlatformAuthDeviceToken_InvalidParams(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	handler := s.methods["platform.authDeviceToken"]
	_, err := handler(context.Background(), json.RawMessage(`{invalid json`))
	if err == nil || !strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("expected 'invalid params', got: %v", err)
	}
}

// --- platform.authGithub ---

func TestPlatformAuthGithub_Success(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/github" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"access_token":"at_gh","refresh_token":"rt_gh","expires_in":3600,"status":"authorized","token_type":"bearer"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	result, err := callHandler(t, s, "platform.authGithub", PlatformAuthGithubParams{
		GithubAccessToken: "gho_xxx",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got platformapi.AuthTokenResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.AccessToken != "at_gh" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "at_gh")
	}
}

func TestPlatformAuthGithub_MissingToken(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authGithub", PlatformAuthGithubParams{
		GithubAccessToken: "",
	})
	if err == nil || !strings.Contains(err.Error(), "githubAccessToken is required") {
		t.Fatalf("expected 'githubAccessToken is required', got: %v", err)
	}
}

func TestPlatformAuthGithub_Non200(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, "unauthorized")
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authGithub", PlatformAuthGithubParams{
		GithubAccessToken: "bad_token",
	})
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "authGithub") {
		t.Fatalf("expected error prefixed with 'authGithub', got: %v", err)
	}
}

// --- platform.authRefresh ---

func TestPlatformAuthRefresh_Success(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/token/refresh" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"access_token":"at_new","refresh_token":"rt_new","expires_in":3600,"status":"authorized","token_type":"bearer"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	result, err := callHandler(t, s, "platform.authRefresh", PlatformAuthRefreshParams{
		RefreshToken: "rt_old",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got platformapi.AuthTokenResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.AccessToken != "at_new" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "at_new")
	}
	if got.RefreshToken != "rt_new" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "rt_new")
	}
}

func TestPlatformAuthRefresh_MissingToken(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authRefresh", PlatformAuthRefreshParams{
		RefreshToken: "",
	})
	if err == nil || !strings.Contains(err.Error(), "refreshToken is required") {
		t.Fatalf("expected 'refreshToken is required', got: %v", err)
	}
}

func TestPlatformAuthRefresh_Non200(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, "unauthorized")
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authRefresh", PlatformAuthRefreshParams{
		RefreshToken: "bad_token",
	})
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "authRefresh") {
		t.Fatalf("expected error prefixed with 'authRefresh', got: %v", err)
	}
}

// --- platform.authSignout ---

func TestPlatformAuthSignout_Success(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/signout" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"status":"signed_out","message":"Successfully signed out"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	result, err := callHandler(t, s, "platform.authSignout", PlatformAuthSignoutParams{
		RefreshToken: "rt_xxx",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got platformapi.AuthSignoutResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.Status != "signed_out" {
		t.Errorf("Status = %q, want %q", got.Status, "signed_out")
	}
	if got.Message != "Successfully signed out" {
		t.Errorf("Message = %q, want %q", got.Message, "Successfully signed out")
	}
}

func TestPlatformAuthSignout_MissingToken(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authSignout", PlatformAuthSignoutParams{
		RefreshToken: "",
	})
	if err == nil || !strings.Contains(err.Error(), "refreshToken is required") {
		t.Fatalf("expected 'refreshToken is required', got: %v", err)
	}
}

func TestPlatformAuthSignout_Non200(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, "forbidden")
	}))
	defer mock.Close()

	s := newTestPlatformServer(t, mock.URL)
	_, err := callHandler(t, s, "platform.authSignout", PlatformAuthSignoutParams{
		RefreshToken: "bad_token",
	})
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
	if !strings.Contains(err.Error(), "authSignout") {
		t.Fatalf("expected error prefixed with 'authSignout', got: %v", err)
	}
}
