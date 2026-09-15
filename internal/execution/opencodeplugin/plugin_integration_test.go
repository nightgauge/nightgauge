//go:build opencode_integration

package opencodeplugin

// TestPluginLoadsOnRealOpenCode drives the embedded plugin tree through the
// real opencode 1.18.30 binary's own loader (#1635 fix round), not the
// Node-harness substitution the rest of this package's tests use. The
// harness in plugin_test.go imports nightgauge.js directly
// (`mod.NightgaugePlugin || mod.default`) and calls it as a plain function,
// which is exactly what 1.18.30's loader does NOT do: on a module whose
// default export is not itself a function, it walks every named export via
// Object.values(module) and throws "Plugin export is not a function" on the
// first one that is neither a function nor a {server} object. Exporting the
// version/hooks constants (`export const NIGHTGAUGE_PLUGIN_VERSION`,
// `export const NIGHTGAUGE_HOOK_NAMES`) put exactly such values in that
// iteration, so the real loader rejected the plugin while every Go-side and
// Node-harness test kept passing.
//
//	go test -tags opencode_integration ./internal/execution/opencodeplugin/ -run TestPluginLoadsOnRealOpenCode -count=1 -v
//
// opencode's own loader installs @opencode-ai/plugin the moment any `plugin`
// entry is non-empty, 1.18.30, independent of this plugin's own imports.
// TestPluginLoadsOnRealOpenCode seeds its XDG config directory with the same
// embedded, version-pinned copy (WriteDependencies) InstallNightgaugePlugin
// seeds a real run with, and points npm at a closed port besides, so this
// makes no registry request of its own (#1635 fix round finding 4, the
// maintainer decision that no test may) — the run isolation integration
// suite (internal/execution/opencode_isolation_integration_test.go) does the
// same for the adapter path. Only whether the real loader accepts the
// plugin, and writes its sentinel, is left to succeed or fail on its own.
import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pluginIntegrationOpenCodeVersion = "1.18.30"

func realOpenCodeForPluginTest(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("opencode")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("opencode is not on PATH, and CI must run this case: install opencode-ai@%s", pluginIntegrationOpenCodeVersion)
		}
		t.Skip("opencode is not on PATH")
	}
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	if v := strings.TrimSpace(string(out)); v != pluginIntegrationOpenCodeVersion {
		t.Fatalf("opencode %s is installed; this case was observed on %s", v, pluginIntegrationOpenCodeVersion)
	}
	return path
}

// pluginIntegrationNoRegistry points npm, which 1.18.30 would otherwise run
// to install @opencode-ai/plugin the moment a resolved config's `plugin`
// array is non-empty, at a loopback port nothing listens on: a
// belt-and-suspenders check on top of the WriteDependencies seed below, not
// what makes this test offline-safe by itself (#1635 fix round finding 4;
// mirrors internal/execution's openCodeNoRegistry).
const pluginIntegrationNoRegistry = "npm_config_registry=http://127.0.0.1:9/"

