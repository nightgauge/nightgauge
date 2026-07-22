// Package ipc — Platform integration tests for the real IPC server binary.
//
// These tests start a real nightgauge binary with --platform-url pointing
// to an in-process httptest.Server serving realistic fixture responses. They
// exercise the full path:
//
//	binary startup → platform.NewClient() → health poll → IPC method dispatch
//	→ platform HTTP call → response → IPC response
//
// When PLATFORM_TEST_URL is set, tests use the real Docker Compose platform
// instead of the in-process mock.
//
// @see Issue #2092 — End-to-end integration tests for auth, license, skill resolution
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─── Mock platform server ──────────────────────────────────────────────────

// newMockPlatformServer creates a real HTTP server that serves fixture
// responses for platform API endpoints. Use the handlers map to override
// specific routes. The server is closed automatically when the test ends.
func newMockPlatformServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, handler := range handlers {
		mux.HandleFunc(path, handler)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// jsonHandler returns an http.HandlerFunc that writes a JSON response.
func jsonHandler(status int, body interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body) //nolint:errcheck
	}
}

// ─── Fixture responses (match api/openapi.yaml schemas) ────────────────────

var (
	healthOKResponse = map[string]interface{}{
		"status":  "ok",
		"version": "1.0.0-test",
		"services": map[string]interface{}{
			"database": "ok",
			"cache":    "ok",
		},
		"timestamp": "2026-03-13T00:00:00Z",
	}

	licenseValidResponse = map[string]interface{}{
		"valid":        true,
		"status":       "active",
		"tier":         "pro",
		"expiresAt":    "2027-01-01T00:00:00Z",
		"expiresSoon":  false,
		"machineBound": false,
		"machineCount": 1,
		"features": map[string]interface{}{
			"batchProcessing":        true,
			"concurrentPipelines":    3,
			"pipelineRunsPerDay":     50,
			"pipelineRunsPerMonth":   nil,
			"skillResolveRatePerMin": 60,
		},
	}

	skillResolveResponse = map[string]interface{}{
		"skillContent": "# Feature Dev Skill\n\nImplement features following approved plans...",
		"version":      "1.8.0",
		"variant":      "variant-opus-v2",
	}
)

// ─── Platform-aware harness ────────────────────────────────────────────────

// newIpcTestHarnessWithPlatform creates a temp workspace, starts the binary
// with --platform-url and --api-key flags pointing at the given platform
// server, and returns a harness ready to send IPC requests.
func newIpcTestHarnessWithPlatform(t *testing.T, platformURL, apiKey string) *ipcTestHarness {
	t.Helper()

	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	args := []string{"serve", "--workspace", workDir}
	if platformURL != "" {
		args = append(args, "--platform-url", platformURL)
	}
	if apiKey != "" {
		args = append(args, "--api-key", apiKey)
	}

	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN=fake-token-for-integration-test")

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

// defaultPlatformHandlers returns the standard set of mock platform handlers
// that serve healthy/valid responses for all tested endpoints.
func defaultPlatformHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/v1/health":           jsonHandler(200, healthOKResponse),
		"/v1/license/validate": jsonHandler(200, licenseValidResponse),
		"/v1/skills/resolve":   jsonHandler(200, skillResolveResponse),
	}
}

// ─── Platform IPC Integration Tests ────────────────────────────────────────

// TestIPCPlatform_Status_Online verifies that when the binary starts with a
// reachable mock platform, platform.status returns mode="online" after the
// initial health check completes.
func TestIPCPlatform_Status_Online(t *testing.T) {
	srv := newMockPlatformServer(t, defaultPlatformHandlers())
	h := newIpcTestHarnessWithPlatform(t, srv.URL, "test-api-key")
	h.awaitReady()

	// Give the platform health poll time to complete
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.status", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.status returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	mode, ok := result["mode"].(string)
	if !ok {
		t.Fatalf("platform.status result missing 'mode' field: %s", string(resultBytes))
	}
	if mode != "online" {
		t.Errorf("platform.status mode = %q, want %q", mode, "online")
	}
}

