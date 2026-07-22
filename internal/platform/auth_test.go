package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthService_ExchangeGitHubToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/github" && r.Method == "POST":
			jsonResponse(w, map[string]interface{}{
				"access_token":  "platform-jwt-token",
				"refresh_token": "platform-refresh-token",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"status":        "authorized",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, APIKey: "test-key"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	resp, err := svc.ExchangeGitHubToken(context.Background(), "gh-token-123")
	if err != nil {
		t.Fatal(err)
	}

	if resp.AccessToken != "platform-jwt-token" {
		t.Errorf("access_token = %s, want platform-jwt-token", resp.AccessToken)
	}
	if resp.RefreshToken != "platform-refresh-token" {
		t.Errorf("refresh_token = %s, want platform-refresh-token", resp.RefreshToken)
	}
	if resp.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want 3600", resp.ExpiresIn)
	}
}

func TestAuthService_ExchangeGitHubToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/github":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			jsonResponse(w, map[string]interface{}{
				"error":   "unauthorized",
				"message": "Invalid GitHub token",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	_, err = svc.ExchangeGitHubToken(context.Background(), "bad-token")
	if err == nil {
		t.Fatal("expected error for unauthorized token")
	}
}

func TestAuthService_StartDeviceFlow_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/device-code" && r.Method == "POST":
			jsonResponse(w, map[string]interface{}{
				"device_code":      "dev-code-123",
				"user_code":        "ABCD-1234",
				"verification_uri": "https://example.com/device",
				"expires_in":       900,
				"interval":         5,
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	resp, err := svc.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if resp.DeviceCode != "dev-code-123" {
		t.Errorf("device_code = %s, want dev-code-123", resp.DeviceCode)
	}
	if resp.UserCode != "ABCD-1234" {
		t.Errorf("user_code = %s, want ABCD-1234", resp.UserCode)
	}
	if resp.Interval != 5 {
		t.Errorf("interval = %d, want 5", resp.Interval)
	}
}

func TestAuthService_PollDeviceToken_Pending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/device-token" && r.Method == "POST":
			jsonResponse(w, map[string]interface{}{
				"status": "authorization_pending",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	tokenResp, pendingResp, err := svc.PollDeviceToken(context.Background(), "dev-code-123")
	if err != nil {
		t.Fatal(err)
	}
	if tokenResp != nil {
		t.Error("expected nil tokenResp for pending state")
	}
	if pendingResp == nil {
		t.Fatal("expected non-nil pendingResp")
	}
	if string(pendingResp.Status) != "authorization_pending" {
		t.Errorf("status = %s, want authorization_pending", pendingResp.Status)
	}
}

func TestAuthService_PollDeviceToken_Authorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/device-token" && r.Method == "POST":
			jsonResponse(w, map[string]interface{}{
				"access_token":  "device-jwt",
				"refresh_token": "device-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"status":        "authorized",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	tokenResp, pendingResp, err := svc.PollDeviceToken(context.Background(), "dev-code-123")
	if err != nil {
		t.Fatal(err)
	}
	if pendingResp != nil {
		t.Error("expected nil pendingResp for authorized state")
	}
	if tokenResp == nil {
		t.Fatal("expected non-nil tokenResp")
	}
	if tokenResp.AccessToken != "device-jwt" {
		t.Errorf("access_token = %s, want device-jwt", tokenResp.AccessToken)
	}
}

func TestAuthService_RefreshToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/token/refresh" && r.Method == "POST":
			jsonResponse(w, map[string]interface{}{
				"access_token":  "new-jwt",
				"refresh_token": "new-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"status":        "authorized",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	resp, err := svc.RefreshToken(context.Background(), "old-refresh-token")
	if err != nil {
		t.Fatal(err)
	}

	if resp.AccessToken != "new-jwt" {
		t.Errorf("access_token = %s, want new-jwt", resp.AccessToken)
	}
	if resp.RefreshToken != "new-refresh" {
		t.Errorf("refresh_token = %s, want new-refresh", resp.RefreshToken)
	}
}

func TestAuthService_RefreshToken_Expired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/auth/token/refresh":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			jsonResponse(w, map[string]interface{}{
				"error":   "unauthorized",
				"message": "Refresh token expired",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAuthService(c)
	_, err = svc.RefreshToken(context.Background(), "expired-refresh")
	if err == nil {
		t.Fatal("expected error for expired refresh token")
	}
}
