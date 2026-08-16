package doctor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/models"
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
	// Catalog is the live-vs-registry model catalog comparison for kindCLI
	// adapters with a wired catalog probe (#551; grok first-class). Nil means
	// either the adapter has no catalog probe wired (an explicit,
	// evidence-backed decision — see CatalogWarning), or the probe could not
	// run/parse — see CatalogWarning for why in that case too.
	Catalog *CatalogHealth `json:"catalog,omitempty"`
	// CatalogWarning explains a degraded or skipped catalog probe (CLI spawn
	// error, unauthenticated, unparseable output), a deliberate no-catalog
	// adapter spec (#604, prefixed "no catalog probe: " — claude/codex/gemini/
	// copilot today, see adapterSpec.catalogSkipReason), or reports the
	// inverse-drift warning (catalog offers a model the registry does not
	// mark served). Never causes OK=false by itself — only a confirmed
	// Catalog.Missing does.
	CatalogWarning string `json:"catalog_warning,omitempty"`
	OK             bool   `json:"ok"` // adapter is usable for its kind's primary requirement
	Remediation    string `json:"remediation,omitempty"`
}

// CatalogHealth is the live-vs-registry model catalog comparison for a
// kindCLI adapter with a wired catalog probe (#551): the doctor shells the
// adapter's own catalog-listing command (e.g. `grok models`) and diffs the
// result against the registry's transports.cli.served facts for the
// adapter's provider (internal/models, CheckTransportServed's read-only
// sibling data). This catches the #532 drift class — the registry declares a
// model CLI-served but the live CLI catalog does not actually offer it —
// before a run spawns and fails mid-flight, and its inverse (the catalog
// offers a model the registry does not mark served, or omits entirely).
//
// This is the DETECTION half only. Enforcement at selection time
// (CheckTransportServed, fail-closed before spawn) landed in #579.
type CatalogHealth struct {
	Provider  string `json:"provider"`  // registry provider (models.ProviderForAdapter)
	Transport string `json:"transport"` // always "cli" today (models.TransportCLI)
	// Live is every model id the CLI's own catalog command reported.
	Live []string `json:"live"`
	// Default is the CLI's own reported default model id, when the catalog
	// output states one. Diagnostic only — never compared against anything.
	Default string `json:"default,omitempty"`
	// Missing lists registry models declared transports.cli.served=true for
	// Provider/Transport whose id is absent from Live — confirmed drift.
	// Non-empty Missing sets AdapterHealth.OK=false with a named Remediation.
	Missing []string `json:"missing,omitempty"`
	// Undeclared lists Live ids the registry does not mark served:true for
	// Provider/Transport (served:false, the model/transport key is
	// unexpressed, or the id has no registry entry at all) — the inverse
	// drift direction. Warning-only, via AdapterHealth.CatalogWarning; never
	// flips OK.
	Undeclared []string `json:"undeclared,omitempty"`
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
	// catalogArgs/catalogParser (#551): kindCLI adapters that can list their
	// own model catalog wire both — catalogArgs is the subcommand (e.g.
	// grok's {"models"}) and catalogParser turns its output into the live
	// model ids + the CLI's own reported default.
	catalogArgs   []string
	catalogParser func(output string) (ids []string, defaultID string, ok bool)
	// catalogSkipReason (#604) is set on kindCLI adapters that deliberately
	// leave catalogArgs/catalogParser unset: captured, real evidence (a
	// `--help` invocation, or the CLI's absence) showed no model-listing
	// command to build a parser against — see
	// testdata/no-catalog-cli-probes/README.md for the provenance. Exactly
	// one of catalogParser/catalogSkipReason is set for any kindCLI adapter;
	// TestAdapterSpecs_CatalogWiringIsMutuallyExclusive guards this. When
	// set, applyCatalogSkip surfaces it via AdapterHealth.CatalogWarning so
	// "no catalog probe" is always an explicit, explained state rather than
	// silently absent fields indistinguishable from "not gotten to yet."
	catalogSkipReason string
}

