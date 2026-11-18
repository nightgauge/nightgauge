package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
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

// A repository tier may not use a YAML merge key or a top-level alias: the
// strips read literal keys and the final parse resolves both, so either could
// bring a stripped block back. Each form is refused, naming the file and the
// rule, and none reaches the resolved forges config.
func TestLoadMergedRefusesRepoTierMergeKeys(t *testing.T) {
	cases := map[string]string{
		"merge-key-inline": `
<<: {forges: {corp: {kind: gitlab, base_url: "http://repo-chosen.example", token_env: GITHUB_TOKEN}}}
`,
		"merge-key-via-anchor": `
x-evil: &evil
  forges:
    corp:
      base_url: http://repo-chosen.example
      token_env: GITHUB_TOKEN
<<: *evil
`,
		"nested merge key": `
x-d: &d {base_url: "http://repo-chosen.example", token_env: GITHUB_TOKEN}
forges:
  corp:
    <<: *d
    kind: gitlab
`,
		"tagged merge key": `
x-d: &d {platform: {url: "http://repo-chosen.example"}}
? !!merge <<
: *d
`,
		"root-alias": `
x-f: &f
  corp: {base_url: "http://repo-chosen.example", token_env: GITHUB_TOKEN}
forges: *f
`,
	}
	for name, body := range cases {
		for _, tier := range []string{"project", "local"} {
			t.Run(name+"/"+tier, func(t *testing.T) {
				withMachineConfig(t, "forges:\n  corp:\n    base_url: https://gitlab.corp.example\n")
				dir := t.TempDir()
				doc := "schema_version: \"2\"\nowner: o\n" + body
				want := ProjectConfigPath(dir)
				if tier == "project" {
					writeProjectYAML(t, dir, doc)
				} else {
					writeProjectYAML(t, dir, "schema_version: \"2\"\nowner: o\n")
					writeLocalYAML(t, dir, body)
					want = LocalConfigPath(dir)
				}
				cfg, err := LoadMerged(dir)
				if err == nil {
					t.Fatalf("loaded; forges.corp = %+v", cfg.Forges["corp"])
				}
				if !errors.Is(err, ErrRepoTierMergeKey) {
					t.Fatalf("error is not ErrRepoTierMergeKey: %v", err)
				}
				if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "merge key") {
					t.Fatalf("error does not name the file and the rule: %v", err)
				}
			})
		}
	}
}

// A normal repository document, including nested anchors used as plain
// values, still loads.
func TestLoadMergedAcceptsRepoTierWithoutMergeKeys(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: o
autonomous:
  enabled_repos: &repos [a, b]
forges:
  corp: {kind: gitlab, auth_method: pat}
`)
	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Forges["corp"] == nil || cfg.Forges["corp"].Kind != "gitlab" {
		t.Fatalf("forges.corp = %+v", cfg.Forges["corp"])
	}
}

// The operator's machine tier may still use merge keys and anchors.
func TestLoadMergedMachineTierMayUseMergeKeys(t *testing.T) {
	withMachineConfig(t, `
x-corp: &corp
  base_url: https://gitlab.corp.example
  token_env: CORP_TOKEN
forges:
  corp:
    <<: *corp
    kind: gitlab
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, "schema_version: \"2\"\nowner: o\n")
	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if c := cfg.Forges["corp"]; c == nil || c.BaseURL != "https://gitlab.corp.example" || c.TokenEnv != "CORP_TOKEN" {
		t.Fatalf("machine-tier merge key not honoured: %+v", c)
	}
}

// The strips and the merge fail closed on their own, whatever runs before
// them.
func TestStripsAndMergeRefuseMergeKeys(t *testing.T) {
	doc := []byte("owner: o\nx: &x {platform: {url: u}}\n<<: *x\n")
	if out := removeTopLevelYAMLKey(doc, "platform"); len(out) != 0 {
		t.Fatalf("removeTopLevelYAMLKey kept a merge-key document:\n%s", out)
	}
	if out, _ := stripRepoTierForgeEndpoints(doc); len(out) != 0 {
		t.Fatalf("stripRepoTierForgeEndpoints kept a merge-key document:\n%s", out)
	}
	if _, err := mergeYAMLDocuments(nil, doc, nil); !errors.Is(err, ErrRepoTierMergeKey) {
		t.Fatalf("mergeYAMLDocuments accepted a project-tier merge key: %v", err)
	}
	if _, err := mergeYAMLDocuments(doc, nil, nil); err != nil {
		t.Fatalf("mergeYAMLDocuments refused a machine-tier merge key: %v", err)
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

// Tiers are resolved one at a time before they are merged, so a repository
// tier writing to a key the machine tier anchored cannot change the value the
// machine tier aliased under a protected key.
func TestRepoTierCannotRewriteMachineTierAnchor(t *testing.T) {
	withMachineConfig(t, `
x-shared: &m
  kind: gitlab
  base_url: https://gitlab.corp.example
  token_env: CORP_TOKEN
forges:
  corp: *m
`)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: o
x-shared:
  base_url: http://repo-chosen.example
  token_env: GITHUB_TOKEN
`)
	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	c := cfg.Forges["corp"]
	if c == nil || c.BaseURL != "https://gitlab.corp.example" || c.TokenEnv != "CORP_TOKEN" {
		t.Fatalf("a repository-tier key rewrote the machine tier's aliased forge: %+v", c)
	}
}

func TestResolveYAMLTierFoldsMergeKeysAndBoundsAliases(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("a: &a {x: 1, y: 2}\nb:\n  <<: *a\n  y: 3\n"), &doc); err != nil {
		t.Fatal(err)
	}
	root, err := resolveYAMLTier(doc.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]int
	if err := root.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["b"]["x"] != 1 || got["b"]["y"] != 3 || hasYAMLMergeKey(root) {
		t.Fatalf("merge key not folded with explicit keys winning: %v", got)
	}
	laughs := "a: &a [x, x, x, x, x, x, x, x, x, x]\n"
	prev := "a"
	for _, n := range []string{"b", "c", "d", "e", "f", "g", "h"} {
		laughs += fmt.Sprintf("%s: &%s [*%s, *%s, *%s, *%s, *%s, *%s, *%s, *%s, *%s, *%s]\n", n, n, prev, prev, prev, prev, prev, prev, prev, prev, prev, prev)
		prev = n
	}
	var bomb yaml.Node
	if err := yaml.Unmarshal([]byte(laughs), &bomb); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveYAMLTier(bomb.Content[0]); !errors.Is(err, errYAMLTooLarge) {
		t.Fatalf("exponential aliases were not bounded: %v", err)
	}
}
