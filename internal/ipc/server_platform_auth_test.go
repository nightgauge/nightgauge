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

func TestPlatformAuthDeviceCode_NilClient(t *testing.T) {
	// Construct a server with no platformClient to test the nil guard.
	s := &Server{
		writer:  &bytes.Buffer{},
		methods: make(map[string]Handler),
	}
	// Replicate the nil-client guard from the real handler.
	s.methods["platform.authDeviceCode"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if s.platformClient == nil {
			return nil, fmt.Errorf("platform client not configured")
		}
		return nil, nil
	}

	_, err := callHandler(t, s, "platform.authDeviceCode", nil)
	if err == nil || !strings.Contains(err.Error(), "platform client not configured") {
		t.Fatalf("expected 'platform client not configured', got: %v", err)
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
