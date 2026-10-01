package doctor

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/platform"
)

func TestPlatformURLDetail(t *testing.T) {
	on, off := true, false
	def := platform.DefaultConfig().BaseURL
	cases := []struct {
		name   string
		env    map[string]string
		cfg    *config.Config
		detail string
	}{
		{"nothing set", nil, nil, def + " (default)"},
		{"env wins over config", map[string]string{"NIGHTGAUGE_PLATFORM_URL": "https://staging.example.test"},
			&config.Config{PlatformEnabled: &on, PlatformURL: "https://config.example.test"},
			"https://staging.example.test (non-default, from NIGHTGAUGE_PLATFORM_URL)"},
		{"config when enabled", nil, &config.Config{PlatformEnabled: &on, PlatformURL: "https://config.example.test"},
			"https://config.example.test (non-default, from platform.api_url)"},
		{"config ignored when not enabled", nil, &config.Config{PlatformEnabled: &off, PlatformURL: "https://config.example.test"},
			def + " (default); platform.api_url is set but ignored because platform.enabled is not true"},
		{"production spelled explicitly is not non-default", nil,
			&config.Config{PlatformEnabled: &on, PlatformURL: def + "/"},
			def + "/ (from platform.api_url)"},
		{"localhost http is fine", map[string]string{"NIGHTGAUGE_PLATFORM_URL": "http://localhost:8787"}, nil,
			"http://localhost:8787 (non-default, from NIGHTGAUGE_PLATFORM_URL)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, detail := platformURLFindings(envOf(tc.env), tc.cfg)
			if detail != tc.detail {
				t.Errorf("detail = %q, want %q", detail, tc.detail)
			}
			if len(fs) != 0 {
				t.Errorf("findings = %s, want none", findingsText(fs))
			}
		})
	}
}

func TestPlatformURLUnsafeFindings(t *testing.T) {
	for _, u := range []string{"http://staging.example.test", "not a url", "https://user:secret@staging.example.test"} {
		t.Run(u, func(t *testing.T) {
			fs, detail := platformURLFindings(envOf(map[string]string{"NIGHTGAUGE_PLATFORM_URL": u}), nil)
			if strings.Contains(detail, "secret") {
				t.Errorf("detail leaks userinfo: %q", detail)
			}
			if strings.HasPrefix(u, "https://") {
				if len(fs) != 0 {
					t.Errorf("https URL produced findings: %s", findingsText(fs))
				}
				return
			}
			if len(fs) != 1 || fs[0].Code != codePlatformURL || fs[0].Severity != SeverityWarning {
				t.Fatalf("findings = %s, want one %s warning", findingsText(fs), codePlatformURL)
			}
			if fs[0].Evidence["source"] != "NIGHTGAUGE_PLATFORM_URL" {
				t.Errorf("source evidence = %q", fs[0].Evidence["source"])
			}
		})
	}
}
