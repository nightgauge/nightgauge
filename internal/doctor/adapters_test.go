package doctor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// fakeProbe builds an adapterProbe whose side effects are driven by in-memory
// maps so adapter health can be tested without real CLIs or filesystem.
type fakeProbe struct {
	paths           map[string]string // binary -> resolved path ("" / absent => not found)
	versions        map[string]string // path -> `--version` combined output
	verErrs         map[string]error  // path -> error from the version spawn
	env             map[string]string
	files           map[string][]byte // absolute path -> file content
	codex           string
	serverErr       error // kindHTTP transport error (non-nil = unreachable)
	noServer        bool  // when true, the probe reports unreachable
	catalog         []string
	catalogParseErr bool
	machineModels   map[string]string // canonical adapter -> machine-tier model
	probedURLs      []string
	catalogOutputs  map[string]string // #551: path -> catalog-command combined output
	catalogErrs     map[string]error  // #551: path -> error from the catalog-command spawn
}

func (f fakeProbe) toProbe() adapterProbe {
	return adapterProbe{
		lookPath: func(bin string) (string, error) {
			if p, ok := f.paths[bin]; ok && p != "" {
				return p, nil
			}
			return "", errors.New("not found")
		},
		runVersion: func(path string) (string, error) {
			return f.versions[path], f.verErrs[path]
		},
		runCatalog: func(path string, args []string) (string, error) {
			return f.catalogOutputs[path], f.catalogErrs[path]
		},
		readFile: func(path string) ([]byte, error) {
			if b, ok := f.files[path]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		getenv: func(k string) string { return f.env[k] },
		httpProbe: func(baseURL string) localServerProbeResult {
			if f.noServer || f.serverErr != nil {
				return localServerProbeResult{}
			}
			return localServerProbeResult{
				reachable: true,
				ids:       f.catalog,
				parseErr:  f.catalogParseErr,
			}
		},
		machineModel: func(adapter string) string {
			if f.machineModels == nil {
				return ""
			}
			return f.machineModels[adapter]
		},
		codexHome: f.codex,
	}
}

func TestCheckAdapter_CodexInstalledHealthy(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	content := "[some.user.table]\nfoo = 1\n\n" + codexManagedMcpBegin + "\n[mcp_servers.fs]\n# <<< END NIGHTGAUGE MANAGED MCP <<<\n"

	fp := fakeProbe{
		paths:    map[string]string{"codex": "/usr/local/bin/codex"},
		versions: map[string]string{"/usr/local/bin/codex": "codex 0.112.0\n"},
		files:    map[string][]byte{configPath: []byte(content)},
		codex:    codexHome,
	}

	h := checkAdapter("codex", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected codex OK, got remediation=%q", h.Remediation)
	}
	if h.Kind != "cli" {
		t.Errorf("expected kind cli, got %q", h.Kind)
	}
	if h.Version != "0.112.0" {
		t.Errorf("expected version 0.112.0, got %q", h.Version)
	}
	if !h.VersionOK {
		t.Errorf("expected VersionOK true (0.112.0 >= 0.111.0)")
	}
	if h.Mcp == nil || !h.Mcp.ConfigPresent || !h.Mcp.ManagedBlock {
		t.Errorf("expected codex MCP managed block present, got %+v", h.Mcp)
	}
}

func TestCheckAdapter_CodexBelowMinVersion(t *testing.T) {
	fp := fakeProbe{
		paths:    map[string]string{"codex": "/bin/codex"},
		versions: map[string]string{"/bin/codex": "codex 0.110.0\n"},
		codex:    t.TempDir(),
	}
	h := checkAdapter("codex", fp.toProbe())
	if h.OK {
		t.Fatal("expected codex !OK when below min version")
	}
	if h.VersionOK {
		t.Error("expected VersionOK=false for 0.110.0 < 0.111.0")
	}
	if h.Remediation == "" {
		t.Error("expected a remediation hint for stale version")
	}
}

func TestCheckAdapter_CodexNotInstalled(t *testing.T) {
	fp := fakeProbe{codex: t.TempDir()}
	h := checkAdapter("codex", fp.toProbe())
	if h.OK || h.Installed {
		t.Fatal("expected codex not installed/!OK when binary missing")
	}
	if h.Remediation == "" {
		t.Error("expected remediation when binary missing")
	}
	// MCP is still probed even when the binary is absent (config may pre-exist).
	if h.Mcp == nil {
		t.Error("expected MCP health to be populated for codex")
	}
	if h.Mcp.ConfigPresent {
		t.Error("expected ConfigPresent=false for empty codex home")
	}
}

func TestCheckAdapter_ClaudeAliasAndNoVersionFloor(t *testing.T) {
	fp := fakeProbe{
		paths:    map[string]string{"claude": "/opt/claude"},
		versions: map[string]string{"/opt/claude": "claude 2.1.38 (Claude Code)\n"},
	}
	// "claude" is an alias for "claude-headless".
	h := checkAdapter("claude", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected claude OK, got %q", h.Remediation)
	}
	if h.Binary != "claude" {
		t.Errorf("expected binary claude, got %q", h.Binary)
	}
	if h.Version != "2.1.38" {
		t.Errorf("expected version 2.1.38, got %q", h.Version)
	}
	if h.MinVersion != "" {
		t.Errorf("expected no min version floor for claude, got %q", h.MinVersion)
	}
	if h.Mcp != nil {
		t.Error("expected no MCP section for claude")
	}
}

func TestCheckAdapter_SdkApiKey(t *testing.T) {
	withKey := fakeProbe{env: map[string]string{"GEMINI_API_KEY": "x"}}
	h := checkAdapter("gemini-sdk", withKey.toProbe())
	if !h.OK || !h.Installed {
		t.Fatalf("expected gemini-sdk OK when GEMINI_API_KEY set, got %+v", h)
	}
	if h.Kind != "sdk" {
		t.Errorf("expected kind sdk, got %q", h.Kind)
	}

	noKey := fakeProbe{env: map[string]string{}}
	h2 := checkAdapter("gemini-sdk", noKey.toProbe())
	if h2.OK {
		t.Error("expected gemini-sdk !OK when no API key set")
	}
	if h2.Remediation == "" {
		t.Error("expected remediation listing the API key env vars")
	}
}

func TestCheckAdapter_HttpLocalModelEnv(t *testing.T) {
	// ollama/lm-studio run THROUGH the claude CLI bridge, so readiness requires
	// the model env, the claude binary on PATH, a reachable server (#57), and
	// the resolved model present in the /models catalog (#520).
	ready := fakeProbe{
		env:     map[string]string{"NIGHTGAUGE_OLLAMA_MODEL": "llama3.2"},
		paths:   map[string]string{"claude": "/opt/claude"},
		catalog: []string{"llama3.2"},
	}
	h := checkAdapter("ollama", ready.toProbe())
	if !h.OK || h.Kind != "http" {
		t.Fatalf("expected ollama OK/http when model env set + claude bridge present + server up + model in catalog, got %+v", h)
	}
	if !h.ServerReachable || h.ServerURL != "http://localhost:11434/v1" {
		t.Errorf("expected reachable default server URL, got %+v", h)
	}
	if h.Model != "llama3.2" || h.ModelOK == nil || !*h.ModelOK {
		t.Errorf("expected model llama3.2 present in catalog, got %+v", h)
	}

	unset := fakeProbe{env: map[string]string{}, paths: map[string]string{"claude": "/opt/claude"}}
	h2 := checkAdapter("lm-studio", unset.toProbe())
	if h2.OK {
		t.Error("expected lm-studio !OK when model env unset")
	}
	if h2.ModelOK == nil || *h2.ModelOK {
		t.Errorf("expected model_ok=false when unconfigured, got %+v", h2)
	}
	if !strings.Contains(h2.Remediation, "no default") ||
		!strings.Contains(h2.Remediation, "NIGHTGAUGE_LM_STUDIO_MODEL") ||
		!strings.Contains(h2.Remediation, "lm_studio.model") {
		t.Errorf("expected unconfigured remediation to name env, config key, and no-default, got %q", h2.Remediation)
	}
}

// TestCheckAdapter_HttpMissingBridge guards the #4031-review finding: an HTTP
// adapter with its model env set but the claude CLI bridge missing must NOT be
// reported ready (it would fail at spawn time).
func TestCheckAdapter_HttpMissingBridge(t *testing.T) {
	fp := fakeProbe{env: map[string]string{"NIGHTGAUGE_OLLAMA_MODEL": "llama3.2"}} // no claude in paths
	h := checkAdapter("ollama", fp.toProbe())
	if h.OK || h.Installed {
		t.Fatalf("expected ollama !OK when the claude bridge binary is missing, got %+v", h)
	}
	if !strings.Contains(h.Remediation, "claude") {
		t.Errorf("expected remediation to mention the claude bridge, got %q", h.Remediation)
	}
}

// TestCheckCodexMcp_PresentNoBlock covers the most common real Codex state:
// config.toml exists but the nightgauge managed MCP block has not been
// provisioned. Also exercises line-anchoring (embedded substring must NOT match)
// and CRLF handling.
func TestCheckCodexMcp_PresentNoBlock(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	// A user marker-looking string embedded mid-line must not be treated as the block.
	content := "model = \"gpt-5.5\"\nnote = \"see # >>> BEGIN NIGHTGAUGE MANAGED MCP >>> inline\"\r\n"
	fp := fakeProbe{
		paths: map[string]string{"codex": "/bin/codex"},
		files: map[string][]byte{configPath: []byte(content)},
		codex: codexHome,
	}
	h := checkAdapter("codex", fp.toProbe())
	if h.Mcp == nil || !h.Mcp.ConfigPresent {
		t.Fatalf("expected codex config present, got %+v", h.Mcp)
	}
	if h.Mcp.ManagedBlock {
		t.Error("expected ManagedBlock=false when the marker only appears mid-line (anchoring)")
	}

	// A CRLF-terminated marker on its own line (with leading whitespace) SHOULD match,
	// mirroring the SDK's `^[ \t]*<marker>` semantics.
	content2 := "[other]\r\n  " + codexManagedMcpBegin + "\r\n"
	fp2 := fakeProbe{
		paths: map[string]string{"codex": "/bin/codex"},
		files: map[string][]byte{configPath: []byte(content2)},
		codex: codexHome,
	}
	h2 := checkAdapter("codex", fp2.toProbe())
	if h2.Mcp == nil || !h2.Mcp.ManagedBlock {
		t.Errorf("expected ManagedBlock=true for an indented CRLF marker line, got %+v", h2.Mcp)
	}
}

// TestCheckAdapter_GeminiAndCopilot exercises the remaining CLI adapters
// end-to-end so their spec floor/binary are tied to observed behavior.
func TestCheckAdapter_GeminiAndCopilot(t *testing.T) {
	// gemini below its 0.29.0 floor → not OK.
	geminiOld := fakeProbe{
		paths:    map[string]string{"gemini": "/bin/gemini"},
		versions: map[string]string{"/bin/gemini": "gemini 0.28.9"},
	}
	g := checkAdapter("gemini", geminiOld.toProbe())
	if g.OK || g.VersionOK {
		t.Errorf("expected gemini !OK below 0.29.0 floor, got %+v", g)
	}
	if !strings.Contains(g.Remediation, "0.29.0") {
		t.Errorf("expected gemini remediation to mention 0.29.0, got %q", g.Remediation)
	}
	// gemini at/above floor → OK.
	geminiOK := fakeProbe{
		paths:    map[string]string{"gemini": "/bin/gemini"},
		versions: map[string]string{"/bin/gemini": "gemini 0.29.0"},
	}
	if g2 := checkAdapter("gemini", geminiOK.toProbe()); !g2.OK {
		t.Errorf("expected gemini OK at floor, got %+v", g2)
	}

	// copilot has no floor → any version is OK; binary is "copilot"; no MCP.
	copilot := fakeProbe{
		paths:    map[string]string{"copilot": "/bin/copilot"},
		versions: map[string]string{"/bin/copilot": "copilot 0.1.0"},
	}
	c := checkAdapter("copilot", copilot.toProbe())
	if !c.OK || c.Binary != "copilot" || c.Mcp != nil {
		t.Errorf("expected copilot OK, binary=copilot, no MCP, got %+v", c)
	}
}

// TestCheckAdapter_VersionSpawnError: binary present but `--version` errors →
// the adapter is reported not ready against a floor with an "unknown" hint;
// a floor-less adapter stays OK.
func TestCheckAdapter_VersionSpawnError(t *testing.T) {
	codexErr := fakeProbe{
		paths:   map[string]string{"codex": "/bin/codex"},
		verErrs: map[string]error{"/bin/codex": errors.New("exec: hung")},
		codex:   t.TempDir(),
	}
	h := checkAdapter("codex", codexErr.toProbe())
	if !h.Installed {
		t.Error("expected Installed=true when the binary is on PATH")
	}
	if h.Version != "" || h.VersionOK || h.OK {
		t.Errorf("expected unknown version → VersionOK/OK false, got %+v", h)
	}
	if !strings.Contains(h.Remediation, "unknown") {
		t.Errorf("expected 'unknown' in remediation, got %q", h.Remediation)
	}

	// claude has no floor → a version-spawn error still leaves it OK.
	claudeErr := fakeProbe{
		paths:   map[string]string{"claude": "/bin/claude"},
		verErrs: map[string]error{"/bin/claude": errors.New("boom")},
	}
	if h2 := checkAdapter("claude", claudeErr.toProbe()); !h2.OK {
		t.Errorf("expected floor-less claude OK despite version error, got %+v", h2)
	}
}

func TestCheckAdapter_UnknownAdapter(t *testing.T) {
	h := checkAdapter("not-a-real-adapter", fakeProbe{}.toProbe())
	if h.OK {
		t.Fatal("expected unknown adapter to be !OK")
	}
	if h.Remediation == "" {
		t.Error("expected remediation naming the valid adapter set")
	}
}

func TestCheckAdapters_OrderAndCount(t *testing.T) {
	fp := fakeProbe{
		paths:    map[string]string{"codex": "/b/codex", "claude": "/b/claude"},
		versions: map[string]string{"/b/codex": "codex 0.112.0", "/b/claude": "claude 2.1.0"},
		codex:    t.TempDir(),
	}
	got := checkAdaptersWithProbe([]string{"codex", "claude"}, fp.toProbe())
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].Adapter != "codex" || got[1].Adapter != "claude" {
		t.Errorf("expected input order preserved, got %q,%q", got[0].Adapter, got[1].Adapter)
	}
}

