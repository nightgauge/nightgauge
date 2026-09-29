package doctor

import (
	"net/url"
	"strings"
)

// linkHosts is the closed allowlist of hosts a surface may open a remedy link
// on (ADR-025 § 8). Finding text can derive from repository content, so any
// other host renders as text and is never opened. Adding a host is a reviewed
// change.
var linkHosts = map[string]bool{
	"github.com":      true,
	"docs.github.com": true,
	"nightgauge.dev":  true,
}

// AllowedLink reports whether raw may be opened: an absolute https URL with no
// user info, no explicit port, and a host on the allowlist.
func AllowedLink(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	if u.Port() != "" {
		return false
	}
	return linkHosts[strings.ToLower(u.Hostname())]
}

// DocsURL is the published page for a finding's docs anchor
// ("docs/DOCTOR.md#ngd014"), or "" when docs is empty.
func DocsURL(docs string) string {
	if docs == "" {
		return ""
	}
	return "https://github.com/nightgauge/nightgauge/blob/main/" + strings.TrimPrefix(docs, "/")
}
