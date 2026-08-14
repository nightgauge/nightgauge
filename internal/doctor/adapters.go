package doctor

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	yaml "gopkg.in/yaml.v3"
)

// AdapterHealth is the deterministic, per-adapter section of the doctor report
// (Issue #4031). It captures only facts the Go binary can establish without an
// LLM and without provider auth round-trips: CLI binary presence + version,
// SDK/HTTP adapter configuration (API key / local-model env), and — for Codex —
// the MCP managed-block state in `config.toml`.
//
// Auth status (e.g. `codex login status`) is intentionally NOT probed here: the
// VSCode Adapter Doctor layers the SDK's `validateAdapterAuth` on top of this
// structured data, keeping auth logic in one place (the SDK adapters) and this
// section fast + side-effect-light for skill preflight that parses
// `nightgauge doctor --adapters … --json`.
type AdapterHealth struct {
	Adapter         string            `json:"adapter"`           // requested name (e.g. "codex", "claude")
	Kind            string            `json:"kind"`              // "cli" | "sdk" | "http"
	Binary          string            `json:"binary,omitempty"`  // CLI binary name (cli kind)
	Installed       bool              `json:"installed"`         // cli: on PATH; sdk: api key set; http: model configured + bridge present
	Path            string            `json:"path,omitempty"`    // resolved binary path (cli kind)
	Version         string            `json:"version,omitempty"` // parsed semver from `<bin> --version`
	VersionOK       bool              `json:"version_ok"`        // version >= MinVersion (true when no floor)
	MinVersion      string            `json:"min_version,omitempty"`
	Mcp             *AdapterMcpHealth `json:"mcp,omitempty"`        // codex only
	ServerURL       string            `json:"server_url,omitempty"` // http kind: resolved local server base URL
	ServerReachable bool              `json:"server_reachable"`     // http kind: base URL answered the probe (#57)
	Model           string            `json:"model,omitempty"`      // http kind: resolved model id (env, else machine-tier config)
	ModelOK         *bool             `json:"model_ok,omitempty"`   // http kind: configured model is in the /models catalog
	OK              bool              `json:"ok"`                   // adapter is usable for its kind's primary requirement
	Remediation     string            `json:"remediation,omitempty"`
}

// AdapterMcpHealth describes the Codex MCP managed-block state (Issue #4025).
// MCP is informational — its absence never flips AdapterHealth.OK false, since
// Codex runs fine without the pipeline's MCP servers provisioned.
type AdapterMcpHealth struct {
	ConfigPath    string `json:"config_path"`    // $CODEX_HOME/config.toml (resolved)
	ConfigPresent bool   `json:"config_present"` // the file exists
	ManagedBlock  bool   `json:"managed_block"`  // the nightgauge managed MCP block is present
}

// adapterKind classifies how an adapter is invoked, which determines what
// "installed/configured" means for it.
type adapterKind string

const (
	kindCLI  adapterKind = "cli"  // spawns a CLI binary (claude, codex, gemini, copilot)
	kindSDK  adapterKind = "sdk"  // native SDK via API key (claude-sdk, gemini-sdk)
	kindHTTP adapterKind = "http" // local OpenAI-compatible server (ollama, lm-studio)
)

// codexManagedMcpBegin is the line-anchored marker the SDK CodexMcpProvisioner
// writes at the head of its managed `[mcp_servers.*]` block. MIRRORS
// CODEX_MCP_MANAGED_BEGIN in
// packages/nightgauge-sdk/src/context/codexMcpConfig.ts — keep in sync.
const codexManagedMcpBegin = "# >>> BEGIN NIGHTGAUGE MANAGED MCP >>>"