func TestVersionParsingAndFloor(t *testing.T) {
	if v := parseAdapterVersion("codex 0.112.0\n", nil); v != "0.112.0" {
		t.Errorf("parse: expected 0.112.0, got %q", v)
	}
	if v := parseAdapterVersion("garbage", nil); v != "" {
		t.Errorf("parse: expected empty for no semver, got %q", v)
	}
	if v := parseAdapterVersion("anything", errors.New("boom")); v != "" {
		t.Errorf("parse: expected empty on spawn error, got %q", v)
	}
	cases := []struct {
		version, min string
		want         bool
	}{
		{"0.112.0", "0.111.0", true},
		{"0.111.0", "0.111.0", true},
		{"0.110.9", "0.111.0", false},
		{"1.0.0", "", true},    // no floor => always ok
		{"", "0.111.0", false}, // unknown version against a floor => fail
		{"0.29.5", "0.29.0", true},
	}
	for _, c := range cases {
		if got := versionMeetsFloor(c.version, c.min); got != c.want {
			t.Errorf("versionMeetsFloor(%q,%q)=%v want %v", c.version, c.min, got, c.want)
		}
	}
}

// TestAdapterSpecConstants guards the min-version constants that MIRROR the SDK
// MIN_KNOWN_VERSION values. A drift here must be a deliberate edit kept in sync
// with packages/nightgauge-sdk/src/cli/adapters/*Adapter.ts.
func TestAdapterSpecConstants(t *testing.T) {
	if adapterSpecs["codex"].minVersion != "0.111.0" {
		t.Errorf("codex minVersion drifted from SDK MIN_KNOWN_VERSION 0.111.0: %q", adapterSpecs["codex"].minVersion)
	}
	if adapterSpecs["gemini"].minVersion != "0.29.0" {
		t.Errorf("gemini minVersion drifted from SDK MIN_KNOWN_VERSION 0.29.0: %q", adapterSpecs["gemini"].minVersion)
	}
	if adapterSpecs["grok"].minVersion != "1.0.0" {
		t.Errorf("grok minVersion drifted from SDK GROK_MIN_KNOWN_VERSION 1.0.0: %q", adapterSpecs["grok"].minVersion)
	}
	if !adapterSpecs["codex"].mcp {
		t.Error("codex must be flagged as MCP-provisioning")
	}
	if len(AllAdapterNames()) != 9 {
		t.Errorf("expected 9 adapters in AllAdapterNames, got %d", len(AllAdapterNames()))
	}
}

