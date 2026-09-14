package doctor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// openCodeManifest is the opencode compat manifest, the source of every
// version these tests use.
func openCodeManifest(t *testing.T) adaptercompat.Manifest {
	t.Helper()
	m, ok := adaptercompat.Get("opencode")
	if !ok || m.MinVersion == "" || m.MaxTested == "" {
		t.Fatalf("the opencode compat manifest has no floor and max-tested: %+v", m)
	}
	return m
}

func patchBelow(t *testing.T, version string) string {
	t.Helper()
	parts := strings.Split(version, ".")
	patch, err := strconv.Atoi(parts[2])
	if err != nil || patch == 0 {
		t.Fatalf("cannot step %q down", version)
	}
	parts[2] = strconv.Itoa(patch - 1)
	return strings.Join(parts, ".")
}

// readOpenCodeFixture reads a captured OpenCode output from testdata (see
// testdata/opencode-capture/README.md).
func readOpenCodeFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "opencode-1.18.30-"+name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// openCodeLMStudio is the reference machine's block: LM Studio on loopback
// with a 131072-token window loaded. The port is the discard port; a test
// that probes the endpoint for real points base_url at its own server.
func openCodeLMStudio() config.OpenCodeConfig {
	return config.OpenCodeConfig{
		Model:    "lmstudio/qwen/qwen3.8-27b",
		Provider: "lm-studio",
		BaseURL:  "http://127.0.0.1:9/v1",
		Limit:    config.OpenCodeLimit{Context: 131072, Output: 8192},
	}
}

// openCodeFixture is a doctor probe for one test: the gate open, a home of
// its own, the block settings, a binary at max-tested whose `opencode
// models` prints the configured-model capture, and every endpoint ready at
// the context it is given.
type openCodeFixture struct {
	env   map[string]string
	home  string
	probe openCodeProbe
}

func newOpenCodeFixture(t *testing.T, settings config.OpenCodeConfig) *openCodeFixture {
	t.Helper()
	m := openCodeManifest(t)
	f := &openCodeFixture{env: map[string]string{adapters.ExperimentalOpenCodeEnvVar: "1"}, home: t.TempDir()}
	configured := readOpenCodeFixture(t, "models-configured")
	f.probe = openCodeProbe{
		getenv:           func(k string) string { return f.env[k] },
		lookupEnv:        func(k string) (string, bool) { v, ok := f.env[k]; return v, ok },
		home:             func() (string, error) { return f.home, nil },
		machineConfigDir: func() (string, error) { return filepath.Join(f.home, ".nightgauge"), nil },
		settings:         func() (config.OpenCodeConfig, error) { return settings, nil },
		lookPath:         func(string) (string, error) { return "/nightgauge-test/bin/opencode", nil },
		version:          func(string) (string, error) { return m.MaxTested, nil },
		models:           func(string, config.OpenCodeConfig, string) (string, error) { return configured, nil },
		endpoint: func(target adapters.OpenCodeEndpointTarget, model string, injected int) adapters.OpenCodeEndpointReadiness {
			return adapters.OpenCodeEndpointReadiness{Endpoint: target.ID, Kind: target.Kind, Model: model,
				Reachable: true, Ready: true, InjectedContext: injected, LoadedContext: injected}
		},
		readFile: os.ReadFile,
		glob:     filepath.Glob,
		goos:     runtime.GOOS,
	}
	return f
}

func (f *openCodeFixture) check() AdapterHealth {
	return checkAdapter("opencode", adapterProbe{opencode: f.probe})
}

// rowText is everything a row says, for asserting what it never says.
func rowText(t *testing.T, h AdapterHealth) string {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n" + h.Remediation + "\n" + strings.Join(h.Warnings, "\n") + "\n" + strings.Join(h.Notes, "\n")
}

