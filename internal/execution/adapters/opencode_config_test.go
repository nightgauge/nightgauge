package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
	"github.com/nightgauge/nightgauge/internal/models"
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

// goldenWorktree is a worktree that is never created: BuildOpenCodeConfig
// only names files in it.
const goldenWorktree = "/nightgauge-test-worktree"

// goldenRepository is what the reference stage is given from its repository:
// its AGENTS.md and a file it imports, the baseline steering, and a local and
// a remote MCP server, translated from the pipeline's .mcp.json shape.
func goldenRepository(t *testing.T) codexprovision.OpenCodeProvision {
	t.Helper()
	servers, warnings := codexprovision.OpenCodeMcpServers(map[string]codexprovision.PipelineMcpServer{
		"fs":     {Command: "npx", Args: []string{"-y", "srv"}, Env: map[string]string{"LEVEL": "debug", "TOKEN": "${FIXTURE_TOKEN}"}},
		"remote": {Type: "http", URL: "https://mcp.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer ${FIXTURE_TOKEN}", "X-Team": "core"}},
	})
	if len(warnings) > 0 {
		t.Fatalf("the reference servers drew warnings: %v", warnings)
	}
	return codexprovision.OpenCodeProvision{
		Root:         goldenWorktree,
		Steering:     "# Nightgauge Pipeline Steering (OpenCode)\n\n## Key Rules\n\n- Never push directly to main",
		Instructions: []string{goldenWorktree + "/AGENTS.md", goldenWorktree + "/docs/rules.md"},
		MCP:          servers,
		McpSource:    "origin/main",
	}
}

