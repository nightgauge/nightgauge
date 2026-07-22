package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProbe builds an adapterProbe whose side effects are driven by in-memory
// maps so adapter health can be tested without real CLIs or filesystem.
type fakeProbe struct {
	paths      map[string]string // binary -> resolved path ("" / absent => not found)
	versions   map[string]string // path -> `--version` combined output
	verErrs    map[string]error  // path -> error from the version spawn
	env        map[string]string
	files      map[string][]byte // absolute path -> file content
	codex      string
	serverErr  error // kindHTTP reachability probe result (nil = reachable)
	noServer   bool  // when true, the probe reports unreachable
	probedURLs []string
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
		readFile: func(path string) ([]byte, error) {
			if b, ok := f.files[path]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		getenv: func(k string) string { return f.env[k] },
		httpReachable: func(baseURL string) error {
			if f.noServer {
				return errors.New("connection refused")
			}
			return f.serverErr
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
	// the model env, the claude binary on PATH, AND a reachable server (#57).
	ready := fakeProbe{
		env:   map[string]string{"NIGHTGAUGE_OLLAMA_MODEL": "llama3.2"},
		paths: map[string]string{"claude": "/opt/claude"},
	}
	h := checkAdapter("ollama", ready.toProbe())
	if !h.OK || h.Kind != "http" {
		t.Fatalf("expected ollama OK/http when model env set + claude bridge present + server up, got %+v", h)
	}
	if !h.ServerReachable || h.ServerURL != "http://localhost:11434/v1" {
		t.Errorf("expected reachable default server URL, got %+v", h)
	}

	unset := fakeProbe{env: map[string]string{}, paths: map[string]string{"claude": "/opt/claude"}}
	h2 := checkAdapter("lm-studio", unset.toProbe())
	if h2.OK {
		t.Error("expected lm-studio !OK when model env unset")
	}
	if h2.Remediation == "" {
		t.Error("expected remediation for unset local model env")
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
	if !adapterSpecs["codex"].mcp {
		t.Error("codex must be flagged as MCP-provisioning")
	}
	if len(AllAdapterNames()) != 8 {
		t.Errorf("expected 8 adapters in AllAdapterNames, got %d", len(AllAdapterNames()))
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
		paths: map[string]string{"claude": "/opt/claude"},
	}
	h := checkAdapter("lm-studio", fp.toProbe())
	if h.ServerURL != "http://10.0.0.5:9999/v1" {
		t.Errorf("expected overridden server URL, got %q", h.ServerURL)
	}
	if !h.OK {
		t.Errorf("expected OK with override reachable, got %+v", h)
	}
}