// TestOpenCodeHealthyRow: with the gate open and everything in order the row
// is usable, reports the binary at the manifest's versions, and lists the
// configured model as present.
func TestOpenCodeHealthyRow(t *testing.T) {
	m := openCodeManifest(t)
	h := newOpenCodeFixture(t, openCodeLMStudio()).check()
	if !h.OK {
		t.Fatalf("a healthy opencode row is not OK: %s", h.Remediation)
	}
	if h.Kind != "cli" || h.Binary != "opencode" || h.Version != m.MaxTested || !h.VersionOK {
		t.Errorf("row = %+v, want the cli binary at %s", h, m.MaxTested)
	}
	if h.MinVersion != m.MinVersion || h.OpenCode.MaxTested != m.MaxTested || h.OpenCode.FloorPolicy != m.FloorPolicy {
		t.Errorf("floor %q, max-tested %q, policy %q; want the manifest's %q, %q, %q",
			h.MinVersion, h.OpenCode.MaxTested, h.OpenCode.FloorPolicy, m.MinVersion, m.MaxTested, m.FloorPolicy)
	}
	if h.ModelOK == nil || !*h.ModelOK || h.Model != "lmstudio/qwen/qwen3.8-27b" {
		t.Errorf("model %q ModelOK %v, want the configured model present", h.Model, h.ModelOK)
	}
	if len(h.Warnings) != 0 {
		t.Errorf("a healthy row warned: %q", h.Warnings)
	}
}

// TestOpenCodeRowWithTheGateClosedRunsNothing: while the enable gate is
// closed the row is not usable, says why, and runs no check at all, so cap
// recovery never hops onto an adapter whose dispatch is refused and costs
// nothing to ask.
func TestOpenCodeRowWithTheGateClosedRunsNothing(t *testing.T) {
	m := openCodeManifest(t)
	for _, value := range []string{"", "true", " 1"} {
		f := newOpenCodeFixture(t, openCodeLMStudio())
		f.env[adapters.ExperimentalOpenCodeEnvVar] = value
		called := func(name string) { t.Errorf("gate %q: the closed gate still ran %s", value, name) }
		f.probe.home = func() (string, error) { called("home"); return "", nil }
		f.probe.settings = func() (config.OpenCodeConfig, error) { called("settings"); return config.OpenCodeConfig{}, nil }
		f.probe.lookPath = func(string) (string, error) { called("lookPath"); return "", nil }
		f.probe.version = func(string) (string, error) { called("version"); return "", nil }
		h := f.check()
		if h.OK || h.OpenCode == nil || h.OpenCode.Enabled {
			t.Errorf("gate %q: row = %+v, want not usable and not enabled", value, h)
		}
		if !strings.Contains(h.Remediation, adapters.ExperimentalOpenCodeEnvVar+"=1") {
			t.Errorf("gate %q: remediation %q does not name the switch", value, h.Remediation)
		}
		if h.MinVersion != m.MinVersion || h.OpenCode.MaxTested != m.MaxTested {
			t.Errorf("gate %q: the closed row does not report the manifest's floor and max-tested", value)
		}
	}
	// A probe that wires no OpenCode dependencies at all, as every other
	// doctor test's does, reads the gate as closed.
	if h := checkAdapter("opencode", fakeProbe{}.toProbe()); h.OK || h.OpenCode.Enabled {
		t.Errorf("an unwired probe ran the OpenCode checks: %+v", h)
	}
}

// TestOpenCodeCatalogProbe: the captured `opencode models` output lists the
// configured provider/model or does not, and the row says so; a missing model
// blocks, with remediation.
func TestOpenCodeCatalogProbe(t *testing.T) {
	for _, c := range []struct {
		fixture string
		want    bool
	}{{"models-configured", true}, {"models-other", false}} {
		t.Run(c.fixture, func(t *testing.T) {
			f := newOpenCodeFixture(t, openCodeLMStudio())
			out := readOpenCodeFixture(t, c.fixture)
			f.probe.models = func(string, config.OpenCodeConfig, string) (string, error) { return out, nil }
			h := f.check()
			if h.ModelOK == nil || *h.ModelOK != c.want {
				t.Fatalf("ModelOK = %v, want %v", h.ModelOK, c.want)
			}
			if h.OK != c.want {
				t.Errorf("OK = %v, want %v: a model the catalog lacks blocks", h.OK, c.want)
			}
			if !c.want && !strings.Contains(h.Remediation, "`opencode models` does not list opencode.model lmstudio/qwen/qwen3.8-27b") {
				t.Errorf("remediation %q does not name the missing model", h.Remediation)
			}
		})
	}
	ids, _, ok := parseOpenCodeCatalog(readOpenCodeFixture(t, "models-configured"))
	if !ok || len(ids) != 4 || ids[3] != "lmstudio/qwen/qwen3.8-27b" {
		t.Errorf("parseOpenCodeCatalog(capture) = %v, %v", ids, ok)
	}
	if _, _, ok := parseOpenCodeCatalog("Error: something went wrong\n"); ok {
		t.Error("an output with no provider/model line parsed as a catalog")
	}
}

