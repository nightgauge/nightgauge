package adapters

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// fixedOpenCodeSettings stands in for the machine-tier `opencode:` block, so
// no test reads the machine's real config.
func fixedOpenCodeSettings(cfg config.OpenCodeConfig) func(string) (config.OpenCodeConfig, error) {
	return func(string) (config.OpenCodeConfig, error) { return cfg, nil }
}

// lmStudioSettings is the reference machine: one LM Studio on loopback with a
// 131072-token window loaded and 8192 tokens a reply.
func lmStudioSettings() config.OpenCodeConfig {
	return config.OpenCodeConfig{
		Provider: "lm-studio",
		BaseURL:  "http://127.0.0.1:1234/v1",
		Limit:    config.OpenCodeLimit{Context: 131072, Output: 8192},
	}
}

// goldenRunRoot is a run root that is never created: BuildOpenCodeConfig only
// names files in it.
const goldenRunRoot = "/nightgauge-test-home/.nightgauge/opencode/runs/" + testRunID

// buildOpenCodeConfigFor builds the config a dispatch of run gets on a machine
// whose `opencode:` block is settings, with env as the environment.
func buildOpenCodeConfigFor(t *testing.T, settings config.OpenCodeConfig, run RunOptions, env map[string]string) (OpenCodeRunConfig, error) {
	t.Helper()
	in, err := OpenCodeConfigInputFor(settings, run, goldenRunRoot, envLookup(env))
	if err != nil {
		return OpenCodeRunConfig{}, err
	}
	return BuildOpenCodeConfig(in)
}

// decodeOpenCodeConfig decodes built content into generic JSON.
func decodeOpenCodeConfig(t *testing.T, content string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("the config content is not JSON: %v\n%s", err, content)
	}
	return doc
}

// jsonPath walks doc along keys and returns what it finds, or nil.
func jsonPath(doc map[string]any, keys ...string) any {
	var cur any = doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

// TestOpenCodeConfigGolden pins the whole per-run config for the reference
// dispatch: a feature-dev stage with a 40-turn cap on the reference LM Studio.
// Every key the builder sets is in the golden, so dropping one (share,
// small_model, a limit, a steps cap) changes it. The content is compared byte
// for byte after the golden's whitespace is removed.
//
//	NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/adapters/ -run TestOpenCodeConfigGolden
func TestOpenCodeConfigGolden(t *testing.T) {
	built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{
		Stage:    "feature-dev",
		Model:    "lmstudio/qwen/qwen3.8-27b",
		MaxTurns: 40,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "opencode-config", "lmstudio-feature-dev.json")
	if os.Getenv("NIGHTGAUGE_UPDATE_GOLDEN") == "1" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, []byte(built.Content), "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read the golden: %v", err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, raw); err != nil {
		t.Fatalf("the golden is not JSON: %v", err)
	}
	if built.Content != want.String() {
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, []byte(built.Content), "", "  ")
		t.Errorf("the per-run config differs from %s; if the change is intended, regenerate it with NIGHTGAUGE_UPDATE_GOLDEN=1. Got:\n%s", golden, pretty.String())
	}

	file := filepath.Join(goldenRunRoot, "nightgauge", "lmstudio.base-url")
	if got := built.Files; len(got) != 1 || got[file] != "http://127.0.0.1:1234/v1" {
		t.Errorf("Files = %q, want the endpoint's base URL at %s and nothing else", got, file)
	}
	if built.NonLoopback {
		t.Error("a loopback endpoint was flagged non_loopback")
	}
}

// TestOpenCodeConfigRefusesAZeroLimit: LM Studio reports a context limit of
// 0, and OpenCode never compacts a session whose limit is 0, so a stage would
// run into the loaded window. A limit of 0, or one that is missing, refuses
// the config with an error naming the key and saying what to set.
func TestOpenCodeConfigRefusesAZeroLimit(t *testing.T) {
	run := RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40}
	for name, tc := range map[string]struct {
		limit config.OpenCodeLimit
		key   string
	}{
		"context 0":       {config.OpenCodeLimit{Context: 0, Output: 8192}, "opencode.limit.context"},
		"output 0":        {config.OpenCodeLimit{Context: 131072, Output: 0}, "opencode.limit.output"},
		"both missing":    {config.OpenCodeLimit{}, "opencode.limit.context"},
		"negative output": {config.OpenCodeLimit{Context: 131072, Output: -1}, "opencode.limit.output"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := lmStudioSettings()
			settings.Limit = tc.limit
			built, err := buildOpenCodeConfigFor(t, settings, run, nil)
			if err == nil {
				t.Fatalf("a config was built with limit %+v: %s", tc.limit, built.Content)
			}
			for _, want := range []string{tc.key, "endpoint lmstudio", "never", "~/.nightgauge/config.yaml"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
		})
	}

	settings := lmStudioSettings()
	settings.Limit = config.OpenCodeLimit{Context: 8192, Output: 8192}
	if _, err := buildOpenCodeConfigFor(t, settings, run, nil); err == nil || !strings.Contains(err.Error(), "must be less than") {
		t.Errorf("an output limit as large as the window was accepted: %v", err)
	}
}

