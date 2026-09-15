package adapters

// Unit coverage for InstallNightgaugePlugin (#1635): the Go-side wiring that
// writes the embedded Nightgauge OpenCode plugin into a run's PluginDir and
// adds it to the per-run config. OPENCODE_DISABLE_PROJECT_CONFIG=1 IS set
// here (#1635 fix round, maintainer decision on the round-2 review, ADR-022
// amendment 2026-09-14): AC2 requires it, on every spawn, so neither a
// target repository's `.opencode/plugins/*` nor its own `plugin[]` entries
// ever load beside the embedded Nightgauge plugin. The cost — a target
// repository's own opencode.json no longer merges into the resolved config
// at all, not only its plugin entries, because 1.18.30 offers no
// finer-grained switch — is paid and recorded there, not hidden; see
// TestOpenCodeIntegrationPerRunConfigReachesOpenCode and
// TestOpenCodeIntegrationAnthropicBlockHoldsItsServer in
// internal/execution/opencode_isolation_integration_test.go for the
// behaviour this now asserts instead.
//
// The real-binary regression this package's opencode_integration suite
// (plugin_integration_test.go's TestPluginLoadsOnRealOpenCode, and
// internal/execution's TestOpenCodeIntegration* suite) now covers: the
// embedded tree loads via the real 1.18.30 loader (not only the Node harness
// substitution plugin_test.go's own suite uses) and a dispatch no longer
// waits on the real npm registry on every run — nor, since the #1635 fix
// round's second pass, does it ever install a live npm package at all
// (opencode_plugin_deps.go's pluginDependencySeeder extracts an embedded,
// version-pinned copy instead).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

// stubPluginDependencySeeder replaces pluginDependencySeeder with a no-op
// for the duration of a test: these are unit tests of the config-patching
// logic, not of the dependency seed (opencodeplugin's own deps_test.go
// covers that), and a stub keeps them from paying even the real, entirely
// local extraction's cost.
func stubPluginDependencySeeder(t *testing.T) {
	t.Helper()
	prev := pluginDependencySeeder
	pluginDependencySeeder = func(context.Context, string) error { return nil }
	t.Cleanup(func() { pluginDependencySeeder = prev })
}

