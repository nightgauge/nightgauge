package ipc

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// newIpcTestHarnessWithEnv creates a test harness with additional environment
// variables injected into the subprocess. This enables platform integration
// tests to point the binary at a mock platform server.
func newIpcTestHarnessWithEnv(t *testing.T, extraEnv []string) *ipcTestHarness {
	t.Helper()

	workDir := t.TempDir()
	configDir := workDir + "/.nightgauge"
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(configDir+"/config.yaml", []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binaryPath, "serve", "--workspace", workDir)
	env := append(os.Environ(), "GITHUB_TOKEN=fake-token-for-integration-test")
	env = append(env, extraEnv...)
	cmd.Env = env

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	h := &ipcTestHarness{
		t:      t,
		cmd:    cmd,
		stdin:  stdinPipe,
		lines:  make(chan string, 64),
		nextID: 1,
	}

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			h.lines <- scanner.Text()
		}
		close(h.lines)
	}()

	t.Cleanup(func() {
		stdinPipe.Close()
		if cmd.Process != nil {
			cmd.Process.Signal(os.Interrupt)
			cmd.Wait()
		}
	})

	return h
}

// mockPlatformHandler returns an http.Handler that simulates the platform API
// for integration testing.
func mockPlatformHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Health check
		case r.URL.Path == "/v1/health" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":         "ok",
				"version":        "1.0.0-test",
				"uptime_seconds": 42,
				"dependencies":   map[string]interface{}{},
			})

		// License validation
		case r.URL.Path == "/v1/license/validate" && r.Method == "POST":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Key string `json:"key"`
			}
			json.Unmarshal(body, &req)

			if req.Key == "" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]interface{}{"error": "missing key"})
				return
			}
			// Camelcase shape matching the platform's live contract (#4159).
			json.NewEncoder(w).Encode(map[string]interface{}{
				"valid":        true,
				"status":       "active",
				"tier":         "pro",
				"expiresAt":    time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
				"machineBound": false,
				"machineCount": 0,
				"features": map[string]interface{}{
					"batchProcessing":        true,
					"concurrentPipelines":    3,
					"pipelineRunsPerDay":     nil,
					"pipelineRunsPerMonth":   nil,
					"skillResolveRatePerMin": 100,
				},
			})

		// Skill resolution
		case r.URL.Path == "/v1/skills/resolve" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"skill_content": "# Mock Skill Content\nThis is a test skill.",
				"version":       "1.0.0",
				"variant":       "default",
			})

		// Auth: GitHub exchange
		case r.URL.Path == "/v1/auth/github" && r.Method == "POST":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				GithubAccessToken string `json:"github_access_token"`
			}
			json.Unmarshal(body, &req)

			if req.GithubAccessToken == "bad-token" {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error":   "unauthorized",
					"message": "Invalid GitHub token",
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "platform-jwt-from-github",
				"refresh_token": "platform-refresh-from-github",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"status":        "authorized",
			})

		// Auth: Device code
		case r.URL.Path == "/v1/auth/device-code" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"device_code":      "test-device-code",
				"user_code":        "TEST-1234",
				"verification_uri": "https://example.com/device",
				"expires_in":       900,
				"interval":         5,
			})

		// Auth: Device token poll
		case r.URL.Path == "/v1/auth/device-token" && r.Method == "POST":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				DeviceCode string `json:"device_code"`
			}
			json.Unmarshal(body, &req)

			if req.DeviceCode == "pending-code" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "authorization_pending",
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "device-jwt",
				"refresh_token": "device-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"status":        "authorized",
			})

		// Auth: Token refresh
		case r.URL.Path == "/v1/auth/token/refresh" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "refreshed-jwt",
				"refresh_token": "refreshed-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"status":        "authorized",
			})

		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": "not found"})
		}
	})
}