// TestOpenCodeConfigStepsCap: the build agent and every subagent get the
// stage's turn cap as their steps cap, so a stage cannot loop forever, and
// the legacy mode.build entry carries it too, because opencode 1.18.30 applies
// mode.build over agent.build after every config layer. With no turn cap the
// documented default applies. subagent_depth is always set.
func TestOpenCodeConfigStepsCap(t *testing.T) {
	for _, tc := range []struct {
		maxTurns, want int
	}{{40, 40}, {0, openCodeDefaultSteps}, {-3, openCodeDefaultSteps}} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: tc.maxTurns}, nil)
		if err != nil {
			t.Fatal(err)
		}
		doc := decodeOpenCodeConfig(t, built.Content)
		for _, path := range [][]string{
			{"agent", "build"}, {"agent", "plan"}, {"agent", "general"}, {"agent", "explore"}, {"mode", "build"},
		} {
			if got := jsonPath(doc, append(path, "steps")...); got != float64(tc.want) {
				t.Errorf("MaxTurns %d: %s.steps = %v, want %d", tc.maxTurns, strings.Join(path, "."), got, tc.want)
			}
		}
		if got := jsonPath(doc, "subagent_depth"); got != float64(openCodeSubagentDepth) {
			t.Errorf("subagent_depth = %v, want %d", got, openCodeSubagentDepth)
		}
	}
	if openCodeDefaultSteps <= 0 {
		t.Fatal("the default steps cap must bound a stage")
	}
}

// TestOpenCodeConfigPinsEveryModel (ADR-022 § 15): small_model and every
// built-in agent's model are the dispatched model, the title agent is off so
// no title request is sent, the stage runs on the build agent, no agent can be
// disabled from below, and OpenCode loads the dispatched provider alone.
// Observed on 1.18.30, without enabled_providers a run holding the forge
// tokens and cloud credentials a stage keeps loads amazon-bedrock,
// github-copilot, gitlab, google-vertex and OpenCode's own free provider.
func TestOpenCodeConfigPinsEveryModel(t *testing.T) {
	for _, tc := range []struct {
		model, key string
		env        map[string]string
	}{
		{"lmstudio/qwen/qwen3.8-27b", "lmstudio", nil},
		{"anthropic/claude-sonnet-5", "anthropic", map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"}},
		{"openai/gpt-5.5", "openai", nil},
	} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: tc.model}, tc.env)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		doc := decodeOpenCodeConfig(t, built.Content)
		for _, key := range []string{"model", "small_model"} {
			if doc[key] != tc.model {
				t.Errorf("%s: %s = %v, want the dispatched model", tc.model, key, doc[key])
			}
		}
		agents, _ := doc["agent"].(map[string]any)
		for _, name := range []string{"build", "plan", "general", "explore", "title", "summary", "compaction"} {
			agent, _ := agents[name].(map[string]any)
			if agent["model"] != tc.model {
				t.Errorf("%s: agent.%s.model = %v, want the dispatched model", tc.model, name, agent["model"])
			}
			wantDisabled := name == "title"
			if agent["disable"] != wantDisabled {
				t.Errorf("%s: agent.%s.disable = %v, want %v", tc.model, name, agent["disable"], wantDisabled)
			}
		}
		if doc["default_agent"] != "build" {
			t.Errorf("%s: default_agent = %v, want build", tc.model, doc["default_agent"])
		}
		enabled, _ := doc["enabled_providers"].([]any)
		if len(enabled) != 1 || enabled[0] != tc.key {
			t.Errorf("%s: enabled_providers = %v, want [%s] alone", tc.model, enabled, tc.key)
		}
		providers, _ := doc["provider"].(map[string]any)
		for key := range providers {
			if key != tc.key {
				t.Errorf("%s: the config injects a block for %s, which the stage does not dispatch to", tc.model, key)
			}
		}
		for key, want := range map[string]any{"share": "disabled", "autoupdate": false, "snapshot": false, "lsp": true, "formatter": true} {
			if doc[key] != want {
				t.Errorf("%s: %s = %v, want %v", tc.model, key, doc[key], want)
			}
		}
		if got, _ := doc["instructions"].([]any); got == nil || len(got) != 0 {
			t.Errorf("%s: instructions = %v, want []", tc.model, doc["instructions"])
		}
		if got, _ := jsonPath(doc, "skills", "urls").([]any); got == nil || len(got) != 0 {
			t.Errorf("%s: skills.urls = %v, want []", tc.model, jsonPath(doc, "skills", "urls"))
		}
	}
}

