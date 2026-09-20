package config

import (
	"strings"
	"testing"
)

// TestOpenCodeEndpointsMachineTierOnly: a project-tier YAML declaring
// opencode.endpoints is refused the same way as every other opencode: key
// (LoadOpenCodeConfig already checks for the parent key's presence, not
// per-child-key), and the same block in the machine tier parses into the
// declared endpoints, decoded strictly.
func TestOpenCodeEndpointsMachineTierOnly(t *testing.T) {
	withMachineConfig(t, `
opencode:
  endpoints:
    - id: mtplx
      provider: openai-compatible
      base_url: http://127.0.0.1:4141/v1
      limit:
        context: 262144
        output: 32000
    - id: mtplx-remote
      provider: openai-compatible
      base_url: http://10.0.0.9:4141/v1
      allow_lan: true
      limit:
        context: 262144
        output: 32000
      api_key_env: MTPLX_REMOTE_API_KEY
      max_concurrency: 2
      models:
        - id: qwen/qwen3.8-27b
          variants: [low, medium, xhigh]
`)
	machinePath, _ := machineConfigPathFn()

	worktree := t.TempDir()
	project := writeProjectYAML(t, worktree, "owner: nightgauge\nopencode:\n  endpoints:\n    - id: evil\n      provider: openai-compatible\n      base_url: http://127.0.0.1:1/v1\n")
	_, err := LoadOpenCodeConfig(worktree)
	if err == nil {
		t.Fatal("a committed opencode.endpoints declaration was accepted")
	}
	for _, want := range []string{project, machinePath, "committed repository config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	cfg, err := LoadOpenCodeConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Endpoints) != 2 {
		t.Fatalf("Endpoints = %+v, want 2 entries", cfg.Endpoints)
	}
	first, second := cfg.Endpoints[0], cfg.Endpoints[1]
	if first.ID != "mtplx" || first.Provider != "openai-compatible" || first.BaseURL != "http://127.0.0.1:4141/v1" {
		t.Errorf("first endpoint = %+v", first)
	}
	if second.ID != "mtplx-remote" || !second.AllowLAN || second.APIKeyEnv != "MTPLX_REMOTE_API_KEY" || second.MaxConcurrency != 2 {
		t.Errorf("second endpoint = %+v", second)
	}
	if len(second.Models) != 1 || second.Models[0].ID != "qwen/qwen3.8-27b" || len(second.Models[0].Variants) != 3 {
		t.Errorf("second endpoint models = %+v", second.Models)
	}
}

// TestOpenCodeEndpointsRejectsUnknownKeys: a misspelled key inside an
// endpoints[] entry is reported, using the same strict decode as the rest of
// the opencode: block.
func TestOpenCodeEndpointsRejectsUnknownKeys(t *testing.T) {
	withMachineConfig(t, "opencode:\n  endpoints:\n    - id: mtplx\n      provider: openai-compatible\n      base_url: http://127.0.0.1:4141/v1\n      bogus_key: true\n")
	_, err := LoadOpenCodeConfig("")
	if err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("err = %v, want one naming the unknown key", err)
	}
}
