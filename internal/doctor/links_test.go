package doctor

import "testing"

// #2095. ADR-025 § 8: remedy links open only for https on allowlisted hosts.
func TestAllowedLink(t *testing.T) {
	for u, want := range map[string]bool{
		"https://github.com/settings/apps":             true,
		"https://docs.github.com/en/authentication":    true,
		"https://nightgauge.dev/docs":                  true,
		"https://GitHub.com/x":                         true,
		"http://github.com/x":                          false,
		"https://github.com.evil.example/x":            false,
		"https://evil.example/https://github.com":      false,
		"https://user@github.com/x":                    false,
		"https://github.com:8443/x":                    false,
		"https://github.com/a b":                       false,
		"https://github.com/a\n--flag":                 false,
		"file:///etc/passwd":                           false,
		"javascript:alert(1)":                          false,
		"github.com/x":                                 false,
		"":                                             false,
		"-https://github.com/x":                        false,
		"https://api.github.com/repos/nightgauge/x":    false,
		"https://gist.github.com/someone/0123456789ab": false,
	} {
		if got := AllowedLink(u); got != want {
			t.Errorf("AllowedLink(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestDocsURL(t *testing.T) {
	if got := DocsURL(DocsAnchor("NGD017")); got != "https://github.com/nightgauge/nightgauge/blob/main/docs/DOCTOR.md#ngd017" {
		t.Errorf("DocsURL = %q", got)
	}
	if !AllowedLink(DocsURL(DocsAnchor("NGD017"))) {
		t.Error("the docs URL must itself be allowlisted")
	}
	if DocsURL("") != "" {
		t.Error("empty docs must give an empty URL")
	}
}
