package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/models"
)

// The per-run OpenCode config (ADR-022 § 7, § 8, § 10, § 12, § 15, § 17).
//
// BuildOpenCodeConfig is the one authority for what an opencode spawn is
// configured with: the adapter sets its output as OPENCODE_CONFIG_CONTENT, and
// `nightgauge opencode config` prints the same bytes for the SDK path (#1648),
// through PrepareOpenCodeRun, which both call.
//
// Everything Nightgauge sets goes in OPENCODE_CONFIG_CONTENT. Observed on
// opencode 1.18.30, that layer is merged after the run's XDG config file, the
// repository's opencode.json and an inherited OPENCODE_CONFIG_DIR, and wins
// over each of them for every key it sets: the dispatched model's entry pins
// its id and provider.npm (which 1.18.30 uses in place of the block's), a
// declared endpoint's or the anthropic block's base URL and, for a declared
// endpoint, its limits. None of them can change those fields, or turn
// sharing back on. Only the machine's managed config sits above it, and
// PrepareOpenCodeRun refuses a dispatch while one exists unless the operator
// opted into their own config.
//
// Pinning those fields does not pin the model actually served, because a
// lower layer can add keys this config does not set without touching the id
// or provider.npm pins. Reproduced on opencode 1.18.30, a repository
// opencode.json or .opencode/agent/*.md can still: set options.model on the
// dispatched model's own entry (provider.<key>.models.<id>.options.model) or
// on an agent (agent.<name>.options.model), or add a variant, which merges
// last — @ai-sdk/openai-compatible spreads an unknown providerOptions key,
// options.model included, into the request body after the pinned id, so the
// request can still name another model; on anthropic, set options.speed or
// options.fallbacks in the same place, which the SDK turns into its own beta
// headers, turning on fast mode or a server-side fallback for the dispatched
// model (openCodeAnthropicModelRefusal below only refuses naming a fast-mode
// entry directly; it is not a control against options.speed on the base
// model); and give an agent options.mcpServers with an authorizationToken of
// "{env:ANTHROPIC_API_KEY}" (or another variable the run holds), which
// OpenCode resolves and sends as an MCP authorization token to a URL the
// repository names. #1638, the project-config tamper gate, is the control
// that closes these three routes.
//
// A lower layer can also add a mode.general or mode.explore entry, which
// 1.18.30 merges over that subagent's model and steps cap after every layer
// (the content cannot set those two without making the subagents primary); a
// block for a hosted provider other than anthropic, which this config gives
// none, so the block can re-point that provider and send its model as
// another; and a hosted model's limits, anthropic's included, which come
// from OpenCode's catalog and which this config does not set. The
// project-config tamper-gate, endpoint-policy and stage-limits warning lines
// say so.
//
// Two things are never in the content itself:
//
//   - A credential. The anthropic block reads its key as the reference
//     {env:ANTHROPIC_API_KEY}, which OpenCode resolves in its own process.
//   - A model server's base URL. The environment reaches every tool a stage
//     runs, so the URL is written to a 0600 file in the run's root, and the
//     content names that file with a {file:...} reference, which 1.18.30
//     resolves in OPENCODE_CONFIG_CONTENT as it does in a file.

// OpenCodeConfigSchemaVersion is the schema_version of `nightgauge opencode
// config --json`. A caller refuses an output whose major version it does not
// know (#1648).
const OpenCodeConfigSchemaVersion = "1.0"

// openCodeConfigContentEnvVar is the inline config layer OpenCode merges last
// of every layer Nightgauge does not refuse.
const openCodeConfigContentEnvVar = "OPENCODE_CONFIG_CONTENT"

// openCodeDefaultSteps is the steps cap of the build agent and every
// subagent when a stage sets no turn cap (RunOptions.MaxTurns is 0). A step
// is one model request with its tool calls; when a session reaches the cap,
// OpenCode forces a text-only reply and the run ends. 200 steps is room for a
// long implementation stage on a local model and still ends a session that
// keeps looping, such as one that resumes on the synthetic continue turn
// after every compaction.
const openCodeDefaultSteps = 200

// openCodeSubagentDepth is set explicitly: a subagent the task tool starts
// cannot start another, so a stage's steps cap bounds every level below it.
const openCodeSubagentDepth = 1

// openCodeDefaultTimeout is an endpoint's header and chunk timeout when the
// machine-tier config sets none. A cold prefill of a 131072-token window on
// LM Studio was measured at 76 seconds before the first byte, and LM Studio
// sends no chunk until prefill ends, so both waits need that much and more.
const openCodeDefaultTimeout = 3 * time.Minute

// openCodeMinTimeout rejects a timeout that is almost certainly a unit
// mistake: a bare number in the YAML is read as nanoseconds.
const openCodeMinTimeout = time.Second

// Tool output above either cap is written to a file and the model gets a
// preview, so one command's output cannot fill a local model's window.
const (
	openCodeToolOutputMaxLines = 1000
	openCodeToolOutputMaxBytes = 32 * 1024
)