func TestResolveCodexHome_EnvOverride(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex/home")
	if got := resolveCodexHome(); got != "/custom/codex/home" {
		t.Errorf("expected CODEX_HOME override, got %q", got)
	}
}

// TestCheckAdapter_HttpServerUnreachable guards the #57 finding: before the
// reachability probe, ollama could report healthy with no server running.
func TestCheckAdapter_HttpServerUnreachable(t *testing.T) {
	fp := fakeProbe{
		env:      map[string]string{"NIGHTGAUGE_OLLAMA_MODEL": "llama3.2"},
		paths:    map[string]string{"claude": "/opt/claude"},
		noServer: true,
	}
	h := checkAdapter("ollama", fp.toProbe())
	if h.OK {
		t.Fatalf("expected ollama !OK when the local server is unreachable, got %+v", h)
	}
	if h.ServerReachable {
		t.Error("expected ServerReachable=false")
	}
	if h.ModelOK != nil {
		t.Errorf("expected model_ok omitted when catalog cannot be evaluated, got %+v", h.ModelOK)
	}
	if !strings.Contains(h.Remediation, "http://localhost:11434/v1") ||
		!strings.Contains(h.Remediation, "NIGHTGAUGE_OLLAMA_BASE_URL") {
		t.Errorf("expected remediation naming the URL and override env, got %q", h.Remediation)
	}
}