// TestPluginLoadsOnRealOpenCode: the embedded plugin tree, referenced from a
// per-run config's `plugin` array exactly as InstallNightgaugePlugin builds
// it, loads under the real 1.18.30 binary with no "Plugin export is not a
// function" error, and the handshake sentinel it writes carries this run's
// nonce and PluginVersion — the same two facts the Go-side manager verifies
// (VerifyLoaded) in production. Backing out the nightgauge.js fix (restoring
// the two `export const` keywords) turns this red: 1.18.30 logs the loader
// error to stderr and no sentinel is ever written.
//
// The XDG config directory is seeded with WriteDependencies before opencode
// ever reads it, and npm is pointed at a closed port, so this needs no real
// npm registry access — the maintainer decision on #1635 that no test may
// make one (#1635 fix round finding 4). Skipping the seed call turns THIS
// red on a machine with no registry access: opencode's own install then
// waits out the closed port before returning, well past the 60s the SDK path
// TestOpenCodeConfigVerbMatchesTheAdapter et al. budget for a dispatch.
func TestPluginLoadsOnRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)

	home := t.TempDir()
	xdgConfigHome := filepath.Join(home, ".config")
	pluginDir := filepath.Join(xdgConfigHome, "opencode", "nightgauge-plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDependencies(filepath.Join(xdgConfigHome, "opencode")); err != nil {
		t.Fatal(err)
	}

	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(t.TempDir(), "sentinel.json")

	configContent, err := json.Marshal(map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"plugin":  []string{entry},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, "debug", "config")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		"XDG_CONFIG_HOME=" + xdgConfigHome,
		"XDG_DATA_HOME=" + filepath.Join(home, ".data"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".state"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
		"OPENCODE_CONFIG_CONTENT=" + string(configContent),
		pluginIntegrationNoRegistry,
		EnvNonce + "=" + nonce,
		EnvSentinel + "=" + sentinelPath,
		EnvPluginPath + "=" + entry,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode debug config: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "Plugin export is not a function") {
		t.Fatalf("1.18.30's real loader rejected the embedded plugin:\n%s", out)
	}

	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("the plugin did not write the handshake sentinel at %s (the real loader likely rejected it): %v\noutput:\n%s", sentinelPath, err, out)
	}
	var s Sentinel
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("the sentinel did not parse as JSON: %v (%s)", err, data)
	}
	if s.Nonce != nonce {
		t.Errorf("sentinel nonce = %q, want this run's %q", s.Nonce, nonce)
	}
	if s.PluginVersion != PluginVersion {
		t.Errorf("sentinel plugin_version = %q, want %q", s.PluginVersion, PluginVersion)
	}
}

// TestNightgaugeNeverWritesAnOperatorToolDirectory (#1635/A11 round 6,
// ADR-022 amendment 2026-09-15, narrowed AC1): an operator-owned OpenCode
// config directory holding a custom tool that imports @opencode-ai/plugin —
// OpenCode's own documented way to write one
// (`import { tool } from "@opencode-ai/plugin"`) — is never touched by
// Nightgauge at all: no WriteDependencies (run-directory-only by contract),
// no MergeDependencies (removed this round), nothing. Round 5's merge tried
// to keep such a directory both "satisfied" and import-resolvable at once
// and a review found it fragile; round 6 does not attempt that trade at
// all — the directory is exactly as the operator left it before and after a
// dispatch touches a config naming it, proven here with no opencode spawn
// and so no registry request, real or stubbed.
func TestNightgaugeNeverWritesAnOperatorToolDirectory(t *testing.T) {
	home := t.TempDir()
	xdgConfigHome := filepath.Join(home, ".config")
	configDir := filepath.Join(xdgConfigHome, "opencode")
	toolDir := filepath.Join(configDir, "tool")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolSrc := "import { tool } from \"@opencode-ai/plugin\"\n" +
		"export default tool({\n" +
		"  description: \"hello\",\n" +
		"  args: {},\n" +
		"  async execute() { return \"hi\" },\n" +
		"})\n"
	toolPath := filepath.Join(toolDir, "hello.ts")
	if err := os.WriteFile(toolPath, []byte(toolSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}

	// The one thing InstallNightgaugePlugin's caller (adapters) does with
	// this kind of directory is read whether it already satisfies the pin —
	// never write it, whatever the answer.
	if OperatorInstallSatisfied(configDir) {
		t.Fatal("test premise broken: a directory with no node_modules at all must not read as satisfied")
	}

	after, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the operator's own tool file changed:\nbefore=%s\nafter=%s", before, after)
	}
	if entries, err := os.ReadDir(configDir); err != nil || len(entries) != 1 || entries[0].Name() != "tool" {
		t.Errorf("the operator directory gained or lost entries: entries=%v err=%v, want only the pre-existing tool/", entries, err)
	}
}