// Compaction settings. tail_turns keeps the stage prompt's turn and the one
// after it verbatim. preserve_recent_tokens takes the bounds opencode 1.18.30
// applies to its own default (between 2000 and 15000, and at most a quarter
// of the usable window), computed from the limits Nightgauge knows, so the
// value is explicit and does not move with the binary.
//
// On 1.18.30 the compaction threshold of a model with a limit.input is
// limit.input less reserved, and of any other model limit.context less its
// output limit (read from its bundled source). A lower config layer can give
// a model a limit.input the per-run config does not set, and so move the
// threshold past the loaded window. The block of a declared endpoint's model
// therefore sets limit.input to limit.context, and reserved to the output
// limit, which keeps the threshold at limit.context less limit.output. A
// hosted model's reserved is 20000, the most OpenCode's own default reserves.
const (
	openCodeCompactionTailTurns     = 2
	openCodeCompactionReservedCap   = 20000
	openCodeCompactionPreserveCap   = 15000
	openCodeCompactionPreserveFloor = 2000
)

// openCodeEndpointNPM is the AI SDK package every endpoint block uses: LM
// Studio and Ollama both serve an OpenAI-compatible API.
const openCodeEndpointNPM = "@ai-sdk/openai-compatible"

// openCodeAnthropicKey is the provider key whose one credential is
// ANTHROPIC_API_KEY (ADR-022 § 17).
const openCodeAnthropicKey = "anthropic"

// The anthropic block pins the SDK package and the API root, the values the
// 1.18.30 catalog and its bundled @ai-sdk/anthropic use, so no lower config
// layer can send ANTHROPIC_API_KEY to another server by setting a baseURL of
// its own (ADR-022 § 17).
const (
	openCodeAnthropicNPM     = "@ai-sdk/anthropic"
	openCodeAnthropicBaseURL = "https://api.anthropic.com/v1"
)

// openCodePinnedModeAgents are the built-in agents whose legacy mode.<name>
// entry the config sets beside agent.<name>. Observed on 1.18.30, every
// mode.<name> of the merged config is merged over agent.<name> after every
// layer, forced to mode "primary", so a lower layer's mode entry would win
// over the content's agent entry. These agents are primary already, so the
// content's own mode entry changes nothing about them and wins over a lower
// layer's. The general and explore subagents are not here: a mode entry
// would turn them into primary agents (ADR-022 § 8).
var openCodePinnedModeAgents = []string{"build", "plan", "title", "summary", "compaction"}

// openCodeRunFilesDir is the directory of the run root, outside all four XDG
// directories, where Nightgauge keeps the files the config refers to.
const openCodeRunFilesDir = "nightgauge"

// openCodeEndpointIDs maps an endpoint kind to the provider key its single
// endpoint is dispatched under (ADR-022 § 1): LM Studio's catalog key
// `lmstudio`, and `ollama`, which is not a catalog key.
var openCodeEndpointIDs = map[string]string{
	"lm-studio": "lmstudio",
	"ollama":    "ollama",
}

// OpenCodeEndpoint is a model server the operator runs. It becomes one
// provider block keyed by ID, so several endpoints of one kind are several
// blocks (ADR-022 § Endpoints).
type OpenCodeEndpoint struct {
	// ID is the OpenCode provider key a stage names on -m.
	ID string
	// Provider is the endpoint's kind: lm-studio or ollama.
	Provider string
	// BaseURL is the server's API root. It is validated by OpenCodeEndpoints,
	// and it never appears in the config content, an error or the verb's
	// output.
	BaseURL string
	// NonLoopback is true when BaseURL's host is not this machine, so a run
	// on it sends the stage's code over the network.
	NonLoopback bool
	// Limit is what the server has loaded.
	Limit config.OpenCodeLimit
	// HeaderTimeout and ChunkTimeout bound the waits for the first response
	// byte and between streamed chunks.
	HeaderTimeout time.Duration
	ChunkTimeout  time.Duration
	// ConfigKey is where the endpoint is declared, for messages:
	// "opencode" for the flat machine-tier keys.
	ConfigKey string
}

// OpenCodeEndpoints returns the endpoints the machine-tier `opencode:` block
// declares: none, or the one its flat keys describe. A block that declares a
// server it cannot describe fully is an error, and so is a base_url that is
// not an http or https URL or that carries credentials. Limits are checked
// when a stage dispatches to the endpoint (BuildOpenCodeConfig), not here.
func OpenCodeEndpoints(cfg config.OpenCodeConfig) ([]OpenCodeEndpoint, error) {
	declared := cfg.Provider != "" || cfg.BaseURL != "" || cfg.Limit != (config.OpenCodeLimit{}) ||
		cfg.Timeouts != (config.OpenCodeTimeouts{})
	if !declared {
		return nil, nil
	}
	const key = "opencode"
	if cfg.Provider == "" {
		return nil, fmt.Errorf("%s.provider is not set: %s.base_url, .limit and .timeouts describe a model server, and %s.provider names its kind, lm-studio or ollama", key, key, key)
	}
	id, ok := openCodeEndpointIDs[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("%s.provider %q is not a model server kind the opencode adapter supports: set lm-studio or ollama", key, cfg.Provider)
	}
	nonLoopback, err := openCodeCheckBaseURL(cfg.BaseURL, key, id)
	if err != nil {
		return nil, err
	}
	header, err := openCodeTimeout(cfg.Timeouts.Header.Duration(), key+".timeouts.header")
	if err != nil {
		return nil, err
	}
	chunk, err := openCodeTimeout(cfg.Timeouts.Chunk.Duration(), key+".timeouts.chunk")
	if err != nil {
		return nil, err
	}
	return []OpenCodeEndpoint{{
		ID:            id,
		Provider:      cfg.Provider,
		BaseURL:       cfg.BaseURL,
		NonLoopback:   nonLoopback,
		Limit:         cfg.Limit,
		HeaderTimeout: header,
		ChunkTimeout:  chunk,
		ConfigKey:     key,
	}}, nil
}