// adapterSpecs is keyed by canonical adapter name. The user-facing names from
// the VSCode extension (claude, codex, gemini, gemini-sdk, lm-studio, ollama,
// copilot) all resolve here after normalizeAdapterName.
var adapterSpecs = map[string]adapterSpec{
	"claude-headless": {binary: "claude", kind: kindCLI, catalogSkipReason: claudeNoCatalogReason},
	"claude-sdk":      {kind: kindSDK, apiKeyEnvs: []string{"ANTHROPIC_API_KEY"}},
	"codex":           {binary: "codex", kind: kindCLI, minVersion: "0.111.0", mcp: true, catalogSkipReason: codexNoCatalogReason},
	"gemini":          {binary: "gemini", kind: kindCLI, minVersion: "0.29.0", catalogSkipReason: geminiNoCatalogReason},
	"gemini-sdk":      {kind: kindSDK, apiKeyEnvs: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	"ollama":          {kind: kindHTTP, modelEnv: "NIGHTGAUGE_OLLAMA_MODEL", modelConfigKey: "ollama.model", pullHint: "ollama pull", bridgeBinary: "claude", baseURLEnv: "NIGHTGAUGE_OLLAMA_BASE_URL", defaultBaseURL: "http://localhost:11434/v1"},
	"lm-studio":       {kind: kindHTTP, modelEnv: "NIGHTGAUGE_LM_STUDIO_MODEL", modelConfigKey: "lm_studio.model", pullHint: "lms get", bridgeBinary: "claude", baseURLEnv: "NIGHTGAUGE_LM_STUDIO_BASE_URL", defaultBaseURL: "http://localhost:1234/v1"},
	"copilot":         {binary: "copilot", kind: kindCLI, catalogSkipReason: copilotNoCatalogReason},
	"grok":            {binary: "grok", kind: kindCLI, minVersion: "1.0.0", catalogArgs: []string{"models"}, catalogParser: parseGrokCatalog},
}

// No-catalog skip reasons (#604): each names the captured, real evidence
// backing the "no catalog probe" decision — never a guess. Full provenance
// (host, date, exact command, exit code) lives in
// testdata/no-catalog-cli-probes/README.md; codex-help.txt and
// claude-help.txt there are the verbatim captured `--help` outputs these
// reference.
const (
	codexNoCatalogReason = "codex CLI has no models/catalog listing command — `codex --help` " +
		"(codex-cli 0.145.0, captured 2026-08-16) lists only `-m/--model` to SELECT a model, " +
		"no subcommand that LISTS available ones (evidence: testdata/no-catalog-cli-probes/codex-help.txt). " +
		"Revisit if a future codex release adds one (#604)."
	claudeNoCatalogReason = "claude CLI has no models/catalog listing command — `claude --help` " +
		"(2.1.233, captured 2026-08-16) lists only `--model <model>` to SELECT a model, " +
		"no subcommand that LISTS available ones (evidence: testdata/no-catalog-cli-probes/claude-help.txt). " +
		"Revisit if a future claude release adds one (#604)."
	geminiNoCatalogReason = "no gemini CLI was installed on the machine used to capture doctor catalog-probe " +
		"evidence (2026-08-16, macOS 26.0/Darwin 27.0.0 arm64) — `gemini` was not on PATH and no global npm " +
		"package provided it, so no command/output shape exists to build a parser against " +
		"(evidence: testdata/no-catalog-cli-probes/README.md). Wire a probe once real gemini catalog-command " +
		"output is captured (#604)."
	copilotNoCatalogReason = "no functioning standalone Copilot CLI was found to capture doctor catalog-probe " +
		"evidence (2026-08-16) — the only `copilot` on this machine's PATH is a VS Code Copilot Chat extension " +
		"bootstrap shim that fails immediately without a real CLI installed (`Cannot find GitHub Copilot CLI`), " +
		"not a genuine CLI (evidence: testdata/no-catalog-cli-probes/copilot-help.txt). Wire a probe once real " +
		"copilot catalog-command output is captured (#604)."
)

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
	runVersion   func(path string) (string, error)                // combined output of `<path> --version`
	runCatalog   func(path string, args []string) (string, error) // kindCLI: combined output of `<path> <catalogArgs...>` (#551)
	readFile     func(string) ([]byte, error)
	getenv       func(string) string
	httpProbe    func(baseURL string) localServerProbeResult // kindHTTP: reachability + /models catalog (#520)
	machineModel func(adapter string) string                 // kindHTTP: machine-tier model fallback
	codexHome    string                                      // resolved $CODEX_HOME (or ~/.codex); injectable for tests
}