// TestCheckAdapter_HttpBaseURLOverride: the env override wins over the default.
func TestCheckAdapter_HttpBaseURLOverride(t *testing.T) {
	fp := fakeProbe{
		env: map[string]string{
			"NIGHTGAUGE_LM_STUDIO_MODEL":    "qwen3-coder",
			"NIGHTGAUGE_LM_STUDIO_BASE_URL": "http://10.0.0.5:9999/v1",
		},
		paths:   map[string]string{"claude": "/opt/claude"},
		catalog: []string{"qwen3-coder"},
	}
	h := checkAdapter("lm-studio", fp.toProbe())
	if h.ServerURL != "http://10.0.0.5:9999/v1" {
		t.Errorf("expected overridden server URL, got %q", h.ServerURL)
	}
	if !h.OK {
		t.Errorf("expected OK with override reachable, got %+v", h)
	}
}

// TestCheckAdapter_HttpModelCatalogPresentAbsentUnconfigured is the #520
// contract: stubbed /models catalog — present stays OK, absent degrades with
// a named remediation, unconfigured reports the missing model explicitly.
func TestCheckAdapter_HttpModelCatalogPresentAbsentUnconfigured(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		fp := fakeProbe{
			env:     map[string]string{"NIGHTGAUGE_LM_STUDIO_MODEL": "qwen/qwen3-coder-30b"},
			paths:   map[string]string{"claude": "/opt/claude"},
			catalog: []string{"qwen/qwen3-coder-30b", "text-embedding-nomic-embed-text-v1.5"},
		}
		h := checkAdapter("lm-studio", fp.toProbe())
		if !h.OK || h.ModelOK == nil || !*h.ModelOK {
			t.Fatalf("expected OK + model_ok when catalog contains the model, got %+v", h)
		}
		if h.Remediation != "" {
			t.Errorf("expected no remediation on the healthy path, got %q", h.Remediation)
		}
	})

	t.Run("absent", func(t *testing.T) {
		fp := fakeProbe{
			env:     map[string]string{"NIGHTGAUGE_LM_STUDIO_MODEL": "google/gemma-4-26b-a4b"},
			paths:   map[string]string{"claude": "/opt/claude"},
			catalog: []string{"qwen/qwen3-coder-30b"},
		}
		h := checkAdapter("lm-studio", fp.toProbe())
		if h.OK {
			t.Fatal("expected !OK when configured model is absent from the catalog")
		}
		if !h.Installed || !h.ServerReachable {
			t.Errorf("expected installed+reachable to stay true, got %+v", h)
		}
		if h.Model != "google/gemma-4-26b-a4b" || h.ModelOK == nil || *h.ModelOK {
			t.Errorf("expected model_ok=false naming the configured model, got %+v", h)
		}
		if !strings.Contains(h.Remediation, "google/gemma-4-26b-a4b") ||
			!strings.Contains(h.Remediation, "lms get google/gemma-4-26b-a4b") {
			t.Errorf("expected remediation naming the model and lms get hint, got %q", h.Remediation)
		}
	})

	t.Run("unconfigured", func(t *testing.T) {
		fp := fakeProbe{
			env:     map[string]string{},
			paths:   map[string]string{"claude": "/opt/claude"},
			catalog: []string{"qwen/qwen3-coder-30b"},
		}
		h := checkAdapter("lm-studio", fp.toProbe())
		if h.OK || h.Installed {
			t.Fatalf("expected !OK/!installed when no model is configured, got %+v", h)
		}
		if h.Model != "" || h.ModelOK == nil || *h.ModelOK {
			t.Errorf("expected empty model and model_ok=false, got %+v", h)
		}
		if !strings.Contains(h.Remediation, "no default") {
			t.Errorf("expected explicit no-default wording, got %q", h.Remediation)
		}
	})

	t.Run("ollama absent uses ollama pull", func(t *testing.T) {
		fp := fakeProbe{
			env:     map[string]string{"NIGHTGAUGE_OLLAMA_MODEL": "llama3.2"},
			paths:   map[string]string{"claude": "/opt/claude"},
			catalog: []string{"mistral"},
		}
		h := checkAdapter("ollama", fp.toProbe())
		if h.OK || h.ModelOK == nil || *h.ModelOK {
			t.Fatalf("expected ollama !OK when model missing from catalog, got %+v", h)
		}
		if !strings.Contains(h.Remediation, "ollama pull llama3.2") {
			t.Errorf("expected ollama pull hint, got %q", h.Remediation)
		}
	})
}

func TestCheckAdapter_HttpMachineTierModelFallback(t *testing.T) {
	fp := fakeProbe{
		env:           map[string]string{},
		paths:         map[string]string{"claude": "/opt/claude"},
		catalog:       []string{"qwen/qwen3-coder-30b"},
		machineModels: map[string]string{"lm-studio": "qwen/qwen3-coder-30b"},
	}
	h := checkAdapter("lm-studio", fp.toProbe())
	if !h.OK || h.Model != "qwen/qwen3-coder-30b" {
		t.Fatalf("expected machine-tier model to satisfy readiness, got %+v", h)
	}

	envWins := fakeProbe{
		env:           map[string]string{"NIGHTGAUGE_LM_STUDIO_MODEL": "env-model"},
		paths:         map[string]string{"claude": "/opt/claude"},
		catalog:       []string{"env-model"},
		machineModels: map[string]string{"lm-studio": "machine-model"},
	}
	h2 := checkAdapter("lm-studio", envWins.toProbe())
	if h2.Model != "env-model" || !h2.OK {
		t.Fatalf("expected env to win over machine-tier, got %+v", h2)
	}
}

