package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// machineConcurrencyYAML is the machine-tier block from #2510.
const machineConcurrencyYAML = `
concurrency:
  workspace_max: 3
  per_repo_max: 1
  repository_overrides:
    nightgauge/nightgauge: 2
`

const nestedProjectYAML = `
schema_version: "2"
owner: nightgauge
`

// TestLoadMergedKeepsMachineConcurrency pins #2510: the parser dropped the
// concurrency block, so ResolveConcurrency always returned the defaults.
func TestLoadMergedKeepsMachineConcurrency(t *testing.T) {
	withMachineConfig(t, machineConcurrencyYAML)
	dir := t.TempDir()
	writeProjectYAML(t, dir, nestedProjectYAML)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Concurrency == nil {
		t.Fatal("machine-tier concurrency block was dropped")
	}
	rc := ResolveConcurrency(cfg)
	if rc.WorkspaceMax != 3 {
		t.Errorf("WorkspaceMax = %d, want 3", rc.WorkspaceMax)
	}
	if got := rc.CapForRepo("nightgauge/nightgauge"); got != 2 {
		t.Errorf("CapForRepo(nightgauge/nightgauge) = %d, want 2", got)
	}

	out, err := Render(cfg, "concurrency", false, false)
	if err != nil {
		t.Fatalf("Render concurrency: %v", err)
	}
	if !strings.Contains(out, "per_repo_max") {
		t.Errorf("rendered concurrency lacks per_repo_max:\n%s", out)
	}
}

func TestParseYAMLFlatKeepsConcurrency(t *testing.T) {
	cfg, err := parseYAML([]byte("owner: nightgauge\nrepo: nightgauge\n" + machineConcurrencyYAML))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	if got := ResolveConcurrency(cfg).CapForRepo("nightgauge/nightgauge"); got != 2 {
		t.Errorf("flat format: CapForRepo = %d, want 2", got)
	}
}

func TestLoadMergedLocalConcurrencyOverridesMachine(t *testing.T) {
	withMachineConfig(t, machineConcurrencyYAML)
	dir := t.TempDir()
	writeProjectYAML(t, dir, nestedProjectYAML)
	local := filepath.Join(dir, ".nightgauge", "config.local.yaml")
	if err := os.WriteFile(local, []byte("concurrency:\n  per_repo_max: 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	rc := ResolveConcurrency(cfg)
	if rc.PerRepoMax != 4 {
		t.Errorf("PerRepoMax = %d, want 4 from the local tier", rc.PerRepoMax)
	}
	if got := rc.CapForRepo("nightgauge/nightgauge"); got != 2 {
		t.Errorf("machine override lost in merge: CapForRepo = %d, want 2", got)
	}
}

func TestLoadMergedWarnsWhenProjectShadowsMachineConcurrency(t *testing.T) {
	resetShadowWarnDedup()
	withMachineConfig(t, machineConcurrencyYAML)
	dir := t.TempDir()
	writeProjectYAML(t, dir, nestedProjectYAML+"concurrency:\n  per_repo_max: 2\n")

	var cfg *Config
	logged := captureLog(t, func() {
		var err error
		if cfg, err = LoadMerged(dir); err != nil {
			t.Fatalf("LoadMerged: %v", err)
		}
	})
	if !strings.Contains(logged, "concurrency") || !strings.Contains(logged, "machine tier") {
		t.Errorf("expected shadow warning for concurrency, got:\n%s", logged)
	}
	if got := ResolveConcurrency(cfg).PerRepoMax; got != 2 {
		t.Errorf("project block not honoured: PerRepoMax = %d, want 2", got)
	}
}

func TestTierAuditClassifiesConcurrencyAsMachine(t *testing.T) {
	withMachineConfig(t, machineConcurrencyYAML)
	dir := t.TempDir()
	writeProjectYAML(t, dir, nestedProjectYAML)

	entries, err := BuildAuditReport(dir)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Key, "concurrency.") {
			continue
		}
		found = true
		if e.TargetTier != "machine" || e.Status != "OK" {
			t.Errorf("%s: target=%q status=%q, want machine/OK", e.Key, e.TargetTier, e.Status)
		}
	}
	if !found {
		t.Fatal("no concurrency.* rows in the tier audit")
	}
}

// TestEveryConfigKeyIsParsed guards the #2510 class: a top-level Config block
// whose yaml key is missing from a parse struct is silently discarded.
func TestEveryConfigKeyIsParsed(t *testing.T) {
	// The nested format reads telemetry from under platform:, not the root.
	nestedElsewhere := map[string]bool{"telemetry": true}

	tags := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0]
			if name != "" && name != "-" {
				out[name] = true
			}
		}
		return out
	}
	nested, flat := tags(yamlConfigNested{}), tags(yamlConfigFlat{})
	for key := range tags(Config{}) {
		if !nested[key] && !nestedElsewhere[key] {
			t.Errorf("Config key %q has no field in yamlConfigNested", key)
		}
		if !flat[key] {
			t.Errorf("Config key %q has no field in yamlConfigFlat", key)
		}
	}
}

// TestLoadMergedStripsRepoTierUsers: users is an identity map read by inbound
// auth, so a repository tier must never supply it (#2510).
func TestLoadMergedStripsRepoTierUsers(t *testing.T) {
	cfg, err := parseYAMLNested([]byte("owner: o\nrepo: r\nusers:\n  - mattermost_user_id: u1\n    github_login: a\n"))
	if err != nil || len(cfg.Users) != 1 {
		t.Fatalf("parse users: %v %+v", err, cfg)
	}
	if !isHardStrippedMachineKey("users") {
		t.Fatal("users is not hard-stripped from repository tiers")
	}
}

// TestLegacyJSONStripsUsers: the legacy config.json path clears users too.
func TestLegacyJSONStripsUsers(t *testing.T) {
	cfg := &Config{Users: []UserMappingEntry{{MattermostUserID: "u1", GitHubLogin: "a"}}}
	if err := ValidateLegacyJSONCredentials(cfg, "/repo/.nightgauge/config.json"); err != nil {
		t.Fatal(err)
	}
	if cfg.Users != nil {
		t.Fatalf("users survived the legacy JSON tier: %+v", cfg.Users)
	}
}
