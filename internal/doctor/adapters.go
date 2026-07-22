package doctor

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	Installed       bool              `json:"installed"`         // cli: on PATH; sdk: api key set; http: model env set
	Path            string            `json:"path,omitempty"`    // resolved binary path (cli kind)
	Version         string            `json:"version,omitempty"` // parsed semver from `<bin> --version`
	VersionOK       bool              `json:"version_ok"`        // version >= MinVersion (true when no floor)
	MinVersion      string            `json:"min_version,omitempty"`
	Mcp             *AdapterMcpHealth `json:"mcp,omitempty"`        // codex only
	ServerURL       string            `json:"server_url,omitempty"` // http kind: resolved local server base URL
	ServerReachable bool              `json:"server_reachable"`     // http kind: base URL answered the probe (#57)
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
	"ollama":          {kind: kindHTTP, modelEnv: "NIGHTGAUGE_OLLAMA_MODEL", bridgeBinary: "claude", baseURLEnv: "NIGHTGAUGE_OLLAMA_BASE_URL", defaultBaseURL: "http://localhost:11434/v1"},
	"lm-studio":       {kind: kindHTTP, modelEnv: "NIGHTGAUGE_LM_STUDIO_MODEL", bridgeBinary: "claude", baseURLEnv: "NIGHTGAUGE_LM_STUDIO_BASE_URL", defaultBaseURL: "http://localhost:1234/v1"},
	"copilot":         {binary: "copilot", kind: kindCLI},
}

// adapterAliases maps user-facing aliases to the canonical adapterSpecs key,
// mirroring the execution registry's alias table.
var adapterAliases = map[string]string{
	"claude":          "claude-headless",
	"gemini-headless": "gemini",
	"lmstudio":        "lm-studio",
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
	lookPath      func(string) (string, error)
	runVersion    func(path string) (string, error) // combined output of `<path> --version`
	readFile      func(string) ([]byte, error)
	getenv        func(string) string
	httpReachable func(baseURL string) error // kindHTTP: local-server reachability probe (#57)
	codexHome     string                     // resolved $CODEX_HOME (or ~/.codex); injectable for tests
}

func defaultAdapterProbe() adapterProbe {
	return adapterProbe{
		lookPath: exec.LookPath,
		runVersion: func(path string) (string, error) {
			out, err := exec.Command(path, "--version").CombinedOutput()
			return string(out), err
		},
		readFile:      os.ReadFile,
		getenv:        os.Getenv,
		httpReachable: probeLocalServer,
		codexHome:     resolveCodexHome(),
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
		// adapterBinary(). So readiness requires the local-model env, the bridge
		// binary on PATH, AND a reachable local server (#57 — previously the
		// adapter could report healthy with no server running at all).
		h.VersionOK = true // no CLI version floor for local servers
		modelSet := strings.TrimSpace(probe.getenv(spec.modelEnv)) != ""
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
		if probe.httpReachable != nil {
			h.ServerReachable = probe.httpReachable(baseURL) == nil
		}
		h.Installed = modelSet && bridgeOK
		h.OK = h.Installed && h.ServerReachable
		switch {
		case !modelSet && !bridgeOK:
			h.Remediation = "Set " + spec.modelEnv + ", start the local server, and install the " + spec.bridgeBinary + " CLI bridge (must be on PATH)."
		case !modelSet:
			h.Remediation = "Set " + spec.modelEnv + " and start the local server."
		case !bridgeOK:
			h.Remediation = "Install the " + spec.bridgeBinary + " CLI bridge that " + canonical + " runs through (must be on PATH)."
		case !h.ServerReachable:
			h.Remediation = "Local server unreachable at " + baseURL + ": start it, or point " + spec.baseURLEnv + " at the right address."
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

// probeLocalServer answers whether an OpenAI-compatible local server
// (ollama / LM Studio) is reachable at baseURL. Any HTTP response counts as
// reachable — even an error status proves a listener is up; only transport
// failures (connection refused, timeout, DNS) fail the probe. Bounded so a
// black-holed address cannot stall the doctor.
func probeLocalServer(baseURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