// TestOpenCodeConfigCredentialsAreReferences (ADR-022 § 17): an anthropic/
// stage's key is read by OpenCode from ANTHROPIC_API_KEY through the reference
// {env:ANTHROPIC_API_KEY}; its value appears nowhere in the config.
func TestOpenCodeConfigCredentialsAreReferences(t *testing.T) {
	const secret = "fake-anthropic-credential-1625"
	built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: "anthropic/claude-sonnet-5", MaxTurns: 40},
		map[string]string{"ANTHROPIC_API_KEY": secret})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(built.Content, secret) {
		t.Errorf("the credential's value is in the config content:\n%s", built.Content)
	}
	for _, content := range built.Files {
		if strings.Contains(content, secret) {
			t.Error("the credential's value is in a file the config refers to")
		}
	}
	doc := decodeOpenCodeConfig(t, built.Content)
	if got := jsonPath(doc, "provider", "anthropic", "options", "apiKey"); got != "{env:ANTHROPIC_API_KEY}" {
		t.Errorf("provider.anthropic.options.apiKey = %v, want the reference {env:ANTHROPIC_API_KEY}", got)
	}
}

// TestOpenCodeAnthropicRequiresAPIKey: the builder refuses an anthropic/ model
// while ANTHROPIC_API_KEY is unset or empty, naming the variable, the same
// refusal PreDispatch makes, so the verb, which never calls PreDispatch,
// refuses it too. With the variable set the config is built.
func TestOpenCodeAnthropicRequiresAPIKey(t *testing.T) {
	run := RunOptions{Model: "anthropic/claude-sonnet-5", MaxTurns: 40}
	for _, env := range []map[string]string{nil, {"ANTHROPIC_API_KEY": ""}} {
		if built, err := buildOpenCodeConfigFor(t, config.OpenCodeConfig{}, run, env); err == nil {
			t.Errorf("with ANTHROPIC_API_KEY %v a config was built: %s", env, built.Content)
		} else if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY is not set") || !strings.Contains(err.Error(), "claude-headless") {
			t.Errorf("the refusal does not name the variable and the way out: %v", err)
		}
	}
	if _, err := buildOpenCodeConfigFor(t, config.OpenCodeConfig{}, run, map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"}); err != nil {
		t.Errorf("with ANTHROPIC_API_KEY set the config was refused: %v", err)
	}
}