// TestOpenCodeConfigGolden pins the whole per-run config for the reference
// dispatch: a feature-dev stage with a 40-turn cap on the reference LM
// Studio, in a repository with steering and MCP servers. Every key the
// builder sets is in the golden, so dropping one (share, small_model, a
// limit, a steps cap, an instructions entry, an MCP server's oauth) changes
// it. The content is compared byte for byte after the golden's whitespace is
// removed.
//
//	NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/adapters/ -run TestOpenCodeConfigGolden
func TestOpenCodeConfigGolden(t *testing.T) {
	in, err := OpenCodeConfigInputFor(lmStudioSettings(), RunOptions{
		Stage:       "feature-dev",
		Model:       "lmstudio/qwen/qwen3.8-27b",
		MaxTurns:    40,
		WorktreeDir: goldenWorktree,
	}, goldenRunRoot, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	in.Repository = goldenRepository(t)
	built, err := BuildOpenCodeConfig(in)
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
	steering := filepath.Join(goldenRunRoot, "nightgauge", "steering.md")
	if got := built.Files; len(got) != 2 || got[file] != "http://127.0.0.1:1234/v1" || got[steering] != in.Repository.Steering {
		t.Errorf("Files = %q, want the endpoint's base URL at %s, the steering at %s and nothing else", got, file, steering)
	}
	if built.NonLoopback {
		t.Error("a loopback endpoint was flagged non_loopback")
	}
}

// TestOpenCodeConfigRefusesAnUnresolvedLimit: LM Studio reports a context
// limit of 0 to OpenCode, and OpenCode never compacts a session whose limit is
// 0, so a stage would run into the loaded window. A limit the machine-tier
// config does not set and discovery cannot find refuses the config with an
// error naming the key, the reason discovery gave, and what to set. A
// negative limit, and an output limit as large as the window, are refused
// too.
func TestOpenCodeConfigRefusesAnUnresolvedLimit(t *testing.T) {
	run := RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40}
	build := func(limit config.OpenCodeLimit) (OpenCodeRunConfig, error) {
		settings := lmStudioSettings()
		settings.Limit = limit
		in, err := OpenCodeConfigInputFor(settings, run, goldenRunRoot, envLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		in.Discover = func(OpenCodeEndpoint, string) (models.LocalDescriptor, error) {
			return models.LocalDescriptor{}, errors.New("the server refused the connection")
		}
		return BuildOpenCodeConfig(in)
	}
	for name, tc := range map[string]struct {
		limit config.OpenCodeLimit
		key   string
	}{
		"context 0":    {config.OpenCodeLimit{Context: 0, Output: 8192}, "opencode.limit.context"},
		"output 0":     {config.OpenCodeLimit{Context: 131072, Output: 0}, "opencode.limit.output"},
		"both missing": {config.OpenCodeLimit{}, "opencode.limit.context"},
	} {
		t.Run(name, func(t *testing.T) {
			built, err := build(tc.limit)
			if err == nil {
				t.Fatalf("a config was built with limit %+v and nothing discovered: %s", tc.limit, built.Content)
			}
			for _, want := range []string{tc.key, "endpoint lmstudio", "the server refused the connection", "never", "~/.nightgauge/config.yaml"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
		})
	}

	for _, limit := range []config.OpenCodeLimit{{Context: 131072, Output: -1}, {Context: -1, Output: 8192}} {
		if _, err := build(limit); err == nil || !strings.Contains(err.Error(), "is negative") {
			t.Errorf("a negative limit %+v was accepted: %v", limit, err)
		}
	}
	if _, err := build(config.OpenCodeLimit{Context: 8192, Output: 8192}); err == nil || !strings.Contains(err.Error(), "less than opencode.limit.context") {
		t.Errorf("an output limit as large as the window was accepted: %v", err)
	}
}

// discoveredOpenCodeInput is the input of the reference dispatch on a machine
// whose endpoint override is limit, where the server reports desc for the
// dispatched model. It fails the test if discovery is asked about anything
// but the declared endpoint and the dispatched model.
func discoveredOpenCodeInput(t *testing.T, limit config.OpenCodeLimit, maxTokens int, desc models.LocalDescriptor, discoverErr error) OpenCodeConfigInput {
	t.Helper()
	settings := lmStudioSettings()
	settings.Limit = limit
	const model = "lmstudio/qwen/qwen3.8-27b"
	in, err := OpenCodeConfigInputFor(settings, RunOptions{Stage: "feature-dev", Model: model, MaxTokens: maxTokens}, goldenRunRoot, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	in.Discover = func(ep OpenCodeEndpoint, got string) (models.LocalDescriptor, error) {
		if ep.ID != "lmstudio" || ep.Provider != "lm-studio" || ep.BaseURL != settings.BaseURL || got != model {
			t.Errorf("discovery was asked for %q on endpoint %+v; want %s on the declared lmstudio endpoint", got, ep, model)
		}
		return desc, discoverErr
	}
	return in
}

// endpointModelLimit is the dispatched model's limit.<key> and the
// compaction reserve in built content.
func endpointModelLimit(t *testing.T, built OpenCodeRunConfig) (context, input, output, reserved any) {
	t.Helper()
	doc := decodeOpenCodeConfig(t, built.Content)
	limit := func(key string) any {
		return jsonPath(doc, "provider", "lmstudio", "models", "qwen/qwen3.8-27b", "limit", key)
	}
	return limit("context"), limit("input"), limit("output"), jsonPath(doc, "compaction", "reserved")
}

// TestOpenCodeConfigLimitsFromTheDescriptor: with no machine-tier override,
// the endpoint model's limits are the descriptor discovered from the server:
// its loaded window, and its output cap or, where the server reports none,
// the smaller of OpenCode's own 32000-token reply cap and a quarter of the
// window. An override of one limit keeps the other discovered.
func TestOpenCodeConfigLimitsFromTheDescriptor(t *testing.T) {
	lmStudio := models.LocalDescriptor{Endpoint: "lmstudio", Provider: "lm-studio", Model: "qwen/qwen3.8-27b", ContextWindow: 131072, ToolCall: true}
	capped := lmStudio
	capped.MaxOutput = 4096
	small := lmStudio
	small.ContextWindow = 16384
	for _, tc := range []struct {
		name            string
		limit           config.OpenCodeLimit
		maxTokens       int
		desc            models.LocalDescriptor
		context, output int
	}{
		{"no override", config.OpenCodeLimit{}, 0, lmStudio, 131072, 32000},
		{"the server's output cap", config.OpenCodeLimit{}, 0, capped, 131072, 4096},
		{"a small window", config.OpenCodeLimit{}, 0, small, 16384, 4096},
		{"the stage's token cap", config.OpenCodeLimit{}, 2048, lmStudio, 131072, 2048},
		{"an output override", config.OpenCodeLimit{Output: 8192}, 0, lmStudio, 131072, 8192},
		{"a context override below the window", config.OpenCodeLimit{Context: 65536}, 0, lmStudio, 65536, 16384},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, err := BuildOpenCodeConfig(discoveredOpenCodeInput(t, tc.limit, tc.maxTokens, tc.desc, nil))
			if err != nil {
				t.Fatal(err)
			}
			context, input, output, reserved := endpointModelLimit(t, built)
			if context != float64(tc.context) || input != float64(tc.context) || output != float64(tc.output) || reserved != float64(tc.output) {
				t.Errorf("limit.context %v, limit.input %v, limit.output %v, compaction.reserved %v; want %d, %d, %d, %d",
					context, input, output, reserved, tc.context, tc.context, tc.output, tc.output)
			}
			if len(built.Warnings) != 0 {
				t.Errorf("warnings for limits at or below the window: %q", built.Warnings)
			}
		})
	}
}

// TestOpenCodeConfigClampsTheContextOverride: a machine-tier context limit
// above the window the server has loaded is clamped to that window, because
// the server fails a request past it, and the warning says so, naming the
// endpoint by id and never its address. An override the server could not be
// asked about is used as it is.
func TestOpenCodeConfigClampsTheContextOverride(t *testing.T) {
	desc := models.LocalDescriptor{Endpoint: "lmstudio", Provider: "lm-studio", Model: "qwen/qwen3.8-27b", ContextWindow: 131072, ToolCall: true}
	built, err := BuildOpenCodeConfig(discoveredOpenCodeInput(t, config.OpenCodeLimit{Context: 200000, Output: 8192}, 0, desc, nil))
	if err != nil {
		t.Fatal(err)
	}
	context, input, output, _ := endpointModelLimit(t, built)
	if context != float64(131072) || input != float64(131072) || output != float64(8192) {
		t.Errorf("limit.context %v, limit.input %v, limit.output %v; want the loaded window 131072, 131072 and the override 8192", context, input, output)
	}
	if len(built.Warnings) != 1 {
		t.Fatalf("warnings = %q; want the clamp's", built.Warnings)
	}
	for _, want := range []string{"opencode.limit.context (200000)", "131072", "endpoint lmstudio", "qwen/qwen3.8-27b", "uses 131072"} {
		if !strings.Contains(built.Warnings[0], want) {
			t.Errorf("the warning does not say %q: %s", want, built.Warnings[0])
		}
	}
	if strings.Contains(built.Warnings[0], "127.0.0.1") {
		t.Errorf("the warning names the endpoint's address: %s", built.Warnings[0])
	}

	built, err = BuildOpenCodeConfig(discoveredOpenCodeInput(t, config.OpenCodeLimit{Context: 200000, Output: 8192}, 0, models.LocalDescriptor{}, errors.New("the server refused the connection")))
	if err != nil {
		t.Fatal(err)
	}
	if context, _, _, _ := endpointModelLimit(t, built); context != float64(200000) || len(built.Warnings) != 0 {
		t.Errorf("with nothing discovered: limit.context %v, warnings %q; want the override 200000 and none", context, built.Warnings)
	}
}

// TestPrepareOpenCodeRunDiscoversFromTheMachineTierEndpoint: a dispatch
// discovers the model's window from the server the machine-tier config
// declares, once, and prints the clamp's warning; the server a repository's
// opencode.json names is never asked.
func TestPrepareOpenCodeRunDiscoversFromTheMachineTierEndpoint(t *testing.T) {
	t.Cleanup(SwapOpenCodeLocalDiscoveryForTest(nil))
	const model = "qwen/prepare-run-discovery"
	listing := func(loaded int) string {
		return `{"data":[{"id":"` + model + `","object":"model","type":"llm","state":"loaded","max_context_length":262144,"loaded_context_length":` + strconv.Itoa(loaded) + `,"capabilities":["tool_use"]}],"object":"list"}`
	}
	var mu sync.Mutex
	hits := map[string]int{}
	server := func(name string, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[name+" "+r.Method+" "+r.URL.Path]++
			mu.Unlock()
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	machine := server("machine", listing(65536))
	repository := server("repository", listing(262144))

	wt := openCodeFixtureRepo(t, map[string]string{
		"AGENTS.md":     "# Rules\n",
		"opencode.json": `{"provider":{"lmstudio":{"options":{"baseURL":"` + repository.URL + `/v1"}}}}`,
	})
	settings := config.OpenCodeConfig{Provider: "lm-studio", BaseURL: machine.URL + "/v1", Limit: config.OpenCodeLimit{Context: 200000, Output: 8192}}
	home := t.TempDir()
	var run *OpenCodeRun
	var err error
	stderr := captureAdapterStderr(t, func() {
		run, err = PrepareOpenCodeRun(withMcpForge(OpenCodeRunRequest{
			Home:               home,
			ID:                 testRunID,
			MachineConfigDir:   filepath.Join(home, ".nightgauge"),
			Run:                RunOptions{Stage: "feature-dev", Model: "lmstudio/" + model, WorktreeDir: wt},
			Settings:           settings,
			Lookup:             envLookup(nil),
			GOOS:               "linux",
			ManagedConfigFiles: []string{},
		}, nil))
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeOpenCodeConfig(t, run.ConfigContent)
	if got := jsonPath(doc, "provider", "lmstudio", "models", model, "limit", "context"); got != float64(65536) {
		t.Errorf("limit.context = %v; want 65536, the window the machine-tier endpoint has loaded", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || hits["machine GET /api/v0/models"] != 1 {
		t.Errorf("requests = %v; want one GET /api/v0/models to the machine-tier endpoint and none to the repository's", hits)
	}
	if !strings.Contains(stderr, "[opencode] opencode.limit.context (200000) is larger than the 65536 tokens endpoint lmstudio has loaded") {
		t.Errorf("stderr lacks the clamp's warning:\n%s", stderr)
	}
	if host := strings.TrimPrefix(machine.URL, "http://"); strings.Contains(stderr, host) {
		t.Errorf("stderr names the endpoint's address:\n%s", stderr)
	}
}

// TestOpenCodeConfigStepsCap: the build agent and every subagent get the
// stage's turn cap as their steps cap, so a stage cannot loop forever, and
// the legacy mode.build and mode.plan entries carry it too, because opencode
// 1.18.30 applies mode.<name> over agent.<name> after every config layer.
// With no turn cap the documented default applies. subagent_depth is always
// set.
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
			{"agent", "build"}, {"agent", "plan"}, {"agent", "general"}, {"agent", "explore"}, {"mode", "build"}, {"mode", "plan"},
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

// TestOpenCodeConfigPinsTheModeEntries: observed on opencode 1.18.30, every
// mode.<name> of the merged config is merged over agent.<name> after all
// layers, so a repository's mode.title could turn session titles back on and
// its mode.compaction could move the transcript to another model. The content
// sets the mode entry of every built-in primary agent to its agent entry, so
// its own values win. The general and explore subagents get none: a mode
// entry would make them primary agents.
func TestOpenCodeConfigPinsTheModeEntries(t *testing.T) {
	built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40}, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeOpenCodeConfig(t, built.Content)
	for _, name := range []string{"build", "plan", "title", "summary", "compaction"} {
		agent, mode := jsonPath(doc, "agent", name), jsonPath(doc, "mode", name)
		if mode == nil {
			t.Errorf("mode.%s is not set, so a lower layer's mode.%s would win over agent.%s", name, name, name)
			continue
		}
		a, _ := json.Marshal(agent)
		m, _ := json.Marshal(mode)
		if string(a) != string(m) {
			t.Errorf("mode.%s = %s, want agent.%s's %s", name, m, name, a)
		}
	}
	for _, name := range []string{"general", "explore"} {
		if mode := jsonPath(doc, "mode", name); mode != nil {
			t.Errorf("mode.%s = %v; a mode entry turns the %s subagent into a primary agent", name, mode, name)
		}
	}
}

// TestOpenCodeConfigLimitInputHoldsTheCompactionThreshold: on opencode
// 1.18.30 a model with a limit.input compacts at limit.input less
// compaction.reserved, so a lower layer that added a limit.input to the
// endpoint's model could lift the threshold past the loaded window. The
// block sets limit.input to limit.context, and reserved to the output limit,
// so the threshold is limit.context less limit.output, for an output limit
// above OpenCode's own 20000-token reserve as well.
func TestOpenCodeConfigLimitInputHoldsTheCompactionThreshold(t *testing.T) {
	for _, tc := range []struct {
		limit     config.OpenCodeLimit
		maxTokens int
		output    int
	}{
		{config.OpenCodeLimit{Context: 131072, Output: 8192}, 0, 8192},
		{config.OpenCodeLimit{Context: 131072, Output: 32768}, 0, 32768},
		{config.OpenCodeLimit{Context: 131072, Output: 32768}, 4096, 4096},
	} {
		settings := lmStudioSettings()
		settings.Limit = tc.limit
		built, err := buildOpenCodeConfigFor(t, settings, RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", MaxTokens: tc.maxTokens}, nil)
		if err != nil {
			t.Fatal(err)
		}
		doc := decodeOpenCodeConfig(t, built.Content)
		limit := func(key string) any {
			return jsonPath(doc, "provider", "lmstudio", "models", "qwen/qwen3.8-27b", "limit", key)
		}
		if limit("input") != float64(tc.limit.Context) {
			t.Errorf("%+v: limit.input = %v, want limit.context %d", tc.limit, limit("input"), tc.limit.Context)
		}
		if limit("output") != float64(tc.output) {
			t.Errorf("%+v: limit.output = %v, want %d", tc.limit, limit("output"), tc.output)
		}
		reserved, _ := jsonPath(doc, "compaction", "reserved").(float64)
		if threshold := tc.limit.Context - int(reserved); threshold != tc.limit.Context-tc.output {
			t.Errorf("%+v, max tokens %d: compaction.reserved = %v puts the threshold at %d, want limit.context less limit.output, %d",
				tc.limit, tc.maxTokens, reserved, threshold, tc.limit.Context-tc.output)
		}
	}
}

// TestOpenCodeConfigAnthropicBlockPinsItsServer (ADR-022 § 17): the anthropic
// block sets the SDK package and Anthropic's API root beside the key
// reference, so a repository or inherited config that sets a baseURL of its
// own for anthropic cannot send ANTHROPIC_API_KEY to its server.
func TestOpenCodeConfigAnthropicBlockPinsItsServer(t *testing.T) {
	built, err := buildOpenCodeConfigFor(t, config.OpenCodeConfig{}, RunOptions{Model: "anthropic/claude-sonnet-5"},
		map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeOpenCodeConfig(t, built.Content)
	for path, want := range map[string]string{
		"npm":             "@ai-sdk/anthropic",
		"options.baseURL": "https://api.anthropic.com/v1",
		"options.apiKey":  "{env:ANTHROPIC_API_KEY}",
	} {
		if got := jsonPath(doc, append([]string{"provider", "anthropic"}, strings.Split(path, ".")...)...); got != want {
			t.Errorf("provider.anthropic.%s = %v, want %q", path, got, want)
		}
	}
}

// TestOpenCodeConfigPinsTheServedModel: on opencode 1.18.30 a config model
// entry's id is the model name OpenCode sends, and its provider.npm is the SDK
// package that receives the key, whatever the provider block says. The
// content's own entry for the dispatched model sets both, so a lower layer's
// entry cannot send the stage to another model or package. The model is sent
// under the id the stage names. The anthropic entry sets nothing else, so the
// catalog's limits and options still apply.
func TestOpenCodeConfigPinsTheServedModel(t *testing.T) {
	anthropicKey := map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"}
	for _, tc := range []struct {
		model, key, modelID, servedID, npm string
		keys                               []string
	}{
		{"lmstudio/qwen/qwen3.8-27b", "lmstudio", "qwen/qwen3.8-27b", "qwen/qwen3.8-27b", "@ai-sdk/openai-compatible", []string{"id", "limit", "provider", "tool_call"}},
		{"anthropic/claude-sonnet-5", "anthropic", "claude-sonnet-5", "claude-sonnet-5", "@ai-sdk/anthropic", []string{"id", "provider"}},
		{"anthropic/claude-haiku-4-5-20251001", "anthropic", "claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001", "@ai-sdk/anthropic", []string{"id", "provider"}},
	} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: tc.model}, anthropicKey)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		doc := decodeOpenCodeConfig(t, built.Content)
		models, _ := jsonPath(doc, "provider", tc.key, "models").(map[string]any)
		if len(models) != 1 || models[tc.modelID] == nil {
			t.Errorf("%s: provider.%s.models = %v, want the dispatched model's entry alone", tc.model, tc.key, keysOf(models))
			continue
		}
		entry, _ := models[tc.modelID].(map[string]any)
		if got := entry["id"]; got != tc.servedID {
			t.Errorf("%s: models.%s.id = %v, want %q, so a lower layer cannot send another model", tc.model, tc.modelID, got, tc.servedID)
		}
		if got := jsonPath(entry, "provider", "npm"); got != tc.npm {
			t.Errorf("%s: models.%s.provider.npm = %v, want %q, the provider block's own package", tc.model, tc.modelID, got, tc.npm)
		}
		if got := jsonPath(doc, "provider", tc.key, "npm"); got != tc.npm {
			t.Errorf("%s: provider.%s.npm = %v, want %q", tc.model, tc.key, got, tc.npm)
		}
		if got := keysOf(entry); !slices.Equal(got, tc.keys) {
			t.Errorf("%s: models.%s sets %v, want %v", tc.model, tc.modelID, got, tc.keys)
		}
	}
}

// TestOpenCodeConfigRefusesAnAnthropicModelItCannotPin: the content's entry
// for an anthropic model defines that model even where OpenCode's bundled
// catalog does not list it, and such a model has no limits, so OpenCode would
// never compact its session. A fast-mode entry cannot be pinned either:
// OpenCode derives it from a base model and sends the base model's id with
// options and a header of its own, so pinning the entry's own id sends a model
// Anthropic does not serve, and pinning the base id drops the fast mode. Both
// are refused before spawn, naming the model and a model to dispatch instead.
func TestOpenCodeConfigRefusesAnAnthropicModelItCannotPin(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"}
	for _, tc := range []struct {
		model string
		want  []string
	}{
		{"anthropic/claude-nightgauge-no-such-model", []string{"bundled catalog", "never compacts", "claude-sonnet-4-6"}},
		{"anthropic/claude-sonnet-5-fast", []string{"bundled catalog", "never compacts", "claude-sonnet-4-6"}},
		{"anthropic/qwen/qwen3.8-27b", []string{"bundled catalog", "never compacts", "claude-sonnet-4-6"}},
		{"anthropic/claude-opus-5-fast", []string{"fast-mode", "anthropic/claude-opus-5 instead"}},
		{"anthropic/claude-opus-4-8-fast", []string{"fast-mode", "anthropic/claude-opus-4-8 instead"}},
	} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: tc.model}, env)
		if err == nil {
			t.Errorf("%s: a config was built: %s", tc.model, built.Content)
			continue
		}
		for _, want := range append([]string{strconv.Quote(tc.model)}, tc.want...) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", tc.model, want, err)
			}
		}
	}
}

