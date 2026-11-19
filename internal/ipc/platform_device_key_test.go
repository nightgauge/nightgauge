package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// platform.reloadLicense hands the daemon's own platform client to the
// reloader serve injected (#1883), and refuses when none was injected.
func TestReloadLicense_CallsTheInjectedReloaderWithTheDaemonClient(t *testing.T) {
	pc := newTestPlatformClientFor(t, "http://127.0.0.1:1", "")
	var got *platform.Client
	s := NewServer(nil, WithPlatformClient(pc), WithLicenseReloader(
		func(_ context.Context, c *platform.Client) (*PlatformReloadLicenseResult, error) {
			got = c
			return &PlatformReloadLicenseResult{LicensePresent: true, Adopted: true, Source: "keychain"}, nil
		}))

	res, err := s.methods["platform.reloadLicense"](context.Background(), nil)
	if err != nil {
		t.Fatalf("reloadLicense: %v", err)
	}
	if got != pc {
		t.Fatal("the reloader did not receive the daemon's platform client")
	}
	if r := res.(*PlatformReloadLicenseResult); !r.Adopted || r.Source != "keychain" {
		t.Fatalf("result = %+v", r)
	}
}

func TestReloadLicense_RefusesWithoutAReloader(t *testing.T) {
	s := NewServer(nil)
	if _, err := s.methods["platform.reloadLicense"](context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want not available", err)
	}
}

// platform.requestDeviceKey carries the editor's machine fields and the
// per-call JWT through, and its error leads with the platform code.
func TestRequestDeviceKey_ForwardsTokenAndSurfacesTheCode(t *testing.T) {
	var auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/license/device-key" {
			w.WriteHeader(http.StatusOK)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"LICENSE_MACHINE_LIMIT"}}`))
	}))
	t.Cleanup(srv.Close)
	pc := newTestPlatformClientFor(t, srv.URL, "")
	s := NewServer(nil, WithPlatformClient(pc))

	raw, _ := json.Marshal(PlatformRequestDeviceKeyParams{
		AccessToken: "header.payload.signature", MachineID: "vscode-machine", Hostname: "mac", Platform: "darwin",
	})
	_, err := s.methods["platform.requestDeviceKey"](context.Background(), raw)
	if err == nil || !strings.HasPrefix(err.Error(), "LICENSE_MACHINE_LIMIT:") {
		t.Fatalf("err = %v, want a LICENSE_MACHINE_LIMIT-led message", err)
	}
	if auth != "Bearer header.payload.signature" {
		t.Fatalf("Authorization = %q", auth)
	}
	if body["machineId"] != platform.DeviceKeyMachineID("vscode-machine") || body["hostname"] != "mac" {
		t.Fatalf("body = %v", body)
	}
}