func TestInstallNightgaugePluginAddsOnlyThePluginKey(t *testing.T) {
	stubPluginDependencySeeder(t)
	root := t.TempDir()
	before := `{"model":"lmstudio/x","agent":{},"mcp":{}}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}

	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81625"); err != nil {
		t.Fatal(err)
	}

	var beforeMap, afterMap map[string]any
	if err := json.Unmarshal([]byte(before), &beforeMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(run.ConfigContent), &afterMap); err != nil {
		t.Fatal(err)
	}
	delete(afterMap, "plugin")
	if len(beforeMap) != len(afterMap) {
		t.Fatalf("patched config gained keys beyond \"plugin\": before=%v after(-plugin)=%v", beforeMap, afterMap)
	}
	for k, v := range beforeMap {
		bv, _ := json.Marshal(v)
		av, _ := json.Marshal(afterMap[k])
		if string(bv) != string(av) {
			t.Errorf("key %q changed: before=%s after=%s", k, bv, av)
		}
	}

	var patched map[string]any
	if err := json.Unmarshal([]byte(run.ConfigContent), &patched); err != nil {
		t.Fatal(err)
	}
	plugins, ok := patched["plugin"].([]any)
	if !ok || len(plugins) != 1 {
		t.Fatalf("plugin key = %v, want a single-entry array", patched["plugin"])
	}
	wantEntry := filepath.Join(run.PluginDir, opencodeplugin.EntryFile)
	if plugins[0] != wantEntry {
		t.Errorf("plugin entry = %v, want %q", plugins[0], wantEntry)
	}
	if _, err := os.Stat(wantEntry); err != nil {
		t.Errorf("the plugin entry file was not written: %v", err)
	}
	if run.Env[openCodeConfigContentEnvVar] != run.ConfigContent {
		t.Error("run.Env[OPENCODE_CONFIG_CONTENT] must be re-patched alongside run.ConfigContent")
	}
	if run.Env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Error("OPENCODE_DISABLE_PROJECT_CONFIG must be \"1\": AC2 requires it on every spawn so a target repository's .opencode/plugins/* and plugin[] never load beside the embedded plugin (ADR-022 amendment 2026-09-14)")
	}
	if run.Env[opencodeplugin.EnvPluginPath] != wantEntry {
		t.Errorf("env[%s] = %q, want %q", opencodeplugin.EnvPluginPath, run.Env[opencodeplugin.EnvPluginPath], wantEntry)
	}
	if run.Env[opencodeplugin.EnvNonce] == "" {
		t.Error("a run with a run identity must get a handshake nonce")
	}
	if run.Env[opencodeplugin.EnvSentinel] == "" {
		t.Error("a run with a run identity must get a handshake sentinel path")
	}
}

func TestInstallNightgaugePluginWithoutRunIDSkipsHandshake(t *testing.T) {
	stubPluginDependencySeeder(t)
	root := t.TempDir()
	run := &OpenCodeRun{
		ConfigContent: `{}`,
		Env:           map[string]string{},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", ""); err != nil {
		t.Fatal(err)
	}
	if run.Env[opencodeplugin.EnvNonce] != "" || run.Env[opencodeplugin.EnvSentinel] != "" {
		t.Error("no run identity (the SDK config-print path, which never spawns opencode) must mint no handshake")
	}
	// The plugin is still written and referenced even without a live run to
	// verify: `nightgauge opencode config` still prints the bytes a real
	// dispatch would use.
	var patched map[string]any
	if err := json.Unmarshal([]byte(run.ConfigContent), &patched); err != nil {
		t.Fatal(err)
	}
	if _, ok := patched["plugin"]; !ok {
		t.Error("the plugin key must still be present without a run identity")
	}
}

func TestInstallNightgaugePluginRejectsUnparseableConfig(t *testing.T) {
	stubPluginDependencySeeder(t)
	root := t.TempDir()
	run := &OpenCodeRun{
		ConfigContent: `not json`,
		Env:           map[string]string{},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81625"); err == nil {
		t.Fatal("want an error decoding an unparseable per-run config")
	}
}

// TestInstallNightgaugePluginSeedsEmbeddedDependencies wires
// InstallNightgaugePlugin end to end through the REAL pluginDependencySeeder
// (no stub): the run's OpenCode config directory must end up holding the
// embedded, version-pinned @opencode-ai/plugin copy
// opencodeplugin.WriteDependencies extracts, with no npm binary and no
// network reachable — PATH is emptied and the npm registry variable points
// at an address nothing answers, exactly like a dispatch on a machine with
// no npm at all. Weakening seedPluginDependencies back to shelling out to
// npm turns this red for the same reason
// opencodeplugin.TestWriteDependenciesIsPureExtraction does.
func TestInstallNightgaugePluginSeedsEmbeddedDependencies(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("npm_config_registry", "http://192.0.2.1:1/")
	root := t.TempDir()
	before := `{"model":"lmstudio/x"}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81625"); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Dir(run.PluginDir)
	if _, err := os.Stat(filepath.Join(configDir, "node_modules", "@opencode-ai", "plugin", "package.json")); err != nil {
		t.Errorf("InstallNightgaugePlugin did not seed @opencode-ai/plugin into %s: %v", configDir, err)
	}
}

// depsMarker is the file WriteDependencies' embedded copy always creates,
// checked below in place of enumerating the whole extracted tree.
func depsMarker(dir string) string {
	return filepath.Join(dir, "node_modules", "@opencode-ai", "plugin", "package.json")
}