func TestCheckAdapter_HttpCatalogParseError(t *testing.T) {
	fp := fakeProbe{
		env:             map[string]string{"NIGHTGAUGE_LM_STUDIO_MODEL": "qwen/qwen3-coder-30b"},
		paths:           map[string]string{"claude": "/opt/claude"},
		catalogParseErr: true,
	}
	h := checkAdapter("lm-studio", fp.toProbe())
	if h.OK || h.ModelOK == nil || *h.ModelOK {
		t.Fatalf("expected !OK/model_ok=false when catalog parse fails, got %+v", h)
	}
	if !h.ServerReachable {
		t.Error("expected reachable even when the catalog cannot be parsed")
	}
	if !strings.Contains(h.Remediation, "could not be parsed") {
		t.Errorf("expected parse-failure remediation, got %q", h.Remediation)
	}
}

func TestParseOpenAIModelIDs_ObservedLMStudioShape(t *testing.T) {
	// Exact envelope observed 2026-08-14 from GET http://127.0.0.1:1234/v1/models.
	body := []byte(`{
  "data": [
    {
      "id": "qwen/qwen3-coder-30b",
      "object": "model",
      "owned_by": "organization_owner"
    },
    {
      "id": "text-embedding-nomic-embed-text-v1.5",
      "object": "model",
      "owned_by": "organization_owner"
    }
  ],
  "object": "list"
}`)
	ids, ok := parseOpenAIModelIDs(body)
	if !ok {
		t.Fatal("expected observed LM Studio /models body to parse")
	}
	if len(ids) != 2 || ids[0] != "qwen/qwen3-coder-30b" || ids[1] != "text-embedding-nomic-embed-text-v1.5" {
		t.Fatalf("unexpected ids: %v", ids)
	}

	if _, ok := parseOpenAIModelIDs([]byte(`not-json`)); ok {
		t.Error("expected unparseable body to fail")
	}
	ids, ok = parseOpenAIModelIDs([]byte(`{"data":[]}`))
	if !ok || len(ids) != 0 {
		t.Errorf("empty catalog must parse as success with no ids, got ok=%v ids=%v", ok, ids)
	}
	ids, ok = parseOpenAIModelIDs([]byte(`{"models":[{"id":"alt"}]}`))
	if !ok || len(ids) != 1 || ids[0] != "alt" {
		t.Errorf("models[] fallback: ok=%v ids=%v", ok, ids)
	}
}

func TestProbeLocalServer_ParsesCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen/qwen3-coder-30b","object":"model"}]}`))
	}))
	defer srv.Close()

	got := probeLocalServer(srv.URL + "/v1")
	if !got.reachable || got.parseErr {
		t.Fatalf("expected reachable parsed catalog, got %+v", got)
	}
	if !catalogContains(got.ids, "qwen/qwen3-coder-30b") {
		t.Errorf("expected catalog to contain qwen/qwen3-coder-30b, got %v", got.ids)
	}
}

func TestReadMachineHTTPModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("lm_studio:\n  model: qwen/qwen3-coder-30b\nollama:\n  model: llama3.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := config.SwapMachineConfigPathForTest(func() (string, error) { return path, nil })
	defer restore()

	if got := readMachineHTTPModel("lm-studio"); got != "qwen/qwen3-coder-30b" {
		t.Errorf("lm-studio machine model = %q", got)
	}
	if got := readMachineHTTPModel("ollama"); got != "llama3.2" {
		t.Errorf("ollama machine model = %q", got)
	}
	if got := readMachineHTTPModel("codex"); got != "" {
		t.Errorf("non-http adapter must not resolve a machine model, got %q", got)
	}
}

// --- CLI catalog drift probe (#551) ---
//
// The fixtures below all derive from the real captured
// testdata/grok-catalog/grok-models.txt (see its README) by substituting a
// bullet line's model id, per that README's rule against inventing a new
// catalog shape from scratch. The registry facts asserted against
// (grok-4.6/grok-4.5 served:true, grok-build-0.1 served:false) are the real
// embedded xai entries — TestServedCLIModelDiff_RealRegistry pins that
// coupling explicitly so a registry edit that removes it is a loud test
// failure, not a silent gap.

func readGrokCatalogFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/grok-catalog/grok-models.txt")
	if err != nil {
		t.Fatalf("could not read grok catalog fixture: %v", err)
	}
	return string(b)
}

func TestParseGrokCatalog_CapturedFixture(t *testing.T) {
	ids, defaultID, ok := parseGrokCatalog(readGrokCatalogFixture(t))
	if !ok {
		t.Fatal("expected the captured fixture to parse")
	}
	if defaultID != "grok-4.6" {
		t.Errorf("expected default model grok-4.6, got %q", defaultID)
	}
	if len(ids) != 2 || ids[0] != "grok-4.6" || ids[1] != "grok-4.5" {
		t.Fatalf("expected [grok-4.6 grok-4.5], got %v", ids)
	}
}

func TestParseGrokCatalog_MalformedOutput(t *testing.T) {
	// Truncated before "Available models:" — a shape change the parser must
	// not silently misread as an empty catalog.
	truncated := strings.Split(readGrokCatalogFixture(t), "Available models:")[0]
	if _, _, ok := parseGrokCatalog(truncated); ok {
		t.Error("expected ok=false when the Available models: section is missing")
	}

	if _, _, ok := parseGrokCatalog("garbage, not grok output at all"); ok {
		t.Error("expected ok=false for unrecognized output")
	}

	if _, _, ok := parseGrokCatalog(""); ok {
		t.Error("expected ok=false for empty output")
	}
}