// openCodeCheckBaseURL validates an endpoint's base URL and reports whether
// its host is somewhere other than this machine. The URL is never quoted in
// an error: the machine-tier config is the only place an endpoint's address
// may appear (ADR-022 § Endpoints), so an error names the endpoint instead.
func openCodeCheckBaseURL(raw, key, id string) (nonLoopback bool, err error) {
	name := fmt.Sprintf("%s.base_url (endpoint %s)", key, id)
	if raw == "" {
		return false, fmt.Errorf("%s is not set: name the server's OpenAI-compatible API root, such as http://127.0.0.1:1234/v1 for LM Studio", name)
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false, fmt.Errorf("%s contains whitespace or a control character", name)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false, fmt.Errorf("%s is not a valid URL", name)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false, fmt.Errorf("%s must be an http or https URL", name)
	}
	if u.User != nil {
		return false, fmt.Errorf("%s must not carry a user name or password: a credential in the URL would reach every request and every error that quotes it", name)
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return false, fmt.Errorf("%s must name a host", name)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false, fmt.Errorf("%s must not carry a query or fragment: OpenCode appends the API path to it", name)
	}
	return !openCodeIsLoopbackHost(u.Hostname()), nil
}

// openCodeIsLoopbackHost reports whether host is this machine: a loopback
// address or the name localhost. Nothing is resolved, so the answer never
// depends on the network.
func openCodeIsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// openCodeTimeout is d, or the default when d is zero.
func openCodeTimeout(d time.Duration, key string) (time.Duration, error) {
	if d == 0 {
		return openCodeDefaultTimeout, nil
	}
	if d < openCodeMinTimeout {
		return 0, fmt.Errorf("%s is %s, under a second: write it as a duration such as 3m (a bare number is read as nanoseconds)", key, d)
	}
	return d, nil
}

// OpenCodeConfigInput is what BuildOpenCodeConfig builds a run's config from.
type OpenCodeConfigInput struct {
	// Run is the dispatch: its Model, MaxTurns and MaxTokens decide the
	// content; Stage and WorktreeDir name it.
	Run RunOptions
	// Endpoints are the model servers the operator runs (OpenCodeEndpoints).
	Endpoints []OpenCodeEndpoint
	// Snapshot, LSP and Formatter are ADR-022 § 12's settings.
	Snapshot  bool
	LSP       bool
	Formatter bool
	// RunRoot is the run's root (OpenCodeRunRoot). The content refers to
	// files in it, so it must be an absolute path.
	RunRoot string
	// Lookup reads the environment the provider's credential comes from.
	Lookup func(string) (string, bool)
}

// OpenCodeConfigInputFor is the input a dispatch of run gets on a machine
// whose `opencode:` block is settings, in the root runRoot.
func OpenCodeConfigInputFor(settings config.OpenCodeConfig, run RunOptions, runRoot string, lookup func(string) (string, bool)) (OpenCodeConfigInput, error) {
	endpoints, err := OpenCodeEndpoints(settings)
	if err != nil {
		return OpenCodeConfigInput{}, err
	}
	orDefault := func(v *bool, def bool) bool {
		if v == nil {
			return def
		}
		return *v
	}
	return OpenCodeConfigInput{
		Run:       run,
		Endpoints: endpoints,
		Snapshot:  orDefault(settings.Snapshot, false),
		LSP:       orDefault(settings.LSP, true),
		Formatter: orDefault(settings.Formatter, true),
		RunRoot:   runRoot,
		Lookup:    lookup,
	}, nil
}

// OpenCodeRunConfig is a built per-run config.
type OpenCodeRunConfig struct {
	// Content is OPENCODE_CONFIG_CONTENT.
	Content string
	// Files are what Content refers to, by absolute path under the run root:
	// the dispatched endpoint's base URL. Each is written with mode 0600
	// before the spawn.
	Files map[string]string
	// NonLoopback is false only when the stage dispatches to a declared
	// endpoint whose base URL is on this machine. A hosted provider's model
	// runs elsewhere, so it is true for every other dispatch.
	NonLoopback bool
}

// The JSON shape of OPENCODE_CONFIG_CONTENT. Every key is one opencode
// 1.18.30 accepts (its bundled config schema); 1.18.30 drops an unknown key
// without a word, so a misspelled key here would silently do nothing.
type openCodeConfigJSON struct {
	Model            string                       `json:"model"`
	SmallModel       string                       `json:"small_model"`
	DefaultAgent     string                       `json:"default_agent"`
	EnabledProviders []string                     `json:"enabled_providers"`
	Provider         map[string]any               `json:"provider"`
	Agent            map[string]openCodeAgentJSON `json:"agent"`
	Mode             map[string]openCodeAgentJSON `json:"mode"`
	SubagentDepth    int                          `json:"subagent_depth"`
	Compaction       openCodeCompactionJSON       `json:"compaction"`
	ToolOutput       openCodeToolOutputJSON       `json:"tool_output"`
	Share            string                       `json:"share"`
	Autoupdate       bool                         `json:"autoupdate"`
	Snapshot         bool                         `json:"snapshot"`
	LSP              bool                         `json:"lsp"`
	Formatter        bool                         `json:"formatter"`
	Instructions     []string                     `json:"instructions"`
	Skills           openCodeSkillsJSON           `json:"skills"`
}