// TestOpenCodeConfigNonLoopbackUnlessALocalEndpoint: non_loopback is false
// only for a declared endpoint on this machine. A hosted provider's model runs
// on its servers, so an offline claim holds for none of them.
func TestOpenCodeConfigNonLoopbackUnlessALocalEndpoint(t *testing.T) {
	lan := lmStudioSettings()
	lan.BaseURL = "http://192.0.2.10:1234/v1"
	for _, tc := range []struct {
		settings config.OpenCodeConfig
		model    string
		want     bool
	}{
		{lmStudioSettings(), "lmstudio/qwen/qwen3.8-27b", false},
		{lan, "lmstudio/qwen/qwen3.8-27b", true},
		{lmStudioSettings(), "anthropic/claude-sonnet-5", true},
		{lmStudioSettings(), "openai/gpt-5.5", true},
		{lmStudioSettings(), "openrouter/meta-llama/llama-4", true},
	} {
		built, err := buildOpenCodeConfigFor(t, tc.settings, RunOptions{Model: tc.model},
			map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"})
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		if built.NonLoopback != tc.want {
			t.Errorf("%s on %s: NonLoopback = %v, want %v", tc.model, tc.settings.BaseURL, built.NonLoopback, tc.want)
		}
	}
}

// TestOpenCodeConfigRefusesAnUndeclaredProviderKey: a provider key that no
// endpoint declares and OpenCode's bundled catalog does not know can only be
// defined by a config Nightgauge does not build, whose base URL and limits it
// never checked, such as a second LM Studio the repository or the operator's
// own config names. It is refused before spawn, and the refusal never quotes
// an endpoint's address. A hosted catalog provider is dispatched as before.
func TestOpenCodeConfigRefusesAnUndeclaredProviderKey(t *testing.T) {
	for _, model := range []string{"lmstudio-remote/qwen/qwen3.8-27b", "nosuchprovider/x", "lab/qwen/qwen3.8-27b"} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: model}, nil)
		if err == nil {
			t.Errorf("%s: a config was built: %s", model, built.Content)
			continue
		}
		key, _, _ := strings.Cut(model, "/")
		for _, want := range []string{strconv.Quote(key), "neither an endpoint", "never compacts"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", model, want, err)
			}
		}
		if strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("%s: the refusal quotes the endpoint's address: %v", model, err)
		}
	}
	for _, model := range []string{"openai/gpt-5.5", "openrouter/meta-llama/llama-4", "deepseek/deepseek-chat", "ollama-cloud/qwen3-coder:480b"} {
		if _, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: model}, nil); err != nil {
			t.Errorf("%s: a hosted catalog provider was refused: %v", model, err)
		}
	}
}

