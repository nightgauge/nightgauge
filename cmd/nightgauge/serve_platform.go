package main

import (
	"github.com/nightgauge/nightgauge/internal/config"
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
	// platformSourceConfig means nothing came from flag/env, but the merged
	// config file (project + machine + local tiers) supplied a value.
	platformSourceConfig platformConfigSource = "config"
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
// flagURL / flagAPIKey / flagLicenseKey are the cobra flag variables as
// bound by serveCmd, taken AFTER flag parsing. serveCmd registers each
// flag's *default* as os.Getenv(...) (e.g. `--license-key` defaults to
// NIGHTGAUGE_LICENSE_KEY), so an explicit --flag and its backing env var
// are already indistinguishable by the time RunE observes them: an empty
// string means neither was set, and a non-empty string means "flag or env,
// flag taking precedence when both are present" — cobra's own flag-parsing
// already enforces that ordering. This function therefore only has one real
// decision left to make: fall back to the merged config file's platform
// section when flag/env supplied nothing.
//
// cfg is the result of config.Load(workspaceRoot) — already merged across
// the machine (~/.nightgauge/config.yaml), project (.nightgauge/config.yaml),
// and local tiers, so a workspace config lacking a platform: section still
// picks up the developer's global license key. cfg may be nil (config.Load
// failed) — treated the same as "config has no platform section".
//
// There is no config-file source for the API key: the VSCode extension's
// PlatformConfigSchema (packages/nightgauge-vscode/src/config/schema.ts)
// has no platform.api_key field, only platform.api_url and
// platform.license_key — so API key resolution is flag/env only, unchanged
// from pre-#333 behavior.
func resolvePlatformConfig(flagURL, flagAPIKey, flagLicenseKey string, cfg *config.Config) resolvedPlatformConfig {
	r := resolvedPlatformConfig{URL: flagURL, APIKey: flagAPIKey, LicenseKey: flagLicenseKey}

	// licenseFromFlagEnv is captured before the config fallback below
	// mutates r.LicenseKey — it drives the Source label because LicenseKey
	// is the field both downstream gates (remote-command poller,
	// #330 Action Center bridge) actually check.
	licenseFromFlagEnv := r.LicenseKey != ""

	urlFromFlagEnv := r.URL != ""
	licenseFromConfig, urlFromConfig := false, false
	if cfg != nil {
		if r.URL == "" && cfg.PlatformURL != "" {
			r.URL = cfg.PlatformURL
			urlFromConfig = true
		}
		if r.LicenseKey == "" && cfg.LicenseKey != "" {
			r.LicenseKey = cfg.LicenseKey
			licenseFromConfig = true
		}
	}

	switch {
	case !r.Configured():
		r.Source = platformSourceAbsent
	case licenseFromFlagEnv || (r.LicenseKey == "" && (urlFromFlagEnv || r.APIKey != "")):
		r.Source = platformSourceFlagEnv
	case licenseFromConfig || urlFromConfig:
		r.Source = platformSourceConfig
	default:
		r.Source = platformSourceFlagEnv
	}
	return r
}