// TestCheckAdapter_GrokCatalogHealthy pins the no-drift path against the
// REAL registry: grok-4.6 and grok-4.5 are both transports.cli.served=true
// for xai, and the captured fixture lists exactly those two, so nothing is
// missing or undeclared.
func TestCheckAdapter_GrokCatalogHealthy(t *testing.T) {
	fp := fakeProbe{
		paths:          map[string]string{"grok": "/bin/grok"},
		versions:       map[string]string{"/bin/grok": "grok 1.0.4 (d846eb93d94d) [stable]"},
		catalogOutputs: map[string]string{"/bin/grok": readGrokCatalogFixture(t)},
	}
	h := checkAdapter("grok", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected grok OK with a matching catalog, got remediation=%q catalog_warning=%q", h.Remediation, h.CatalogWarning)
	}
	if h.CatalogWarning != "" {
		t.Errorf("expected no catalog warning on the healthy path, got %q", h.CatalogWarning)
	}
	if h.Catalog == nil {
		t.Fatal("expected Catalog to be populated")
	}
	if h.Catalog.Provider != "xai" || h.Catalog.Transport != "cli" {
		t.Errorf("expected provider=xai transport=cli, got %+v", h.Catalog)
	}
	if h.Catalog.Default != "grok-4.6" {
		t.Errorf("expected default grok-4.6, got %q", h.Catalog.Default)
	}
	if len(h.Catalog.Missing) != 0 || len(h.Catalog.Undeclared) != 0 {
		t.Errorf("expected no drift, got missing=%v undeclared=%v", h.Catalog.Missing, h.Catalog.Undeclared)
	}
}

// TestCheckAdapter_GrokCatalogMissingServedModel is the #532 class itself:
// the registry declares grok-4.5 served over cli, but the live catalog
// (derived from the real fixture with that bullet dropped) does not offer
// it. The failure must name the provider, the concrete model id, and the
// transport, and it must fail the adapter (#551 AC).
func TestCheckAdapter_GrokCatalogMissingServedModel(t *testing.T) {
	dropped := strings.Replace(readGrokCatalogFixture(t), "  - grok-4.5\n", "", 1)
	fp := fakeProbe{
		paths:          map[string]string{"grok": "/bin/grok"},
		versions:       map[string]string{"/bin/grok": "grok 1.0.4"},
		catalogOutputs: map[string]string{"/bin/grok": dropped},
	}
	h := checkAdapter("grok", fp.toProbe())
	if h.OK {
		t.Fatal("expected grok !OK when a served model is absent from the live catalog")
	}
	if h.Catalog == nil || len(h.Catalog.Missing) != 1 || h.Catalog.Missing[0] != "grok-4.5" {
		t.Fatalf("expected Missing=[grok-4.5], got %+v", h.Catalog)
	}
	for _, want := range []string{"xai", "grok-4.5", "cli"} {
		if !strings.Contains(h.Remediation, want) {
			t.Errorf("expected remediation to name %q, got %q", want, h.Remediation)
		}
	}
}

// TestCheckAdapter_GrokCatalogUndeclaredWarning is the inverse drift
// direction: the live catalog (derived from the real fixture with a
// grok-build-0.1 bullet appended) offers a model the registry marks
// served:false for cli. Warning-only — the adapter must stay OK.
func TestCheckAdapter_GrokCatalogUndeclaredWarning(t *testing.T) {
	extra := strings.Replace(readGrokCatalogFixture(t), "  - grok-4.5\n", "  - grok-4.5\n  - grok-build-0.1\n", 1)
	fp := fakeProbe{
		paths:          map[string]string{"grok": "/bin/grok"},
		versions:       map[string]string{"/bin/grok": "grok 1.0.4"},
		catalogOutputs: map[string]string{"/bin/grok": extra},
	}
	h := checkAdapter("grok", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected grok to stay OK on an undeclared-only drift, got remediation=%q", h.Remediation)
	}
	if h.Catalog == nil || len(h.Catalog.Undeclared) != 1 || h.Catalog.Undeclared[0] != "grok-build-0.1" {
		t.Fatalf("expected Undeclared=[grok-build-0.1], got %+v", h.Catalog)
	}
	for _, want := range []string{"grok-build-0.1", "xai", "#532"} {
		if !strings.Contains(h.CatalogWarning, want) {
			t.Errorf("expected catalog warning to mention %q, got %q", want, h.CatalogWarning)
		}
	}
}

// TestCheckAdapter_GrokCatalogProbeErrorDegradesToWarning covers "CLI not
// authenticated" and any other catalog-spawn failure (#551 AC): the probe
// must degrade to a warning naming why, never a hard doctor failure, when
// the adapter's baseline health (installed, version floor met) already
// passed.
func TestCheckAdapter_GrokCatalogProbeErrorDegradesToWarning(t *testing.T) {
	fp := fakeProbe{
		paths:       map[string]string{"grok": "/bin/grok"},
		versions:    map[string]string{"/bin/grok": "grok 1.0.4"},
		catalogErrs: map[string]error{"/bin/grok": errors.New("exit status 1: not authenticated")},
	}
	h := checkAdapter("grok", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected grok to stay OK when only the catalog probe fails, got remediation=%q", h.Remediation)
	}
	if h.Catalog != nil {
		t.Errorf("expected no Catalog when the probe could not run, got %+v", h.Catalog)
	}
	if h.CatalogWarning == "" || !strings.Contains(h.CatalogWarning, "not authenticated") {
		t.Errorf("expected catalog warning naming the spawn error, got %q", h.CatalogWarning)
	}
}

// TestCheckAdapter_GrokCatalogParseFailureDegradesToWarning: the CLI runs
// and exits cleanly but the output does not match the known shape (a future
// CLI update). Degrades to warning, never a hard failure.
func TestCheckAdapter_GrokCatalogParseFailureDegradesToWarning(t *testing.T) {
	fp := fakeProbe{
		paths:          map[string]string{"grok": "/bin/grok"},
		versions:       map[string]string{"/bin/grok": "grok 1.0.4"},
		catalogOutputs: map[string]string{"/bin/grok": "grok has changed its models command output entirely"},
	}
	h := checkAdapter("grok", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected grok to stay OK when the catalog cannot be parsed, got remediation=%q", h.Remediation)
	}
	if h.Catalog != nil {
		t.Errorf("expected no Catalog when parsing failed, got %+v", h.Catalog)
	}
	if h.CatalogWarning == "" || !strings.Contains(h.CatalogWarning, "could not parse") {
		t.Errorf("expected a parse-failure catalog warning, got %q", h.CatalogWarning)
	}
}

