package main

import (
	"log"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/keychain"
)

// platformConfigSource identifies where a serve invocation's effective
// platform-client configuration came from, for the startup visibility log
// line required by #333: an extension-spawned daemon that silently never
// wires up the platform client (and therefore never activates the Action
// Center bridge, #330, or the remote-command poller) must be diagnosable
// from the daemon's own log output instead of failing silently.
type platformConfigSource string

const (
	// platformSourceFlagEnv means at least one of URL/API key/license key came
	// from an explicit --flag or its backing environment variable (the two
	// are indistinguishable here — see resolvePlatformConfig doc comment).
	platformSourceFlagEnv platformConfigSource = "flag/env"
	// platformSourceConfig means nothing came from flag/env, but the config
	// file supplied a value: the URL from the merged tiers, the license key
	// from the machine-tier file.
	platformSourceConfig platformConfigSource = "config"
	// platformSourceKeychain means the license key came from the OS keychain
	// entry (`nightgauge auth license set`).
	platformSourceKeychain platformConfigSource = "keychain"
	// platformSourceAbsent means no platform is configured anywhere — the
	// fully-local, zero-behavior-change default.
	platformSourceAbsent platformConfigSource = "absent"
)

// resolvedPlatformConfig is the effective platform-client configuration for
// `nightgauge serve`, after applying resolvePlatformConfig's precedence.
type resolvedPlatformConfig struct {
	URL        string
	APIKey     string
	LicenseKey string
	Source     platformConfigSource
}

// Configured reports whether any platform credential resolved to a non-empty
// value — the same predicate the platform-client construction block, the
// remote-command poller gate, and the Action Center bridge gate all need.
func (r resolvedPlatformConfig) Configured() bool {
	return r.URL != "" || r.APIKey != "" || r.LicenseKey != ""
}

// resolvePlatformConfig applies flag > env > config precedence to the
// platform client's connection settings (#333).
//
// flagURL is the --platform-url flag variable, taken AFTER flag parsing;
// serveCmd registers its *default* as os.Getenv("NIGHTGAUGE_PLATFORM_URL"),
// so an empty string means neither was set. flagAPIKey / flagLicenseKey are
// NIGHTGAUGE_API_KEY / NIGHTGAUGE_LICENSE_KEY: the keys have no flag, because
// a flag puts a credential on argv (ADR-024 § 5). This function therefore only has one real
// decision left to make: fall back to the merged config file's platform
// section when flag/env supplied nothing.
//
// cfg is the result of config.Load(workspaceRoot) — already merged across
// the machine (~/.nightgauge/config.yaml), project (.nightgauge/config.yaml),
// and local tiers. It supplies the URL and the platform.enabled opt-in. cfg
// may be nil (config.Load failed) — treated the same as "config has no
// platform section".
//
// storedLicense is the shared license-key resolution (internal/keychain:
// NIGHTGAUGE_LICENSE_KEY, then the OS keychain, then the machine-tier file).
// It is consulted only when flag/env supplied no key and platform.enabled is
// true: a stored credential is not an opt-in on its own.
//
// There is no config-file source for the API key: the VSCode extension's
// PlatformConfigSchema (packages/nightgauge-vscode/src/config/schema.ts)
// has no platform.api_key field, only platform.api_url and
// platform.license_key — so the API key comes from NIGHTGAUGE_API_KEY only;
// the --api-key flag was removed because it put the key on argv (ADR-024 § 5).
func resolvePlatformConfig(flagURL, flagAPIKey, flagLicenseKey string, cfg *config.Config, storedLicense func() (keychain.Result, error)) resolvedPlatformConfig {
	r := resolvedPlatformConfig{URL: flagURL, APIKey: flagAPIKey, LicenseKey: flagLicenseKey}

	// licenseFromFlagEnv is captured before the config fallback below
	// mutates r.LicenseKey — it drives the Source label because LicenseKey
	// is the field both downstream gates (remote-command poller,
	// #330 Action Center bridge) actually check.
	licenseFromFlagEnv := r.LicenseKey != ""

	urlFromFlagEnv := r.URL != ""
	licenseFromConfig, licenseFromKeychain, urlFromConfig := false, false, false
	// Explicit flags/environment variables remain an opt-in even when the file
	// setting is absent or false. Config-file credentials are used only when
	// platform.enabled is explicitly true; omitted is the local-only default.
	configEnabled := cfg != nil && cfg.PlatformEnabled != nil && *cfg.PlatformEnabled
	if configEnabled {
		if r.URL == "" && cfg.PlatformURL != "" {
			r.URL = cfg.PlatformURL
			urlFromConfig = true
		}
		if r.LicenseKey == "" && storedLicense != nil {
			res, err := storedLicense()
			if err != nil {
				log.Printf("serve: license key lookup failed: %v", err)
			}
			if res.KeychainErr != nil {
				log.Printf("serve: OS keychain unavailable (%v); using the machine-tier file", res.KeychainErr)
			}
			if res.Value != "" {
				r.LicenseKey = res.Value
				switch res.Source {
				case keychain.SourceKeychain:
					licenseFromKeychain = true
				case keychain.SourceEnv:
					licenseFromFlagEnv = true
				default:
					licenseFromConfig = true
				}
			}
		}
	}

	switch {
	case !r.Configured():
		r.Source = platformSourceAbsent
	case licenseFromFlagEnv || (r.LicenseKey == "" && (urlFromFlagEnv || r.APIKey != "")):
		r.Source = platformSourceFlagEnv
	case licenseFromKeychain:
		r.Source = platformSourceKeychain
	case licenseFromConfig || urlFromConfig:
		r.Source = platformSourceConfig
	default:
		r.Source = platformSourceFlagEnv
	}
	return r
}