// TestOpenCodeConfigProviderKeyedByEndpoint (ADR-022 § Endpoints): a provider
// block is keyed by its endpoint's id, not its kind, so two endpoints of one
// kind are two blocks. The flat machine-tier keys declare the endpoint
// `lmstudio`; an endpoint renamed `lab` is dispatched and keyed as `lab`.
func TestOpenCodeConfigProviderKeyedByEndpoint(t *testing.T) {
	endpoints, err := OpenCodeEndpoints(lmStudioSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].ID != "lmstudio" || endpoints[0].Provider != "lm-studio" {
		t.Fatalf("the flat keys declare %+v, want one lm-studio endpoint with id lmstudio", endpoints)
	}
	for _, id := range []string{"lmstudio", "lab"} {
		ep := endpoints[0]
		ep.ID = id
		built, err := BuildOpenCodeConfig(OpenCodeConfigInput{
			Run:       RunOptions{Model: id + "/qwen/qwen3.8-27b", MaxTurns: 40},
			Endpoints: []OpenCodeEndpoint{ep},
			RunRoot:   goldenRunRoot,
			Lookup:    envLookup(nil),
		})
		if err != nil {
			t.Fatalf("id %s: %v", id, err)
		}
		doc := decodeOpenCodeConfig(t, built.Content)
		providers, _ := doc["provider"].(map[string]any)
		if len(providers) != 1 || providers[id] == nil {
			t.Errorf("id %s: provider keys = %v, want [%s]", id, keysOf(providers), id)
		}
		if got := jsonPath(doc, "provider", id, "options", "baseURL"); got != "{file:"+filepath.Join(goldenRunRoot, "nightgauge", id+".base-url")+"}" {
			t.Errorf("id %s: baseURL = %v, want a reference to the endpoint's own file", id, got)
		}
		if got := jsonPath(doc, "provider", id, "models", "qwen/qwen3.8-27b", "limit", "context"); got != float64(131072) {
			t.Errorf("id %s: the model's limit.context = %v, want 131072", id, got)
		}
		for _, key := range []string{"npm", "env"} {
			if jsonPath(doc, "provider", id, key) == nil {
				t.Errorf("id %s: the endpoint block has no %s", id, key)
			}
		}
		if got := jsonPath(doc, "provider", id, "options", "apiKey"); got != "" {
			t.Errorf("id %s: apiKey = %v, want empty so no variable binds to the endpoint", id, got)
		}
	}
}

func keysOf(m map[string]any) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestOpenCodeConfigEndpointURL: an endpoint's base_url must be http or https
// with no user name or password; a host that is not this machine is accepted
// and flagged. The URL never reaches the config content or an error: the
// content refers to a private file, and errors name the endpoint.
func TestOpenCodeConfigEndpointURL(t *testing.T) {
	for _, tc := range []struct {
		url         string
		ok          bool
		nonLoopback bool
	}{
		{"http://127.0.0.1:1234/v1", true, false},
		{"http://localhost:1234/v1", true, false},
		{"http://[::1]:1234/v1", true, false},
		{"https://127.0.0.1:1234/v1", true, false},
		{"http://192.0.2.10:1234/v1", true, true},
		{"http://[2001:db8::10]:1234/v1", true, true},
		{"ftp://127.0.0.1:1234/v1", false, false},
		{"http://u:p@127.0.0.1:1234", false, false},
		{"http://u@192.0.2.10:1234", false, false},
		{"127.0.0.1:1234/v1", false, false},
		{"http:///v1", false, false},
		{"http://127.0.0.1:1234/v1?key=x", false, false},
		{"http://127.0.0.1:1234/v1 ", false, false},
	} {
		settings := lmStudioSettings()
		settings.BaseURL = tc.url
		built, err := buildOpenCodeConfigFor(t, settings, RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}, nil)
		if !tc.ok {
			if err == nil {
				t.Errorf("%s: accepted", tc.url)
			} else if strings.Contains(err.Error(), strings.TrimSpace(tc.url)) || strings.Contains(err.Error(), "192.0.2.10") {
				t.Errorf("%s: the error quotes the URL: %v", tc.url, err)
			} else if !strings.Contains(err.Error(), "endpoint lmstudio") {
				t.Errorf("%s: the error does not name the endpoint: %v", tc.url, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: refused: %v", tc.url, err)
			continue
		}
		if built.NonLoopback != tc.nonLoopback {
			t.Errorf("%s: NonLoopback = %v, want %v", tc.url, built.NonLoopback, tc.nonLoopback)
		}
		if strings.Contains(built.Content, strings.TrimSuffix(tc.url, "/v1")) {
			t.Errorf("%s: the URL is in the config content:\n%s", tc.url, built.Content)
		}
	}
}

// TestOpenCodeConfigRefusesAnUndeclaredLocalEndpoint: a model server the
// operator runs is described only by a declared endpoint, so a local provider
// key with none, or with one of another kind, is refused before anything
// uses OpenCode's own defaults for it (a context limit of 0 for LM Studio).
func TestOpenCodeConfigRefusesAnUndeclaredLocalEndpoint(t *testing.T) {
	ollama := config.OpenCodeConfig{Provider: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Limit: config.OpenCodeLimit{Context: 32768, Output: 4096}}
	for _, tc := range []struct {
		settings config.OpenCodeConfig
		model    string
	}{
		{config.OpenCodeConfig{}, "lmstudio/qwen/qwen3.8-27b"},
		{config.OpenCodeConfig{}, "ollama/qwen3-coder:30b"},
		{ollama, "lmstudio/qwen/qwen3.8-27b"},
		{lmStudioSettings(), "ollama/qwen3-coder:30b"},
	} {
		_, err := buildOpenCodeConfigFor(t, tc.settings, RunOptions{Model: tc.model}, nil)
		if err == nil || !strings.Contains(err.Error(), "declares no endpoint") || !strings.Contains(err.Error(), "opencode.limit.context") {
			t.Errorf("%s with %q declared: err = %v, want a refusal naming the missing endpoint", tc.model, tc.settings.Provider, err)
		}
	}
	if _, err := buildOpenCodeConfigFor(t, ollama, RunOptions{Model: "ollama/qwen3-coder:30b"}, nil); err != nil {
		t.Errorf("a declared ollama endpoint was refused: %v", err)
	}
}

// TestOpenCodeConfigRefusesABraceInTheModel: OpenCode substitutes {env:...}
// and {file:...} in its config text before parsing it, so a model id that
// carried one would send a secret's value as the model name.
func TestOpenCodeConfigRefusesABraceInTheModel(t *testing.T) {
	for _, model := range []string{"lmstudio/{env:GITHUB_TOKEN}", "openai/x{file:/etc/hosts}", "openai/}"} {
		if built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: model}, nil); err == nil {
			t.Errorf("%s: built %s", model, built.Content)
		}
	}
}