// TestCheckAdapter_GrokCatalogSkippedWhenNotInstalled: the catalog probe
// must never run (and never fabricate a Catalog/CatalogWarning) when the
// binary itself is missing — that failure is already reported via the
// existing Installed/Remediation path.
func TestCheckAdapter_GrokCatalogSkippedWhenNotInstalled(t *testing.T) {
	h := checkAdapter("grok", fakeProbe{}.toProbe())
	if h.OK || h.Installed {
		t.Fatal("expected grok !OK/!installed when the binary is missing")
	}
	if h.Catalog != nil || h.CatalogWarning != "" {
		t.Errorf("expected no catalog probe attempt when not installed, got catalog=%+v warning=%q", h.Catalog, h.CatalogWarning)
	}
}

// TestCheckAdapter_GrokCatalogSkippedBelowVersionFloor: same skip rule when
// the binary is present but below the version floor — no redundant second
// complaint layered on an adapter that already fails for another reason.
func TestCheckAdapter_GrokCatalogSkippedBelowVersionFloor(t *testing.T) {
	fp := fakeProbe{
		paths:    map[string]string{"grok": "/bin/grok"},
		versions: map[string]string{"/bin/grok": "grok 0.9.0"},
		// Even a matching catalog must not be fetched/compared here.
		catalogOutputs: map[string]string{"/bin/grok": readGrokCatalogFixture(t)},
	}
	h := checkAdapter("grok", fp.toProbe())
	if h.OK || h.VersionOK {
		t.Fatal("expected grok !OK below its version floor")
	}
	if h.Catalog != nil {
		t.Errorf("expected no catalog probe below the version floor, got %+v", h.Catalog)
	}
}

// TestCheckAdapter_NonGrokCLIHasNoCatalogProbeButSurfacesSkipReason (#604):
// adapters with no catalogParser wired (claude, codex, gemini, copilot
// today) must never populate Catalog with a guessed/fabricated
// command/shape (#551) — but, unlike PR #602's original architecture-only
// state, they now surface WHY via CatalogWarning rather than leaving the
// field silently empty and indistinguishable from "not gotten to yet."
// CONTRACT CHANGE (#604): this test previously asserted CatalogWarning=="".
// It now asserts the opposite for exactly this reason — the AC requires the
// skip to be explicit, not silently absent.
func TestCheckAdapter_NonGrokCLIHasNoCatalogProbeButSurfacesSkipReason(t *testing.T) {
	fp := fakeProbe{
		paths:    map[string]string{"codex": "/bin/codex"},
		versions: map[string]string{"/bin/codex": "codex 0.112.0"},
		codex:    t.TempDir(),
	}
	h := checkAdapter("codex", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected codex OK (a no-catalog skip reason never fails the adapter), got %+v", h)
	}
	if h.Catalog != nil {
		t.Errorf("expected codex to have no fabricated Catalog, got %+v", h.Catalog)
	}
	if !strings.HasPrefix(h.CatalogWarning, "no catalog probe: ") || !strings.Contains(h.CatalogWarning, "codex --help") {
		t.Errorf("expected codex CatalogWarning to explain the skip reason, got %q", h.CatalogWarning)
	}
}

// TestAdapterSpecs_CatalogWiringIsMutuallyExclusive (#604) pins that every
// kindCLI adapter declares EXACTLY one of catalogParser (a wired, evidence-backed
// probe) or catalogSkipReason (an explicit, evidence-backed "no probe"
// decision) — never both, never neither. A kindCLI adapter with neither would
// regress to #602's original silent-absence state; one with both would be
// self-contradictory spec data.
func TestAdapterSpecs_CatalogWiringIsMutuallyExclusive(t *testing.T) {
	for name, spec := range adapterSpecs {
		if spec.kind != kindCLI {
			continue
		}
		wired := spec.catalogParser != nil
		skipped := spec.catalogSkipReason != ""
		if wired == skipped {
			t.Errorf("adapter %q: exactly one of catalogParser/catalogSkipReason must be set, got wired=%v skipped=%v", name, wired, skipped)
		}
	}
	// Pin the specific split as of #604.
	for _, name := range []string{"claude-headless", "codex", "gemini", "copilot"} {
		spec := adapterSpecs[name]
		if spec.catalogParser != nil {
			t.Errorf("adapter %q: expected no catalogParser (no captured command evidence exists)", name)
		}
		if spec.catalogSkipReason == "" {
			t.Errorf("adapter %q: expected a non-empty catalogSkipReason", name)
		}
		if len(spec.catalogArgs) != 0 {
			t.Errorf("adapter %q: expected no catalogArgs alongside a skip reason, got %v", name, spec.catalogArgs)
		}
	}
	if adapterSpecs["grok"].catalogParser == nil {
		t.Error(`adapter "grok": expected catalogParser to remain wired`)
	}
	if adapterSpecs["grok"].catalogSkipReason != "" {
		t.Error(`adapter "grok": expected no catalogSkipReason alongside a wired catalogParser`)
	}
}