type openCodeAgentJSON struct {
	Model   string `json:"model"`
	Steps   int    `json:"steps,omitempty"`
	Disable bool   `json:"disable"`
}

type openCodeCompactionJSON struct {
	Auto                 bool `json:"auto"`
	Prune                bool `json:"prune"`
	Reserved             int  `json:"reserved"`
	TailTurns            int  `json:"tail_turns"`
	PreserveRecentTokens int  `json:"preserve_recent_tokens"`
}

type openCodeToolOutputJSON struct {
	MaxLines int `json:"max_lines"`
	MaxBytes int `json:"max_bytes"`
}

type openCodeSkillsJSON struct {
	URLs []string `json:"urls"`
}

// openCodeEndpointBlockJSON is a complete endpoint block (ADR-022
// § Endpoints): env [] and an empty apiKey, so no environment variable binds
// to the endpoint, whatever the catalog says about its key.
type openCodeEndpointBlockJSON struct {
	NPM     string                       `json:"npm"`
	Env     []string                     `json:"env"`
	Options openCodeEndpointOptionsJSON  `json:"options"`
	Models  map[string]openCodeModelJSON `json:"models"`
}

type openCodeEndpointOptionsJSON struct {
	BaseURL       string `json:"baseURL"`
	APIKey        string `json:"apiKey"`
	HeaderTimeout int64  `json:"headerTimeout"`
	ChunkTimeout  int64  `json:"chunkTimeout"`
}

// openCodeModelJSON is a declared endpoint's model entry. On 1.18.30 a model
// entry's id is the model name OpenCode sends, and its provider.npm is the SDK
// package it loads for the model, both in place of the provider block's, so
// the entry pins both: a lower layer's own id or provider.npm for the model
// loses to this one. That does not close the entry's own options.model (see
// the file header comment and #1638): a lower layer can still add that key
// without touching id or provider.npm.
type openCodeModelJSON struct {
	ID       string                    `json:"id"`
	Provider openCodeModelProviderJSON `json:"provider"`
	Limit    openCodeLimitJSON         `json:"limit"`
	ToolCall bool                      `json:"tool_call"`
}

type openCodeModelProviderJSON struct {
	NPM string `json:"npm"`
}

// openCodeHostedModelJSON is the anthropic model entry: the same two pins and
// nothing else, so the catalog's limits, options and headers for the model
// still apply.
type openCodeHostedModelJSON struct {
	ID       string                    `json:"id"`
	Provider openCodeModelProviderJSON `json:"provider"`
}

// openCodeLimitJSON is a model's limits. Input is always set, to Context:
// see the compaction settings for why.
type openCodeLimitJSON struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

// openCodeAnthropicBlockJSON is the anthropic provider block: the SDK package,
// the API root, the reference the API key is read from, and the dispatched
// model's entry, so a lower layer's own npm, baseURL or model id/provider.npm
// for anthropic loses to these. It does not close the model entry's own
// options (speed, fallbacks) or an agent's options.mcpServers; see the file
// header comment and #1638.
type openCodeAnthropicBlockJSON struct {
	NPM     string                             `json:"npm"`
	Options openCodeAnthropicOptionsJSON       `json:"options"`
	Models  map[string]openCodeHostedModelJSON `json:"models"`
}

type openCodeAnthropicOptionsJSON struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
}