// adapterSpec is the declarative description of an adapter's health
// requirements. Min versions MIRROR the canonical SDK constants
// (packages/nightgauge-sdk/src/cli/adapters/*Adapter.ts MIN_KNOWN_VERSION);
// TestAdapterSpecConstants guards the values so a drift is a deliberate edit.
type adapterSpec struct {
	binary         string      // CLI binary name (kindCLI only)
	kind           adapterKind //
	minVersion     string      // "" when no floor is enforced
	apiKeyEnvs     []string    // kindSDK: any one present satisfies "configured"
	modelEnv       string      // kindHTTP: env var carrying the required local model id
	modelConfigKey string      // kindHTTP: machine-tier dotted key (e.g. lm_studio.model)
	pullHint       string      // kindHTTP: remediation command prefix (e.g. "lms get")
	bridgeBinary   string      // kindHTTP: CLI the adapter spawns through (mirrors registry.adapterBinary)
	baseURLEnv     string      // kindHTTP: env var overriding the local server base URL
	defaultBaseURL string      // kindHTTP: base URL when the env override is unset
	mcp            bool        // codex: provisions an MCP managed block in config.toml
}

// adapterSpecs is keyed by canonical adapter name. The user-facing names from
// the VSCode extension (claude, codex, gemini, gemini-sdk, lm-studio, ollama,
// copilot) all resolve here after normalizeAdapterName.
var adapterSpecs = map[string]adapterSpec{
	"claude-headless": {binary: "claude", kind: kindCLI},
	"claude-sdk":      {kind: kindSDK, apiKeyEnvs: []string{"ANTHROPIC_API_KEY"}},
	"codex":           {binary: "codex", kind: kindCLI, minVersion: "0.111.0", mcp: true},
	"gemini":          {binary: "gemini", kind: kindCLI, minVersion: "0.29.0"},
	"gemini-sdk":      {kind: kindSDK, apiKeyEnvs: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	"ollama":          {kind: kindHTTP, modelEnv: "NIGHTGAUGE_OLLAMA_MODEL", modelConfigKey: "ollama.model", pullHint: "ollama pull", bridgeBinary: "claude", baseURLEnv: "NIGHTGAUGE_OLLAMA_BASE_URL", defaultBaseURL: "http://localhost:11434/v1"},
	"lm-studio":       {kind: kindHTTP, modelEnv: "NIGHTGAUGE_LM_STUDIO_MODEL", modelConfigKey: "lm_studio.model", pullHint: "lms get", bridgeBinary: "claude", baseURLEnv: "NIGHTGAUGE_LM_STUDIO_BASE_URL", defaultBaseURL: "http://localhost:1234/v1"},
	"copilot":         {binary: "copilot", kind: kindCLI},
	"grok":            {binary: "grok", kind: kindCLI, minVersion: "1.0.0"},
}

// adapterAliases maps user-facing aliases to the canonical adapterSpecs key,
// mirroring the execution registry's alias table.
var adapterAliases = map[string]string{
	"claude":          "claude-headless",
	"gemini-headless": "gemini",
	"lmstudio":        "lm-studio",
	"grok-headless":   "grok",
	"xai":             "grok",
}

// AllAdapterNames returns every canonical adapter the doctor can health-check,
// in a stable display order (CLI adapters first, then SDK, then local HTTP).
// Backs `doctor --adapters all`.
func AllAdapterNames() []string {
	return []string{
		"claude",
		"codex",
		"gemini",
		"copilot",
		"grok",
		"claude-sdk",
		"gemini-sdk",
		"ollama",
		"lm-studio",
	}
}

// normalizeAdapterName lowercases, trims, and resolves aliases.
func normalizeAdapterName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := adapterAliases[n]; ok {
		return canonical
	}
	return n
}

// adapterProbe bundles the side-effecting dependencies so tests can inject
// fakes for binary lookup, the `--version` spawn, and filesystem reads.
type adapterProbe struct {
	lookPath     func(string) (string, error)
	runVersion   func(path string) (string, error) // combined output of `<path> --version`
	readFile     func(string) ([]byte, error)
	getenv       func(string) string
	httpProbe    func(baseURL string) localServerProbeResult // kindHTTP: reachability + /models catalog (#520)
	machineModel func(adapter string) string                 // kindHTTP: machine-tier model fallback
	codexHome    string                                      // resolved $CODEX_HOME (or ~/.codex); injectable for tests
}