// TestCheckAdapter_CatalogSkipReasonSurfacedForClaudeGeminiCopilot (#604)
// exercises the remaining three no-catalog adapters end-to-end (codex is
// covered above): once each passes its baseline health, CatalogWarning must
// explain the skip — never leave Catalog/CatalogWarning both empty and never
// fail OK on account of it.
func TestCheckAdapter_CatalogSkipReasonSurfacedForClaudeGeminiCopilot(t *testing.T) {
	cases := []struct {
		adapter  string
		fp       fakeProbe
		wantText string
	}{
		{
			adapter:  "claude",
			fp:       fakeProbe{paths: map[string]string{"claude": "/opt/claude"}, versions: map[string]string{"/opt/claude": "claude 2.1.233"}},
			wantText: "claude --help",
		},
		{
			adapter:  "gemini",
			fp:       fakeProbe{paths: map[string]string{"gemini": "/bin/gemini"}, versions: map[string]string{"/bin/gemini": "gemini 0.29.0"}},
			wantText: "no gemini CLI was installed",
		},
		{
			adapter:  "copilot",
			fp:       fakeProbe{paths: map[string]string{"copilot": "/bin/copilot"}, versions: map[string]string{"/bin/copilot": "copilot 0.1.0"}},
			wantText: "Cannot find GitHub Copilot CLI",
		},
	}
	for _, c := range cases {
		t.Run(c.adapter, func(t *testing.T) {
			h := checkAdapter(c.adapter, c.fp.toProbe())
			if !h.OK {
				t.Fatalf("expected %s OK, got remediation=%q", c.adapter, h.Remediation)
			}
			if h.Catalog != nil {
				t.Errorf("expected %s to have no fabricated Catalog, got %+v", c.adapter, h.Catalog)
			}
			if !strings.HasPrefix(h.CatalogWarning, "no catalog probe: ") || !strings.Contains(h.CatalogWarning, c.wantText) {
				t.Errorf("expected %s CatalogWarning to mention %q, got %q", c.adapter, c.wantText, h.CatalogWarning)
			}
		})
	}
}

// TestCheckAdapter_CatalogSkipReasonSkippedWhenBaselineFails (#604) mirrors
// grok's TestCheckAdapter_GrokCatalogSkippedWhenNotInstalled /
// …SkippedBelowVersionFloor: the no-catalog explanation must stay silent
// when the adapter already fails for an unrelated reason — no redundant
// second note layered on a baseline failure that's already reported via
// Installed/VersionOK/Remediation.
func TestCheckAdapter_CatalogSkipReasonSkippedWhenBaselineFails(t *testing.T) {
	notInstalled := checkAdapter("codex", fakeProbe{codex: t.TempDir()}.toProbe())
	if notInstalled.OK || notInstalled.Installed {
		t.Fatal("expected codex !OK/!installed when the binary is missing")
	}
	if notInstalled.CatalogWarning != "" {
		t.Errorf("expected no catalog-skip note when not installed, got %q", notInstalled.CatalogWarning)
	}

	belowFloor := checkAdapter("gemini", fakeProbe{
		paths:    map[string]string{"gemini": "/bin/gemini"},
		versions: map[string]string{"/bin/gemini": "gemini 0.28.9"},
	}.toProbe())
	if belowFloor.OK || belowFloor.VersionOK {
		t.Fatal("expected gemini !OK below its version floor")
	}
	if belowFloor.CatalogWarning != "" {
		t.Errorf("expected no catalog-skip note below the version floor, got %q", belowFloor.CatalogWarning)
	}
}

// TestNoCatalogEvidence_CodexHelpHasNoModelsCommand and its claude sibling
// below pin the captured evidence backing codexNoCatalogReason /
// claudeNoCatalogReason (testdata/no-catalog-cli-probes/). If a future CLI
// release adds a models/catalog-listing command, a freshly captured
// `--help` would mention "models" and this test would fail — the intended
// trip-wire telling a maintainer to revisit the skip reason and wire a real
// probe (testdata/no-catalog-cli-probes/README.md § Revisiting this
// decision), rather than the omission going unnoticed indefinitely.
func TestNoCatalogEvidence_CodexHelpHasNoModelsCommand(t *testing.T) {
	b, err := os.ReadFile("testdata/no-catalog-cli-probes/codex-help.txt")
	if err != nil {
		t.Fatalf("could not read codex --help fixture: %v", err)
	}
	if strings.Contains(strings.ToLower(string(b)), "models") {
		t.Error(`captured codex --help now mentions "models" — revisit codexNoCatalogReason (#604): a newer codex release may have added a catalog-listing command`)
	}
}

func TestNoCatalogEvidence_ClaudeHelpHasNoModelsCommand(t *testing.T) {
	b, err := os.ReadFile("testdata/no-catalog-cli-probes/claude-help.txt")
	if err != nil {
		t.Fatalf("could not read claude --help fixture: %v", err)
	}
	if strings.Contains(strings.ToLower(string(b)), "models") {
		t.Error(`captured claude --help now mentions "models" — revisit claudeNoCatalogReason (#604): a newer claude release may have added a catalog-listing command`)
	}
}

// TestServedCLIModelDiff_RealRegistry pins servedCLIModelDiff against the
// real embedded xai registry entries: grok-4.6 and grok-4.5 are
// transports.cli.served=true; grok-build-0.1 is explicitly served=false
// (kept for historical cost replay only, #532). A registry edit that
// changes these facts must fail this test loudly rather than silently
// widen/narrow what #551's drift probe catches.
func TestServedCLIModelDiff_RealRegistry(t *testing.T) {
	cases := []struct {
		name           string
		live           []string
		wantMissing    []string
		wantUndeclared []string
	}{
		{"exact match", []string{"grok-4.6", "grok-4.5"}, nil, nil},
		{"missing one served model", []string{"grok-4.6"}, []string{"grok-4.5"}, nil},
		{"missing all served models", nil, []string{"grok-4.5", "grok-4.6"}, nil},
		{"deprecated unserved model offered", []string{"grok-4.6", "grok-4.5", "grok-build-0.1"}, nil, []string{"grok-build-0.1"}},
		{"unknown id offered", []string{"grok-4.6", "grok-4.5", "grok-9-mystery"}, nil, []string{"grok-9-mystery"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			missing, undeclared := servedCLIModelDiff("xai", c.live)
			if !equalStrings(missing, c.wantMissing) {
				t.Errorf("missing = %v, want %v", missing, c.wantMissing)
			}
			if !equalStrings(undeclared, c.wantUndeclared) {
				t.Errorf("undeclared = %v, want %v", undeclared, c.wantUndeclared)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