// TestInstallNightgaugePluginNeverWritesOperatorHomeOpenCode (#1635/A11
// round 6, ADR-022 amendment 2026-09-15, narrowed AC1): Nightgauge never
// seeds or merges anything into $HOME/.opencode, whether or not it already
// exists — an earlier round did, and a review found the seed it fell back to
// permanently broke an operator's own `import ... from "@opencode-ai/plugin"`.
// A pre-existing $HOME/.opencode (the layout the official install script
// leaves: bin/, no node_modules yet) is left exactly as it is, and the run's
// env carries the risk marker naming it, so the manager can bound and
// classify a stage that waits on OpenCode's own install there instead.
func TestInstallNightgaugePluginNeverWritesOperatorHomeOpenCode(t *testing.T) {
	stubPluginDependencySeeder(t)
	home := t.TempDir()
	operatorOpenCode := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCode, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	before := `{"model":"lmstudio/x"}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81626"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(depsMarker(operatorOpenCode)); err == nil {
		t.Errorf("InstallNightgaugePlugin wrote @opencode-ai/plugin into the operator's %s; it must never write into an operator-owned directory", operatorOpenCode)
	}
	if entries, err := os.ReadDir(operatorOpenCode); err != nil || len(entries) != 1 || entries[0].Name() != "bin" {
		t.Errorf("$HOME/.opencode changed: entries=%v err=%v, want only the pre-existing bin/", entries, err)
	}
	if got := run.Env[opencodeplugin.EnvOperatorInstallRisk]; got != operatorOpenCode {
		t.Errorf("run.Env[%s] = %q, want %q (a pre-existing, unsatisfied $HOME/.opencode)", opencodeplugin.EnvOperatorInstallRisk, got, operatorOpenCode)
	}
}

// TestInstallNightgaugePluginNeverCreatesOperatorHomeOpenCode: a machine with
// no $HOME/.opencode at all must stay that way — writing into it would
// create a directory 1.18.30 itself would then start treating as one of its
// config directories, which InstallNightgaugePlugin must never do as a side
// effect of a pipeline dispatch. No risk is flagged either: opencode never
// creates $HOME/.opencode on its own, so an absent one is not this
// dispatch's problem.
func TestInstallNightgaugePluginNeverCreatesOperatorHomeOpenCode(t *testing.T) {
	stubPluginDependencySeeder(t)
	home := t.TempDir()
	root := t.TempDir()
	before := `{"model":"lmstudio/x"}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81627"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".opencode")); err == nil {
		t.Error("InstallNightgaugePlugin created $HOME/.opencode, which did not exist before it ran")
	}
	if got := run.Env[opencodeplugin.EnvOperatorInstallRisk]; got != "" {
		t.Errorf("run.Env[%s] = %q, want \"\": an absent $HOME/.opencode is not a risk", opencodeplugin.EnvOperatorInstallRisk, got)
	}
}