// BuildOpenCodeConfig builds the per-run config for in.Run. It is pure: it
// reads no file and no environment beyond in.Lookup, and the same input gives
// the same bytes.
//
// It sets:
//
//   - model and small_model, and every agent's model, to the dispatched model,
//     and default_agent to build, so no request carrying the stage's prompt or
//     transcript names another model (ADR-022 § 15). That pins the model
//     string OpenCode looks up; it does not pin what is sent over the wire
//     for it (see the file header comment and #1638);
//   - enabled_providers to the dispatched provider key alone, so OpenCode
//     loads no other provider from credentials the stage keeps for its tools
//     (the forge tokens, AWS and Google Cloud) and not its own free one;
//   - the dispatched provider's block: a complete block for a declared
//     endpoint, keyed by the endpoint's id, with its limits, timeouts and
//     tool calls, or the anthropic block, with its SDK package, API root and
//     key reference; in either, the dispatched model's entry pins the model
//     id OpenCode sends and the SDK package that sends it;
//   - steps on the build, plan, general and explore agents; the legacy
//     mode.<name> entry of every agent in openCodePinnedModeAgents, the same
//     as its agent entry; subagent_depth; the title agent disabled;
//   - compaction, tool_output, share "disabled", autoupdate false, snapshot,
//     lsp and formatter, and empty instructions and skills.urls.
//
// It refuses a model the adapter cannot dispatch; an anthropic/ model while
// ANTHROPIC_API_KEY is unset and a platform provider's model
// (openCodeCredentialRefusal); a provider key that is neither a declared
// endpoint nor a provider OpenCode's bundled catalog knows, a local one
// included; an anthropic model whose entry it cannot pin: one the bundled
// catalog does not list, or a fast-mode entry (openCodeAnthropicModelRefusal);
// and an endpoint whose limit.context or limit.output is 0 or missing.
func BuildOpenCodeConfig(in OpenCodeConfigInput) (OpenCodeRunConfig, error) {
	if in.Lookup == nil {
		return OpenCodeRunConfig{}, errors.New("opencode config: no environment to check the provider's credential against")
	}
	model, err := openCodeModelArg(in.Run.Model)
	if err != nil {
		return OpenCodeRunConfig{}, err
	}
	// OpenCode substitutes {env:NAME} and {file:path} in the config text
	// before it parses it, so a brace in the model id could read a secret
	// into the model name a request sends.
	if strings.ContainsAny(model, "{}") {
		return OpenCodeRunConfig{}, fmt.Errorf("model %q is refused: OpenCode substitutes {env:...} and {file:...} references in its config, so a model id may not contain a brace", model)
	}
	if err := openCodeCredentialRefusal(model, in.Lookup); err != nil {
		return OpenCodeRunConfig{}, err
	}
	if err := openCodeCheckRunRoot(in.RunRoot); err != nil {
		return OpenCodeRunConfig{}, err
	}
	key, modelID, _ := strings.Cut(model, "/")

	// A hosted provider's model runs on its servers; only a declared endpoint
	// on this machine keeps the stage here.
	built := OpenCodeRunConfig{NonLoopback: true}
	providers := map[string]any{}
	var known *config.OpenCodeLimit // the dispatched model's limits, when Nightgauge knows them
	_, catalogKey := openCodeCatalogEnv[key]
	switch ep, declared := findOpenCodeEndpoint(in.Endpoints, key); {
	case declared:
		limit, err := ep.effectiveLimit(in.Run.MaxTokens)
		if err != nil {
			return OpenCodeRunConfig{}, err
		}
		file := openCodeEndpointURLFile(in.RunRoot, ep.ID)
		providers[ep.ID] = openCodeEndpointBlockJSON{
			NPM: openCodeEndpointNPM,
			Env: []string{},
			Options: openCodeEndpointOptionsJSON{
				BaseURL:       "{file:" + file + "}",
				APIKey:        "",
				HeaderTimeout: ep.HeaderTimeout.Milliseconds(),
				ChunkTimeout:  ep.ChunkTimeout.Milliseconds(),
			},
			Models: map[string]openCodeModelJSON{
				modelID: {
					ID:       modelID,
					Provider: openCodeModelProviderJSON{NPM: openCodeEndpointNPM},
					Limit:    openCodeLimitJSON{Context: limit.Context, Input: limit.Context, Output: limit.Output},
					ToolCall: true,
				},
			},
		}
		built.Files = map[string]string{file: ep.BaseURL}
		built.NonLoopback = ep.NonLoopback
		known = &limit
	case openCodeIsLocalKey(model):
		return OpenCodeRunConfig{}, fmt.Errorf(
			"model %q names provider key %q, a model server you run, but the machine-tier opencode: config declares no endpoint with that id, so its limits are unknown: LM Studio reports a context limit of 0, and OpenCode never compacts a session whose limit is 0. "+
				"Set opencode.provider, opencode.base_url, opencode.limit.context and opencode.limit.output in ~/.nightgauge/config.yaml. See docs/SETTINGS_ARCHITECTURE.md",
			model, key)
	case !catalogKey:
		return OpenCodeRunConfig{}, fmt.Errorf(
			"model %q names provider key %q, which is neither an endpoint the machine-tier opencode: config declares nor a provider OpenCode's bundled catalog knows, so only a config Nightgauge does not build (the repository's opencode.json or your own OpenCode config) could define it, with a base URL and limits Nightgauge never checked: a missing or 0 context limit means OpenCode never compacts the session. "+
				"Dispatch to the endpoint your opencode: block declares (its id is lmstudio for provider lm-studio and ollama for provider ollama), or name a provider OpenCode knows. See docs/SETTINGS_ARCHITECTURE.md",
			model, key)
	case key == openCodeAnthropicKey:
		if err := openCodeAnthropicModelRefusal(model, modelID); err != nil {
			return OpenCodeRunConfig{}, err
		}
		providers[key] = openCodeAnthropicBlockJSON{
			NPM: openCodeAnthropicNPM,
			Options: openCodeAnthropicOptionsJSON{
				BaseURL: openCodeAnthropicBaseURL,
				APIKey:  "{env:ANTHROPIC_API_KEY}",
			},
			Models: map[string]openCodeHostedModelJSON{
				modelID: {ID: modelID, Provider: openCodeModelProviderJSON{NPM: openCodeAnthropicNPM}},
			},
		}
	}

	steps := in.Run.MaxTurns
	if steps <= 0 {
		steps = openCodeDefaultSteps
	}
	agents := map[string]openCodeAgentJSON{
		"build":      {Model: model, Steps: steps},
		"plan":       {Model: model, Steps: steps},
		"general":    {Model: model, Steps: steps},
		"explore":    {Model: model, Steps: steps},
		"title":      {Model: model, Disable: true},
		"summary":    {Model: model},
		"compaction": {Model: model},
	}
	modes := map[string]openCodeAgentJSON{}
	for _, name := range openCodePinnedModeAgents {
		modes[name] = agents[name]
	}
	cfg := openCodeConfigJSON{
		Model:            model,
		SmallModel:       model,
		DefaultAgent:     "build",
		EnabledProviders: []string{key},
		Provider:         providers,
		Agent:            agents,
		Mode:             modes,
		SubagentDepth:    openCodeSubagentDepth,
		Compaction:       openCodeCompaction(known),
		ToolOutput:       openCodeToolOutputJSON{MaxLines: openCodeToolOutputMaxLines, MaxBytes: openCodeToolOutputMaxBytes},
		Share:            "disabled",
		Autoupdate:       false,
		Snapshot:         in.Snapshot,
		LSP:              in.LSP,
		Formatter:        in.Formatter,
		Instructions:     []string{},
		Skills:           openCodeSkillsJSON{URLs: []string{}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return OpenCodeRunConfig{}, fmt.Errorf("opencode config: %w", err)
	}
	built.Content = string(raw)
	return built, nil
}

// openCodeAnthropicModelRefusal refuses an anthropic model whose entry the
// per-run config cannot pin (ADR-022 § 17). On 1.18.30 a model entry in any
// config layer defines the model, and one the bundled catalog does not list
// then has no limits, so OpenCode never compacts its session. A fast-mode
// entry is one OpenCode derives from a base model and sends under the base
// model's id with options and a header of its own: pinning the entry's own id
// sends a model Anthropic does not serve, and pinning the base model's id
// drops the options and the header, so it is refused for its base model. This
// is a naming refusal, not a control: it only blocks dispatching a model id
// that names a fast-mode entry. A repository config can still set
// options.speed or options.fallbacks on the dispatched base model's own
// entry and turn the same beta headers on without naming the fast-mode entry
// at all; #1638 is what closes that route.
func openCodeAnthropicModelRefusal(model, modelID string) error {
	served, listed := openCodeAnthropicModels[modelID]
	switch {
	case !listed:
		var dispatchable []string
		for id, s := range openCodeAnthropicModels {
			if s == id {
				dispatchable = append(dispatchable, id)
			}
		}
		slices.Sort(dispatchable)
		return fmt.Errorf(
			"model %q is refused: the anthropic models of OpenCode's bundled catalog (opencode 1.18.30) do not include %q, so OpenCode knows no limits for it, and it never compacts a session whose context limit is 0. "+
				"Dispatch a model the catalog lists: anthropic/ followed by %s",
			model, modelID, strings.Join(dispatchable, ", "))
	case served != modelID:
		return fmt.Errorf(
			"model %q is refused: it is the fast-mode entry OpenCode derives from anthropic/%s, which it sends under that model's id with options and a header of its own, and the per-run config cannot pin the model OpenCode sends for it without dropping them. "+
				"Dispatch anthropic/%s instead",
			model, served, served)
	}
	return nil
}

// openCodeCompaction is the compaction policy for a model whose limits are
// known, or OpenCode's own bounds when they are not (a hosted model, whose
// limits come from OpenCode's catalog). For a known model, reserved is the
// output limit, so with the block's limit.input at limit.context the
// threshold is limit.context less limit.output.
func openCodeCompaction(known *config.OpenCodeLimit) openCodeCompactionJSON {
	c := openCodeCompactionJSON{
		Auto:                 true,
		Prune:                true,
		Reserved:             openCodeCompactionReservedCap,
		TailTurns:            openCodeCompactionTailTurns,
		PreserveRecentTokens: openCodeCompactionPreserveCap,
	}
	if known != nil {
		c.Reserved = known.Output
		usable := known.Context - known.Output
		c.PreserveRecentTokens = min(openCodeCompactionPreserveCap, max(openCodeCompactionPreserveFloor, usable/4))
	}
	return c
}

// effectiveLimit is the endpoint's limit for a dispatch whose token cap is
// maxTokens: limit.output is lowered to the cap when one is set. Either limit
// at 0 is refused, because OpenCode never compacts a session whose context
// limit is 0 (opencode 1.18.30's overflow check returns false for it) and a
// session then runs into the server's loaded window.
func (ep OpenCodeEndpoint) effectiveLimit(maxTokens int) (config.OpenCodeLimit, error) {
	limit := ep.Limit
	for _, v := range []struct {
		name  string
		value int
	}{{"context", limit.Context}, {"output", limit.Output}} {
		if v.value <= 0 {
			return config.OpenCodeLimit{}, fmt.Errorf(
				"%s.limit.%s is 0 or missing for endpoint %s: OpenCode compacts a session only when it knows the model's limits, and with a context limit of 0 it never does, so the stage would run into the server's loaded window. "+
					"Set %s.limit.context to the context the server has loaded (at or below it, not the model's maximum) and %s.limit.output to the most one reply may use, in ~/.nightgauge/config.yaml",
				ep.ConfigKey, v.name, ep.ID, ep.ConfigKey, ep.ConfigKey)
		}
	}
	if limit.Output >= limit.Context {
		return config.OpenCodeLimit{}, fmt.Errorf("%s.limit.output (%d) must be less than %s.limit.context (%d) for endpoint %s: a reply cannot take the whole window", ep.ConfigKey, limit.Output, ep.ConfigKey, limit.Context, ep.ID)
	}
	if maxTokens > 0 && maxTokens < limit.Output {
		limit.Output = maxTokens
	}
	return limit, nil
}

func findOpenCodeEndpoint(endpoints []OpenCodeEndpoint, key string) (OpenCodeEndpoint, bool) {
	for _, ep := range endpoints {
		if ep.ID == key {
			return ep, true
		}
	}
	return OpenCodeEndpoint{}, false
}

// openCodeIsLocalKey reports whether model's provider key names a model
// server the operator runs (lmstudio, ollama), which only a declared endpoint
// can describe. A key outside the normalization table, such as a second
// endpoint's id, is not one; BuildOpenCodeConfig refuses it unless an
// endpoint declares it, because it is not a catalog key either.
func openCodeIsLocalKey(model string) bool {
	provider, _, _ := models.ParseOpenCodeModel(model)
	return models.IsLocalProvider(provider)
}

// openCodeEndpointURLFile is where the run keeps endpoint id's base URL.
func openCodeEndpointURLFile(root, id string) string {
	return filepath.Join(root, openCodeRunFilesDir, id+".base-url")
}

// openCodeCheckRunRoot refuses a root the content cannot refer into: it must
// be absolute, and a {file:...} reference ends at the first '}' and is read
// from the JSON text as written, so the path may hold no brace, quote,
// backslash or control character.
func openCodeCheckRunRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("opencode config: the run root %q is not an absolute path", root)
	}
	if strings.ContainsAny(root, "{}\"\\") || strings.IndexFunc(root, unicode.IsControl) >= 0 {
		return fmt.Errorf("opencode config: the run root %q holds a brace, quote, backslash or control character, which a config file reference cannot carry", root)
	}
	return nil
}

