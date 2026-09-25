package doctor

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/models"
)

// fakeProbe builds an adapterProbe whose side effects are driven by in-memory
// maps so adapter health can be tested without real CLIs or filesystem.
type fakeProbe struct {
	paths          map[string]string // binary -> resolved path ("" / absent => not found)
	versions       map[string]string // path -> `--version` combined output
	verErrs        map[string]error  // path -> error from the version spawn
	env            map[string]string
	files          map[string][]byte // absolute path -> file content
	codex          string
	catalogOutputs map[string]string // #551: path -> catalog-command combined output
	catalogErrs    map[string]error  // #551: path -> error from the catalog-command spawn
	// #1274 model-validity probe. modelProbe nil leaves runModelProbe nil,
	// which is the plain-`doctor` state (probe not wired, nothing spent) —
	// so every pre-existing test keeps exercising exactly that path.
	modelProbe func(path string, args []string) (string, error)
}

func (f fakeProbe) toProbe() adapterProbe {
	return adapterProbe{
		runModelProbe: f.modelProbe,
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
		getenv:    func(k string) string { return f.env[k] },
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
	if !strings.Contains(h.Remediation, "0.111.0") {
		t.Errorf("expected a remediation naming the 0.111.0 floor, got %q", h.Remediation)
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

func TestCheckAdapter_ClaudeAlias(t *testing.T) {
	fp := fakeProbe{
		paths:    map[string]string{"claude": "/opt/claude"},
		versions: map[string]string{"/opt/claude": "claude 2.1.233 (Claude Code)\n"},
	}
	// "claude" is an alias for "claude-headless".
	h := checkAdapter("claude", fp.toProbe())
	if !h.OK {
		t.Fatalf("expected claude OK, got %q", h.Remediation)
	}
	if h.Binary != "claude" {
		t.Errorf("expected binary claude, got %q", h.Binary)
	}
	if h.Version != "2.1.233" {
		t.Errorf("expected version 2.1.233, got %q", h.Version)
	}
	if !h.VersionOK {
		t.Errorf("expected 2.1.233 to meet the claude-headless floor %q", h.MinVersion)
	}
	if h.Mcp != nil {
		t.Error("expected no MCP section for claude")
	}
	// No model probe wired: this is the plain-`doctor` path, which must spend
	// nothing and therefore assess no model (#1274).
	if h.ModelOK != nil {
		t.Errorf("ModelOK = %v with no probe wired; the free path must run no model check", *h.ModelOK)
	}
}

// retentionRejectionOutput is the shape Anthropic returns when the caller's
// organization or workspace is barred from a Covered Model. Captured wording
// from the Fable 5.1 launch note (#1274).
const retentionRejectionOutput = `API Error: 400 {"type":"error","error":` +
	`{"type":"invalid_request_error","message":"To use this model, your organization or workspace ` +
	`must have data retention enabled."}}`

// TestCheckAdapter_ClaudeRetentionRejectionIsNamed is the #1274 regression
// guard. Fable 5.1 is a Covered Model: an org configured for zero data
// retention gets 400 invalid_request_error on EVERY request to it, while the
// CLI is installed, current and authenticated. Nothing about that looks like a
// bad model id, so a generic "the CLI rejected this model" sends the operator
// hunting for a typo in an id that is spelled correctly. Deleting the
// retentionRejection match makes this fall through to the generic branch and
// the remediation assertions below fail.
func TestCheckAdapter_ClaudeRetentionRejectionIsNamed(t *testing.T) {
	var probedArgs []string
	fp := fakeProbe{
		paths:    map[string]string{"claude": "/opt/claude"},
		versions: map[string]string{"/opt/claude": "claude 2.1.233 (Claude Code)\n"},
		modelProbe: func(_ string, args []string) (string, error) {
			probedArgs = args
			return retentionRejectionOutput, errors.New("exit status 1")
		},
	}
	h := checkAdapter("claude", fp.toProbe())

	// The probe asks about the registry's CURRENT fable band leader — a band,
	// not a literal, so registering a new leader re-points it automatically.
	if h.Model != "claude-fable-5-1" {
		t.Errorf("probed model = %q, want claude-fable-5-1 (the fable band leader)", h.Model)
	}
	if !strings.Contains(strings.Join(probedArgs, " "), "claude-fable-5-1") {
		t.Errorf("probe argv %v did not name claude-fable-5-1", probedArgs)
	}
	if h.ModelOK == nil || *h.ModelOK {
		t.Fatalf("ModelOK = %v, want false — the model was rejected", h.ModelOK)
	}

	// The retention remediation names BOTH ways out and says the id is fine.
	for _, want := range []string{"data-retention", "30-day data retention", "claude-opus-5", "not a bad model id"} {
		if !strings.Contains(h.Remediation, want) {
			t.Errorf("remediation %q does not name %q", h.Remediation, want)
		}
	}
	// And it must NOT be the generic "confirm the id" advice, which is wrong here.
	if strings.Contains(h.Remediation, "confirm the id is served") {
		t.Errorf("retention rejection fell through to the generic model-validity remediation: %q", h.Remediation)
	}

	// A model the org cannot use is not "this adapter cannot run a stage":
	// every other band still dispatches, so OK stays true.
	if !h.OK {
		t.Errorf("adapter OK = false; a Covered-Model retention block must not fail the whole adapter")
	}
}

// TestCheckAdapter_ClaudeModelProbeOutcomes covers the other two branches, so
// the retention match is a DISCRIMINATOR rather than a phrase that happens to
// be present: an accepted model reports ModelOK with no remediation, and an
// unrelated rejection gets the generic advice, not the retention text.
func TestCheckAdapter_ClaudeModelProbeOutcomes(t *testing.T) {
	base := func(probe func(string, []string) (string, error)) fakeProbe {
		return fakeProbe{
			paths:      map[string]string{"claude": "/opt/claude"},
			versions:   map[string]string{"/opt/claude": "claude 2.1.233 (Claude Code)\n"},
			modelProbe: probe,
		}
	}

	served := checkAdapter("claude", base(func(string, []string) (string, error) {
		return "ok\n", nil
	}).toProbe())
	if served.ModelOK == nil || !*served.ModelOK {
		t.Errorf("accepted model: ModelOK = %v, want true", served.ModelOK)
	}
	if served.Remediation != "" {
		t.Errorf("accepted model produced a remediation: %q", served.Remediation)
	}

	other := checkAdapter("claude", base(func(string, []string) (string, error) {
		return `API Error: 401 {"type":"error","error":{"type":"authentication_error"}}`, errors.New("exit status 1")
	}).toProbe())
	if other.ModelOK == nil || *other.ModelOK {
		t.Errorf("rejected model: ModelOK = %v, want false", other.ModelOK)
	}
	if !strings.Contains(other.Remediation, "confirm the id is served") {
		t.Errorf("unrelated rejection did not get the generic remediation: %q", other.Remediation)
	}
	if strings.Contains(other.Remediation, "data retention") {
		t.Errorf("unrelated rejection was misreported as a retention block: %q", other.Remediation)
	}
}

// TestCheckAdapter_ModelProbeGatedOnBaseline pins the gate: an adapter that
// already failed its baseline gets no model probe at all. Spawning a request
// through a CLI that is missing or below its version floor would spend money
// to re-report a failure already named by Installed/VersionOK.
func TestCheckAdapter_ModelProbeGatedOnBaseline(t *testing.T) {
	spawned := false
	fp := fakeProbe{
		modelProbe: func(string, []string) (string, error) {
			spawned = true
			return "", nil
		},
	}
	h := checkAdapter("claude", fp.toProbe()) // no claude on PATH
	if spawned {
		t.Error("model probe spawned for an adapter whose binary is not installed")
	}
	if h.ModelOK != nil {
		t.Errorf("ModelOK = %v for an uninstalled adapter, want nil", *h.ModelOK)
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
// claude, usable below its floor, stays OK, and a floor-less adapter is
// untouched.
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

	// claude is usable below its floor → an unknown version is reported
	// against the floor and still leaves it OK.
	claudeErr := fakeProbe{
		paths:   map[string]string{"claude": "/bin/claude"},
		verErrs: map[string]error{"/bin/claude": errors.New("boom")},
	}
	if c := checkAdapter("claude", claudeErr.toProbe()); !c.OK || c.VersionOK || !strings.Contains(c.Remediation, "unknown") {
		t.Errorf("expected claude OK with an unmet floor and an 'unknown' hint despite version error, got %+v", c)
	}

	// copilot has no floor → a version-spawn error leaves it OK and VersionOK.
	copilotErr := fakeProbe{
		paths:   map[string]string{"copilot": "/bin/copilot"},
		verErrs: map[string]error{"/bin/copilot": errors.New("boom")},
	}
	if h2 := checkAdapter("copilot", copilotErr.toProbe()); !h2.OK || !h2.VersionOK {
		t.Errorf("expected floor-less copilot OK despite version error, got %+v", h2)
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

// TestAdapterSpecConstants holds every CLI adapter's floor to its compat
// manifest (internal/adaptercompat/manifests/<adapter>.json), the single
// source. A literal put back in adapterSpecs that differs from the manifest
// turns this red.
func TestAdapterSpecConstants(t *testing.T) {
	for name, spec := range adapterSpecs {
		if spec.kind != kindCLI {
			if spec.minVersion != "" || spec.floorPolicy != "" {
				t.Errorf("%s is not a CLI adapter but has floor %q policy %q", name, spec.minVersion, spec.floorPolicy)
			}
			continue
		}
		m, ok := adaptercompat.Get(name)
		if !ok {
			t.Errorf("CLI adapter %s has no compat manifest", name)
			continue
		}
		if spec.minVersion != m.MinVersion {
			t.Errorf("%s minVersion = %q, want the manifest's min_version %q", name, spec.minVersion, m.MinVersion)
		}
		if spec.floorPolicy != m.FloorPolicy {
			t.Errorf("%s floorPolicy = %q, want the manifest's floor_policy %q", name, spec.floorPolicy, m.FloorPolicy)
		}
		if spec.binary != m.Binary {
			t.Errorf("%s binary = %q, want the manifest's binary %q", name, spec.binary, m.Binary)
		}
	}
	// The four adapters with a floor, named, so an adapter dropped from
	// adapterSpecs cannot pass the loop above by being absent.
	for _, name := range []string{"codex", "gemini", "grok", "claude-headless"} {
		m, _ := adaptercompat.Get(name)
		if m.MinVersion == "" || adapterSpecs[name].minVersion != m.MinVersion {
			t.Errorf("%s minVersion = %q, want the manifest's non-empty min_version %q",
				name, adapterSpecs[name].minVersion, m.MinVersion)
		}
		if adapterSpecs[name].floorPolicy != adaptercompat.FloorWarn {
			t.Errorf("%s floorPolicy = %q, want %q", name, adapterSpecs[name].floorPolicy, adaptercompat.FloorWarn)
		}
	}
	// Only claude stays usable below its floor. Codex, gemini and grok keep
	// the floor they had before the manifest: below it they are not usable.
	for name, spec := range adapterSpecs {
		if want := name == "claude-headless"; spec.usableBelowFloor != want {
			t.Errorf("%s usableBelowFloor = %v, want %v", name, spec.usableBelowFloor, want)
		}
	}
	if !adapterSpecs["codex"].mcp {
		t.Error("codex must be flagged as MCP-provisioning")
	}
	if len(AllAdapterNames()) != 8 {
		t.Errorf("expected 8 adapters in AllAdapterNames, got %d", len(AllAdapterNames()))
	}
}

// TestOpenCodeFloorComesFromTheManifest: the opencode row's floor, floor
// policy and max-tested version are the compat manifest's, read from it and
// never restated. The spec's fields must be the compat* calls, so a literal
// floor turns this red even while it happens to equal the manifest's.
func TestOpenCodeFloorComesFromTheManifest(t *testing.T) {
	m, ok := adaptercompat.Get("opencode")
	if !ok || m.MinVersion == "" || m.FloorPolicy != adaptercompat.FloorFailClosed {
		t.Fatalf("the opencode manifest = %+v, want a fail_closed floor", m)
	}
	spec, ok := adapterSpecs["opencode"]
	if !ok || spec.kind != kindCLI || spec.minVersion != m.MinVersion || spec.floorPolicy != m.FloorPolicy {
		t.Fatalf("opencode spec = %+v, want a cli spec on the manifest's floor %s (%s)", spec, m.MinVersion, m.FloorPolicy)
	}
	h := checkAdapter("opencode", fakeProbe{}.toProbe())
	if h.MinVersion != m.MinVersion || h.OpenCode == nil || h.OpenCode.MaxTested != m.MaxTested || h.OpenCode.FloorPolicy != m.FloorPolicy {
		t.Errorf("row floor %q, max-tested %v; want the manifest's %s and %s", h.MinVersion, h.OpenCode, m.MinVersion, m.MaxTested)
	}

	fields := openCodeSpecFields(t)
	for field, call := range map[string]string{"minVersion": "compatMinVersion", "floorPolicy": "compatFloorPolicy"} {
		expr, ok := fields[field]
		if !ok {
			t.Errorf("the opencode spec sets no %s", field)
			continue
		}
		c, isCall := expr.(*ast.CallExpr)
		fn, isIdent := func() (*ast.Ident, bool) {
			if !isCall {
				return nil, false
			}
			id, ok := c.Fun.(*ast.Ident)
			return id, ok
		}()
		if !isIdent || fn.Name != call || len(c.Args) != 1 {
			t.Errorf("the opencode spec's %s is not %s(\"opencode\"): a floor written out in adapterSpecs stops following the manifest", field, call)
			continue
		}
		if lit, ok := c.Args[0].(*ast.BasicLit); !ok || lit.Value != `"opencode"` {
			t.Errorf("the opencode spec's %s reads another adapter's manifest", field)
		}
	}
}

// openCodeSpecFields parses adapters.go and returns the fields of the
// "opencode" entry of adapterSpecs, by name.
func openCodeSpecFields(t *testing.T) map[string]ast.Expr {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "adapters.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]ast.Expr{}
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if key, ok := kv.Key.(*ast.BasicLit); !ok || key.Value != `"opencode"` {
			return true
		}
		lit, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			if f, ok := elt.(*ast.KeyValueExpr); ok {
				if name, ok := f.Key.(*ast.Ident); ok {
					fields[name.Name] = f.Value
				}
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("adapters.go has no \"opencode\" entry in adapterSpecs")
	}
	return fields
}

// TestCheckAdapter_ClaudeBelowManifestFloorWarns: claude-headless has a floor
// from its compat manifest (the oldest version a captured fixture backs), under
// the warn policy, and its spec keeps it usable below that floor. A claude
// below it is reported with a remediation naming the floor, and the adapter
// stays usable, so the doctor adds no warning for it.
func TestCheckAdapter_ClaudeBelowManifestFloorWarns(t *testing.T) {
	m, ok := adaptercompat.Get("claude-headless")
	if !ok || m.MinVersion != "2.1.223" {
		t.Fatalf("claude-headless manifest floor = %q (found %v), want 2.1.223", m.MinVersion, ok)
	}
	fp := fakeProbe{
		paths:    map[string]string{"claude": "/opt/claude"},
		versions: map[string]string{"/opt/claude": "2.1.100 (Claude Code)\n"},
	}
	h := checkAdapter("claude-headless", fp.toProbe())
	if h.Version != "2.1.100" || h.MinVersion != "2.1.223" {
		t.Errorf("version=%q min=%q, want 2.1.100 against the 2.1.223 floor", h.Version, h.MinVersion)
	}
	if h.VersionOK {
		t.Error("expected VersionOK=false: 2.1.100 is below the floor")
	}
	if !strings.Contains(h.Remediation, "2.1.223") {
		t.Errorf("remediation %q does not name the 2.1.223 floor", h.Remediation)
	}
	if !h.OK {
		t.Errorf("claude below its floor failed the adapter: %+v", h)
	}
}

// TestCheckAdapter_ClaudeBelowFloorKeepsItsProbes: a claude below its floor is
// still usable, so it still gets the deeper checks every usable claude gets:
// the catalog-skip note and the model probe (the data-retention check). The
// probe's remediation follows the floor's rather than replacing it.
func TestCheckAdapter_ClaudeBelowFloorKeepsItsProbes(t *testing.T) {
	base := func(probe func(string, []string) (string, error)) fakeProbe {
		return fakeProbe{
			paths:      map[string]string{"claude": "/opt/claude"},
			versions:   map[string]string{"/opt/claude": "2.1.100 (Claude Code)\n"},
			modelProbe: probe,
		}
	}

	served := checkAdapter("claude-headless", base(func(string, []string) (string, error) {
		return "ok\n", nil
	}).toProbe())
	if served.VersionOK || !served.OK {
		t.Fatalf("want claude 2.1.100 below its floor and usable, got %+v", served)
	}
	if served.ModelOK == nil || !*served.ModelOK {
		t.Errorf("ModelOK = %v, want true: the model probe must run below the floor", served.ModelOK)
	}
	if !strings.HasPrefix(served.CatalogWarning, "no catalog probe: ") {
		t.Errorf("CatalogWarning = %q, want the no-catalog note", served.CatalogWarning)
	}

	barred := checkAdapter("claude-headless", base(func(string, []string) (string, error) {
		return retentionRejectionOutput, errors.New("exit status 1")
	}).toProbe())
	if barred.ModelOK == nil || *barred.ModelOK {
		t.Errorf("ModelOK = %v, want false: the model was rejected", barred.ModelOK)
	}
	for _, want := range []string{"2.1.223", "30-day data retention"} {
		if !strings.Contains(barred.Remediation, want) {
			t.Errorf("remediation %q does not name %q", barred.Remediation, want)
		}
	}
}

// TestCheckAdapter_FailClosedFloorFailsTheAdapter is the other policy: below
// a fail_closed floor the adapter is not usable, even with usableBelowFloor
// set, and it gets no probe. No doctor adapter carries such a floor today, so
// a spec is registered for the test.
func TestCheckAdapter_FailClosedFloorFailsTheAdapter(t *testing.T) {
	const name = "test-fail-closed"
	adapterSpecs[name] = adapterSpec{binary: "fc", kind: kindCLI, minVersion: "1.2.3",
		floorPolicy: adaptercompat.FloorFailClosed, usableBelowFloor: true, catalogSkipReason: "test spec",
		modelProbeBand: models.BandFable, modelProbeArgs: claudeModelProbeArgs}
	t.Cleanup(func() { delete(adapterSpecs, name) })

	spawned := false
	below := fakeProbe{paths: map[string]string{"fc": "/bin/fc"}, versions: map[string]string{"/bin/fc": "fc 1.2.2"},
		modelProbe: func(string, []string) (string, error) { spawned = true; return "ok", nil }}
	h := checkAdapter(name, below.toProbe())
	if h.OK || h.VersionOK || !strings.Contains(h.Remediation, "1.2.3") {
		t.Errorf("below a fail_closed floor: want !OK, !VersionOK and the floor named, got %+v", h)
	}
	if spawned || h.ModelOK != nil || h.CatalogWarning != "" {
		t.Errorf("an adapter that is not usable was probed: spawned=%v %+v", spawned, h)
	}
	at := fakeProbe{paths: map[string]string{"fc": "/bin/fc"}, versions: map[string]string{"/bin/fc": "fc 1.2.3"}}
	if h := checkAdapter(name, at.toProbe()); !h.OK || !h.VersionOK {
		t.Errorf("at a fail_closed floor: want OK, got %+v", h)
	}
}

// TestCheckAdapters_CompatLoadFailureIsOneFailingRow: a compat manifest that
// fails to load leaves every floor unset; the doctor says so in one failing
// row carrying the loader's error, which names the manifest and the field.
func TestCheckAdapters_CompatLoadFailureIsOneFailingRow(t *testing.T) {
	loadErr := &adaptercompat.Error{File: "codex.json", Adapter: "codex", Field: "min_version",
		Msg: `"1.x" is not a semver version (MAJOR.MINOR.PATCH)`}
	orig := loadCompatManifests
	loadCompatManifests = func() error { return loadErr }
	t.Cleanup(func() { loadCompatManifests = orig })

	fp := fakeProbe{paths: map[string]string{"claude": "/b/claude"}, versions: map[string]string{"/b/claude": "claude 2.1.233"}}
	got := checkAdaptersWithProbe([]string{"claude"}, fp.toProbe())
	if len(got) != 2 {
		t.Fatalf("got %d rows, want the compat row plus claude", len(got))
	}
	row := got[0]
	if row.Adapter != compatManifestRowName || row.OK {
		t.Errorf("first row = %+v, want a failing %s row", row, compatManifestRowName)
	}
	for _, want := range []string{"codex.json", "min_version"} {
		if !strings.Contains(row.Remediation, want) {
			t.Errorf("compat row remediation %q does not name %q", row.Remediation, want)
		}
	}
	if got[1].Adapter != "claude" {
		t.Errorf("second row = %q, want claude", got[1].Adapter)
	}

	loadCompatManifests = orig
	if rows := checkAdaptersWithProbe([]string{"claude"}, fp.toProbe()); len(rows) != 1 {
		t.Errorf("the embedded manifests load, yet the doctor reported %d rows for one adapter", len(rows))
	}
}

// TestOpenCodeIsADoctorAdapter: `doctor --adapters` lists opencode, under its
// own name and through AllAdapterNames, among the CLI adapters.
func TestOpenCodeIsADoctorAdapter(t *testing.T) {
	names := AllAdapterNames()
	i := slices.Index(names, "opencode")
	if i < 0 {
		t.Fatalf("AllAdapterNames() = %v, want opencode in it", names)
	}
	if slices.Index(names, "claude-sdk") < i {
		t.Errorf("AllAdapterNames() = %v, want opencode among the CLI adapters, before the SDK ones", names)
	}
	if h := checkAdapter("opencode", fakeProbe{}.toProbe()); h.Adapter != "opencode" || h.Kind != "cli" || h.Binary != "opencode" {
		t.Errorf("row = %+v, want the opencode cli row", h)
	}
}

func TestResolveCodexHome_EnvOverride(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex/home")
	if got := resolveCodexHome(); got != "/custom/codex/home" {
		t.Errorf("expected CODEX_HOME override, got %q", got)
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

// TestCheckAIAdapterAvailable_ZeroUsable guards #862's core defect: before the
// fix, a machine with no coding agent and no API key reported
// "healthy — environment ready for pipeline operations" and exited 0, because
// the default check list never asked whether a stage could run at all.
func TestCheckAIAdapterAvailable_ZeroUsable(t *testing.T) {
	// Nothing on PATH, no API keys, no local model env.
	item, warning := checkAIAdapterAvailable(fakeProbe{}.toProbe())

	if item.OK {
		t.Fatalf("expected the check to fail with no usable adapter, got %+v", item)
	}
	if warning == "" {
		t.Error("expected a warning so the verdict degrades instead of reading healthy")
	}
	if item.Error != warning {
		t.Errorf("row error and warning should be the same text, got %q vs %q", item.Error, warning)
	}
	for _, want := range []string{"claude", "--adapters all"} {
		if !strings.Contains(item.Error, want) {
			t.Errorf("expected remediation to name %q, got %q", want, item.Error)
		}
	}
}

// TestCheckAIAdapterAvailable_OneUsableIsEnough verifies the check asks "can
// this machine run a stage", not "is every adapter installed" — one usable
// adapter passes even though the other eight are absent.
func TestCheckAIAdapterAvailable_OneUsableIsEnough(t *testing.T) {
	fp := fakeProbe{paths: map[string]string{"claude": "/opt/claude"}}

	item, warning := checkAIAdapterAvailable(fp.toProbe())

	if !item.OK {
		t.Fatalf("expected OK with claude usable, got %+v", item)
	}
	if warning != "" {
		t.Errorf("a usable adapter must add no warning, got %q", warning)
	}
	if !strings.Contains(item.Detail, "claude") {
		t.Errorf("expected the detail to name the usable adapter, got %q", item.Detail)
	}
}

// TestCheckAIAdapterAvailable_ShortCircuits guards the cost half of the fix:
// the probe must stop at the first usable adapter rather than probing all nine
// on every doctor run. `claude` is first in AllAdapterNames(), so a usable
// claude means no later adapter is looked up.
func TestCheckAIAdapterAvailable_ShortCircuits(t *testing.T) {
	var lookedUp []string
	probe := fakeProbe{paths: map[string]string{"claude": "/opt/claude"}}.toProbe()
	inner := probe.lookPath
	probe.lookPath = func(bin string) (string, error) {
		lookedUp = append(lookedUp, bin)
		return inner(bin)
	}

	if item, _ := checkAIAdapterAvailable(probe); !item.OK {
		t.Fatalf("precondition: expected claude to be usable, got %+v", item)
	}

	if len(lookedUp) != 1 || lookedUp[0] != "claude" {
		t.Errorf("expected exactly one lookup (claude), got %v", lookedUp)
	}
}