// TestInstallNightgaugePluginNeverWritesInheritedConfigDir (#1635/A11 round
// 6, narrowed AC1): under opencode.inherit_user_config the run's env carries
// OPENCODE_CONFIG_DIR at the operator's own XDG OpenCode config directory —
// Nightgauge never seeds or merges into it either, present or absent, and
// never creates an absent one. The run's env still carries the risk marker,
// since opencode itself creates and installs into this one unconditionally.
func TestInstallNightgaugePluginNeverWritesInheritedConfigDir(t *testing.T) {
	stubPluginDependencySeeder(t)
	// A path under a real temp directory that this test never creates: the
	// production shape of "opencode.inherit_user_config is on, and the
	// operator has never run opencode with XDG_CONFIG_HOME set this way
	// before".
	operatorConfig := filepath.Join(t.TempDir(), "opencode")
	root := t.TempDir()
	before := `{"model":"lmstudio/x"}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env: map[string]string{
			openCodeConfigContentEnvVar: before,
			"OPENCODE_CONFIG_DIR":       operatorConfig,
		},
		PluginDir: filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:    root,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81628"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(operatorConfig); err == nil {
		t.Errorf("InstallNightgaugePlugin created the absent inherited %s, which it must never do", operatorConfig)
	}
	if got := run.Env[opencodeplugin.EnvOperatorInstallRisk]; got != operatorConfig {
		t.Errorf("run.Env[%s] = %q, want %q (opencode creates and installs into OPENCODE_CONFIG_DIR itself)", opencodeplugin.EnvOperatorInstallRisk, got, operatorConfig)
	}
}

// TestInstallNightgaugePluginDoesNotRiskASatisfiedOperatorDir (#1635/A11
// round 8, ADR-022 amendment 2026-09-15, correcting round 7): a
// $HOME/.opencode already holding the FULL set opencode 1.18.30's own "is
// @opencode-ai/plugin already installed" check reads (not only the version
// marker — depsdata/README.md's table) is NOT flagged as a risk. Round 7
// flagged a satisfied directory too, on a re-measurement that seeded only
// the version marker and found the wait unchanged; driven directly against
// the real 1.18.30 binary with the full four-file set this test seeds, a
// satisfied operator directory DOES get OpenCode's local, instant fast path
// (~1s for `debug config`), the same as a run's own XDG-resolved config
// directory — round 7's premise does not hold once the full set is what is
// actually checked and seeded. Flagging (and so arming the watchdog for) a
// directory that never waits on the registry at all would leave narrowed
// AC1's online case indistinguishable from a genuine install wait.
func TestInstallNightgaugePluginDoesNotRiskASatisfiedOperatorDir(t *testing.T) {
	stubPluginDependencySeeder(t)
	home := t.TempDir()
	operatorOpenCode := filepath.Join(home, ".opencode")
	// The full four-file set OperatorInstallSatisfied checks — exactly what
	// WriteDependencies extracts for a run's own directory, used here only as
	// a test fixture for an operator-owned one (production code never writes
	// there; see opencode_plugin_deps.go's operatorInstallRisk doc comment).
	if err := opencodeplugin.WriteDependencies(operatorOpenCode); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	before := `{"model":"lmstudio/x"}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81631"); err != nil {
		t.Fatal(err)
	}
	if got := run.Env[opencodeplugin.EnvOperatorInstallRisk]; got != "" {
		t.Errorf("run.Env[%s] = %q, want \"\": a $HOME/.opencode already holding the full set opencode's own check reads gets the local, instant fast path, so the watchdog must not be armed for it", opencodeplugin.EnvOperatorInstallRisk, got)
	}
}

// TestInstallNightgaugePluginStillRisksAnUnsatisfiedOperatorDir: a
// $HOME/.opencode that exists but holds only the version marker — not the
// full set opencode's own check reads — is still flagged as a risk, exactly
// like one holding nothing at all (#1635/A11 round 8: only a directory
// OperatorInstallSatisfied's full-set check actually passes is exempt).
func TestInstallNightgaugePluginStillRisksAnUnsatisfiedOperatorDir(t *testing.T) {
	stubPluginDependencySeeder(t)
	home := t.TempDir()
	operatorOpenCode := filepath.Join(home, ".opencode")
	markerDir := filepath.Join(operatorOpenCode, "node_modules", "@opencode-ai", "plugin")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "package.json"),
		[]byte(`{"name":"@opencode-ai/plugin","version":"`+opencodeplugin.DepsVersion+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	before := `{"model":"lmstudio/x"}`
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(context.Background(), run, "", "01890a5d-ac96-774b-bcce-b30209a81632"); err != nil {
		t.Fatal(err)
	}
	if got := run.Env[opencodeplugin.EnvOperatorInstallRisk]; got != operatorOpenCode {
		t.Errorf("run.Env[%s] = %q, want %q: the version marker alone does not satisfy opencode's own install check, so the watchdog must still be armed for it", opencodeplugin.EnvOperatorInstallRisk, got, operatorOpenCode)
	}
}