func defaultAdapterProbe() adapterProbe {
	return adapterProbe{
		lookPath: exec.LookPath,
		runVersion: func(path string) (string, error) {
			out, err := exec.Command(path, "--version").CombinedOutput()
			return string(out), err
		},
		readFile:     os.ReadFile,
		getenv:       os.Getenv,
		httpProbe:    probeLocalServer,
		machineModel: readMachineHTTPModel,
		codexHome:    resolveCodexHome(),
	}
}

// resolveCodexHome mirrors the SDK CodexMcpProvisioner.resolveCodexHome:
// `$CODEX_HOME` when set, otherwise `~/.codex`.
func resolveCodexHome() string {
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

// CheckAdapters returns deterministic health for each requested adapter, in the
// given order. Unknown adapter names yield an AdapterHealth with OK=false and a
// remediation naming the valid set, rather than being dropped silently.
func CheckAdapters(names []string) []AdapterHealth {
	return checkAdaptersWithProbe(names, defaultAdapterProbe())
}

func checkAdaptersWithProbe(names []string, probe adapterProbe) []AdapterHealth {
	out := make([]AdapterHealth, 0, len(names))
	for _, name := range names {
		out = append(out, checkAdapter(name, probe))
	}
	return out
}

func checkAdapter(name string, probe adapterProbe) AdapterHealth {
	canonical := normalizeAdapterName(name)
	spec, ok := adapterSpecs[canonical]
	h := AdapterHealth{Adapter: strings.TrimSpace(name)}
	if !ok {
		h.OK = false
		h.Remediation = "Unknown adapter; valid: claude, claude-sdk, codex, gemini, gemini-sdk, ollama, lm-studio, copilot."
		return h
	}
	h.Kind = string(spec.kind)

	switch spec.kind {
	case kindCLI:
		h.Binary = spec.binary
		h.MinVersion = spec.minVersion
		path, err := probe.lookPath(spec.binary)
		if err != nil {
			h.Installed = false
			h.VersionOK = false
			h.Remediation = "Install the " + spec.binary + " CLI and ensure it is on PATH."
		} else {
			h.Installed = true
			h.Path = path
			h.Version = parseAdapterVersion(probe.runVersion(path))
			h.VersionOK = versionMeetsFloor(h.Version, spec.minVersion)
			if !h.VersionOK {
				cur := h.Version
				if cur == "" {
					cur = "unknown"
				}
				h.Remediation = "Update " + spec.binary + " to >= " + spec.minVersion + " (current " + cur + ")."
			}
		}
		h.OK = h.Installed && h.VersionOK

	case kindSDK:
		h.Installed = anyEnvSet(probe.getenv, spec.apiKeyEnvs)
		h.VersionOK = true // no CLI floor for SDK adapters
		h.OK = h.Installed
		if !h.OK {
			h.Remediation = "Set one of: " + strings.Join(spec.apiKeyEnvs, ", ") + "."
		}

	case kindHTTP:
		// ollama / lm-studio do NOT run standalone — the execution registry routes
		// them THROUGH a CLI bridge (claude); see internal/execution/adapters
		// adapterBinary(). Readiness requires a configured model (env, else
		// machine-tier config), the bridge binary on PATH, a reachable local
		// server (#57), AND the resolved model id present in GET /models (#520).
		h.VersionOK = true // no CLI version floor for local servers
		model := strings.TrimSpace(probe.getenv(spec.modelEnv))
		if model == "" && probe.machineModel != nil {
			model = strings.TrimSpace(probe.machineModel(canonical))
		}
		if model != "" {
			h.Model = model
		}
		modelSet := model != ""
		bridgeOK := true
		if spec.bridgeBinary != "" {
			if _, err := probe.lookPath(spec.bridgeBinary); err != nil {
				bridgeOK = false
			}
		}
		baseURL := strings.TrimSpace(probe.getenv(spec.baseURLEnv))
		if baseURL == "" {
			baseURL = spec.defaultBaseURL
		}
		h.ServerURL = baseURL
		var probeRes localServerProbeResult
		if probe.httpProbe != nil {
			probeRes = probe.httpProbe(baseURL)
		}
		h.ServerReachable = probeRes.reachable
		h.Installed = modelSet && bridgeOK

		modelOK := false
		switch {
		case !modelSet:
			h.ModelOK = boolPtr(false)
		case probeRes.reachable && !probeRes.parseErr:
			modelOK = catalogContains(probeRes.ids, model)
			h.ModelOK = boolPtr(modelOK)
		case probeRes.reachable && probeRes.parseErr:
			h.ModelOK = boolPtr(false)
		}
		h.OK = h.Installed && h.ServerReachable && modelOK
		switch {
		case !modelSet && !bridgeOK:
			h.Remediation = "Set " + spec.modelEnv + " or " + spec.modelConfigKey +
				" in ~/.nightgauge/config.yaml (this adapter requires model; there is no default), " +
				"start the local server, and install the " + spec.bridgeBinary + " CLI bridge (must be on PATH)."
		case !modelSet:
			h.Remediation = "Set " + spec.modelEnv + " or " + spec.modelConfigKey +
				" in ~/.nightgauge/config.yaml (this adapter requires model; there is no default)."
		case !bridgeOK:
			h.Remediation = "Install the " + spec.bridgeBinary + " CLI bridge that " + canonical + " runs through (must be on PATH)."
		case !h.ServerReachable:
			h.Remediation = "Local server unreachable at " + baseURL + ": start it, or point " + spec.baseURLEnv + " at the right address."
		case probeRes.parseErr:
			h.Remediation = "Local server at " + baseURL + " is reachable but its /models catalog could not be parsed; cannot verify configured model " + model + "."
		case !modelOK:
			h.Remediation = "Configured model " + model + " is not in the server catalog at " + baseURL +
				". Download it with: " + spec.pullHint + " " + model
		}
	}

	if spec.mcp {
		h.Mcp = checkCodexMcp(probe)
	}
	return h
}

// checkCodexMcp reports whether Codex's config.toml exists and whether the
// nightgauge managed MCP block is present in it. Best-effort: a missing or
// unreadable file simply reports ConfigPresent=false.
func checkCodexMcp(probe adapterProbe) *AdapterMcpHealth {
	configPath := filepath.Join(probe.codexHome, "config.toml")
	mcp := &AdapterMcpHealth{ConfigPath: configPath}
	data, err := probe.readFile(configPath)
	if err != nil {
		return mcp
	}
	mcp.ConfigPresent = true
	mcp.ManagedBlock = lineHasMarker(string(data), codexManagedMcpBegin)
	return mcp
}

// lineHasMarker reports whether marker appears line-anchored in text, matching
// the SDK's lineAnchoredIndex (codexMcpConfig.ts) semantics EXACTLY: leading
// whitespace is allowed and the marker is matched as a prefix (the SDK regex
// `^[ \t]*${marker}` has no end-anchor), so a BEGIN line with trailing content
// still counts as present, just as the SDK's hasManagedMcpBlock would report.
func lineHasMarker(text, marker string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(strings.TrimSuffix(line, "\r"), " \t")
		if strings.HasPrefix(trimmed, marker) {
			return true
		}
	}
	return false
}