// OpenCodeRunRequest is what PrepareOpenCodeRun prepares a spawn from.
type OpenCodeRunRequest struct {
	// Home is the operator's home directory.
	Home string
	// ID names the run root: a run identity, or one minted for the dispatch.
	ID string
	// MachineConfigDir is the machine-tier config directory
	// (config.MachineConfigDir).
	MachineConfigDir string
	// Run is the dispatch.
	Run RunOptions
	// Settings is the machine-tier `opencode:` block
	// (config.LoadOpenCodeConfig).
	Settings config.OpenCodeConfig
	// Lookup reads the environment this process inherited.
	Lookup func(string) (string, bool)
	// GOOS is runtime.GOOS.
	GOOS string
	// ManagedConfigFiles replaces the machine's managed OpenCode config files
	// (openCodeManagedConfigFiles); nil means this machine's. Only tests set
	// it, because the real files are outside any directory a test may write.
	ManagedConfigFiles []string
}

// OpenCodeRun is everything an opencode spawn is given besides its argv and
// the adapter's own exports, and the JSON `nightgauge opencode config` prints.
type OpenCodeRun struct {
	// SchemaVersion is OpenCodeConfigSchemaVersion.
	SchemaVersion string `json:"schema_version"`
	// ConfigContent is OPENCODE_CONFIG_CONTENT.
	ConfigContent string `json:"config_content"`
	// Env is every variable the spawn sets from the run: the isolation
	// variables (OpenCodeIsolationEnv) and OPENCODE_CONFIG_CONTENT. It holds
	// no credential.
	Env map[string]string `json:"env"`
	// EnvWithhold is what the spawn must not inherit: a caller removes every
	// inherited variable it names before it adds Env, as the manager does for
	// the Go path (OpenCodeWithholdsEnv).
	EnvWithhold OpenCodeEnvWithhold `json:"env_withhold"`
	// PluginDir is the directory in the run's OpenCode config directory that
	// OpenCode loads the run's plugins from.
	PluginDir string `json:"plugin_dir"`
	// RunDir is the run's root.
	RunDir string `json:"run_dir"`
	// NonLoopback is false only when the stage dispatches to a declared
	// endpoint on this machine. It is true for an endpoint elsewhere and for
	// every hosted provider, so a claim that the run stays offline does not
	// hold.
	NonLoopback bool `json:"non_loopback"`
}