// TestOpenCodeConfigMaxTokensLowersTheOutputLimit: a stage's token cap lowers
// the endpoint's output limit and never raises it.
func TestOpenCodeConfigMaxTokensLowersTheOutputLimit(t *testing.T) {
	for _, tc := range []struct{ maxTokens, want int }{{0, 8192}, {4096, 4096}, {65536, 8192}} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", MaxTokens: tc.maxTokens}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := jsonPath(decodeOpenCodeConfig(t, built.Content), "provider", "lmstudio", "models", "qwen/qwen3.8-27b", "limit", "output")
		if got != float64(tc.want) {
			t.Errorf("MaxTokens %d: limit.output = %v, want %d", tc.maxTokens, got, tc.want)
		}
	}
}

// TestOpenCodeEndpointsValidateTheFlatKeys: a server the block describes must
// name its kind; the kinds are lm-studio and ollama; a timeout under a second
// is a unit mistake; an unset timeout gets the default sized for cold prefill.
func TestOpenCodeEndpointsValidateTheFlatKeys(t *testing.T) {
	if eps, err := OpenCodeEndpoints(config.OpenCodeConfig{InheritUserConfig: true, Model: "anthropic/claude-sonnet-5"}); err != nil || len(eps) != 0 {
		t.Errorf("a block declaring no server gave %v, %v", eps, err)
	}
	noKind := lmStudioSettings()
	noKind.Provider = ""
	if _, err := OpenCodeEndpoints(noKind); err == nil || !strings.Contains(err.Error(), "opencode.provider is not set") {
		t.Errorf("a server with no kind: %v", err)
	}
	vllm := lmStudioSettings()
	vllm.Provider = "vllm"
	if _, err := OpenCodeEndpoints(vllm); err == nil || !strings.Contains(err.Error(), "lm-studio or ollama") {
		t.Errorf("an unsupported kind: %v", err)
	}
	tiny := lmStudioSettings()
	tiny.Timeouts.Header = config.YAMLDuration(180000) // nanoseconds, meant as milliseconds
	if _, err := OpenCodeEndpoints(tiny); err == nil || !strings.Contains(err.Error(), "opencode.timeouts.header") {
		t.Errorf("a sub-second timeout: %v", err)
	}
	eps, err := OpenCodeEndpoints(lmStudioSettings())
	if err != nil {
		t.Fatal(err)
	}
	if eps[0].HeaderTimeout != openCodeDefaultTimeout || eps[0].ChunkTimeout != openCodeDefaultTimeout {
		t.Errorf("default timeouts = %s, %s; want %s", eps[0].HeaderTimeout, eps[0].ChunkTimeout, openCodeDefaultTimeout)
	}
	if openCodeDefaultTimeout <= 76*time.Second {
		t.Errorf("the default timeout %s does not cover the 76 s cold prefill measured on LM Studio", openCodeDefaultTimeout)
	}
	custom := lmStudioSettings()
	custom.Timeouts = config.OpenCodeTimeouts{Header: config.YAMLDuration(5 * time.Minute), Chunk: config.YAMLDuration(2 * time.Minute)}
	built, err := buildOpenCodeConfigFor(t, custom, RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeOpenCodeConfig(t, built.Content)
	if jsonPath(doc, "provider", "lmstudio", "options", "headerTimeout") != float64(300000) ||
		jsonPath(doc, "provider", "lmstudio", "options", "chunkTimeout") != float64(120000) {
		t.Errorf("configured timeouts did not reach the block in milliseconds: %v", jsonPath(doc, "provider", "lmstudio", "options"))
	}
}

// TestOpenCodeConfigOperatorOverrides: ADR-022 § 12 lets the operator turn
// snapshots on and lsp or formatter off on the machine tier.
func TestOpenCodeConfigOperatorOverrides(t *testing.T) {
	on, off := true, false
	settings := lmStudioSettings()
	settings.Snapshot, settings.LSP, settings.Formatter = &on, &off, &off
	built, err := buildOpenCodeConfigFor(t, settings, RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeOpenCodeConfig(t, built.Content)
	if doc["snapshot"] != true || doc["lsp"] != false || doc["formatter"] != false {
		t.Errorf("snapshot, lsp, formatter = %v, %v, %v; want the operator's true, false, false", doc["snapshot"], doc["lsp"], doc["formatter"])
	}
}

// TestPrepareOpenCodeRunKeepsTheBaseURLOutOfTheEnvironment: the run's
// environment carries the config, and the endpoint's base URL is only in a
// 0600 file in the run root's Nightgauge directory, which the config refers
// to. A refused config creates nothing on disk.
func TestPrepareOpenCodeRunKeepsTheBaseURLOutOfTheEnvironment(t *testing.T) {
	home := t.TempDir()
	req := OpenCodeRunRequest{
		Home:             home,
		ID:               testRunID,
		MachineConfigDir: filepath.Join(home, ".nightgauge"),
		Run:              RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40},
		Settings:         lmStudioSettings(),
		Lookup:           envLookup(nil),
		GOOS:             "linux",
	}

	refused := req
	refused.Settings.Limit.Context = 0
	if _, err := PrepareOpenCodeRun(refused); err == nil {
		t.Fatal("a zero context limit was accepted")
	}
	if _, err := os.Lstat(OpenCodeRunsDir(home)); !os.IsNotExist(err) {
		t.Errorf("a refused config created %s", OpenCodeRunsDir(home))
	}

	run, err := PrepareOpenCodeRun(req)
	if err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != OpenCodeConfigSchemaVersion || run.RunDir != filepath.Join(OpenCodeRunsDir(home), testRunID) {
		t.Errorf("run = %+v", run)
	}
	if run.Env["OPENCODE_CONFIG_CONTENT"] != run.ConfigContent || run.ConfigContent == "" {
		t.Error("the environment does not carry the config content")
	}
	if want := filepath.Join(run.RunDir, "config", "opencode", "plugin"); run.PluginDir != want {
		t.Errorf("PluginDir = %q, want %q", run.PluginDir, want)
	}
	for k, v := range run.Env {
		if strings.Contains(v, "127.0.0.1:1234") {
			t.Errorf("%s carries the endpoint's base URL", k)
		}
	}
	file := filepath.Join(run.RunDir, "nightgauge", "lmstudio.base-url")
	fi, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Errorf("%s has mode %s, want a regular 0600 file", file, fi.Mode())
	}
	if dir, err := os.Lstat(filepath.Dir(file)); err != nil || dir.Mode().Perm() != 0o700 {
		t.Errorf("the Nightgauge directory of the run root is not 0700: %v %v", dir, err)
	}
	if got, _ := os.ReadFile(file); string(got) != "http://127.0.0.1:1234/v1" {
		t.Errorf("%s holds %q", file, got)
	}
	if !strings.Contains(run.ConfigContent, "{file:"+file+"}") {
		t.Errorf("the config does not refer to %s:\n%s", file, run.ConfigContent)
	}

	// A link planted at the file's name is replaced, never followed.
	target := filepath.Join(t.TempDir(), "operator-file")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareOpenCodeRun(req); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("the write followed a link planted at %s", file)
	}
	if fi, _ := os.Lstat(file); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the planted link is still there")
	}
}