// TestOpenCodeCatalogProbeRefusalBlocks: a configured model the adapter
// would refuse to build a config for blocks with the adapter's own refusal,
// without running the probe.
func TestOpenCodeCatalogProbeRefusalBlocks(t *testing.T) {
	settings := openCodeLMStudio()
	settings.Model = "anthropic/claude-sonnet-5"
	f := newOpenCodeFixture(t, settings)
	f.probe.models = func(string, config.OpenCodeConfig, string) (string, error) {
		t.Error("the catalog probe ran for a config the adapter refuses")
		return "", nil
	}
	h := f.check()
	if h.OK || !strings.Contains(h.Remediation, "ANTHROPIC_API_KEY is not set") {
		t.Errorf("row = OK %v, %q; want the adapter's ANTHROPIC_API_KEY refusal", h.OK, h.Remediation)
	}
}

// lmStudioServer is an httptest LM Studio: GET /api/v0/models answers models.
func lmStudioServer(t *testing.T, models string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the readiness probe sent a credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, models)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// lmStudioListing is LM Studio's GET /api/v0/models shape with one model.
func lmStudioListing(state string, loaded int) string {
	return fmt.Sprintf(`{"object":"list","data":[{"id":"qwen/qwen3.8-27b","object":"model","type":"llm","state":%q,"max_context_length":262144,"loaded_context_length":%d},{"id":"text-embedding-nomic","object":"model","type":"embeddings","state":"not-loaded","max_context_length":2048}]}`, state, loaded)
}

// TestOpenCodeEndpointReadinessLMStudio: reachability, whether the model is
// loaded, the context it is loaded with, and the warning when the injected
// limit.context is 0 or larger than that.
func TestOpenCodeEndpointReadinessLMStudio(t *testing.T) {
	target := func(srv *httptest.Server) adapters.OpenCodeEndpointTarget {
		return adapters.OpenCodeEndpointTarget{ID: "lmstudio", Kind: "lm-studio", BaseURL: srv.URL + "/v1"}
	}
	loaded := lmStudioServer(t, lmStudioListing("loaded", 131072))

	r := adapters.ProbeOpenCodeEndpoint(nil, target(loaded), "qwen/qwen3.8-27b", 0)
	if !r.Reachable || !r.Ready || r.Loaded == nil || !*r.Loaded || r.LoadedContext != 131072 {
		t.Errorf("loaded model: %+v, want reachable, ready and loaded at 131072", r)
	}
	if !strings.Contains(r.Warning, "limit.context is 0") {
		t.Errorf("injected 0 against 131072 loaded: warning %q, want the zero-limit warning", r.Warning)
	}
	if r := adapters.ProbeOpenCodeEndpoint(nil, target(loaded), "qwen/qwen3.8-27b", 131072); r.Warning != "" || !r.Ready {
		t.Errorf("injected 131072 against 131072 loaded: %+v, want ready with no warning", r)
	}
	if r := adapters.ProbeOpenCodeEndpoint(nil, target(loaded), "qwen/qwen3.8-27b", 262144); !strings.Contains(r.Warning, "larger than the 131072 tokens") {
		t.Errorf("injected 262144 against 131072 loaded: warning %q", r.Warning)
	}
	if r := adapters.ProbeOpenCodeEndpoint(nil, target(loaded), "qwen/qwen3-coder-30b", 131072); r.Ready || !strings.Contains(r.Problem, "lms get qwen/qwen3-coder-30b") {
		t.Errorf("a model the server lacks: %+v, want not ready with the download command", r)
	}

	notLoaded := lmStudioServer(t, lmStudioListing("not-loaded", 0))
	r = adapters.ProbeOpenCodeEndpoint(nil, target(notLoaded), "qwen/qwen3.8-27b", 131072)
	if r.Ready || r.Loaded == nil || *r.Loaded || !strings.Contains(r.Problem, "lms load qwen/qwen3.8-27b --context-length 131072") {
		t.Errorf("a model that is not loaded: %+v, want not ready with the load command", r)
	}

	closed := lmStudioServer(t, "{}")
	closed.Close()
	start := time.Now()
	r = adapters.ProbeOpenCodeEndpoint(nil, target(closed), "qwen/qwen3.8-27b", 131072)
	if elapsed := time.Since(start); elapsed > 2*time.Second+500*time.Millisecond {
		t.Errorf("the probe of a closed server took %s, want at most 2s", elapsed)
	}
	if r.Reachable || r.Ready || !strings.Contains(r.Problem, "endpoint lmstudio is not answering") {
		t.Errorf("a closed server: %+v, want unreachable, by endpoint id", r)
	}
}

