package main

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestResolvePlatformConfig_PrecedenceTable exercises the flag > env >
// config > absent precedence #333 requires. flag and env are collapsed into
// a single "flagOrEnv" input column because serveCmd's cobra flags already
// default to os.Getenv(...) before RunE ever calls resolvePlatformConfig —
// see the doc comment on resolvePlatformConfig for why that makes the two
// tiers indistinguishable at this layer.
func TestResolvePlatformConfig_PrecedenceTable(t *testing.T) {
	cfgWithBoth := &config.Config{PlatformURL: "https://cfg.example.com", LicenseKey: "lic_cfg"}
	cfgURLOnly := &config.Config{PlatformURL: "https://cfg.example.com"}
	cfgLicenseOnly := &config.Config{LicenseKey: "lic_cfg"}
	cfgEmpty := &config.Config{}

	tests := []struct {
		name           string
		flagURL        string
		flagAPIKey     string
		flagLicenseKey string
		cfg            *config.Config
		wantURL        string
		wantAPIKey     string
		wantLicense    string
		wantSource     platformConfigSource
		wantConfigured bool
	}{
		{
			name:           "flag/env wins over config when both set",
			flagURL:        "https://flag.example.com",
			flagLicenseKey: "lic_flag",
			cfg:            cfgWithBoth,
			wantURL:        "https://flag.example.com",
			wantLicense:    "lic_flag",
			wantSource:     platformSourceFlagEnv,
			wantConfigured: true,
		},
		{
			name:           "config fills in when flag/env absent",
			cfg:            cfgWithBoth,
			wantURL:        "https://cfg.example.com",
			wantLicense:    "lic_cfg",
			wantSource:     platformSourceConfig,
			wantConfigured: true,
		},
		{
			name:           "config supplies url only",
			cfg:            cfgURLOnly,
			wantURL:        "https://cfg.example.com",
			wantSource:     platformSourceConfig,
			wantConfigured: true,
		},
		{
			name:           "config supplies license only",
			cfg:            cfgLicenseOnly,
			wantLicense:    "lic_cfg",
			wantSource:     platformSourceConfig,
			wantConfigured: true,
		},
		{
			name:           "flag license overrides config license, config url still used",
			flagLicenseKey: "lic_flag",
			cfg:            cfgURLOnly,
			wantURL:        "https://cfg.example.com",
			wantLicense:    "lic_flag",
			wantSource:     platformSourceFlagEnv,
			wantConfigured: true,
		},
		{
			name:           "nothing anywhere is absent",
			cfg:            cfgEmpty,
			wantSource:     platformSourceAbsent,
			wantConfigured: false,
		},
		{
			name:           "nil config treated as no platform section",
			flagLicenseKey: "",
			cfg:            nil,
			wantSource:     platformSourceAbsent,
			wantConfigured: false,
		},
		{
			name:           "flag api key alone (no config source for api key)",
			flagAPIKey:     "key_flag",
			cfg:            cfgEmpty,
			wantAPIKey:     "key_flag",
			wantSource:     platformSourceFlagEnv,
			wantConfigured: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePlatformConfig(tt.flagURL, tt.flagAPIKey, tt.flagLicenseKey, tt.cfg)
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if got.APIKey != tt.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", got.APIKey, tt.wantAPIKey)
			}
			if got.LicenseKey != tt.wantLicense {
				t.Errorf("LicenseKey = %q, want %q", got.LicenseKey, tt.wantLicense)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if got.Configured() != tt.wantConfigured {
				t.Errorf("Configured() = %v, want %v", got.Configured(), tt.wantConfigured)
			}
		})
	}
}

// TestResolvePlatformConfig_ExtensionSpawnedDaemon reproduces the exact
// scenario from #333's bug report: `nightgauge serve --workspace <root>`
// with no --license-key flag and no NIGHTGAUGE_LICENSE_KEY env (the
// extension's actual invocation), but a merged config carrying a platform
// section (as it would after config.Load merges in
// ~/.nightgauge/config.yaml). Both downstream gates in serveCmd's RunE
// check `licenseKey != ""`, so this is the exact value that must come out
// non-empty for the remote-command poller and the Action Center bridge to
// activate.
func TestResolvePlatformConfig_ExtensionSpawnedDaemon(t *testing.T) {
	cfg := &config.Config{
		PlatformURL: "https://api.nightgauge.dev",
		LicenseKey:  "lic_from_global_config",
	}

	got := resolvePlatformConfig("", "", "", cfg)

	if got.LicenseKey != "lic_from_global_config" {
		t.Fatalf("LicenseKey = %q, want lic_from_global_config — the #330 bridge and remote-command poller gates both check this value", got.LicenseKey)
	}
	if !got.Configured() {
		t.Fatal("Configured() = false, want true")
	}
	if got.Source != platformSourceConfig {
		t.Errorf("Source = %q, want %q", got.Source, platformSourceConfig)
	}
}

// TestResolvePlatformConfig_FullyOfflineUnchanged verifies the issue's other
// acceptance criterion: no platform config anywhere (no flags, no env, no
// config file section) behaves identically to pre-#333 — platformClient
// stays nil and nothing is configured.
func TestResolvePlatformConfig_FullyOfflineUnchanged(t *testing.T) {
	got := resolvePlatformConfig("", "", "", &config.Config{})
	if got.Configured() {
		t.Fatalf("Configured() = true, want false for a fully local config: %+v", got)
	}
	if got.Source != platformSourceAbsent {
		t.Errorf("Source = %q, want %q", got.Source, platformSourceAbsent)
	}
}