// catalogProbeTimeout bounds the live catalog-listing spawn (#551), mirroring
// the doctor's other bounded CLI probes (doctor.go's binaryVersion, also 5s)
// so a hung/misbehaving adapter cannot stall `doctor --adapters`.
const catalogProbeTimeout = 5 * time.Second

func defaultAdapterProbe() adapterProbe {
	return adapterProbe{
		lookPath: exec.LookPath,
		runVersion: func(path string) (string, error) {
			out, err := exec.Command(path, "--version").CombinedOutput()
			return string(out), err
		},
		runCatalog: func(path string, args []string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), catalogProbeTimeout)
			defer cancel()
			out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
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
		switch {
		case spec.catalogParser != nil:
			applyCatalogProbe(&h, spec, canonical, probe)
		case spec.catalogSkipReason != "":
			applyCatalogSkip(&h, spec)
		}

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

// applyCatalogProbe runs a kindCLI adapter's live catalog-listing command
// (spec.catalogArgs, e.g. `grok models`) and diffs it against the registry's
// transports.cli.served facts for the adapter's provider (#551) — the
// detection half of the #532 drift class: the registry declares a model
// CLI-served but the CLI's own catalog does not actually offer it.
//
// Only runs when the adapter already passed its baseline (binary present,
// version floor met, if any) — an adapter that is already !OK for that
// reason gets no second, redundant complaint layered on top. Any failure to
// RUN or PARSE the catalog (spawn error, timeout, not authenticated,
// unrecognized output shape) degrades to CatalogWarning and never touches
// OK: only a confirmed comparison against a successfully parsed live catalog
// may fail the adapter, matching "never a hard doctor failure for a missing
// optional adapter."
func applyCatalogProbe(h *AdapterHealth, spec adapterSpec, canonical string, probe adapterProbe) {
	if !h.Installed || !h.VersionOK || probe.runCatalog == nil {
		return
	}
	cmdLabel := spec.binary + " " + strings.Join(spec.catalogArgs, " ")
	out, err := probe.runCatalog(h.Path, spec.catalogArgs)
	if err != nil {
		h.CatalogWarning = "could not run `" + cmdLabel +
			"` (adapter may not be installed correctly or authenticated): " + err.Error()
		return
	}
	ids, defaultID, ok := spec.catalogParser(out)
	if !ok {
		h.CatalogWarning = "could not parse `" + cmdLabel + "` output; skipping catalog drift check"
		return
	}

	provider := models.ProviderForAdapter(canonical)
	missing, undeclared := servedCLIModelDiff(provider, ids)
	h.Catalog = &CatalogHealth{
		Provider:   provider,
		Transport:  models.TransportCLI,
		Live:       ids,
		Default:    defaultID,
		Missing:    missing,
		Undeclared: undeclared,
	}

	if len(missing) > 0 {
		h.OK = false
		h.Remediation = "provider " + provider + " model(s) " + strings.Join(missing, ", ") +
			" are declared transports." + models.TransportCLI + ".served=true in the registry, but `" + cmdLabel +
			"` does not list them — confirm with `" + cmdLabel + "`, then correct the registry's transports." +
			models.TransportCLI + ".served fact if the CLI catalog changed (the #532 class)."
	}
	if len(undeclared) > 0 {
		warn := "`" + cmdLabel + "` catalog offers " + strings.Join(undeclared, ", ") +
			", which the registry does not mark transports." + models.TransportCLI + ".served=true for provider " +
			provider + " (served:false or unexpressed) — possible registry drift, the inverse of #532."
		if h.CatalogWarning != "" {
			h.CatalogWarning += "; " + warn
		} else {
			h.CatalogWarning = warn
		}
	}
}

// applyCatalogSkip records why a kindCLI adapter with no wired catalog probe
// (#604) has no Catalog: spec.catalogSkipReason names the captured, real
// evidence (a `--help` invocation showing no models/catalog command, or the
// CLI's absence entirely — see testdata/no-catalog-cli-probes/README.md)
// that justified marking it no-catalog rather than guessing at a shape. This
// keeps "no catalog probe" an explicit, explained state on every checkAdapter
// call for that adapter — never silently indistinguishable from a probe that
// simply has not been wired yet.
//
// Gated the same way as applyCatalogProbe (baseline already passed) so an
// adapter that is already !OK for an unrelated reason (missing binary, stale
// version) gets no additional, redundant note layered on top — that failure
// is already reported via Installed/VersionOK/Remediation.
func applyCatalogSkip(h *AdapterHealth, spec adapterSpec) {
	if !h.Installed || !h.VersionOK {
		return
	}
	h.CatalogWarning = "no catalog probe: " + spec.catalogSkipReason
}

// servedCLIModelDiff compares a live CLI catalog against the registry's
// transports.cli.served facts for provider (#551). missing lists provider
// models declared served:true whose id is absent from live — confirmed
// drift. undeclared lists live ids the registry does not mark served:true
// for provider over the cli transport (served:false, the model/transport key
// unexpressed, or the id has no registry entry at all) — the inverse
// direction. Both are sorted for a deterministic report.
func servedCLIModelDiff(provider string, live []string) (missing, undeclared []string) {
	liveSet := make(map[string]bool, len(live))
	for _, id := range live {
		liveSet[id] = true
	}
	servedSet := make(map[string]bool)
	for _, m := range models.All() {
		if m.Provider != provider {
			continue
		}
		served, known := m.ServedByTransport(models.TransportCLI)
		if !known || !served {
			continue
		}
		servedSet[m.ID] = true
		if !liveSet[m.ID] {
			missing = append(missing, m.ID)
		}
	}
	for _, id := range live {
		if !servedSet[id] {
			undeclared = append(undeclared, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(undeclared)
	return missing, undeclared
}

// grokDefaultModelRe extracts the id from grok's "Default model: <id>" line.
var grokDefaultModelRe = regexp.MustCompile(`(?m)^Default model:\s*(\S+)\s*$`)

// grokCatalogBulletRe matches one catalog entry line under "Available
// models:" — a leading `*` (the CLI's own default marker) or `-` bullet, the
// model id, and an optional trailing "(default)" annotation.
var grokCatalogBulletRe = regexp.MustCompile(`^[*-]\s+(\S+?)(?:\s+\(default\))?$`)

// parseGrokCatalog parses `grok models` output into the live model catalog
// plus the CLI's own reported default (#551). Observed shape (grok CLI
// 1.0.4, captured 2026-08-15 — testdata/grok-catalog/):
//
//	You are logged in with grok.com.
//
//	Default model: grok-4.6
//
//	Available models:
//	  * grok-4.6 (default)
//	  - grok-4.5
//
// The auth-status preamble line is deliberately NOT matched on: the catalog
// listing is free even when unauthenticated (confirmed live and recorded in
// testdata/grok-catalog/README.md), so parsing never branches on its
// wording — it scans past whatever that line says for "Available models:".
//
// ok=false when the "Available models:" section cannot be found, or is found
// with no parseable entries under it — a CLI output shape change the doctor
// must degrade on (CatalogWarning), not silently misread as an empty catalog
// (which would falsely report every served model as missing).
func parseGrokCatalog(output string) (ids []string, defaultID string, ok bool) {
	if m := grokDefaultModelRe.FindStringSubmatch(output); m != nil {
		defaultID = m[1]
	}
	inList := false
	found := false
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !inList {
			if strings.EqualFold(trimmed, "Available models:") {
				inList = true
				found = true
			}
			continue
		}
		m := grokCatalogBulletRe.FindStringSubmatch(trimmed)
		if m == nil {
			break // catalog list ended
		}
		id := m[1]
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, defaultID, found && len(ids) > 0
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
