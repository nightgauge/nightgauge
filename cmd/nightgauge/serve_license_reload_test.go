package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/platform"
)

func newReloadTestClient(t *testing.T, licenseKey string) *platform.Client {
	t.Helper()
	cfg := platform.DefaultConfig()
	cfg.BaseURL = "http://127.0.0.1:1"
	cfg.LicenseKey = licenseKey
	cfg.OnDemandHealth = true
	pc, err := platform.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return pc
}

func countingBridge(attaches *int) *actionCenterBridge {
	b := &actionCenterBridge{}
	b.Bind(func(*platform.Client) bool {
		*attaches++
		return true
	})
	return b
}

func TestLicenseReload_AdoptsStoredKeyAndAttachesBridgeOnce(t *testing.T) {
	pc := newReloadTestClient(t, "")
	attaches := 0
	bridge := countingBridge(&attaches)
	reload := newLicenseReloader(func() resolvedPlatformConfig {
		return resolvedPlatformConfig{LicenseKey: "ng_live_device", Source: platformSourceKeychain}
	}, bridge)

	res, err := reload(context.Background(), pc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !res.Adopted || !res.LicensePresent || !res.BridgeAttached || res.Source != "keychain" {
		t.Fatalf("result = %+v", res)
	}
	if pc.LicenseKey() != "ng_live_device" {
		t.Fatalf("client key = %q", pc.LicenseKey())
	}

	// A second reload (another sign-in, a retry) must not attach again.
	res, err = reload(context.Background(), pc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if res.Adopted || !res.BridgeAttached {
		t.Fatalf("second result = %+v", res)
	}
	if attaches != 1 {
		t.Fatalf("bridge attached %d times, want 1", attaches)
	}
}

func TestLicenseReload_NeverReplacesAKeyTheDaemonHolds(t *testing.T) {
	pc := newReloadTestClient(t, "ng_live_explicit")
	attaches := 0
	reload := newLicenseReloader(func() resolvedPlatformConfig {
		return resolvedPlatformConfig{LicenseKey: "ng_live_explicit", Source: platformSourceFlagEnv}
	}, countingBridge(&attaches))

	res, err := reload(context.Background(), pc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if res.Adopted {
		t.Fatal("reload replaced the daemon's existing key")
	}
	if pc.LicenseKey() != "ng_live_explicit" {
		t.Fatalf("client key = %q, want the explicit one", pc.LicenseKey())
	}
	// A key held since startup still attaches the bridge (once).
	if !res.LicensePresent || !res.BridgeAttached || attaches != 1 {
		t.Fatalf("result = %+v attaches = %d", res, attaches)
	}
}

func TestLicenseReload_ConfigKeyIsNotReplacedByAnotherSource(t *testing.T) {
	pc := newReloadTestClient(t, "ng_live_config")
	attaches := 0
	reload := newLicenseReloader(func() resolvedPlatformConfig {
		return resolvedPlatformConfig{LicenseKey: "ng_live_other", Source: platformSourceConfig}
	}, countingBridge(&attaches))
	res, err := reload(context.Background(), pc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if res.Adopted || pc.LicenseKey() != "ng_live_config" {
		t.Fatalf("result = %+v key = %q, want the held key kept", res, pc.LicenseKey())
	}
}

func TestLicenseReload_ReplacesARevokedIssuedKey(t *testing.T) {
	pc := newReloadTestClient(t, "ng_live_revoked")
	attaches := 0
	reload := newLicenseReloader(func() resolvedPlatformConfig {
		return resolvedPlatformConfig{LicenseKey: "ng_live_reissued", Source: platformSourceKeychain}
	}, countingBridge(&attaches))
	res, err := reload(context.Background(), pc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !res.Adopted || pc.LicenseKey() != "ng_live_reissued" {
		t.Fatalf("result = %+v key = %q, want the re-issued key", res, pc.LicenseKey())
	}
	if !res.BridgeAttached || attaches != 1 {
		t.Fatalf("result = %+v attaches = %d", res, attaches)
	}
}

func TestLicenseReload_NoOpWhileTheStoreIsEmpty(t *testing.T) {
	pc := newReloadTestClient(t, "")
	attaches := 0
	reload := newLicenseReloader(func() resolvedPlatformConfig {
		return resolvedPlatformConfig{Source: platformSourceAbsent}
	}, countingBridge(&attaches))

	res, err := reload(context.Background(), pc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if res.LicensePresent || res.Adopted || res.BridgeAttached || res.Source != "absent" {
		t.Fatalf("result = %+v", res)
	}
	if attaches != 0 {
		t.Fatal("bridge attached with no license key")
	}
}

func TestActionCenterBridge_UnboundOrDecliningStaysDetached(t *testing.T) {
	pc := newReloadTestClient(t, "ng_live_x")
	b := &actionCenterBridge{}
	if b.Attach(pc) {
		t.Fatal("an unbound bridge reported attached")
	}
	calls := 0
	b.Bind(func(*platform.Client) bool { calls++; return calls > 1 })
	if b.Attach(pc) {
		t.Fatal("a declining attach step reported attached")
	}
	if !b.Attach(pc) || !b.Attached() {
		t.Fatal("a later successful attach was not recorded")
	}
	if b.Attach(pc); calls != 2 {
		t.Fatalf("attach step ran %d times, want 2", calls)
	}
}

func TestReloadServeConfig(t *testing.T) {
	started := &config.Config{}
	if got := reloadServeConfig("", started); got != started {
		t.Fatal("an empty root must keep the startup config")
	}
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, ".nightgauge", "config.yaml"), []byte("platform: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := reloadServeConfig(bad, started); got != started {
		t.Fatal("an unreadable config must keep the startup config")
	}
	if got := reloadServeConfig(t.TempDir(), started); got == nil || got == started {
		t.Fatal("a readable workspace must be re-read")
	}
}