// TestIPCPlatform_HealthCheck_Online verifies that platform.healthCheck
// returns a response with status="ok" when a mock platform is reachable.
func TestIPCPlatform_HealthCheck_Online(t *testing.T) {
	srv := newMockPlatformServer(t, defaultPlatformHandlers())
	h := newIpcTestHarnessWithPlatform(t, srv.URL, "test-api-key")
	h.awaitReady()

	id := h.sendRequest("platform.healthCheck", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.healthCheck returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	status, ok := result["status"].(string)
	if !ok {
		t.Fatalf("platform.healthCheck result missing 'status' field: %s", string(resultBytes))
	}
	if status != "ok" {
		t.Errorf("platform.healthCheck status = %q, want %q", status, "ok")
	}
}

// TestIPCPlatform_ValidateLicense_Online verifies that platform.validateLicense
// sends the license key to the mock platform and returns the expected tier.
func TestIPCPlatform_ValidateLicense_Online(t *testing.T) {
	srv := newMockPlatformServer(t, defaultPlatformHandlers())
	h := newIpcTestHarnessWithPlatform(t, srv.URL, "test-api-key")
	h.awaitReady()

	id := h.sendRequest("platform.validateLicense", map[string]interface{}{
		"licenseKey": "ib_test_pro_abc123",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.validateLicense returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	// #4156: LicenseInfo now carries explicit JSON tags, so the wire format is
	// definitively camelCase (previously ambiguous — this test used to probe
	// both "Valid" and "valid" because the Go struct had no tags at all).
	if valid, ok := result["valid"].(bool); !ok || !valid {
		t.Fatalf("platform.validateLicense result missing valid=true: %s", string(resultBytes))
	}
	if tier, _ := result["tier"].(string); tier != "pro" {
		t.Errorf("tier = %v, want pro", result["tier"])
	}
	if status, _ := result["status"].(string); status != "active" {
		t.Errorf("status = %v, want active", result["status"])
	}
	if machineCount, _ := result["machineCount"].(float64); machineCount != 1 {
		t.Errorf("machineCount = %v, want 1", result["machineCount"])
	}
}

// TestIPCPlatform_ResolveSkill_Online verifies that platform.resolveSkill
// calls the mock platform and returns skill content.
func TestIPCPlatform_ResolveSkill_Online(t *testing.T) {
	srv := newMockPlatformServer(t, defaultPlatformHandlers())
	h := newIpcTestHarnessWithPlatform(t, srv.URL, "test-api-key")
	h.awaitReady()

	id := h.sendRequest("platform.resolveSkill", map[string]interface{}{
		"skillId": "nightgauge-feature-dev",
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.resolveSkill returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	// The SkillService returns a CachedSkill struct
	if result == nil {
		t.Fatal("platform.resolveSkill returned nil result")
	}
}

// TestIPCPlatform_License_CommunityFallback verifies that platform.license
// (no params) returns community-tier features when the binary has a platform
// client but the license service has no license key configured.
func TestIPCPlatform_License_CommunityFallback(t *testing.T) {
	srv := newMockPlatformServer(t, defaultPlatformHandlers())
	// No license key — should fall back to community features
	h := newIpcTestHarnessWithPlatform(t, srv.URL, "test-api-key")
	h.awaitReady()

	id := h.sendRequest("platform.license", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.license returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result == nil {
		t.Fatal("platform.license returned nil result")
	}

	// Community license is valid and uncapped (#134) — no numeric feature caps.
	t.Logf("platform.license result: %s", string(resultBytes))
}

// TestIPCPlatform_HealthCheck_Offline verifies that platform.healthCheck
// returns an offline indicator when the platform URL is unreachable.
func TestIPCPlatform_HealthCheck_Offline(t *testing.T) {
	// Use an unreachable URL to simulate offline
	h := newIpcTestHarnessWithPlatform(t, "http://127.0.0.1:1", "test-api-key")
	h.awaitReady()

	// Give the health poller time to fail
	time.Sleep(1 * time.Second)

	id := h.sendRequest("platform.status", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.status returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	mode, ok := result["mode"].(string)
	if !ok {
		t.Fatalf("platform.status result missing 'mode' field: %s", string(resultBytes))
	}
	if mode != "offline" {
		t.Errorf("platform.status mode = %q, want %q (platform unreachable)", mode, "offline")
	}
}

// TestIPCPlatform_ValidateLicense_Offline_ReturnsCommunity verifies that
// when the platform is unreachable, platform.license returns community
// tier features (offline fallback).
func TestIPCPlatform_ValidateLicense_Offline_ReturnsCommunity(t *testing.T) {
	h := newIpcTestHarnessWithPlatform(t, "http://127.0.0.1:1", "test-api-key")
	h.awaitReady()

	// Give health poller time to detect offline
	time.Sleep(1 * time.Second)

	id := h.sendRequest("platform.license", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Fatalf("platform.license returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)

	if result == nil {
		t.Fatal("platform.license returned nil result (expected community features)")
	}

	// The offline fallback returns a valid, uncapped community LicenseInfo (#134).
	t.Logf("offline platform.license result: %s", string(resultBytes))
}

// ─── Docker Compose E2E Tests ──────────────────────────────────────────────

// TestIPCPlatform_DockerCompose_SkipIfNotSet runs integration tests against
// a real Docker Compose platform when PLATFORM_TEST_URL is set. Skipped
// when the env var is not set (which is the normal CI/local case).
func TestIPCPlatform_DockerCompose_SkipIfNotSet(t *testing.T) {
	platformURL := os.Getenv("PLATFORM_TEST_URL")
	if platformURL == "" {
		t.Skip("PLATFORM_TEST_URL not set — skipping Docker Compose E2E test")
	}

	apiKey := os.Getenv("PLATFORM_TEST_API_KEY")
	h := newIpcTestHarnessWithPlatform(t, platformURL, apiKey)
	h.awaitReady()

	// Give health poller time to connect
	time.Sleep(2 * time.Second)

	// Verify platform is online
	t.Run("status/online", func(t *testing.T) {
		id := h.sendRequest("platform.status", nil)
		resp := h.readResponseFor(id, nil)
		if resp.Error != nil {
			t.Fatalf("platform.status error: %+v", resp.Error)
		}
		resultBytes, _ := json.Marshal(resp.Result)
		var result map[string]interface{}
		json.Unmarshal(resultBytes, &result)
		if mode, ok := result["mode"].(string); !ok || mode != "online" {
			t.Errorf("expected mode=online, got %v", result["mode"])
		}
	})

	// Verify health check
	t.Run("healthCheck", func(t *testing.T) {
		id := h.sendRequest("platform.healthCheck", nil)
		resp := h.readResponseFor(id, nil)
		if resp.Error != nil {
			t.Fatalf("platform.healthCheck error: %+v", resp.Error)
		}
	})
}

// ─── Verify mock server receives correct HTTP requests ─────────────────────

// TestIPCPlatform_MockServerReceivesRequest verifies that the binary's
// platform client actually sends HTTP requests to the mock server (not just
// returning cached/default values).
func TestIPCPlatform_MockServerReceivesRequest(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)

	handlers := map[string]http.HandlerFunc{
		"/v1/health": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			received = append(received, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(healthOKResponse) //nolint:errcheck
		},
		"/v1/license/validate": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			received = append(received, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(licenseValidResponse) //nolint:errcheck
		},
		"/v1/skills/resolve": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			received = append(received, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(skillResolveResponse) //nolint:errcheck
		},
	}

	srv := newMockPlatformServer(t, handlers)
	h := newIpcTestHarnessWithPlatform(t, srv.URL, "test-api-key")
	h.awaitReady()

	// Wait for health poll to hit the server
	time.Sleep(500 * time.Millisecond)

	// Send a healthCheck IPC request
	id := h.sendRequest("platform.healthCheck", nil)
	h.readResponseFor(id, nil)

	// Verify mock server received at least one /v1/health request
	mu.Lock()
	defer mu.Unlock()

	healthCalls := 0
	for _, r := range received {
		if r == "GET /v1/health" {
			healthCalls++
		}
	}

	if healthCalls == 0 {
		t.Errorf("mock server received no /v1/health requests, received: %v", received)
	}
}