// TestOpenCodeConfigRefusesPlatformProviders (ADR-022 § 17): a provider whose
// credentials are the forge's or a cloud platform's, which a stage keeps for
// its tools, is refused at config time as it is before dispatch, so the verb
// refuses it too: github-copilot would run on the forge's gh login, a
// Copilot subscription, and google-vertex-anthropic and amazon-bedrock would
// reach Claude without ANTHROPIC_API_KEY.
func TestOpenCodeConfigRefusesPlatformProviders(t *testing.T) {
	for _, provider := range openCodePlatformProviders {
		model := provider + "/claude-sonnet-5"
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: model},
			map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625", "GITHUB_TOKEN": "fake-forge-credential-1625"})
		if err == nil {
			t.Errorf("%s: a config was built: %s", model, built.Content)
			continue
		}
		for _, want := range []string{strconv.Quote(provider), "subscription or OAuth", "§ 17"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", model, want, err)
			}
		}
		if strings.Contains(err.Error(), "fake-forge-credential-1625") {
			t.Errorf("%s: the refusal carries a credential's value: %v", model, err)
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

// TestOpenCodeConfigRefusesAnInstructionsEntryItCannotWriteSafely: the
// builder is the one writer of a run's config, so it refuses an instructions
// entry that is a URL, which OpenCode would fetch, a relative path, a path
// outside the worktree, one holding a brace, which OpenCode's substitution
// reads, and one whose name OpenCode would glob, whatever gave it the entry.
// A worktree entry and the run's steering file are written.
func TestOpenCodeConfigRefusesAnInstructionsEntryItCannotWriteSafely(t *testing.T) {
	build := func(entries ...string) (OpenCodeRunConfig, error) {
		in, err := OpenCodeConfigInputFor(lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}, goldenRunRoot, envLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		in.Repository = codexprovision.OpenCodeProvision{Root: goldenWorktree, Steering: "steering", Instructions: entries}
		return BuildOpenCodeConfig(in)
	}
	for _, entry := range []string{
		"https://example.test/rules.md",
		"AGENTS.md",
		"/elsewhere/AGENTS.md",
		goldenWorktree + "/{file:~/.ssh/id_rsa}.md",
		goldenWorktree + "/docs/*.md",
		goldenWorktree + "/docs/../../etc/passwd",
	} {
		if built, err := build(entry); err == nil {
			t.Errorf("instructions entry %q was written:\n%s", entry, built.Content)
		}
	}
	built, err := build(goldenWorktree + "/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := jsonPath(decodeOpenCodeConfig(t, built.Content), "instructions").([]any)
	steering := filepath.Join(goldenRunRoot, "nightgauge", "steering.md")
	if len(got) != 2 || got[0] != goldenWorktree+"/AGENTS.md" || got[1] != steering || built.Files[steering] != "steering" {
		t.Errorf("instructions = %v, files = %v; want the worktree's AGENTS.md, then the steering file holding the steering", got, built.Files)
	}
}

// TestOpenCodeConfigRefusesAnMcpServerItCannotWriteSafely: an MCP server may
// carry OpenCode's substitution syntax only as a {env:VAR} reference. A
// {file:...} reference, which would read a file into the config, and a
// malformed {env: are refused, and so is a server that is neither a local one
// with a command nor a remote one with a URL.
func TestOpenCodeConfigRefusesAnMcpServerItCannotWriteSafely(t *testing.T) {
	off := false
	build := func(s codexprovision.OpenCodeMcpServer) (OpenCodeRunConfig, error) {
		in, err := OpenCodeConfigInputFor(lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}, goldenRunRoot, envLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		in.Repository = codexprovision.OpenCodeProvision{MCP: map[string]codexprovision.OpenCodeMcpServer{"s": s}}
		return BuildOpenCodeConfig(in)
	}
	for name, s := range map[string]codexprovision.OpenCodeMcpServer{
		"file header":   {Type: "remote", URL: "https://mcp.example.test", Headers: map[string]string{"X": "{file:~/.ssh/id_rsa}"}, OAuth: &off, Enabled: true},
		"malformed env": {Type: "local", Command: []string{"srv"}, Environment: map[string]string{"K": "{env:A B}"}, Enabled: true},
		"file command":  {Type: "local", Command: []string{"{file:/etc/passwd}"}, Enabled: true},
		"no command":    {Type: "local", Enabled: true},
		"no url":        {Type: "remote", OAuth: &off, Enabled: true},
		"no type":       {Command: []string{"srv"}, Enabled: true},
	} {
		if built, err := build(s); err == nil {
			t.Errorf("%s: the server was written:\n%s", name, built.Content)
		}
	}
	built, err := build(codexprovision.OpenCodeMcpServer{Type: "local", Command: []string{"srv", "--token={env:TOKEN}"}, Environment: map[string]string{"K": `{env:K}{"a":1}`}, Enabled: true})
	if err != nil {
		t.Fatalf("a server with {env:VAR} references was refused: %v", err)
	}
	if got := jsonPath(decodeOpenCodeConfig(t, built.Content), "mcp", "s", "type"); got != "local" {
		t.Errorf("mcp.s.type = %v", got)
	}
}

// TestOpenCodeConfigRefusesAValueOpenCodeCannotPaste: OpenCode pastes each
// {env:NAME}'s value into the config text unescaped before it parses it, so
// one value it cannot paste fails the whole config, and its error prints the
// text with every credential it resolved, the MCP servers' included. The
// builder is the one writer of the content, so it refuses a dispatch while
// any reference the content holds names such a value: ANTHROPIC_API_KEY ending
// in a carriage return, as one read from a file with CRLF line endings does,
// holding a quote, a backslash or {file:, or an MCP server's variable. The
// refusal names the variable, never the value.
func TestOpenCodeConfigRefusesAValueOpenCodeCannotPaste(t *testing.T) {
	const value = "fixture-anthropic-value-1626"
	run := RunOptions{Model: "anthropic/claude-sonnet-5", MaxTurns: 40}
	for _, bad := range []string{value + "\r", value + `"`, value + `\x`, value + "{file:/nonexistent}"} {
		built, err := buildOpenCodeConfigFor(t, config.OpenCodeConfig{}, run, map[string]string{"ANTHROPIC_API_KEY": bad})
		switch {
		case err == nil:
			t.Errorf("ANTHROPIC_API_KEY %q: a config was built:\n%s", bad, built.Content)
		case !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") || strings.Contains(err.Error(), value):
			t.Errorf("ANTHROPIC_API_KEY %q: the refusal should name the variable and not its value: %v", bad, err)
		}
	}

	off := false
	build := func(token string) (OpenCodeRunConfig, error) {
		in, err := OpenCodeConfigInputFor(lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}, goldenRunRoot,
			envLookup(map[string]string{"MCP_FIXTURE_TOKEN": token}))
		if err != nil {
			t.Fatal(err)
		}
		in.Repository = codexprovision.OpenCodeProvision{MCP: map[string]codexprovision.OpenCodeMcpServer{
			"r": {Type: "remote", URL: "https://mcp.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer {env:MCP_FIXTURE_TOKEN}"}, OAuth: &off, Enabled: true},
		}}
		return BuildOpenCodeConfig(in)
	}
	if built, err := build(value + "\n"); err == nil {
		t.Errorf("an MCP server's variable holding a newline was written:\n%s", built.Content)
	} else if !strings.Contains(err.Error(), "MCP_FIXTURE_TOKEN") || strings.Contains(err.Error(), value) {
		t.Errorf("the refusal should name the variable and not its value: %v", err)
	}
	if _, err := build(value); err != nil {
		t.Errorf("a value OpenCode can paste was refused: %v", err)
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

// TestPrepareOpenCodeRunNamesWhatTheSpawnMustNotInherit: the prepared run
// carries, as data, exactly the inherited variables the Go path keeps from the
// child (OpenCodeWithholdsEnv), so a caller spawning OpenCode from the verb's
// output composes the same environment without mirroring the catalog. Checked
// for a local and a hosted dispatch, against every catalog variable, both
// base-URL variables, OpenCode's own variables and variables a stage keeps.
func TestPrepareOpenCodeRunNamesWhatTheSpawnMustNotInherit(t *testing.T) {
	home := t.TempDir()
	for _, model := range []string{"lmstudio/qwen/qwen3.8-27b", "openai/gpt-5.5"} {
		run, err := PrepareOpenCodeRun(OpenCodeRunRequest{
			Home:             home,
			ID:               testRunID,
			MachineConfigDir: filepath.Join(home, ".nightgauge"),
			Run:              RunOptions{Stage: "feature-dev", Model: model, WorktreeDir: t.TempDir()},
			Settings:         lmStudioSettings(),
			Lookup:           envLookup(nil),
			GOOS:             "linux",
		})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		raw, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			EnvWithhold *struct {
				Prefixes []string `json:"prefixes"`
				Names    []string `json:"names"`
			} `json:"env_withhold"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.EnvWithhold == nil {
			t.Fatalf("%s: the prepared run names no env_withhold, so a caller cannot compose the child's environment:\n%s", model, raw)
		}
		covers := func(name string) bool {
			for _, p := range out.EnvWithhold.Prefixes {
				if strings.HasPrefix(name, p) {
					return true
				}
			}
			return slices.Contains(out.EnvWithhold.Names, name)
		}
		candidates := []string{
			"OPENCODE_AUTH_CONTENT", "OPENCODE_CONFIG_DIR", "OPENCODE_ANYTHING_LATER",
			"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "PATH", "HOME", "GH_TOKEN",
		}
		for _, vars := range openCodeCatalogEnv {
			candidates = append(candidates, vars...)
		}
		for _, name := range candidates {
			if got, want := covers(name), OpenCodeWithholdsEnv(model, name); got != want {
				t.Errorf("%s: env_withhold covers %s = %v, but the Go path withholds it = %v", model, name, got, want)
			}
		}
		if !slices.IsSorted(out.EnvWithhold.Names) {
			t.Errorf("%s: env_withhold.names is not sorted", model)
		}
	}
}

// TestPrepareOpenCodeRunRefusesConfigARunCannotBeIsolatedFrom: a
// $HOME/.opencode holding config, or the machine's managed OpenCode config,
// refuses the run in the preparation the adapter and the verb share, before
// anything is created, unless the machine tier opts into the operator's own
// OpenCode config, which then says so on stderr and layers it in.
func TestPrepareOpenCodeRunRefusesConfigARunCannotBeIsolatedFrom(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".opencode", "opencode.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := OpenCodeRunRequest{
		Home:             home,
		ID:               testRunID,
		MachineConfigDir: filepath.Join(home, ".nightgauge"),
		Run:              RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()},
		Settings:         lmStudioSettings(),
		Lookup:           envLookup(nil),
		GOOS:             "linux",
	}
	if run, err := PrepareOpenCodeRun(req); err == nil {
		t.Fatalf("a run was prepared while ~/.opencode holds config: %+v", run)
	} else if !strings.Contains(err.Error(), filepath.Join(home, ".opencode")) || !strings.Contains(err.Error(), openCodeInheritSetting) {
		t.Errorf("the refusal does not name ~/.opencode and the opt-in: %v", err)
	}
	if _, err := os.Lstat(OpenCodeRunsDir(home)); !os.IsNotExist(err) {
		t.Errorf("a refused run created %s", OpenCodeRunsDir(home))
	}

	req.Settings.InheritUserConfig = true
	var run *OpenCodeRun
	var err error
	stderr := captureAdapterStderr(t, func() { run, err = PrepareOpenCodeRun(req) })
	if err != nil {
		t.Fatalf("with %s on the run was refused: %v", openCodeInheritSetting, err)
	}
	if run.Env["OPENCODE_CONFIG_DIR"] == "" {
		t.Error("with the opt-in the run does not layer the operator's OpenCode config in")
	}
	if n := strings.Count(stderr, openCodeInheritSetting+" is on"); n != 1 {
		t.Errorf("the opt-in was announced %d times, want once:\n%s", n, stderr)
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
		Run:              RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40, WorktreeDir: t.TempDir()},
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