// OpenCodeEnvWithhold is OpenCodeWithholdsEnv for one dispatch, as data: an
// inherited variable is withheld when its name starts with one of Prefixes
// or is one of Names. Names are sorted.
type OpenCodeEnvWithhold struct {
	Prefixes []string `json:"prefixes"`
	Names    []string `json:"names"`
}

// OpenCodeEnvWithholdFor is the withheld set of a dispatch to model. It holds
// exactly the names OpenCodeWithholdsEnv withholds: every OPENCODE_* variable,
// the provider base-URL variables, and every catalog variable of a model
// service other than the dispatched one.
func OpenCodeEnvWithholdFor(model string) OpenCodeEnvWithhold {
	names := slices.Clone(openCodeEndpointEnv)
	for name := range openCodeCatalogEnvNames {
		if OpenCodeWithholdsEnv(model, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return OpenCodeEnvWithhold{Prefixes: []string{openCodeWithheldPrefix}, Names: slices.Compact(names)}
}

// PrepareOpenCodeRun builds the config for req.Run, and only when that
// succeeds, and the machine holds no OpenCode config a run cannot be isolated
// from, creates the run's root, or reuses it, writes the files the config
// refers to and resolves the isolation environment. A refused dispatch
// creates nothing. Creating a root also sweeps the roots no stage has used
// for OpenCodeOrphanMaxAge, and a root holding stored logins is refused
// (openCodeStoredLoginRefusal).
//
// Unless req.Settings opts into the operator's own OpenCode config, a
// $HOME/.opencode holding config (openCodeHomeConfigRefusal) and the
// machine's managed OpenCode config (openCodeManagedConfigRefusal) refuse
// the dispatch; with the opt-in, stderr says what the run reads. Both follow
// the same Settings the environment is built from, so a setting changed
// between two reads can never leave a run with neither the refusal nor the
// notice.
//
// The adapter's PrepareRunRoot and `nightgauge opencode config` both call it,
// so the SDK path and the Go path run under the same bytes and the same
// refusals.
func PrepareOpenCodeRun(req OpenCodeRunRequest) (*OpenCodeRun, error) {
	rootPath, err := OpenCodeRunRoot(req.Home, req.ID)
	if err != nil {
		return nil, err
	}
	input, err := OpenCodeConfigInputFor(req.Settings, req.Run, rootPath, req.Lookup)
	if err != nil {
		return nil, err
	}
	built, err := BuildOpenCodeConfig(input)
	if err != nil {
		return nil, err
	}
	if req.Settings.InheritUserConfig {
		fmt.Fprintf(os.Stderr, "[opencode] %s is on: this dispatch also reads your own OpenCode config (your XDG OpenCode config directory, ~/.opencode and any managed OpenCode config on this machine). Every key the per-run config sets still wins over it except a managed config's, which outranks them all; stored logins are not inherited, but an API key written in that config is\n",
			openCodeInheritSetting)
	} else {
		if err := openCodeHomeConfigRefusal(req.Home); err != nil {
			return nil, err
		}
		managed := req.ManagedConfigFiles
		if managed == nil {
			managed = openCodeManagedConfigFiles(req.GOOS, openCodeUsername())
		}
		if err := openCodeManagedConfigRefusal(managed); err != nil {
			return nil, err
		}
	}

	root, created, err := EnsureOpenCodeRunRoot(req.Home, req.ID, req.Lookup)
	if err != nil {
		return nil, err
	}
	if created {
		removed, sweepErr := SweepOpenCodeRunRoots(req.Home, OpenCodeOrphanMaxAge, time.Now())
		if len(removed) > 0 {
			fmt.Fprintf(os.Stderr, "[opencode] deleted %d per-run root(s) no stage had used for %s: %s\n",
				len(removed), OpenCodeOrphanMaxAge, strings.Join(removed, ", "))
		}
		if sweepErr != nil {
			fmt.Fprintf(os.Stderr, "[opencode] orphaned per-run root sweep (non-fatal): %v\n", sweepErr)
		}
	}
	if err := openCodeStoredLoginRefusal(root); err != nil {
		return nil, err
	}
	if err := writeOpenCodeRunFiles(root, built.Files); err != nil {
		return nil, err
	}
	env, err := OpenCodeIsolationEnv(OpenCodeIsolation{
		Root:              root,
		Home:              req.Home,
		Lookup:            req.Lookup,
		GOOS:              req.GOOS,
		MachineConfigDir:  req.MachineConfigDir,
		InheritUserConfig: req.Settings.InheritUserConfig,
	})
	if err != nil {
		return nil, err
	}
	env[openCodeConfigContentEnvVar] = built.Content
	return &OpenCodeRun{
		SchemaVersion: OpenCodeConfigSchemaVersion,
		ConfigContent: built.Content,
		Env:           env,
		EnvWithhold:   OpenCodeEnvWithholdFor(req.Run.Model),
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		NonLoopback:   built.NonLoopback,
	}, nil
}

// writeOpenCodeRunFiles writes each file, mode 0600, into the root's
// Nightgauge directory, a 0700 directory that is refused if it is a symbolic
// link. Each file is written to a temporary name and renamed into place, so a
// link planted at the name is replaced, never followed, and a stage of the
// same run reading it sees the old content or the new.
func writeOpenCodeRunFiles(root string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	dir := filepath.Join(root, openCodeRunFilesDir)
	if _, err := ensurePrivateDir(dir); err != nil {
		return err
	}
	for path, content := range files {
		if filepath.Dir(path) != dir {
			return fmt.Errorf("opencode run root: %s is outside %s", path, dir)
		}
		tmp, err := os.CreateTemp(dir, ".write-*")
		if err != nil {
			return fmt.Errorf("opencode run root: %w", err)
		}
		_, writeErr := tmp.WriteString(content)
		closeErr := tmp.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("opencode run root: %w", err)
		}
		if err := os.Chmod(tmp.Name(), 0o600); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("opencode run root: %w", err)
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("opencode run root: %w", err)
		}
	}
	return nil
}
