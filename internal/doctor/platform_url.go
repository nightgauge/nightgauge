package doctor

// The `platform_url` check (#1474).
//
// The platform endpoint can be moved by NIGHTGAUGE_PLATFORM_URL, by the
// machine-tier platform.api_url, or (in VS Code) by the
// `nightgauge.platform.url` setting, which the extension forwards to the
// daemon as NIGHTGAUGE_PLATFORM_URL. Nothing said which one won, so an
// operator testing against another deployment could not confirm it from the
// CLI. The detail line always names the resolved URL and where it came from.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/platform"
)

const codePlatformURL = "NGD047"

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: "platform_url", Title: "Platform URL", Group: "config", Code: codePlatformURL,
		Run: func(ctx context.Context, env *Env) []Finding {
			fs, detail := platformURLFindings(os.Getenv, env.Cfg)
			env.SetDetail("platform_url", detail)
			return fs
		},
	})
}

// resolveDoctorPlatformURL applies the precedence `nightgauge serve` applies
// (resolvePlatformConfig): NIGHTGAUGE_PLATFORM_URL, then platform.api_url when
// platform.enabled is true, then the production default.
func resolveDoctorPlatformURL(getenv func(string) string, cfg *config.Config) (resolved, source string) {
	if v := strings.TrimSpace(getenv("NIGHTGAUGE_PLATFORM_URL")); v != "" {
		return v, "NIGHTGAUGE_PLATFORM_URL"
	}
	if cfg != nil && cfg.PlatformEnabled != nil && *cfg.PlatformEnabled && cfg.PlatformURL != "" {
		return cfg.PlatformURL, "platform.api_url"
	}
	return platform.DefaultConfig().BaseURL, "default"
}

// platformURLFindings reports the resolved platform URL in the detail line. It
// finds only a URL the platform client should not be handed: one that does
// not parse, or plain HTTP to a host other than localhost.
func platformURLFindings(getenv func(string) string, cfg *config.Config) ([]Finding, string) {
	const check = "platform_url"
	resolved, source := resolveDoctorPlatformURL(getenv, cfg)
	shown := resolved
	parsed, err := url.Parse(resolved)
	if err == nil {
		shown = parsed.Redacted()
	}

	// Non-default is judged by origin, so an explicit spelling of the
	// production URL is not flagged.
	detail := shown + " (default)"
	switch {
	case source == "default":
	case sameOrigin(resolved, platform.DefaultConfig().BaseURL):
		detail = fmt.Sprintf("%s (from %s)", shown, source)
	default:
		detail = fmt.Sprintf("%s (non-default, from %s)", shown, source)
	}
	if source == "default" && cfg != nil && cfg.PlatformURL != "" &&
		(cfg.PlatformEnabled == nil || !*cfg.PlatformEnabled) {
		detail += "; platform.api_url is set but ignored because platform.enabled is not true"
	}

	var reason string
	switch {
	case err != nil || parsed.Scheme == "" || parsed.Host == "":
		reason = "it is not an absolute URL"
	case parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()):
		reason = "it is not HTTPS and the host is not localhost"
	}
	if reason == "" {
		return nil, detail
	}
	return []Finding{newFinding(check, codePlatformURL, SeverityWarning,
		"platform-url-unsafe: the resolved platform URL is not a safe endpoint",
		fmt.Sprintf("%s, from %s, is unusable: %s. Platform calls would send the license key to it", shown, source, reason),
		map[string]string{"url": shown, "source": source},
		[]string{source},
		manualRemedy("fix", "Point the platform URL at an HTTPS endpoint", check,
			"Correct "+source+" (or unset it to use the default)",
			"Re-run `nightgauge doctor --only "+codePlatformURL+"`"))}, detail
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1"
}

func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil &&
		strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}
