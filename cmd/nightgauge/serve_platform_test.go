package main

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/keychain"
)

// storedAt returns a stored-license lookup that reports value from source.
func storedAt(value string, source keychain.Source) func() (keychain.Result, error) {
	return func() (keychain.Result, error) {
		return keychain.Result{Value: value, Source: source}, nil
	}
}

// TestResolvePlatformConfig_PrecedenceTable exercises the flag > env >
// config > absent precedence #333 requires. flag and env are collapsed into
// a single "flagOrEnv" input column because serveCmd's cobra flags already
// default to os.Getenv(...) before RunE ever calls resolvePlatformConfig —
// see the doc comment on resolvePlatformConfig for why that makes the two
// tiers indistinguishable at this layer.
func TestResolvePlatformConfig_PrecedenceTable(t *testing.T) {
	enabled := true
	disabled := false
	cfgURL := &config.Config{PlatformEnabled: &enabled, PlatformURL: "https://cfg.example.com"}
	cfgEnabledOnly := &config.Config{PlatformEnabled: &enabled}
	cfgDisabled := &config.Config{PlatformEnabled: &disabled, PlatformURL: "https://cfg.example.com"}
	cfgOmitted := &config.Config{PlatformURL: "https://cfg.example.com"}
	cfgEmpty := &config.Config{}
	fileLicense := storedAt("lic_cfg", keychain.SourceMachineFile)
	keychainLicense := storedAt("lic_keychain", keychain.SourceKeychain)

	tests := []struct {
		name           string
		flagURL        string
		flagAPIKey     string
		flagLicenseKey string
		cfg            *config.Config
		stored         func() (keychain.Result, error)
		wantURL        string
		wantAPIKey     string
		wantLicense    string
		wantSource     platformConfigSource
		wantConfigured bool
	}{
		{
			name:           "config explicitly disabled is ignored",
			cfg:            cfgDisabled,
			stored:         fileLicense,
			wantSource:     platformSourceAbsent,
			wantConfigured: false,
		},
		{
			name:           "omitted enabled defaults to local only",
			cfg:            cfgOmitted,
			stored:         fileLicense,
			wantSource:     platformSourceAbsent,
			wantConfigured: false,
		},
		{
			name:           "explicit flag remains an opt in over disabled config",
			flagLicenseKey: "lic_flag",
			cfg:            cfgDisabled,
			stored:         fileLicense,
			wantLicense:    "lic_flag",
			wantSource:     platformSourceFlagEnv,
			wantConfigured: true,
		},
		{
			name:           "flag/env wins over config when both set",
			flagURL:        "https://flag.example.com",
			flagLicenseKey: "lic_flag",
			cfg:            cfgURL,
			stored:         fileLicense,
			wantURL:        "https://flag.example.com",
			wantLicense:    "lic_flag",
			wantSource:     platformSourceFlagEnv,
			wantConfigured: true,
		},
		{
			name:           "config fills in when flag/env absent",
			cfg:            cfgURL,
			stored:         fileLicense,
			wantURL:        "https://cfg.example.com",
			wantLicense:    "lic_cfg",
			wantSource:     platformSourceConfig,
			wantConfigured: true,
		},
		{
			name:           "config supplies url only",
			cfg:            cfgURL,
			wantURL:        "https://cfg.example.com",
			wantSource:     platformSourceConfig,
			wantConfigured: true,
		},
		{
			name:           "config supplies license only",
			cfg:            cfgEnabledOnly,
			stored:         fileLicense,
			wantLicense:    "lic_cfg",
			wantSource:     platformSourceConfig,
			wantConfigured: true,
		},
		{
			name:           "flag license overrides config license, config url still used",
			flagLicenseKey: "lic_flag",
			cfg:            cfgURL,
			wantURL:        "https://cfg.example.com",
			wantLicense:    "lic_flag",
			wantSource:     platformSourceFlagEnv,
			wantConfigured: true,
		},
		{
			name:           "keychain supplies the license",
			cfg:            cfgURL,
			stored:         keychainLicense,
			wantURL:        "https://cfg.example.com",
			wantLicense:    "lic_keychain",
			wantSource:     platformSourceKeychain,
			wantConfigured: true,
		},
		{
			name:           "stored keychain license is ignored when platform is not enabled",
			cfg:            cfgOmitted,
			stored:         keychainLicense,
			wantSource:     platformSourceAbsent,
			wantConfigured: false,
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
			got := resolvePlatformConfig(tt.flagURL, tt.flagAPIKey, tt.flagLicenseKey, tt.cfg, tt.stored)
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
// with no NIGHTGAUGE_LICENSE_KEY env (the
// extension's actual invocation), but a merged config carrying a platform
// section (as it would after config.Load merges in
// ~/.nightgauge/config.yaml). Both downstream gates in serveCmd's RunE
// check `licenseKey != ""`, so this is the exact value that must come out
// non-empty for the remote-command poller and the Action Center bridge to
// activate.
func TestResolvePlatformConfig_ExtensionSpawnedDaemon(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		PlatformEnabled: &enabled,
		PlatformURL:     "https://api.nightgauge.dev",
	}

	got := resolvePlatformConfig("", "", "", cfg, storedAt("lic_from_global_config", keychain.SourceMachineFile))

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
	got := resolvePlatformConfig("", "", "", &config.Config{}, storedAt("", keychain.SourceNone))
	if got.Configured() {
		t.Fatalf("Configured() = true, want false for a fully local config: %+v", got)
	}
	if got.Source != platformSourceAbsent {
		t.Errorf("Source = %q, want %q", got.Source, platformSourceAbsent)
	}
}

// TestResolvePlatformConfig_ExtensionStoredKeyIsAStoredCredential pins #2398.
// The extension no longer hands the daemon its stored license key in
// NIGHTGAUGE_LICENSE_KEY, where it read as an explicit opt-in: the key it
// stored reaches serve through the shared store (the keychain entry the
// extension writes with `auth license set`, or the machine-tier file), and a
// stored key is used only when platform.enabled is true.
func TestResolvePlatformConfig_ExtensionStoredKeyIsAStoredCredential(t *testing.T) {
	enabled, disabled := true, false
	storedByTheExtension := storedAt("lic_from_vscode", keychain.SourceKeychain)

	off := resolvePlatformConfig("", "", "", &config.Config{PlatformEnabled: &disabled}, storedByTheExtension)
	if off.LicenseKey != "" || off.Configured() {
		t.Fatalf("platform.enabled false: got %+v, want no license and no client — the platform agent (registration, heartbeat, command poller) starts from this key", off)
	}

	on := resolvePlatformConfig("", "", "", &config.Config{PlatformEnabled: &enabled}, storedByTheExtension)
	if on.LicenseKey != "lic_from_vscode" {
		t.Fatalf("platform.enabled true: LicenseKey = %q, want the stored key", on.LicenseKey)
	}
	if on.Source != platformSourceKeychain {
		t.Errorf("platform.enabled true: Source = %q, want %q", on.Source, platformSourceKeychain)
	}

	// A key in the environment that started VS Code is still an explicit
	// opt-in: the extension's spawn inherits it untouched.
	explicit := resolvePlatformConfig("", "", "lic_from_env", &config.Config{PlatformEnabled: &disabled}, storedByTheExtension)
	if explicit.LicenseKey != "lic_from_env" || explicit.Source != platformSourceFlagEnv {
		t.Errorf("explicit env key with platform.enabled false: got %+v, want it used as an opt-in", explicit)
	}
}

// TestOnDemandPlatformEndpoint pins where an account action's on-demand client
// goes (#2398): the flag or environment URL, else the configured
// platform.api_url whatever platform.enabled says, else the default ("").
func TestOnDemandPlatformEndpoint(t *testing.T) {
	disabled := false
	cfg := &config.Config{PlatformEnabled: &disabled, PlatformURL: "https://cfg.example.com"}
	if got := onDemandPlatformEndpoint("https://flag.example.com", cfg); got != "https://flag.example.com" {
		t.Errorf("flag URL: got %q", got)
	}
	if got := onDemandPlatformEndpoint("", cfg); got != "https://cfg.example.com" {
		t.Errorf("config URL with platform.enabled false: got %q, want it", got)
	}
	if got := onDemandPlatformEndpoint("", nil); got != "" {
		t.Errorf("no config: got %q, want the default", got)
	}
}

// TestResolvePlatformConfig_OptedIn pins what counts as the user's opt-in to
// the hosted service, which run telemetry needs (ipc.WithTelemetryPolicy):
// platform.enabled true, or a key in the environment. A platform URL, a
// stored key or (elsewhere) a signed-in session is not one.
func TestResolvePlatformConfig_OptedIn(t *testing.T) {
	enabled, disabled := true, false
	stored := storedAt("lic_stored", keychain.SourceKeychain)
	cases := []struct {
		name                string
		url, apiKey, licEnv string
		cfg                 *config.Config
		want                bool
	}{
		{name: "platform.enabled true", cfg: &config.Config{PlatformEnabled: &enabled}, want: true},
		{name: "license key in the environment", licEnv: "lic_env", cfg: &config.Config{PlatformEnabled: &disabled}, want: true},
		{name: "api key in the environment", apiKey: "key_env", want: true},
		{name: "a platform URL alone", url: "https://staging.example.test", cfg: &config.Config{PlatformEnabled: &disabled}},
		{name: "a stored key with platform.enabled false", cfg: &config.Config{PlatformEnabled: &disabled}},
		{name: "platform.enabled omitted", cfg: &config.Config{PlatformURL: "https://cfg.example.com"}},
		{name: "no config", cfg: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePlatformConfig(tc.url, tc.apiKey, tc.licEnv, tc.cfg, stored)
			if got.OptedIn != tc.want {
				t.Errorf("OptedIn = %v, want %v (%+v)", got.OptedIn, tc.want, got)
			}
		})
	}
}