// TestOpenCodeEndpointReadinessOllama: /api/show gives the model's num_ctx,
// and /api/ps whether it is loaded; a model Ollama lacks is not ready.
func TestOpenCodeEndpointReadinessOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "qwen3:8b" {
				http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprint(w, `{"parameters":"num_ctx                        8192\nstop                           \"<|im_end|>\"","details":{"family":"qwen3"}}`)
		case "/api/ps":
			_, _ = fmt.Fprint(w, `{"models":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target := adapters.OpenCodeEndpointTarget{ID: "ollama", Kind: "ollama", BaseURL: srv.URL + "/v1"}

	r := adapters.ProbeOpenCodeEndpoint(nil, target, "qwen3:8b", 131072)
	if !r.Ready || r.Loaded == nil || *r.Loaded || r.LoadedContext != 8192 {
		t.Errorf("a pulled model that is not loaded: %+v, want ready, not loaded, num_ctx 8192", r)
	}
	if !strings.Contains(r.Warning, "larger than the 8192 tokens") {
		t.Errorf("injected 131072 against num_ctx 8192: warning %q", r.Warning)
	}
	if r := adapters.ProbeOpenCodeEndpoint(nil, target, "llama9:70b", 131072); r.Ready || !strings.Contains(r.Problem, "ollama pull llama9:70b") {
		t.Errorf("a model Ollama lacks: %+v, want not ready with the pull command", r)
	}
}

// TestOpenCodeRowEndpointNotReadyBlocks: a server that is not ready blocks
// the row when opencode.model runs on it, through the real readiness probe.
func TestOpenCodeRowEndpointNotReadyBlocks(t *testing.T) {
	srv := lmStudioServer(t, lmStudioListing("not-loaded", 0))
	settings := openCodeLMStudio()
	settings.BaseURL = srv.URL + "/v1"
	f := newOpenCodeFixture(t, settings)
	f.probe.endpoint = func(target adapters.OpenCodeEndpointTarget, model string, injected int) adapters.OpenCodeEndpointReadiness {
		return adapters.ProbeOpenCodeEndpoint(nil, target, model, injected)
	}
	h := f.check()
	if h.OK || !strings.Contains(h.Remediation, "is not loaded on endpoint lmstudio") {
		t.Errorf("row = OK %v, %q; want blocked on the unloaded model", h.OK, h.Remediation)
	}
	if len(h.OpenCode.Endpoints) != 1 || h.OpenCode.Endpoints[0].Endpoint != "lmstudio" {
		t.Errorf("endpoints = %+v, want the one declared, by id", h.OpenCode.Endpoints)
	}
}

// TestOpenCodeRunDirsAreTheIsolationDirs: the directories the row prints are
// OpenCode's under the XDG directories run isolation (#1616) gives a spawn,
// and the offline row describes configuration only, egress unverified.
func TestOpenCodeRunDirsAreTheIsolationDirs(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		settings := openCodeLMStudio()
		settings.InheritUserConfig = inherit
		f := newOpenCodeFixture(t, settings)
		h := f.check()
		want, err := adapters.OpenCodeIsolationEnv(adapters.OpenCodeIsolation{
			Root:              filepath.Join(adapters.OpenCodeRunsDir(f.home), "<run-id>"),
			Home:              f.home,
			Lookup:            func(string) (string, bool) { return "", false },
			GOOS:              runtime.GOOS,
			MachineConfigDir:  filepath.Join(f.home, ".nightgauge"),
			InheritUserConfig: inherit,
		})
		if err != nil {
			t.Fatal(err)
		}
		dirs := h.OpenCode.Dirs
		if dirs == nil {
			t.Fatal("the row has no run directories")
		}
		for name, got := range map[string]string{
			"XDG_CONFIG_HOME": dirs.Config, "XDG_DATA_HOME": dirs.Data,
			"XDG_CACHE_HOME": dirs.Cache, "XDG_STATE_HOME": dirs.State,
		} {
			if w := filepath.Join(want[name], "opencode"); got != w {
				t.Errorf("inherit=%v: %s dir = %q, want OpenCode's under the isolation's %s, %q", inherit, name, got, name, w)
			}
			if !strings.Contains(strings.Join(h.Notes, "\n"), got) {
				t.Errorf("inherit=%v: the row does not print %s", inherit, got)
			}
		}
		if (dirs.OperatorConfig != "") != inherit || dirs.OperatorConfig != want["OPENCODE_CONFIG_DIR"] {
			t.Errorf("inherit=%v: operator config %q, want %q", inherit, dirs.OperatorConfig, want["OPENCODE_CONFIG_DIR"])
		}
		if !strings.Contains(h.OpenCode.Offline, "egress unverified") || !strings.Contains(strings.Join(h.Notes, "\n"), "egress unverified") {
			t.Errorf("inherit=%v: offline row %q does not say egress is unverified", inherit, h.OpenCode.Offline)
		}
		if !strings.Contains(h.OpenCode.Offline, "on endpoint lmstudio on this machine") {
			t.Errorf("inherit=%v: offline row %q does not place the model", inherit, h.OpenCode.Offline)
		}
	}
	hosted := openCodeOfflinePosture("anthropic/claude-sonnet-5", nil)
	if !strings.Contains(hosted, "hosted provider anthropic, so a run on it is not offline") {
		t.Errorf("a hosted model's offline row = %q", hosted)
	}
}

// TestOpenCodeFlagsAnAnthropicOAuthLogin: an OAuth-type anthropic entry in
// the operator's auth.json, in OPENCODE_AUTH_CONTENT, or in a run root's
// auth.json is flagged, naming where it is and never what it holds; other
// entries and API-key entries are not.
func TestOpenCodeFlagsAnAnthropicOAuthLogin(t *testing.T) {
	f := newOpenCodeFixture(t, openCodeLMStudio())
	operator := filepath.Join(f.home, ".local", "share", "opencode", "auth.json")
	runRoot := filepath.Join(adapters.OpenCodeRunsDir(f.home), "01890a5d-ac96-774b-bcce-b302099a8057", "data", "opencode", "auth.json")
	for path, body := range map[string]string{
		operator: `{"anthropic":{"type":"oauth","access":"SECRET","refresh":"SECRET","expires":1},"openai":{"type":"oauth","access":"SECRET"}}`,
		runRoot:  `{"anthropic":{"type":"oauth","access":"SECRET"}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.env["OPENCODE_AUTH_CONTENT"] = `{"anthropic":{"type":"oauth","access":"SECRET"}}`

	h := f.check()
	var sources []string
	for _, login := range h.OpenCode.StoredLogins {
		if login.Provider != "anthropic" || login.Type != "oauth" {
			t.Errorf("flagged %+v; only an anthropic OAuth entry is flagged", login)
		}
		sources = append(sources, login.Source)
	}
	if strings.Join(sources, ",") != strings.Join([]string{operator, "OPENCODE_AUTH_CONTENT", runRoot}, ",") {
		t.Errorf("flagged sources %q, want the operator's auth.json, OPENCODE_AUTH_CONTENT and the run root's auth.json", sources)
	}
	warnings := strings.Join(h.Warnings, "\n")
	for _, want := range []string{"subscription or OAuth login for anthropic", "ANTHROPIC_API_KEY", "claude-headless", "never uses it"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the finding does not say %q:\n%s", want, warnings)
		}
	}
	if strings.Contains(rowText(t, h), "SECRET") {
		t.Errorf("a stored credential's value reached the row:\n%s", rowText(t, h))
	}
	if !h.OK {
		t.Errorf("a stored login a run never uses blocked the row: %s", h.Remediation)
	}

	f.env["OPENCODE_AUTH_CONTENT"] = `{"anthropic":{"type":"api","key":"SECRET"}}`
	_ = os.Remove(operator)
	_ = os.Remove(runRoot)
	if h := f.check(); len(h.OpenCode.StoredLogins) != 0 {
		t.Errorf("an API-key entry was flagged as a login: %+v", h.OpenCode.StoredLogins)
	}
}

// TestOpenCodePinnedBinaryVersionAndDrift: opencode.binary pins the binary
// the row reads its version from, a relative pin is refused and not looked
// up, and a version other than the last dispatch's is a drift warning.
func TestOpenCodePinnedBinaryVersionAndDrift(t *testing.T) {
	m := openCodeManifest(t)
	pin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(pin, []byte("#!/bin/sh\n[ \"$1\" = --version ] && { echo "+m.MaxTested+"; exit 0; }\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := openCodeLMStudio()
	settings.Binary = pin
	f := newOpenCodeFixture(t, settings)
	f.probe.version = adapters.OpenCodeVersionOf
	f.probe.lookPath = func(string) (string, error) {
		t.Error("a pinned binary was looked up on PATH")
		return "", nil
	}
	h := f.check()
	if !h.OK || h.Path != pin || h.Version != m.MaxTested || !h.OpenCode.Pinned {
		t.Fatalf("row = OK %v path %q version %q pinned %v (%s); want the pin at %s", h.OK, h.Path, h.Version, h.OpenCode.Pinned, h.Remediation, m.MaxTested)
	}

	last := patchBelow(t, m.MaxTested)
	if err := adapters.RecordOpenCodeDispatch(f.home, adapters.OpenCodeDispatchRecord{Binary: pin, Version: last}); err != nil {
		t.Fatal(err)
	}
	h = f.check()
	warnings := strings.Join(h.Warnings, "\n")
	if h.OpenCode.LastDispatchVersion != last || !strings.Contains(warnings, "is "+m.MaxTested+", and the last OpenCode dispatch on this machine ran "+last) {
		t.Errorf("drift %s against %s: last %q, warnings:\n%s", m.MaxTested, last, h.OpenCode.LastDispatchVersion, warnings)
	}
	if !h.OK {
		t.Errorf("drift blocked the row: %s", h.Remediation)
	}

	for _, relative := range []string{"bin/opencode", "opencode"} {
		settings.Binary = relative
		f := newOpenCodeFixture(t, settings)
		f.probe.lookPath = func(string) (string, error) {
			t.Errorf("the relative pin %q was looked up on PATH", relative)
			return "", nil
		}
		if h := f.check(); h.OK || !strings.Contains(h.Remediation, "is not an absolute path") {
			t.Errorf("pin %q: row = OK %v, %q; want it refused", relative, h.OK, h.Remediation)
		}
	}
}

// TestOpenCodeRowVersionPolicy: below the floor the row is blocked with the
// adapter's adapter_incompatible refusal; above max-tested it warns, and
// blocks only when opencode.model runs on an endpoint.
func TestOpenCodeRowVersionPolicy(t *testing.T) {
	m := openCodeManifest(t)
	below := newOpenCodeFixture(t, openCodeLMStudio())
	below.probe.version = func(string) (string, error) { return patchBelow(t, m.MinVersion), nil }
	if h := below.check(); h.OK || h.VersionOK || !strings.Contains(h.Remediation, "adapter_incompatible") {
		t.Errorf("below the floor: OK %v VersionOK %v, %q", h.OK, h.VersionOK, h.Remediation)
	}

	parts := strings.Split(m.MaxTested, ".")
	patch, _ := strconv.Atoi(parts[2])
	above := strings.Join([]string{parts[0], parts[1], strconv.Itoa(patch + 1)}, ".")
	local := newOpenCodeFixture(t, openCodeLMStudio())
	local.probe.version = func(string) (string, error) { return above, nil }
	if h := local.check(); h.OK || !h.OpenCode.AboveMaxTested || !strings.Contains(h.Remediation, "no endpoint is dispatched above it") {
		t.Errorf("above max-tested on an endpoint: OK %v, %q", h.OK, h.Remediation)
	}
	hostedSettings := openCodeLMStudio()
	hostedSettings.Model = "anthropic/claude-sonnet-5"
	hosted := newOpenCodeFixture(t, hostedSettings)
	hosted.env["ANTHROPIC_API_KEY"] = "set-by-the-test"
	hosted.probe.version = func(string) (string, error) { return above, nil }
	hosted.probe.models = func(string, config.OpenCodeConfig, string) (string, error) {
		return "anthropic/claude-sonnet-5\nanthropic/claude-opus-5\n", nil
	}
	h := hosted.check()
	if !h.OK || !strings.Contains(strings.Join(h.Warnings, "\n"), "runs a self-test") {
		t.Errorf("above max-tested on a hosted model: OK %v, %q, warnings %q", h.OK, h.Remediation, h.Warnings)
	}
}

// TestOpenCodeUnreachableServerIsNotACapHopTarget drives CheckAdapters, the
// function orchestrator.AdapterUsableForCapHop reads its verdict from: with
// the configured model's server unreachable, the row is not usable and its
// remediation is the doctor's, which is the reason the cap hop logs; with the
// server ready, it is usable.
func TestOpenCodeUnreachableServerIsNotACapHopTarget(t *testing.T) {
	closed := lmStudioServer(t, "{}")
	closed.Close()
	settings := openCodeLMStudio()
	settings.BaseURL = closed.URL + "/v1"
	f := newOpenCodeFixture(t, settings)
	f.probe.endpoint = func(target adapters.OpenCodeEndpointTarget, model string, injected int) adapters.OpenCodeEndpointReadiness {
		return adapters.ProbeOpenCodeEndpoint(nil, target, model, injected)
	}
	prev := newOpenCodeProbe
	newOpenCodeProbe = func() openCodeProbe { return f.probe }
	t.Cleanup(func() { newOpenCodeProbe = prev })

	usable, reason := capHopVerdict(CheckAdapters([]string{"opencode"}))
	if usable || !strings.Contains(reason, "endpoint lmstudio is not answering") {
		t.Errorf("unreachable server: usable %v, reason %q; want not usable with the doctor's remediation", usable, reason)
	}

	ready := lmStudioServer(t, lmStudioListing("loaded", 131072))
	settings.BaseURL = ready.URL + "/v1"
	f.probe.settings = func() (config.OpenCodeConfig, error) { return settings, nil }
	if usable, reason := capHopVerdict(CheckAdapters([]string{"opencode"})); !usable {
		t.Errorf("ready server: not usable: %s", reason)
	}
}

// capHopVerdict is orchestrator.AdapterUsableForCapHop's reading of a
// CheckAdapters result, which this package cannot import.
func capHopVerdict(health []AdapterHealth) (bool, string) {
	if len(health) == 0 {
		return false, "adapter doctor returned no reading"
	}
	if h := health[0]; !h.OK {
		return false, h.Remediation
	}
	return true, ""
}

// TestOpenCodeProbeRedactsBaseURL: a probe of an endpoint on another machine
// (a documentation address, RFC 5737) reports the endpoint by id and never
// prints the address, although Go's own error for the failed request quotes
// the whole URL.
func TestOpenCodeProbeRedactsBaseURL(t *testing.T) {
	const address = "192.0.2.7"
	client := &http.Client{Timeout: 200 * time.Millisecond}
	r := adapters.ProbeOpenCodeEndpoint(client,
		adapters.OpenCodeEndpointTarget{ID: "lmstudio-remote", Kind: "lm-studio", BaseURL: "http://" + address + ":1234/v1"},
		"qwen/qwen3.8-27b", 131072)
	b, _ := json.Marshal(r)
	if r.Reachable || !strings.Contains(r.Problem, "endpoint lmstudio-remote") {
		t.Errorf("probe = %+v, want unreachable, named by id", r)
	}
	if strings.Contains(string(b), address) {
		t.Errorf("the probe's result prints the address: %s", b)
	}

	settings := openCodeLMStudio()
	settings.BaseURL = "http://" + address + ":1234/v1"
	f := newOpenCodeFixture(t, settings)
	f.probe.endpoint = func(target adapters.OpenCodeEndpointTarget, model string, injected int) adapters.OpenCodeEndpointReadiness {
		return adapters.ProbeOpenCodeEndpoint(client, target, model, injected)
	}
	h := f.check()
	if h.OK || !strings.Contains(h.Remediation, "endpoint lmstudio is not answering") {
		t.Errorf("row = OK %v, %q; want the unreachable endpoint named by id", h.OK, h.Remediation)
	}
	if text := rowText(t, h); strings.Contains(text, address) {
		t.Errorf("the doctor row prints the address:\n%s", text)
	}
	if !strings.Contains(strings.Join(h.Warnings, "\n"), "endpoint lmstudio is on another machine and is reached over plain http") {
		t.Errorf("a plain-http endpoint on another machine did not warn: %q", h.Warnings)
	}
}

// debugConfigCase is one case of the captured `debug config` evidence.
type debugConfigCase struct {
	name, content string
	res           adapters.OpenCodeProbeResult
}

// readDebugConfigCases parses opencode-1.18.30-debug-config-invalid.txt,
// whose cases capture.sh writes as "==> <field>" sections.
func readDebugConfigCases(t *testing.T) map[string]debugConfigCase {
	t.Helper()
	cases := map[string]debugConfigCase{}
	var cur *debugConfigCase
	var section string
	var stdout, stderr strings.Builder
	finish := func() {
		if cur != nil {
			cur.res.Stdout = []byte(stdout.String())
			cur.res.Stderr = []byte(stderr.String())
			cases[cur.name] = *cur
		}
		cur, section = nil, ""
		stdout.Reset()
		stderr.Reset()
	}
	for _, line := range strings.Split(readOpenCodeFixture(t, "debug-config-invalid"), "\n") {
		if marker, ok := strings.CutPrefix(line, "==> "); ok {
			field, value, _ := strings.Cut(marker, " ")
			switch field {
			case "case":
				finish()
				cur = &debugConfigCase{name: value}
			case "exit":
				code, err := strconv.Atoi(value)
				if err != nil || cur == nil {
					t.Fatalf("malformed exit line %q", line)
				}
				cur.res.ExitCode = code
			case "end":
				finish()
			default:
				section = field
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch section {
		case "content":
			cur.content += line
		case "stdout":
			stdout.WriteString(line + "\n")
		case "stderr":
			stderr.WriteString(line + "\n")
		}
	}
	finish()
	return cases
}

// TestOpenCodeSelfTestEvaluatesTheCapturedDebugConfig holds the self-test's
// `debug config` check to what opencode 1.18.30 was observed to do: the
// unchanged per-run config passes; a wrong-typed value exits 1 and fails; an
// unknown key exits 0, the finding the issue did not expect, and fails
// because the key is missing from the merged config.
func TestOpenCodeSelfTestEvaluatesTheCapturedDebugConfig(t *testing.T) {
	cases := readDebugConfigCases(t)
	for name, wantExit := range map[string]int{"valid": 0, "unknown-key": 0, "wrong-type": 1} {
		c, ok := cases[name]
		if !ok {
			t.Fatalf("the capture has no %q case", name)
		}
		if c.res.ExitCode != wantExit {
			t.Errorf("%s: captured exit %d, want %d; re-read ADR-022 § 20 if the binary changed", name, c.res.ExitCode, wantExit)
		}
	}

	if err := adapters.EvaluateOpenCodeDebugConfig(cases["valid"].content, cases["valid"].res, nil); err != nil {
		t.Errorf("the unchanged per-run config failed the self-test: %v", err)
	}
	unknown := adapters.EvaluateOpenCodeDebugConfig(cases["unknown-key"].content, cases["unknown-key"].res, nil)
	if unknown == nil || !strings.Contains(unknown.Error(), "nightgauge_unknown_key") {
		t.Errorf("a dropped key passed the self-test: %v", unknown)
	}
	wrong := adapters.EvaluateOpenCodeDebugConfig(cases["wrong-type"].content, cases["wrong-type"].res, nil)
	if wrong == nil || !strings.Contains(wrong.Error(), "exited 1") || !strings.Contains(wrong.Error(), "Configuration is invalid at OPENCODE_CONFIG_CONTENT") {
		t.Errorf("a wrong-typed value passed the self-test, or its reason was lost: %v", wrong)
	}
	secret := adapters.EvaluateOpenCodeDebugConfig(cases["wrong-type"].content, cases["wrong-type"].res, []string{"OPENCODE_CONFIG_CONTENT"})
	if secret == nil || strings.Contains(secret.Error(), "at OPENCODE_CONFIG_CONTENT") {
		t.Errorf("a redacted string reached the self-test's error: %v", secret)
	}
}
