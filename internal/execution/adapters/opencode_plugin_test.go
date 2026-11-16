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
// (the plugin needs no npm package at all, #3306).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

// writeSatisfiedOperatorDir writes the files opencode's own install check
// reads (package.json, package-lock.json naming @opencode-ai/plugin, and
// node_modules/), so opencodeplugin.OperatorInstallSatisfied reports dir
// satisfied. Test fixture only: production code never writes there.
func writeSatisfiedOperatorDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"dependencies":{"@opencode-ai/plugin":"1.18.30"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"),
		[]byte(`{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"1.18.30"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// builtOpenCodeContent is a per-run document the builder wrote, which
// addNightgaugePluginToConfig's validation accepts.
func builtOpenCodeContent(t *testing.T) string {
	t.Helper()
	built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return built.Content
}

// TestInstallNightgaugePluginRefusesAnInvalidDocument: the document OpenCode
// receives is the validated one, so a document that would lose a control on
// 2.x is refused after the plugins key is added, not dispatched.
func TestInstallNightgaugePluginRefusesAnInvalidDocument(t *testing.T) {
	for name, content := range map[string]string{
		"1.x document":    `{"model":"lmstudio/x","agent":{},"mcp":{}}`,
		"empty document":  `{}`,
		"subagent opened": strings.Replace(builtOpenCodeContent(t), `{"action":"subagent","resource":"*","effect":"deny"}]`, `{"action":"subagent","resource":"*","effect":"allow"}]`, 1),
	} {
		root := t.TempDir()
		run := &OpenCodeRun{
			ConfigContent: content,
			Env:           map[string]string{openCodeConfigContentEnvVar: content},
			PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
			RunDir:        root,
		}
		if err := InstallNightgaugePlugin(run, "", ""); err == nil {
			t.Errorf("%s: the plugin was added to a document 2.x would load with a control missing", name)
		}
		if run.Env[openCodeConfigContentEnvVar] != content {
			t.Errorf("%s: the refused document still reached OPENCODE_CONFIG_CONTENT", name)
		}
	}
}

func TestInstallNightgaugePluginAddsOnlyThePluginKey(t *testing.T) {
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}

	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81625"); err != nil {
		t.Fatal(err)
	}

	var beforeMap, afterMap map[string]any
	if err := json.Unmarshal([]byte(before), &beforeMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(run.ConfigContent), &afterMap); err != nil {
		t.Fatal(err)
	}
	delete(afterMap, "plugins")
	if len(beforeMap) != len(afterMap) {
		t.Fatalf("patched config gained keys beyond \"plugins\": before=%v after(-plugin)=%v", beforeMap, afterMap)
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
	plugins, ok := patched["plugins"].([]any)
	if !ok || len(plugins) != 1 {
		t.Fatalf("plugins key = %v, want a single-entry array", patched["plugins"])
	}
	// OpenCode 2.x loads a local plugin only from a directory (#3302).
	wantEntry := filepath.Clean(run.PluginDir)
	if plugins[0] != wantEntry {
		t.Errorf("plugin entry = %v, want the plugin directory %q", plugins[0], wantEntry)
	}
	for _, name := range []string{opencodeplugin.EntryFile, "nightgauge.js"} {
		if _, err := os.Stat(filepath.Join(wantEntry, name)); err != nil {
			t.Errorf("the plugin directory has no %s: %v", name, err)
		}
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
	root := t.TempDir()
	run := &OpenCodeRun{
		ConfigContent: builtOpenCodeContent(t),
		Env:           map[string]string{},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}
	if err := InstallNightgaugePlugin(run, "", ""); err != nil {
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
	if _, ok := patched["plugins"]; !ok {
		t.Error("the plugin key must still be present without a run identity")
	}
}

func TestInstallNightgaugePluginRejectsUnparseableConfig(t *testing.T) {
	root := t.TempDir()
	run := &OpenCodeRun{
		ConfigContent: `not json`,
		Env:           map[string]string{},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81625"); err == nil {
		t.Fatal("want an error decoding an unparseable per-run config")
	}
}

// TestInstallNightgaugePluginInstallsADirectoryWithPackageJSON (#3306 AC1/AC2):
// the config's `plugins` entry is a directory holding package.json and
// index.js, and no npm package is seeded into the run's config directory.
func TestInstallNightgaugePluginInstallsADirectoryWithPackageJSON(t *testing.T) {
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81625"); err != nil {
		t.Fatal(err)
	}
	entry := run.Env[opencodeplugin.EnvPluginPath]
	if !strings.Contains(run.ConfigContent, entry) {
		t.Fatalf("per-run config does not name the plugin directory %s: %s", entry, run.ConfigContent)
	}
	if fi, err := os.Stat(entry); err != nil || !fi.IsDir() {
		t.Fatalf("plugin entry %s is not a directory: %v", entry, err)
	}
	for _, name := range []string{"package.json", "index.js"} {
		if _, err := os.Stat(filepath.Join(entry, name)); err != nil {
			t.Errorf("plugin directory lacks %s: %v", name, err)
		}
	}
	configDir := filepath.Dir(run.PluginDir)
	if _, err := os.Stat(filepath.Join(configDir, "node_modules")); err == nil {
		t.Errorf("InstallNightgaugePlugin seeded node_modules into %s; the plugin needs no npm package", configDir)
	}
}

// TestInstallNightgaugePluginFailsWhenPluginDirIsAFile (#3306 AC4): an
// install whose entry is a regular file (the 1.x file entry) fails the
// dispatch before spawn with an IncompatibleError naming the plugin, and
// the config is left unchanged. writeNightgaugePlugin is swapped so the
// write itself succeeds: only VerifyInstalled can reject it.
func TestInstallNightgaugePluginFailsWhenPluginDirIsAFile(t *testing.T) {
	cases := map[string]func(dir string) (string, error){
		"file entry": func(dir string) (string, error) {
			f := filepath.Join(filepath.Dir(dir), "nightgauge.js")
			return f, os.WriteFile(f, []byte("export default async () => ({})\n"), 0o600)
		},
		"directory without package.json": func(dir string) (string, error) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", err
			}
			return dir, os.WriteFile(filepath.Join(dir, opencodeplugin.EntryFile), []byte("export default {}\n"), 0o600)
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			orig := writeNightgaugePlugin
			writeNightgaugePlugin = write
			t.Cleanup(func() { writeNightgaugePlugin = orig })
			root := t.TempDir()
			before := builtOpenCodeContent(t)
			run := &OpenCodeRun{
				ConfigContent: before,
				Env:           map[string]string{openCodeConfigContentEnvVar: before},
				PluginDir:     filepath.Join(root, "plugin"),
				RunDir:        root,
			}
			err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81633")
			var inc *opencodeplugin.IncompatibleError
			if !errors.As(err, &inc) {
				t.Fatalf("want an *opencodeplugin.IncompatibleError from VerifyInstalled, got %v", err)
			}
			if !strings.Contains(err.Error(), "did not install") {
				t.Errorf("error %q does not say the plugin did not install", err)
			}
			if run.ConfigContent != before {
				t.Error("the per-run config changed despite the failed install")
			}
		})
	}
}

// depsMarker is the installed @opencode-ai/plugin version marker.
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
	home := t.TempDir()
	operatorOpenCode := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCode, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81626"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(depsMarker(operatorOpenCode)); err == nil {
		t.Errorf("InstallNightgaugePlugin wrote @opencode-ai/plugin into the operator's %s; it must never write into an operator-owned directory", operatorOpenCode)
	}
	if entries, err := os.ReadDir(operatorOpenCode); err != nil || len(entries) != 1 || entries[0].Name() != "bin" {
		t.Errorf("$HOME/.opencode changed: entries=%v err=%v, want only the pre-existing bin/", entries, err)
	}
	if got := run.OperatorInstallRisk; got != operatorOpenCode {
		t.Errorf("run.OperatorInstallRisk = %q, want %q (a pre-existing, unsatisfied $HOME/.opencode)", got, operatorOpenCode)
	} // #1802: the marker is the manager's alone. It never enters the run's
	// Env, which is both the child's environment and the config verb's
	// printed env.
	if got, ok := run.Env["NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK"]; ok {
		t.Errorf("run.Env carries NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK = %q; the install-risk marker must reach only the manager", got)
	}
	for k, v := range run.Env {
		if v == operatorOpenCode {
			t.Errorf("run.Env[%s] = %q names the operator's flagged directory; it must reach only the manager", k, v)
		}
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
	home := t.TempDir()
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81627"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".opencode")); err == nil {
		t.Error("InstallNightgaugePlugin created $HOME/.opencode, which did not exist before it ran")
	}
	if got := run.OperatorInstallRisk; got != "" {
		t.Errorf("run.OperatorInstallRisk = %q, want \"\": an absent $HOME/.opencode is not a risk", got)
	}
}

// TestInstallNightgaugePluginNeverWritesInheritedConfigDir (#1635/A11 round
// 6, narrowed AC1): under opencode.inherit_user_config the run's env carries
// OPENCODE_CONFIG_DIR at the operator's own XDG OpenCode config directory —
// Nightgauge never seeds or merges into it either, present or absent, and
// never creates an absent one. The run's env still carries the risk marker,
// since opencode itself creates and installs into this one unconditionally.
func TestInstallNightgaugePluginNeverWritesInheritedConfigDir(t *testing.T) {
	// A path under a real temp directory that this test never creates: the
	// production shape of "opencode.inherit_user_config is on, and the
	// operator has never run opencode with XDG_CONFIG_HOME set this way
	// before".
	operatorConfig := filepath.Join(t.TempDir(), "opencode")
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env: map[string]string{
			openCodeConfigContentEnvVar: before,
			"OPENCODE_CONFIG_DIR":       operatorConfig,
		},
		PluginDir: filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:    root,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81628"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(operatorConfig); err == nil {
		t.Errorf("InstallNightgaugePlugin created the absent inherited %s, which it must never do", operatorConfig)
	}
	if got := run.OperatorInstallRisk; got != operatorConfig {
		t.Errorf("run.OperatorInstallRisk = %q, want %q (opencode creates and installs into OPENCODE_CONFIG_DIR itself)", got, operatorConfig)
	}
}

// TestInstallNightgaugePluginDoesNotRiskASatisfiedOperatorDir (#1635/A11
// round 8, ADR-022 amendment 2026-09-15, correcting round 7): a
// $HOME/.opencode already holding what opencode 1.18.30's own "is
// @opencode-ai/plugin already installed" check requires (not only the
// version marker) is NOT flagged as a risk. Round 7
// flagged a satisfied directory too, on a re-measurement that seeded only
// the version marker and found the wait unchanged; driven directly against
// the real 1.18.30 binary with the files this test seeds
// (exactly the node_modules/, package.json and package-lock.json that
// OperatorInstallSatisfied reads), a satisfied
// operator directory DOES get OpenCode's local, instant fast path (~1s for
// `debug config`), the same as a run's own XDG-resolved config directory —
// round 7's premise does not hold once the real predicate is what is
// actually checked and seeded. Flagging (and so arming the watchdog for) a
// directory that never waits on the registry at all would leave narrowed
// AC1's online case indistinguishable from a genuine install wait.
func TestInstallNightgaugePluginDoesNotRiskASatisfiedOperatorDir(t *testing.T) {
	home := t.TempDir()
	operatorOpenCode := filepath.Join(home, ".opencode")
	writeSatisfiedOperatorDir(t, operatorOpenCode)
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81631"); err != nil {
		t.Fatal(err)
	}
	if got := run.OperatorInstallRisk; got != "" {
		t.Errorf("run.OperatorInstallRisk = %q, want \"\": a $HOME/.opencode already satisfying opencode's own check gets the local, instant fast path, so the watchdog must not be armed for it", got)
	}
}

// TestInstallNightgaugePluginStillRisksAnUnsatisfiedOperatorDir: a
// $HOME/.opencode that exists but holds only the version marker — which
// opencode's own check does not read at all — is still flagged as a risk,
// exactly like one holding nothing at all (#1635/A11 round 8: only a
// directory OperatorInstallSatisfied actually reports satisfied is exempt).
func TestInstallNightgaugePluginStillRisksAnUnsatisfiedOperatorDir(t *testing.T) {
	home := t.TempDir()
	operatorOpenCode := filepath.Join(home, ".opencode")
	markerDir := filepath.Join(operatorOpenCode, "node_modules", "@opencode-ai", "plugin")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "package.json"),
		[]byte(`{"name":"@opencode-ai/plugin","version":"1.18.30"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	before := builtOpenCodeContent(t)
	run := &OpenCodeRun{
		ConfigContent: before,
		Env:           map[string]string{openCodeConfigContentEnvVar: before},
		PluginDir:     filepath.Join(root, "config", "opencode", "plugin"),
		RunDir:        root,
		Home:          home,
	}
	if err := InstallNightgaugePlugin(run, "", "01890a5d-ac96-774b-bcce-b30209a81632"); err != nil {
		t.Fatal(err)
	}
	if got := run.OperatorInstallRisk; got != operatorOpenCode {
		t.Errorf("run.OperatorInstallRisk = %q, want %q: the version marker alone does not satisfy opencode's own install check, so the watchdog must still be armed for it", got, operatorOpenCode)
	}
}