// startMockPlatform starts a mock platform server and returns its URL.
func startMockPlatform(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(mockPlatformHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// harnessWithPlatform creates a test harness with a mock platform server.
func harnessWithPlatform(t *testing.T) (*ipcTestHarness, string) {
	t.Helper()
	platformURL := startMockPlatform(t)
	h := newIpcTestHarnessWithEnv(t, []string{
		"NIGHTGAUGE_PLATFORM_URL=" + platformURL,
		"NIGHTGAUGE_API_KEY=test-api-key",
		"NIGHTGAUGE_LICENSE_KEY=IB-TEST-TEST-TEST",
	})
	return h, platformURL
}

// ─── Platform Status Tests ──────────────────────────────────────────────────

func TestIPC_Platform_Status_Online(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	// Give health polling a moment to transition to online
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.status", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.status error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	mode, ok := result["mode"]
	if !ok {
		t.Fatal("result missing 'mode' key")
	}
	if mode != "online" {
		t.Errorf("mode = %s, want online", mode)
	}
}

func TestIPC_Platform_Status_NoPlatformClient(t *testing.T) {
	// No platform env vars → no platform client
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("platform.status", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.status error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["mode"] != "offline" {
		t.Errorf("mode = %v, want offline", result["mode"])
	}
}

// ─── Platform Health Check ──────────────────────────────────────────────────

func TestIPC_Platform_HealthCheck_OK(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("platform.healthCheck", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.healthCheck error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["status"] != "ok" {
		t.Errorf("status = %v, want ok", result["status"])
	}
}

// ─── License Validation ─────────────────────────────────────────────────────

func TestIPC_Platform_LicenseValidate_Pro(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	// Give health polling a moment
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.validateLicense", map[string]interface{}{
		"licenseKey": "IB-TEST-TEST-TEST",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.validateLicense error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	// #4156: LicenseInfo now carries JSON tags — the wire format is camelCase
	// ("tier"), matching what the TS LicenseInfo interface has always read.
	// Pre-#4156 this asserted the (buggy) PascalCase "Tier" key.
	if result["tier"] != "pro" {
		t.Errorf("tier = %v, want pro", result["tier"])
	}
	if result["status"] != "active" {
		t.Errorf("status = %v, want active", result["status"])
	}
}

func TestIPC_Platform_LicenseValidate_Community(t *testing.T) {
	// No license key — should get a valid, uncapped community license via
	// platform.license.
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("platform.license", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.license error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	// Without a platform client, returns the uncapped community LicenseInfo
	// (#134): valid → allowed, tier "community", with no numeric feature caps.
	if valid, ok := result["valid"].(bool); !ok || !valid {
		t.Errorf("valid = %v, want true (community is always allowed)", result["valid"])
	}
	if result["tier"] != "community" {
		t.Errorf("tier = %v, want community", result["tier"])
	}
	if _, exists := result["concurrentPipelines"]; exists {
		t.Error("community LicenseInfo must not carry numeric feature caps (concurrentPipelines) post-#134")
	}
}

// ─── Skill Resolution ───────────────────────────────────────────────────────

func TestIPC_Platform_ResolveSkill_Success(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	// Give health polling a moment
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.resolveSkill", map[string]interface{}{
		"skillId": "feature-dev",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.resolveSkill error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	content, ok := result["Content"]
	if !ok {
		t.Fatal("result missing Content")
	}
	if !strings.Contains(content.(string), "Mock Skill Content") {
		t.Errorf("content = %v, want to contain 'Mock Skill Content'", content)
	}
}

func TestIPC_Platform_ResolveSkill_NoPlatformClient(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("platform.resolveSkill", map[string]interface{}{
		"skillId": "feature-dev",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error == nil {
		t.Fatal("expected error when no platform client configured")
	}
	if !strings.Contains(resp.Error.Message, "not configured") {
		t.Errorf("error = %q, want to contain 'not configured'", resp.Error.Message)
	}
}

// ─── Auth Tests ─────────────────────────────────────────────────────────────

func TestIPC_Auth_ExchangeGitHub_Success(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("auth.exchangeGitHub", map[string]interface{}{
		"github_token": "valid-gh-token",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("auth.exchangeGitHub error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["access_token"] != "platform-jwt-from-github" {
		t.Errorf("access_token = %v, want platform-jwt-from-github", result["access_token"])
	}
}

func TestIPC_Auth_ExchangeGitHub_Unauthorized(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("auth.exchangeGitHub", map[string]interface{}{
		"github_token": "bad-token",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestIPC_Auth_DeviceFlowStart_Success(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("auth.deviceFlowStart", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("auth.deviceFlowStart error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["device_code"] != "test-device-code" {
		t.Errorf("device_code = %v, want test-device-code", result["device_code"])
	}
	if result["user_code"] != "TEST-1234" {
		t.Errorf("user_code = %v, want TEST-1234", result["user_code"])
	}
}

func TestIPC_Auth_DeviceFlowPoll_Pending(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("auth.deviceFlowPoll", map[string]interface{}{
		"device_code": "pending-code",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("auth.deviceFlowPoll error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["status"] != "authorization_pending" {
		t.Errorf("status = %v, want authorization_pending", result["status"])
	}
}

func TestIPC_Auth_DeviceFlowPoll_Authorized(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("auth.deviceFlowPoll", map[string]interface{}{
		"device_code": "authorized-code",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("auth.deviceFlowPoll error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["status"] != "authorized" {
		t.Errorf("status = %v, want authorized", result["status"])
	}
	if result["access_token"] != "device-jwt" {
		t.Errorf("access_token = %v, want device-jwt", result["access_token"])
	}
}

func TestIPC_Auth_Refresh_Success(t *testing.T) {
	h, _ := harnessWithPlatform(t)
	h.awaitReady()

	id := h.sendRequest("auth.refresh", map[string]interface{}{
		"refresh_token": "old-refresh",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("auth.refresh error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["access_token"] != "refreshed-jwt" {
		t.Errorf("access_token = %v, want refreshed-jwt", result["access_token"])
	}
}

// ─── Offline Fallback ───────────────────────────────────────────────────────

func TestIPC_Platform_OfflineFallback(t *testing.T) {
	// Start a platform that returns 503 to force offline mode
	offlineSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	t.Cleanup(offlineSrv.Close)

	h := newIpcTestHarnessWithEnv(t, []string{
		"NIGHTGAUGE_PLATFORM_URL=" + offlineSrv.URL,
		"NIGHTGAUGE_API_KEY=test-key",
	})
	h.awaitReady()

	// Wait for health poll to register offline
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.status", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.status error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result["mode"] != "offline" {
		t.Errorf("mode = %v, want offline", result["mode"])
	}
}
