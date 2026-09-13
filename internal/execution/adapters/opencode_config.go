package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
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
// over each of them for every key it sets, so none of them can lift a limit,
// re-point a provider or re-enable sharing. Only the machine's managed config
// sits above it, and PreDispatch refuses a dispatch while one exists unless
// the operator opted into their own config. A layer can still add keys this
// config does not set; the project-config tamper-gate warning says which.
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
// after it verbatim. reserved and preserve_recent_tokens take the bounds
// opencode 1.18.30 applies to its own defaults (reserved at most 20000,
// preserve_recent_tokens between 2000 and 15000 and at most a quarter of the
// usable window), computed from the limits Nightgauge knows, so the values are
// explicit and do not move with the binary. On 1.18.30 reserved changes the
// compaction threshold only for a model with a limit.input, which no block
// here sets; the threshold is limit.context less limit.output.
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
	// NonLoopback is true when the dispatched endpoint is not on this
	// machine.
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

type openCodeModelJSON struct {
	Limit    openCodeLimitJSON `json:"limit"`
	ToolCall bool              `json:"tool_call"`
}

type openCodeLimitJSON struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// openCodeKeyedBlockJSON is a hosted provider block whose only setting is the
// reference its API key is read from.
type openCodeKeyedBlockJSON struct {
	Options struct {
		APIKey string `json:"apiKey"`
	} `json:"options"`
}

// BuildOpenCodeConfig builds the per-run config for in.Run. It is pure: it
// reads no file and no environment beyond in.Lookup, and the same input gives
// the same bytes.
//
// It sets:
//
//   - model and small_model, and every agent's model, to the dispatched model,
//     and default_agent to build, so no request carrying the stage's prompt or
//     transcript goes to another model (ADR-022 § 15);
//   - enabled_providers to the dispatched provider key alone, so OpenCode
//     loads no other provider from credentials the stage keeps for its tools
//     (the forge tokens, AWS and Google Cloud) and not its own free one;
//   - the dispatched provider's block: a complete block for a declared
//     endpoint, keyed by the endpoint's id, with its limits, timeouts and
//     tool calls, or the anthropic key reference;
//   - steps on the build, plan, general and explore agents, and the same on
//     the legacy mode.build entry, which 1.18.30 applies over agent.build
//     after every layer; subagent_depth; the title agent disabled;
//   - compaction, tool_output, share "disabled", autoupdate false, snapshot,
//     lsp and formatter, and empty instructions and skills.urls.
//
// It refuses a model the adapter cannot dispatch, an anthropic/ model while
// ANTHROPIC_API_KEY is unset, a local provider key no endpoint declares, and
// an endpoint whose limit.context or limit.output is 0 or missing.
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
	if err := openCodeAnthropicRefusal(model, in.Lookup); err != nil {
		return OpenCodeRunConfig{}, err
	}
	if err := openCodeCheckRunRoot(in.RunRoot); err != nil {
		return OpenCodeRunConfig{}, err
	}
	key, modelID, _ := strings.Cut(model, "/")

	built := OpenCodeRunConfig{}
	providers := map[string]any{}
	var known *config.OpenCodeLimit // the dispatched model's limits, when Nightgauge knows them
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
				modelID: {Limit: openCodeLimitJSON{Context: limit.Context, Output: limit.Output}, ToolCall: true},
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
	case key == openCodeAnthropicKey:
		block := openCodeKeyedBlockJSON{}
		block.Options.APIKey = "{env:ANTHROPIC_API_KEY}"
		providers[key] = block
	}

	steps := in.Run.MaxTurns
	if steps <= 0 {
		steps = openCodeDefaultSteps
	}
	build := openCodeAgentJSON{Model: model, Steps: steps}
	cfg := openCodeConfigJSON{
		Model:            model,
		SmallModel:       model,
		DefaultAgent:     "build",
		EnabledProviders: []string{key},
		Provider:         providers,
		Agent: map[string]openCodeAgentJSON{
			"build":      build,
			"plan":       {Model: model, Steps: steps},
			"general":    {Model: model, Steps: steps},
			"explore":    {Model: model, Steps: steps},
			"title":      {Model: model, Disable: true},
			"summary":    {Model: model},
			"compaction": {Model: model},
		},
		Mode:          map[string]openCodeAgentJSON{"build": build},
		SubagentDepth: openCodeSubagentDepth,
		Compaction:    openCodeCompaction(known),
		ToolOutput:    openCodeToolOutputJSON{MaxLines: openCodeToolOutputMaxLines, MaxBytes: openCodeToolOutputMaxBytes},
		Share:         "disabled",
		Autoupdate:    false,
		Snapshot:      in.Snapshot,
		LSP:           in.LSP,
		Formatter:     in.Formatter,
		Instructions:  []string{},
		Skills:        openCodeSkillsJSON{URLs: []string{}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return OpenCodeRunConfig{}, fmt.Errorf("opencode config: %w", err)
	}
	built.Content = string(raw)
	return built, nil
}

// openCodeCompaction is the compaction policy for a model whose limits are
// known, or OpenCode's own bounds when they are not (a hosted model, whose
// limits come from OpenCode's catalog).
func openCodeCompaction(known *config.OpenCodeLimit) openCodeCompactionJSON {
	c := openCodeCompactionJSON{
		Auto:                 true,
		Prune:                true,
		Reserved:             openCodeCompactionReservedCap,
		TailTurns:            openCodeCompactionTailTurns,
		PreserveRecentTokens: openCodeCompactionPreserveCap,
	}
	if known != nil {
		c.Reserved = min(openCodeCompactionReservedCap, known.Output)
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
// can describe.
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
	// PluginDir is the directory in the run's OpenCode config directory that
	// OpenCode loads the run's plugins from.
	PluginDir string `json:"plugin_dir"`
	// RunDir is the run's root.
	RunDir string `json:"run_dir"`
	// NonLoopback is true when the dispatched endpoint is not on this
	// machine, so a claim that the run stays offline does not hold.
	NonLoopback bool `json:"non_loopback"`
}

// PrepareOpenCodeRun builds the config for req.Run, and only when that
// succeeds creates the run's root, or reuses it, writes the files the config
// refers to and resolves the isolation environment. A refused config creates
// nothing. Creating a root also sweeps the roots no stage has used for
// OpenCodeOrphanMaxAge, and a root holding stored logins is refused
// (openCodeStoredLoginRefusal).
//
// The adapter's PrepareRunRoot and `nightgauge opencode config` both call it,
// so the SDK path and the Go path run under the same bytes.
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