func anyEnvSet(getenv func(string) string, keys []string) bool {
	for _, k := range keys {
		if strings.TrimSpace(getenv(k)) != "" {
			return true
		}
	}
	return false
}

// adapterVersionRe extracts the first dotted numeric version from `--version`
// output (e.g. "codex 0.112.0\n" → "0.112.0"). Mirrors the SDK
// verifyCLIInstalled regex /(\d+\.\d+\.\d+)/.
var adapterVersionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

func parseAdapterVersion(out string, err error) string {
	if err != nil {
		return ""
	}
	m := adapterVersionRe.FindString(out)
	return m
}

// versionMeetsFloor reports whether version >= min. An empty min means no floor
// (always true). An unparseable/empty version against a real floor is treated
// as failing — we cannot prove it meets the floor.
func versionMeetsFloor(version, min string) bool {
	if strings.TrimSpace(min) == "" {
		return true
	}
	if strings.TrimSpace(version) == "" {
		return false
	}
	cmp := compareDottedVersions(version, min)
	return cmp >= 0
}

// compareDottedVersions returns -1/0/1 for a<b / a==b / a>b across dot-separated
// numeric components, zero-padding the shorter to the longer length. Non-numeric
// components compare as 0. Self-contained to avoid coupling doctor to the
// release package's semver helper.
func compareDottedVersions(a, b string) int {
	pa := dottedParts(a)
	pb := dottedParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(pa) {
			ai = pa[i]
		}
		if i < len(pb) {
			bi = pb[i]
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

func dottedParts(v string) []int {
	fields := strings.Split(strings.TrimSpace(v), ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

// localServerProbeResult is the kindHTTP /models probe: reachability plus the
// parsed catalog. Reachability is independent of parse success — a listener
// that returns HTML still counts as up (#57); catalog membership is #520.
type localServerProbeResult struct {
	reachable bool
	ids       []string
	parseErr  bool
}

// maxModelsBody bounds the /models response so a pathological payload cannot
// stall or balloon doctor. Observed LM Studio catalogs are a few KB.
const maxModelsBody = 1 << 20

// probeLocalServer answers whether an OpenAI-compatible local server
// (ollama / LM Studio) is reachable at baseURL and, when it is, parses the
// GET /models catalog. Any HTTP response counts as reachable — even an error
// status proves a listener is up; only transport failures (connection refused,
// timeout, DNS) fail the probe. Bounded so a black-holed address cannot stall
// the doctor.
func probeLocalServer(baseURL string) localServerProbeResult {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil {
		return localServerProbeResult{}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBody+1))
	out := localServerProbeResult{reachable: true}
	if err != nil || len(body) > maxModelsBody {
		out.parseErr = true
		return out
	}
	ids, ok := parseOpenAIModelIDs(body)
	if !ok {
		out.parseErr = true
		return out
	}
	out.ids = ids
	return out
}

func boolPtr(v bool) *bool { return &v }

func catalogContains(ids []string, model string) bool {
	for _, id := range ids {
		if id == model {
			return true
		}
	}
	return false
}

// parseOpenAIModelIDs extracts model ids from an OpenAI-compatible /models
// body: {"object":"list","data":[{"id":"<model>"}, ...]}. Observed LM Studio
// (2026-08-14, GET http://127.0.0.1:1234/v1/models) matches this shape exactly.
// A top-level array or a "models" array is accepted as a defensive fallback
// (same variants LmStudioService already handles) so a minor envelope change
// does not silently pass the catalog check.
func parseOpenAIModelIDs(body []byte) ([]string, bool) {
	var top any
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, false
	}
	var items []any
	switch v := top.(type) {
	case map[string]any:
		switch {
		case isJSONArray(v["data"]):
			items = v["data"].([]any)
		case isJSONArray(v["models"]):
			items = v["models"].([]any)
		default:
			return nil, false
		}
	case []any:
		items = v
	default:
		return nil, false
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		switch m := item.(type) {
		case map[string]any:
			id, ok := m["id"].(string)
			id = strings.TrimSpace(id)
			if ok && id != "" {
				ids = append(ids, id)
			}
		case string:
			id := strings.TrimSpace(m)
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids, true
}

func isJSONArray(v any) bool {
	_, ok := v.([]any)
	return ok
}

// readMachineHTTPModel returns the machine-tier model id for an HTTP adapter
// (lm_studio.model / ollama.model in ~/.nightgauge/config.yaml). Workspace
// lm_studio is deprecated (#3338) and is not consulted. Fail-open: a missing
// or unreadable file is treated as "no machine-tier model".
func readMachineHTTPModel(adapter string) string {
	key := ""
	switch adapter {
	case "lm-studio":
		key = "lm_studio.model"
	case "ollama":
		key = "ollama.model"
	default:
		return ""
	}
	path, err := config.MachineConfigPath()
	if err != nil || strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return yamlDottedString(data, key)
}

func yamlDottedString(data []byte, dotted string) string {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ""
	}
	cur := root
	for _, part := range strings.Split(dotted, ".") {
		m, ok := asStringMap(cur)
		if !ok {
			return ""
		}
		next, ok := m[part]
		if !ok {
			return ""
		}
		cur = next
	}
	s, ok := cur.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func asStringMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}
