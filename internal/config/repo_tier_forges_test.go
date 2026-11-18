package config

import (
	"strings"
	"testing"
)

// A repository tier declares a forge's kind and auth method, never where its
// credential goes or whether TLS is verified: those fields come from the
// machine tier only.
func TestLoadMergedStripsRepoTierForgeEndpoints(t *testing.T) {
	withMachineConfig(t, `
forges:
  corp:
    base_url: https://gitlab.corp.example
    token_env: CORP_GITLAB_TOKEN
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: o
forges:
  corp:
    kind: gitlab
    auth_method: pat
    base_url: http://repo-chosen.example
    graphql_url: http://repo-chosen.example/graphql
    host: repo-chosen.example
    proxy: http://repo-chosen.example:3128
    ca_bundle: repo-ca.pem
    token_env: GITHUB_TOKEN
    insecure_skip_tls: true
  extra:
    kind: github
    base_url: http://repo-only.example
`)
	writeLocalYAML(t, dir, `
forges:
  corp:
    insecure_skip_tls: true
    base_url: http://local-chosen.example
`)
	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	corp := cfg.Forges["corp"]
	if corp == nil {
		t.Fatal("forges.corp missing")
	}
	if corp.Kind != "gitlab" || corp.AuthMethod != "pat" {
		t.Errorf("repository-settable fields lost: %+v", corp)
	}
	if corp.BaseURL != "https://gitlab.corp.example" || corp.TokenEnv != "CORP_GITLAB_TOKEN" {
		t.Errorf("machine-tier endpoint not used: base_url=%q token_env=%q", corp.BaseURL, corp.TokenEnv)
	}
	if corp.InsecureSkipTLS || corp.GraphQLURL != "" || corp.Host != "" || corp.Proxy != "" || corp.CABundle != "" {
		t.Errorf("repository-tier endpoint field survived: %+v", corp)
	}
	if extra := cfg.Forges["extra"]; extra == nil || extra.BaseURL != "" {
		t.Errorf("forges.extra base_url from the project tier survived: %+v", extra)
	}
}

// Aliases and merge keys are resolved before the strip, so neither carries an
// endpoint past it.
func TestStripRepoTierForgeEndpointsResolvesAliases(t *testing.T) {
	out, removed := stripRepoTierForgeEndpoints([]byte(`
owner: o
defaults: &d
  base_url: http://repo-chosen.example
  token_env: GITHUB_TOKEN
forges:
  x:
    <<: *d
    kind: gitlab
`))
	if len(removed) == 0 {
		t.Fatal("nothing removed")
	}
	cfg, err := parseYAML(out)
	if err != nil {
		t.Fatalf("parse stripped: %v\n%s", err, out)
	}
	if x := cfg.Forges["x"]; x == nil || x.BaseURL != "" || x.TokenEnv != "" || x.Kind != "gitlab" {
		t.Fatalf("alias carried an endpoint past the strip: %+v\n%s", x, out)
	}
}

func TestLegacyJSONClearsForgeEndpoints(t *testing.T) {
	cfg := &Config{Forges: map[string]*ForgeConfigEntry{"x": {
		Kind: "gitlab", BaseURL: "http://repo.example", TokenEnv: "GITHUB_TOKEN", InsecureSkipTLS: true, Host: "h",
	}}}
	if err := ValidateLegacyJSONCredentials(cfg, "/repo/.nightgauge/config.json"); err != nil {
		t.Fatal(err)
	}
	if x := cfg.Forges["x"]; x.BaseURL != "" || x.TokenEnv != "" || x.InsecureSkipTLS || x.Host != "" || x.Kind != "gitlab" {
		t.Fatalf("legacy JSON forge endpoint survived: %+v", x)
	}
}

func TestValidateForgeConfigRequiresHTTPS(t *testing.T) {
	for _, tc := range []struct {
		entry ForgeConfigEntry
		want  string
	}{
		{ForgeConfigEntry{Kind: "gitlab", BaseURL: "http://example.com"}, "base_url must use https://"},
		{ForgeConfigEntry{Kind: "github", BaseURL: "ftp://example.com"}, "base_url must use https://"},
		{ForgeConfigEntry{Kind: "github", BaseURL: "https://u:p@example.com"}, "must not carry credentials"},
		{ForgeConfigEntry{Kind: "github", GraphQLURL: "http://example.com/graphql"}, "graphql_url must use https://"},
		{ForgeConfigEntry{Kind: "gitlab", BaseURL: "https://gitlab.example.com", Host: "evil.example/path"}, "host must be a host name"},
	} {
		err := ValidateForgeConfig(map[string]*ForgeConfigEntry{"x": &tc.entry}, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.entry, err, tc.want)
		}
	}
	for _, ok := range []ForgeConfigEntry{
		{Kind: "gitlab", BaseURL: "https://gitlab.example.com"},
		{Kind: "gitlab", BaseURL: "http://127.0.0.1:8080"},
		{Kind: "gitlab", BaseURL: "http://localhost:8080"},
		{Kind: "gitlab", BaseURL: "http://[::1]:8080"},
		{Kind: "github", Host: "github.example.com:8443"},
	} {
		if err := ValidateForgeConfig(map[string]*ForgeConfigEntry{"x": &ok}, nil); err != nil {
			t.Errorf("%+v rejected: %v", ok, err)
		}
	}
}
