//go:build opencode_integration || canary

package execution

// Run isolation (ADR-022 § 8, § 22) observed against the real opencode binary,
// pinned to the version the observations were made on:
//
//	go test -tags opencode_integration ./internal/execution/ -run OpenCodeIntegration -count=1
//
// Each case dispatches through Manager.RunStage, so the environment under test
// is the one the manager composes for a real stage. A shim named opencode, first
// on PATH, runs the real binary's `debug paths`, `debug config` and, after the
// stage's own `run`, `session list`, all in that environment. Every other
// call, such as the usage fold's `--version` after the stage, goes straight to
// the real binary, so it cannot overwrite what the stage's environment
// resolved. The stage names a model the catalog does not list under a hosted
// provider, so the run exits 1 before any model request, and every opencode
// call runs with a throwaway HOME and npm pointed at a closed loopback port
// as a belt-and-suspenders check: InstallNightgaugePlugin now seeds
// @opencode-ai/plugin from an embedded, version-pinned copy
// (opencodeplugin.WriteDependencies, ADR-022 amendment 2026-09-14), so
// OpenCode's own install of it finds an already-satisfied node_modules tree
// and makes no request of its own — the closed port proves that, rather than
// being what stops a live install from reaching one. The operator's real
// config is never read either way. With CI=true a missing binary fails every
// case rather than skipping it.
//
// TestOpenCodeIntegrationAbsentInheritedConfigDirOffline and
// TestOpenCodeIntegrationHomeDirBinOnly are the exception: they dispatch
// with an operator-owned OpenCode directory ($HOME/.opencode, or
// OPENCODE_CONFIG_DIR under opencode.inherit_user_config) in play.
// Nightgauge never seeds or merges into either (#1635/A11 round 6, ADR-022
// amendment 2026-09-15, narrowed AC1), so those two expect the dispatch to
// fail, bounded by the manager's operator-install-risk watchdog
// (openCodeOperatorInstallWaitBound, shortened for the test) rather than by
// npm's own registry retry/backoff.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	"github.com/nightgauge/nightgauge/internal/state"
)

// openCodeIntegrationVersion is the version every assertion here was observed
// on. A different binary fails rather than skips: its behaviour is unverified,
// and ADR-022 § 20 re-captures the evidence before max-tested moves.
const openCodeIntegrationVersion = "1.18.30"

// openCodeIntegrationModel names a model OpenCode's bundled catalog does not
// list under a hosted provider it knows, so the per-run config is built and a
// run fails with the model not found before it sends any request, whatever
// credential the environment holds. A provider key OpenCode does not know is
// refused before spawn (BuildOpenCodeConfig), so it would spawn nothing.
const openCodeIntegrationModel = "deepseek/nightgauge-no-such-model"

// openCodeInheritConfig is the reference machine-tier config with the opt-in
// into the operator's own OpenCode config (ADR-022 § 8).
const openCodeInheritConfig = openCodeMachineConfig + "  inherit_user_config: true\n"

// openCodeInheritNotice is the stderr line an opted-in dispatch prints.
const openCodeInheritNotice = "opencode.inherit_user_config is on: this dispatch also reads your own OpenCode config"

// openCodeNoRegistry points npm, which OpenCode would otherwise run to
// install plugin dependencies, at a loopback port nothing listens on: a
// belt-and-suspenders check on top of InstallNightgaugePlugin's embedded
// dependency seed, not what makes these tests offline-safe by itself. Every
// opencode call here carries it, so none can reach a public registry.
const openCodeNoRegistry = "npm_config_registry=http://127.0.0.1:9/"

// nightgaugeCanaryEnv, set by scripts/adapter-canary.sh's cmd_opencode_canary
// (never by the opencode_integration suite), relaxes realOpenCode's exact
// version pin: the whole point of the #1639 canary leg is to exercise
// whatever npm's `latest` dist-tag resolves to today, which is almost never
// openCodeIntegrationVersion. Without this, the daily run can only ever
// report "a newer version exists" against the wrong (pinned) version label
// and never actually drive the newest release through the stream contract.
const nightgaugeCanaryEnv = "NIGHTGAUGE_CANARY"

// realOpenCode resolves the opencode binary before any shim shadows it and
// checks its version under a throwaway HOME. On CI a missing binary fails the
// case: CI installs the pinned version to run these cases. Under the canary
// build (nightgaugeCanaryEnv=true), any resolvable MAJOR.MINOR.PATCH is
// accepted instead of exactly openCodeIntegrationVersion; the
// opencode_integration suite (nightgaugeCanaryEnv unset) always enforces the
// exact pin, because THAT suite's assertions were observed on that version.
func realOpenCode(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("opencode")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("opencode is not on PATH, and CI must run these cases: install opencode-ai@%s", openCodeIntegrationVersion)
		}
		t.Skip("opencode is not on PATH")
	}
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", openCodeNoRegistry}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	v := strings.TrimSpace(string(out))
	if os.Getenv(nightgaugeCanaryEnv) == "true" {
		if !openCodeVersionRE.MatchString(v) {
			t.Fatalf("opencode --version printed %q, not a MAJOR.MINOR.PATCH version", v)
		}
		if v != openCodeIntegrationVersion {
			t.Logf("opencode %s is installed (canary: pin relaxed from the %s baseline observed for ADR-022)", v, openCodeIntegrationVersion)
		}
		return path
	}
	if v != openCodeIntegrationVersion {
		t.Fatalf("opencode %s is installed; these cases were observed on %s. Re-verify ADR-022's observations before changing the pin", v, openCodeIntegrationVersion)
	}
	return path
}

// realOpenCodePinHelperEnv marks a subprocess as
// TestRealOpenCodePinRelaxedUnderCanaryHelper's own invocation, so running
// the whole suite normally never runs it standalone.
const realOpenCodePinHelperEnv = "NG_REALOPENCODE_PIN_HELPER"

// TestRealOpenCodePinRelaxedUnderCanaryHelper does nothing but call
// realOpenCode and let it Fatal or not; TestRealOpenCodePinRelaxedUnderCanary
// re-execs the test binary onto just this test (the standard
// os/exec-style helper-process pattern) so it can assert on the exit code of
// a REAL t.Fatalf, something no amount of t.Run bookkeeping can do without
// also failing the outer test.
func TestRealOpenCodePinRelaxedUnderCanaryHelper(t *testing.T) {
	if os.Getenv(realOpenCodePinHelperEnv) != "1" {
		t.Skip("only runs as TestRealOpenCodePinRelaxedUnderCanary's subprocess")
	}
	realOpenCode(t)
}

// TestRealOpenCodePinRelaxedUnderCanary is the regression test for
// nightgaugeCanaryEnv: without it, realOpenCode failed on any opencode
// version other than openCodeIntegrationVersion, so the #1639 daily canary
// — which installs npm's `latest` dist-tag, almost never that pinned version
// — could never actually drive the newest release through the live stream,
// permission and bad-model legs; it could only fail on the pin itself. With
// it set, a different (but still valid) version is accepted; the
// opencode_integration suite (which never sets it) keeps the exact pin.
func TestRealOpenCodePinRelaxedUnderCanary(t *testing.T) {
	fake := t.TempDir()
	script := filepath.Join(fake, "opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(canary bool) (passed bool, output []byte) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestRealOpenCodePinRelaxedUnderCanaryHelper$", "-test.v")
		// Filter any inherited NIGHTGAUGE_CANARY out of the base environment
		// before deciding whether to set it: this test's own outer process can
		// carry it (scripts/adapter-canary.sh's cmd_opencode_canary sets it for
		// the whole `go test -tags canary` invocation this file's package now
		// runs under), and without filtering, run(false) would inherit
		// "true" and wrongly pass the "pin still holds" case it exists to
		// prove.
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, nightgaugeCanaryEnv+"=") {
				env = append(env, kv)
			}
		}
		env = append(env,
			realOpenCodePinHelperEnv+"=1",
			"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		)
		if canary {
			env = append(env, nightgaugeCanaryEnv+"=true")
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return err == nil, out
	}

	if passed, out := run(false); passed {
		t.Errorf("realOpenCode accepted opencode 9.9.9 with %s unset; the opencode_integration suite's exact pin must still hold:\n%s", nightgaugeCanaryEnv, out)
	}
	if passed, out := run(true); !passed {
		t.Errorf("realOpenCode rejected opencode 9.9.9 with %s=true; the canary leg needs the pin relaxed to drive npm's latest dist-tag:\n%s", nightgaugeCanaryEnv, out)
	}
}

// openCodeShim installs the shim and returns the directory it writes to. It
// records only around the stage's own `run`; any other call is the real
// binary's.
func openCodeShim(t *testing.T, real string) string {
	t.Helper()
	return openCodeShimWithRegistry(t, real, openCodeNoRegistry)
}

// openCodeShimWithRegistry is openCodeShim parameterized on the
// `npm_config_registry=...` line the shim's own script exports: most tests
// want the shared closed-port belt-and-suspenders check (openCodeShim), but
// a test proving no registry request is ever sent — as opposed to merely
// proving the process does not hang — needs a real listener it can count
// connections on instead (#1635 fix round 2).
func openCodeShimWithRegistry(t *testing.T, real, registryEnv string) string {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
export %[3]s
[ "$1" = run ] || exec "%[1]s" "$@"
"%[1]s" debug paths < /dev/null > "%[2]s/paths.txt" 2>&1
"%[1]s" debug config < /dev/null > "%[2]s/config.json" 2> "%[2]s/config.err"
"%[1]s" "$@"
code=$?
"%[1]s" session list < /dev/null > "%[2]s/sessions.txt" 2>&1
exit $code
`, real, out, registryEnv)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}

// localNPMRegistry starts a loopback TCP listener standing in for the npm
// registry and returns the `npm_config_registry=...` line to export it as,
// plus an accessor for how many connections it has accepted so far. Unlike
// openCodeNoRegistry's closed port, a real listener lets a test assert "no
// request was ever sent" positively — zero connections — rather than only
// "the process did not hang forever", which a closed port that happens to
// fail fast for an unrelated reason could satisfy without proving anything.
func localNPMRegistry(t *testing.T) (registryEnv string, connections func() int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var count int32
	done := make(chan struct{})
	t.Cleanup(func() {
		ln.Close()
		<-done
	})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&count, 1)
			conn.Close()
		}
	}()
	return fmt.Sprintf("npm_config_registry=http://%s/", ln.Addr().String()), func() int32 {
		return atomic.LoadInt32(&count)
	}
}

// runOpenCodeIntegrationStage dispatches one stage with no run identity, so
// its root is minted and gone when RunStage returns.
func runOpenCodeIntegrationStage(t *testing.T) (*adapters.RunResult, string, error) {
	t.Helper()
	return runOpenCodeIntegrationStageWithTimeout(t, 120*time.Second)
}

// runOpenCodeIntegrationStageWithTimeout is runOpenCodeIntegrationStage with
// a caller-chosen timeout, for a dispatch whose config touches an
// operator-owned OpenCode directory that does NOT already satisfy the pin,
// which legitimately waits on OpenCode's own real install rather than
// getting a local, instant fast path (#1635/A11 round 6, corrected round 8:
// only an UNSATISFIED $HOME/.opencode or OPENCODE_CONFIG_DIR pays that wait;
// a directory seeded with the full four-file set gets the same fast path a
// run's own XDG-resolved config directory always did — see
// seedOperatorInstallSatisfied's own doc comment). The two CI-required
// opt-in success tests (TestOpenCodeIntegrationInheritUserConfigOptIn,
// TestOpenCodeIntegrationHomeDotOpenCode) seed their operator directories
// satisfied and so no longer need a raised timeout; the two offline tests
// below (TestOpenCodeIntegrationAbsentInheritedConfigDirOffline,
// TestOpenCodeIntegrationHomeDirBinOnly) deliberately leave their operator
// directory unsatisfied and use withShortOperatorInstallWaitBoundForRealBinary
// instead, to prove the bound rather than wait it out.
func runOpenCodeIntegrationStageWithTimeout(t *testing.T, timeout time.Duration) (*adapters.RunResult, string, error) {
	t.Helper()
	var result *adapters.RunResult
	var err error
	t.Setenv("DEEPSEEK_API_KEY", "") // the dispatched provider's own key stays out
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		opts := openCodeStageOptions(openCodeIntegrationModel, nil)
		opts.Timeout = timeout
		result, err = NewManager(openCodeWorkspace(t), adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	return result, stderr, err
}

// operatorOpenCode runs the real binary as the operator would, with the same
// throwaway HOME and none of the run's variables.
func operatorOpenCode(t *testing.T, real, home string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, args...)
	cmd.Dir = t.TempDir()
	cmd.Stdin = nil
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_AUTOUPDATE=1", openCodeNoRegistry}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode %s as the operator: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// writeOperatorOpenCodeConfig writes the operator's global opencode.json in
// their XDG config directory: an agent and an MCP server, and, with plugin, a
// local plugin. A plugin entry is left out where the operator's own OpenCode
// has to load the file: observed on 1.18.30, it then waits on a dependency
// install that has no network to reach.
func writeOperatorOpenCodeConfig(t *testing.T, home string, plugin bool) {
	t.Helper()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{"operator-fixture-mcp": map[string]any{
			"type": "local", "command": []string{"/usr/bin/false"}, "enabled": false,
		}},
		"agent": map[string]any{"operator-fixture-agent": map[string]any{
			"description": "operator fixture agent", "prompt": "operator fixture prompt", "mode": "subagent",
		}},
	}
	if plugin {
		js := filepath.Join(home, "operator-fixture-plugin.js")
		if err := os.WriteFile(js, []byte("export const OperatorFixturePlugin = async () => ({})\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["plugin"] = []string{"file://" + js}
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedOperatorInstallSatisfied writes dir's own FULL four-file set —
// package.json, package-lock.json, node_modules/.package-lock.json, and
// node_modules/@opencode-ai/plugin/package.json naming opencodeplugin.DepsVersion
// — via opencodeplugin.WriteDependencies. That set satisfies both opencode
// 1.18.30's own "is @opencode-ai/plugin already installed" check and
// opencodeplugin.OperatorInstallSatisfied (which reads only a subset of it —
// node_modules, package.json's dependency names and package-lock.json's own
// root package entry, never the hidden lockfile or the version marker; see
// its doc comment). TEST FIXTURE state standing in for "the operator already
// ran opencode themselves and it installed the pinned version", never
// anything Nightgauge's own production code writes (#1635/A11 round 6,
// ADR-022 amendment 2026-09-15: Nightgauge never seeds or merges into an
// operator-owned directory). Without this, a test that opts a real dispatch
// into an operator-owned directory (opencode.inherit_user_config, or a
// fixture under $HOME/.opencode) hits the SAME operator-install-risk wait
// the offline tests above exist to prove is bounded — which is real,
// correct behaviour, but is not what THESE tests are about: they prove the
// opt-in itself layers the operator's agent/MCP config back in, once
// OpenCode's own install is already satisfied, exactly as it would be for
// an operator who has used OpenCode before. Seeding only the version marker
// (round 6/7's fixture) does NOT satisfy opencode's own check.
func seedOperatorInstallSatisfied(t *testing.T, dir string) {
	t.Helper()
	if err := opencodeplugin.WriteDependencies(dir); err != nil {
		t.Fatal(err)
	}
}

// debugPaths parses `opencode debug paths` into name → path.
func debugPaths(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	paths := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			paths[fields[0]] = fields[1]
		}
	}
	if len(paths) < 5 {
		t.Fatalf("`opencode debug paths` printed no paths:\n%s", raw)
	}
	return paths
}

func readShimFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("the shim did not write %s, so the stage never ran: %v", name, err)
	}
	return raw
}

// TestOpenCodeIntegrationIsolatesTheRun: under the environment a stage runs
// in, `opencode debug paths` resolves config, data, cache and state inside
// the run's root (home stays); the operator's global opencode.json, with an
// agent, an MCP server and a plugin, is absent from `opencode debug config`;
// and the stage's session is in the run's own session list and never in the
// operator's.
func TestOpenCodeIntegrationIsolatesTheRun(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	// The fixture is config OpenCode loads: the operator's own OpenCode sees
	// its agent and MCP server.
	writeOperatorOpenCodeConfig(t, home, false)
	control := operatorOpenCode(t, real, home, "debug", "config")
	for _, want := range []string{"operator-fixture-agent", "operator-fixture-mcp"} {
		if !strings.Contains(control, want) {
			t.Fatalf("the operator's own OpenCode does not load the fixture's %s, so its absence below would prove nothing:\n%s", want, control)
		}
	}
	writeOperatorOpenCodeConfig(t, home, true)

	out := openCodeShim(t, real)
	result, _, err := runOpenCodeIntegrationStage(t)
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("the stage exited %d; a model the catalog does not list exits 1", result.ExitCode)
	}

	runs := filepath.Join(home, ".nightgauge", "opencode", "runs") + string(os.PathSeparator)
	paths := debugPaths(t, readShimFile(t, out, "paths.txt"))
	for _, name := range []string{"config", "data", "cache", "state"} {
		if !strings.HasPrefix(paths[name], runs) {
			t.Errorf("debug paths %s = %s, want it inside a run root under %s", name, paths[name], runs)
		}
	}
	if paths["home"] != home {
		t.Errorf("debug paths home = %s, want the unmoved %s", paths["home"], home)
	}

	config := string(readShimFile(t, out, "config.json"))
	if !strings.HasPrefix(strings.TrimSpace(config), "{") {
		t.Fatalf("`opencode debug config` printed no config:\n%s\n%s", config, readShimFile(t, out, "config.err"))
	}
	for _, leak := range []string{"operator-fixture-agent", "operator-fixture-mcp", "operator-fixture-plugin"} {
		if strings.Contains(config, leak) {
			t.Errorf("the run's resolved config holds the operator's %s", leak)
		}
	}

	if sessions := string(readShimFile(t, out, "sessions.txt")); !strings.Contains(sessions, "ses_") {
		t.Fatalf("the run's own session list does not show the stage's session, so the operator's proves nothing:\n%s", sessions)
	}
	writeOperatorOpenCodeConfig(t, home, false) // the operator's OpenCode must not wait on the plugin
	if theirs := operatorOpenCode(t, real, home, "session", "list"); strings.Contains(theirs, "ses_") {
		t.Errorf("the stage's session is in the operator's own session list:\n%s", theirs)
	}
	if left, _ := os.ReadDir(runs); len(left) != 0 {
		t.Errorf("the run root survived its dispatch: %d entries in %s", len(left), runs)
	}
}

// TestOpenCodeIntegrationPluginLoadsExactlyOnce (#1635 fix round finding 2):
// the run's resolved `opencode debug config` names the Nightgauge plugin file
// exactly once. 1.18.30 auto-loads every file directly under a
// "plugin"/"plugins" directory of an OpenCode config directory IN ADDITION TO
// whatever the config's own `plugin` array names; PluginDir used to be named
// exactly "plugin" (the auto-scanned name), and the per-run config also
// listed the same file in `plugin`, so it loaded twice — every hook in it,
// careful-gate included, fired twice per tool call. Reverting PluginDir's
// name back to "plugin" turns this red: `opencode debug config` lists the
// file once as a bare path (the config array entry) and once more prefixed
// "file://" (the auto-scan), so the count below is 2, not 1.
func TestOpenCodeIntegrationPluginLoadsExactlyOnce(t *testing.T) {
	real := realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	out := openCodeShim(t, real)
	if _, _, err := runOpenCodeIntegrationStage(t); err != nil {
		t.Fatalf("RunStage: %v", err)
	}

	config := string(readShimFile(t, out, "config.json"))
	if !strings.HasPrefix(strings.TrimSpace(config), "{") {
		t.Fatalf("`opencode debug config` printed no config:\n%s\n%s", config, readShimFile(t, out, "config.err"))
	}
	var resolved struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(config), &resolved); err != nil {
		t.Fatalf("the resolved config is not JSON: %v\n%s", err, config)
	}
	loaded := map[string]int{}
	for _, entry := range resolved.Plugin {
		loaded[strings.TrimPrefix(entry, "file://")]++
	}
	nightgauge := 0
	for path, n := range loaded {
		if strings.HasSuffix(path, "nightgauge.js") {
			nightgauge += n
		}
	}
	if nightgauge != 1 {
		t.Errorf("the resolved config's plugin array names the Nightgauge plugin %d time(s), want exactly 1: %v", nightgauge, resolved.Plugin)
	}
}

// TestOpenCodeIntegrationInheritUserConfigOptIn: with
// opencode.inherit_user_config on in the machine tier, the operator's config is
// layered back into the run, its agent and MCP server appear in `opencode
// debug config`, one stderr line says so, and the run's data still lives in
// its own root, so stored logins stay out.
func TestOpenCodeIntegrationInheritUserConfigOptIn(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeInheritConfig)
	writeOperatorOpenCodeConfig(t, home, false)
	// Test fixture only: stands in for "the operator already ran opencode
	// themselves" (#1635/A11 round 6). Seeded with the FULL four-file set
	// (seedOperatorInstallSatisfied), this DOES shorten this test's own
	// runtime to a few seconds — round 8 (ADR-022 amendment 2026-09-15)
	// found, driven directly against the pinned binary, that OpenCode
	// 1.18.30's "already installed" fast path applies to OPENCODE_CONFIG_DIR
	// exactly as it does to a run's own XDG-resolved config directory, once
	// the full set is what is actually seeded rather than only the version
	// marker (round 6/7's narrower fixture, which did not get the fast
	// path).
	seedOperatorInstallSatisfied(t, filepath.Join(home, ".config", "opencode"))

	out := openCodeShim(t, real)
	started := time.Now()
	result, stderr, err := runOpenCodeIntegrationStage(t)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	// A satisfied OPENCODE_CONFIG_DIR must never arm the operator-install-risk
	// watchdog (#1635/A11 round 8): this dispatch completes on the fast path,
	// not by the watchdog's own bound expiring and the CLI being killed.
	if result != nil && result.ExitCode == -1 {
		t.Error("ExitCode = -1: the dispatch was killed rather than completing on OpenCode's own fast path")
	}
	if strings.Contains(stderr, "adapter_incompatible") || strings.Contains(stderr, "may be waiting on an unreachable registry") {
		t.Errorf("stderr carries an install-risk/adapter_incompatible marker for a directory seeded satisfied:\n%s", stderr)
	}
	if elapsed > 20*time.Second {
		t.Errorf("RunStage took %s; a satisfied OPENCODE_CONFIG_DIR should get OpenCode's own fast path (observed ~4.5s for a whole dispatch on the pinned binary), not the ~70-80s an install wait takes", elapsed)
	}
	config := string(readShimFile(t, out, "config.json"))
	for _, want := range []string{"operator-fixture-agent", "operator-fixture-mcp"} {
		if !strings.Contains(config, want) {
			t.Errorf("with the opt-in, the run's config does not hold the operator's %s:\n%s", want, config)
		}
	}
	if n := strings.Count(stderr, openCodeInheritNotice); n != 1 {
		t.Errorf("the opt-in was announced %d times on stderr, want once:\n%s", n, stderr)
	}
	runs := filepath.Join(home, ".nightgauge", "opencode", "runs") + string(os.PathSeparator)
	if data := debugPaths(t, readShimFile(t, out, "paths.txt"))["data"]; !strings.HasPrefix(data, runs) {
		t.Errorf("with the opt-in the data directory moved to %s; it stays in the run root", data)
	}
}

// TestOpenCodeIntegrationHomeDotOpenCode: OpenCode reads ~/.opencode as a
// config directory whatever the XDG variables say, so an agent there reaches
// a run's config even in the run's environment. That is why an enabled
// dispatch is refused while ~/.opencode holds config, before anything is
// spawned, and why the operator's opt-in lets it through with the agent loaded.
func TestOpenCodeIntegrationHomeDotOpenCode(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	agentDir := filepath.Join(home, ".opencode", "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "home-dotdir-agent.md"),
		[]byte("---\ndescription: from ~/.opencode\nmode: subagent\n---\nhome dot-dir agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The premise: with all four XDG directories elsewhere, and project
	// config disabled, the agent still loads.
	root := t.TempDir()
	cmd := exec.Command(real, "debug", "config")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "OPENCODE_DISABLE_MODELS_FETCH=1", openCodeNoRegistry}
	for _, x := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		cmd.Env = append(cmd.Env, "XDG_"+x+"_HOME="+filepath.Join(root, strings.ToLower(x)))
	}
	premise, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode debug config with the XDG directories moved: %v\n%s", err, premise)
	}
	if !strings.Contains(string(premise), "home-dotdir-agent") {
		t.Fatalf("opencode %s no longer reads ~/.opencode in a run's environment; revisit the refusal in ADR-022 § 8:\n%s", openCodeIntegrationVersion, premise)
	}

	out := openCodeShim(t, real)
	_, _, err = runOpenCodeIntegrationStage(t)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, ".opencode")) {
		t.Fatalf("RunStage with an agent in ~/.opencode = %v; want a refusal naming it", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "paths.txt")); statErr == nil {
		t.Error("the refused dispatch spawned opencode")
	}

	writeOpenCodeMachineConfig(t, openCodeInheritConfig)
	// Test fixture only, standing in for "the operator already ran opencode
	// themselves" (#1635/A11 round 6) — with the full set this DOES shorten
	// this dispatch's own runtime; see
	// TestOpenCodeIntegrationInheritUserConfigOptIn's own comment on why.
	// inherit_user_config also puts OPENCODE_CONFIG_DIR (the operator's own
	// ~/.config/opencode) in play, independent of ~/.opencode, so both are
	// seeded here.
	seedOperatorInstallSatisfied(t, filepath.Join(home, ".opencode"))
	seedOperatorInstallSatisfied(t, filepath.Join(home, ".config", "opencode"))
	started := time.Now()
	result, stderr, err := runOpenCodeIntegrationStage(t)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("RunStage with the opt-in: %v", err)
	}
	if result != nil && result.ExitCode == -1 {
		t.Error("ExitCode = -1: the dispatch was killed rather than completing on OpenCode's own fast path")
	}
	if strings.Contains(stderr, "adapter_incompatible") || strings.Contains(stderr, "may be waiting on an unreachable registry") {
		t.Errorf("stderr carries an install-risk/adapter_incompatible marker for directories seeded satisfied:\n%s", stderr)
	}
	// This test seeds two operator-owned directories (~/.opencode and
	// ~/.config/opencode) and runs a preceding refused dispatch before the
	// timed section starts, so its fast-path wall clock runs measurably
	// higher under CI's shared-runner load than the single-directory cases
	// above (observed 23.7s in CI vs ~5-6s locally). 35s keeps a wide margin
	// below the ~70-80s slow path this assertion exists to catch, while
	// giving that CI variance headroom the tighter 20s bound in the
	// single-directory tests does not need.
	if elapsed > 35*time.Second {
		t.Errorf("RunStage took %s; both operator directories were seeded satisfied and should get OpenCode's own fast path, not the ~70-80s an install wait takes", elapsed)
	}
	if config := string(readShimFile(t, out, "config.json")); !strings.Contains(config, "home-dotdir-agent") {
		t.Errorf("with the opt-in the ~/.opencode agent is not in the run's config:\n%s", config)
	}
}

// depsMarkerPath is the file WriteDependencies' embedded copy always creates
// in the run's own OpenCode config directory, checked below in place of
// enumerating the whole extracted tree.
func depsMarkerPath(dir string) string {
	return filepath.Join(dir, "node_modules", "@opencode-ai", "plugin", "package.json")
}

// openCodeOfflineWallClockCap bounds the two tests below at the shortened
// openCodeOperatorInstallWaitBound this file sets for them, comfortably
// below the multi-minute registry retry/backoff wait ADR-022's amendment
// records (71s and 146.88s observed) a regression back to an UNBOUNDED wait
// would reproduce.
const openCodeOfflineWallClockCap = 20 * time.Second

// withShortOperatorInstallWaitBoundForRealBinary shortens
// openCodeOperatorInstallWaitBound for a real-binary test in this file, so
// it proves the bound without waiting out the production-sized one (or,
// pre-fix, npm's own multi-minute retry/backoff).
func withShortOperatorInstallWaitBoundForRealBinary(t *testing.T, bound time.Duration) {
	t.Helper()
	prev := openCodeOperatorInstallWaitBound
	openCodeOperatorInstallWaitBound = bound
	t.Cleanup(func() { openCodeOperatorInstallWaitBound = prev })
}

// TestOpenCodeIntegrationAbsentInheritedConfigDirOffline (#1635/A11 round 6,
// ADR-022 amendment 2026-09-15, narrowed AC1): opencode.inherit_user_config
// is on and OPENCODE_CONFIG_DIR (the operator's own ~/.config/opencode here)
// does not exist yet — the shape a machine that has never run opencode with
// this HOME has. Nightgauge never creates or seeds it (an earlier round did;
// narrowed AC1 removes that): the real binary's own install waits on the
// registry stand-in, and the dispatch fails, bounded by the shortened
// watchdog rather than by npm's own multi-minute retry/backoff, classified
// adapter_incompatible and naming the directory. Deleting the manager's
// operator-install-risk watchdog turns this red: the dispatch then waits out
// this test's own registry listener well past openCodeOfflineWallClockCap,
// bounded only by RunStage's own 120s context timeout, with no
// adapter_incompatible marker at all.
func TestOpenCodeIntegrationAbsentInheritedConfigDirOffline(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeInheritConfig)
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode")); err == nil {
		t.Fatal("test premise broken: ~/.config/opencode already exists")
	}
	withShortOperatorInstallWaitBoundForRealBinary(t, 6*time.Second)

	registryEnv, _ := localNPMRegistry(t)
	openCodeShimWithRegistry(t, real, registryEnv)

	start := time.Now()
	result, stderr, err := runOpenCodeIntegrationStage(t)
	elapsed := time.Since(start)
	if elapsed > openCodeOfflineWallClockCap {
		t.Errorf("RunStage took %s, want under %s: an absent, unsatisfied OPENCODE_CONFIG_DIR must be bounded by the watchdog, not by npm's own retry/backoff", elapsed, openCodeOfflineWallClockCap)
	}
	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if !strings.Contains(combined, "adapter_incompatible") {
		t.Errorf("stderr/result carries no adapter_incompatible marker:\nerr=%v\nstderr=%s", err, combined)
	}
	if !strings.Contains(combined, filepath.Join(home, ".config", "opencode")) {
		t.Errorf("stderr/result does not name the operator-owned OPENCODE_CONFIG_DIR:\nstderr=%s", combined)
	}
	// Deliberately no assertion that OPENCODE_CONFIG_DIR stays absent:
	// opencode 1.18.30 creates it itself the moment a config naming it
	// resolves, whether or not the install that follows ever completes —
	// exactly the operator's own environment narrowed AC1 accepts. Only
	// Nightgauge's own @opencode-ai/plugin write is forbidden there.
	if _, err := os.Stat(depsMarkerPath(filepath.Join(home, ".config", "opencode"))); err == nil {
		t.Error("Nightgauge must never write @opencode-ai/plugin into the operator-owned OPENCODE_CONFIG_DIR")
	}
}

// TestOpenCodeIntegrationHomeDirBinOnly (#1635/A11 round 6, ADR-022
// amendment 2026-09-15, narrowed AC1): $HOME/.opencode exists but holds only
// bin/ — the shape opencode's own official install script leaves before any
// config file is ever written there. Nightgauge never writes into it (an
// earlier round merged into it; narrowed AC1 removes that): the dispatch
// waits on the real binary's own install, bounded by the shortened watchdog,
// and fails classified rather than hanging or silently succeeding with a
// merge Nightgauge no longer performs.
func TestOpenCodeIntegrationHomeDirBinOnly(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCodeDir := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCodeDir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	withShortOperatorInstallWaitBoundForRealBinary(t, 6*time.Second)

	registryEnv, _ := localNPMRegistry(t)
	openCodeShimWithRegistry(t, real, registryEnv)

	start := time.Now()
	result, stderr, err := runOpenCodeIntegrationStage(t)
	elapsed := time.Since(start)
	if elapsed > openCodeOfflineWallClockCap {
		t.Errorf("RunStage took %s, want under %s: a bin/-only ~/.opencode must be bounded by the watchdog, not by npm's own retry/backoff", elapsed, openCodeOfflineWallClockCap)
	}
	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if !strings.Contains(combined, "adapter_incompatible") {
		t.Errorf("stderr/result carries no adapter_incompatible marker:\nerr=%v\nstderr=%s", err, combined)
	}
	if !strings.Contains(combined, operatorOpenCodeDir) {
		t.Errorf("stderr/result does not name the operator-owned %s:\nstderr=%s", operatorOpenCodeDir, combined)
	}
	if _, err := os.Stat(depsMarkerPath(operatorOpenCodeDir)); err == nil {
		t.Error("Nightgauge must never write @opencode-ai/plugin into an operator-owned $HOME/.opencode")
	}
	if _, err := os.Stat(filepath.Join(operatorOpenCodeDir, "bin")); err != nil {
		t.Errorf("the pre-existing bin/ directory is gone: %v", err)
	}
}

// TestOpenCodeIntegrationPerRunConfigReachesOpenCode: the per-run config is
// what the real binary resolves in the environment a stage runs in. A shim
// runs `opencode debug config` and `opencode models` there instead of the
// stage, so no request is sent. The endpoint's base URL, which is in no
// variable, resolves from the private file the config refers to; the limits,
// the steps cap, the pinned models and the locked keys are all in the
// resolved config; and a config file in the run's own XDG directory cannot
// change them: not with a limit.input of their own, which would lift the
// compaction threshold, not with a steps count of its own on an agent
// InstallNightgaugePlugin's config already locks.
//
// The repository's own opencode.json does not merge in AT ALL, locked keys
// or not: InstallNightgaugePlugin sets OPENCODE_DISABLE_PROJECT_CONFIG=1 on
// every OpenCode dispatch (AC2, ADR-022 amendment 2026-09-14), and 1.18.30
// offers no finer switch than "every project config file this repository
// holds, including .opencode/plugins/* and plugin[]" — so proving a
// dispatched stage cannot see anything the repository's file sets is exactly
// what proves the isolation AC2 requires, until #1638 builds the Go-side
// merge that lets non-plugin repository customisation back in.
func TestOpenCodeIntegrationPerRunConfigReachesOpenCode(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	// A closed loopback port: nothing may answer even if the shim ran a stage.
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "127.0.0.1:1234", "127.0.0.1:9", 1))
	out := openCodeDebugConfigShim(t, real)

	const runID = "01890a5d-ac96-774b-bcce-b30209a81625"
	// A file layer below the per-run config tries to lift every locked key.
	xdgConfig := filepath.Join(home, ".nightgauge", "opencode", "runs", runID, "config", "opencode")
	if err := os.MkdirAll(xdgConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	below := `{"share":"auto","small_model":"opencode/free-model","enabled_providers":["lmstudio","opencode"],` +
		`"provider":{"lmstudio":{"options":{"baseURL":"http://127.0.0.1:8/v1"},"models":{"qwen/qwen3.8-27b":{"limit":{"input":99999999,"context":0,"output":0}}}}},` +
		`"agent":{"build":{"steps":9999}}}`
	if err := os.WriteFile(filepath.Join(xdgConfig, "opencode.json"), []byte(below), 0o600); err != nil {
		t.Fatal(err)
	}
	// The repository's opencode.json tries the mode entries and its own
	// agent — and, with OPENCODE_DISABLE_PROJECT_CONFIG=1 set, must not
	// reach the resolved config at all (asserted below by repo-fixture-agent
	// being ABSENT, not present).
	workspace := openCodeWorkspace(t)
	repo := `{"agent":{"repo-fixture-agent":{"description":"repository fixture agent","prompt":"x","mode":"subagent"}},` +
		`"provider":{"lmstudio":{"models":{"qwen/qwen3.8-27b":{"id":"repo-chosen-model","provider":{"npm":"@ai-sdk/anthropic"},"limit":{"input":99999999,"context":1,"output":1}}}}},` +
		`"mode":{"title":{"disable":false},"compaction":{"model":"lmstudio/other-model"},"summary":{"model":"lmstudio/other-model"},` +
		`"plan":{"steps":99999},"build":{"steps":99999,"model":"lmstudio/other-model"}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".nightgauge", "worktrees", "nightgauge-issue-1612", "opencode.json"), []byte(repo), 0o644); err != nil {
		t.Fatal(err)
	}

	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", state.NewRuntimeState("nightgauge/nightgauge", 1625, "item-1625", runID))
		opts.MaxTurns = 7
		opts.Timeout = 120 * time.Second
		if _, err := NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts); err != nil {
			t.Fatalf("RunStage: %v", err)
		}
	})
	raw := readShimFile(t, out, "config.json")
	var cfg struct {
		Share            string   `json:"share"`
		Autoupdate       bool     `json:"autoupdate"`
		SmallModel       string   `json:"small_model"`
		EnabledProviders []string `json:"enabled_providers"`
		Provider         map[string]struct {
			Options map[string]any `json:"options"`
			Models  map[string]struct {
				Limit struct{ Context, Input, Output int } `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
		Agent map[string]struct {
			Model   string `json:"model"`
			Steps   int    `json:"steps"`
			Disable bool   `json:"disable"`
		} `json:"agent"`
		Compaction struct {
			Auto     bool `json:"auto"`
			Reserved int  `json:"reserved"`
		} `json:"compaction"`
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("`opencode debug config` in the stage's environment printed no config: %v\n%s\n%s", err, raw, readShimFile(t, out, "config.err"))
	}
	// AC2: only the embedded Nightgauge plugin ever appears, on a live
	// dispatch through the adapter — not a config the test built by hand.
	// 1.18.30's `debug config` echoes the one entry the per-run config set
	// twice (its own normalisation of the same path alongside the literal
	// value); every entry must still name only the Nightgauge plugin.
	if len(cfg.Plugin) == 0 {
		t.Errorf("resolved plugin array is empty, want the embedded Nightgauge plugin")
	}
	for _, p := range cfg.Plugin {
		if !strings.Contains(p, "nightgauge.js") {
			t.Errorf("resolved plugin array = %q, want every entry to name only the embedded Nightgauge plugin", cfg.Plugin)
			break
		}
	}
	if _, ok := cfg.Agent["repo-fixture-agent"]; ok {
		t.Fatalf("the repository's opencode.json loaded despite OPENCODE_DISABLE_PROJECT_CONFIG=1: AC2 requires a target repository's project config never to reach a dispatch:\n%s", raw)
	}
	lm := cfg.Provider["lmstudio"]
	if lm.Options["baseURL"] != "http://127.0.0.1:9/v1" {
		t.Errorf("baseURL resolved to %v; want the machine-tier base_url, read from the run's file", lm.Options["baseURL"])
	}
	if l := lm.Models["qwen/qwen3.8-27b"].Limit; l.Context != 131072 || l.Input != 131072 || l.Output != 8192 {
		t.Errorf("limit = %+v, want context and input 131072 and output 8192", l)
	}
	if cfg.Compaction.Reserved != 8192 {
		t.Errorf("compaction.reserved = %d, want 8192, so the threshold is limit.context less limit.output", cfg.Compaction.Reserved)
	}
	const model = "lmstudio/qwen/qwen3.8-27b"
	for name, want := range map[string]struct {
		steps   int
		disable bool
	}{"build": {7, false}, "plan": {7, false}, "general": {7, false}, "title": {0, true}, "summary": {0, false}, "compaction": {0, false}} {
		got := cfg.Agent[name]
		if got.Model != model || got.Steps != want.steps || got.Disable != want.disable {
			t.Errorf("agent.%s = %+v; want model %s, steps %d, disable %v", name, got, model, want.steps, want.disable)
		}
	}
	if cfg.Share != "disabled" || cfg.Autoupdate || cfg.SmallModel != model ||
		len(cfg.EnabledProviders) != 1 || cfg.EnabledProviders[0] != "lmstudio" || !cfg.Compaction.Auto {
		t.Errorf("a locked key did not hold: share %q, autoupdate %v, small_model %q, enabled_providers %v, compaction.auto %v",
			cfg.Share, cfg.Autoupdate, cfg.SmallModel, cfg.EnabledProviders, cfg.Compaction.Auto)
	}

	// What OpenCode sends: the model it resolves from the merged config.
	served := openCodeResolvedModels(t, readShimFile(t, out, "models.txt"))[model]
	if served.API.ID != "qwen/qwen3.8-27b" || served.API.NPM != "@ai-sdk/openai-compatible" {
		t.Errorf("the dispatched model resolved to api.id %q, api.npm %q; want the dispatched qwen/qwen3.8-27b on @ai-sdk/openai-compatible, whatever the repository's model entry says",
			served.API.ID, served.API.NPM)
	}
	if l := served.Limit; l.Context != 131072 || l.Input != 131072 || l.Output != 8192 {
		t.Errorf("the dispatched model resolved to limit %+v, want context and input 131072 and output 8192", l)
	}
}

// openCodeResolvedModel is one model as `opencode models <provider> --verbose`
// prints it: what OpenCode resolved from its catalog and the merged config.
type openCodeResolvedModel struct {
	API struct {
		ID  string `json:"id"`
		NPM string `json:"npm"`
	} `json:"api"`
	Limit struct{ Context, Input, Output int } `json:"limit"`
}

// openCodeResolvedModels parses `opencode models <provider> --verbose`: each
// model's "<provider>/<model>" line, then its JSON object, whose closing brace
// is the only line that is exactly "}".
func openCodeResolvedModels(t *testing.T, raw []byte) map[string]openCodeResolvedModel {
	t.Helper()
	models := map[string]openCodeResolvedModel{}
	lines := strings.Split(string(raw), "\n")
	for i := 0; i+1 < len(lines); i++ {
		if lines[i+1] != "{" {
			continue
		}
		name := strings.TrimSpace(lines[i])
		end := i + 1
		for end < len(lines) && lines[end] != "}" {
			end++
		}
		var m openCodeResolvedModel
		if err := json.Unmarshal([]byte(strings.Join(lines[i+1:end+1], "\n")), &m); err != nil {
			t.Fatalf("`opencode models --verbose` printed %s in a form this parser does not know: %v", name, err)
		}
		models[name] = m
		i = end
	}
	if len(models) == 0 {
		t.Fatalf("`opencode models --verbose` printed no model:\n%s", raw)
	}
	return models
}

// TestOpenCodeIntegrationAnthropicBlockHoldsItsServer (ADR-022 § 17): a
// repository opencode.json that gives anthropic a baseURL and SDK package of
// its own cannot send ANTHROPIC_API_KEY anywhere but Anthropic's API, and a
// model entry of its own that maps the dispatched model to another model and
// another SDK package changes neither what OpenCode sends nor the package
// that gets the key — because, with OPENCODE_DISABLE_PROJECT_CONFIG=1 set on
// every OpenCode dispatch (AC2, ADR-022 amendment 2026-09-14), the
// repository's file never merges into the resolved config at all, so there
// is nothing there for the per-run config to out-rank. A shim runs `opencode
// debug config` and `opencode models` in the stage's environment instead of
// the stage, so no request is sent.
func TestOpenCodeIntegrationAnthropicBlockHoldsItsServer(t *testing.T) {
	real := realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	t.Setenv("ANTHROPIC_API_KEY", "fake-anthropic-credential-1625")
	out := openCodeDebugConfigShim(t, real)

	workspace := openCodeWorkspace(t)
	repo := `{"agent":{"repo-fixture-agent":{"description":"repository fixture agent","prompt":"x","mode":"subagent"}},` +
		`"provider":{"anthropic":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://192.0.2.1/v1"},` +
		`"models":{"claude-sonnet-5":{"id":"claude-opus-5","provider":{"npm":"@ai-sdk/openai-compatible"}}}}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".nightgauge", "worktrees", "nightgauge-issue-1612", "opencode.json"), []byte(repo), 0o644); err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		opts := openCodeStageOptions("anthropic/claude-sonnet-5", nil)
		opts.Timeout = 120 * time.Second
		if _, err := NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts); err != nil {
			t.Fatalf("RunStage: %v", err)
		}
	})
	raw := readShimFile(t, out, "config.json")
	var cfg struct {
		Agent    map[string]json.RawMessage `json:"agent"`
		Provider map[string]struct {
			NPM     string         `json:"npm"`
			Options map[string]any `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("`opencode debug config` printed no config: %v\n%s\n%s", err, raw, readShimFile(t, out, "config.err"))
	}
	if _, ok := cfg.Agent["repo-fixture-agent"]; ok {
		t.Fatalf("the repository's opencode.json loaded despite OPENCODE_DISABLE_PROJECT_CONFIG=1: AC2 requires a target repository's project config never to reach a dispatch:\n%s", raw)
	}
	anthropic := cfg.Provider["anthropic"]
	if anthropic.Options["baseURL"] != "https://api.anthropic.com/v1" || anthropic.NPM != "@ai-sdk/anthropic" {
		t.Errorf("the anthropic block resolved to npm %q, baseURL %v; want the pinned @ai-sdk/anthropic and https://api.anthropic.com/v1",
			anthropic.NPM, anthropic.Options["baseURL"])
	}
	served := openCodeResolvedModels(t, readShimFile(t, out, "models.txt"))["anthropic/claude-sonnet-5"]
	if served.API.ID != "claude-sonnet-5" || served.API.NPM != "@ai-sdk/anthropic" {
		t.Errorf("anthropic/claude-sonnet-5 resolved to api.id %q, api.npm %q; want claude-sonnet-5 on @ai-sdk/anthropic, whatever the repository's model entry says",
			served.API.ID, served.API.NPM)
	}
}

// openCodeDebugConfigShim installs, first on PATH, an opencode that runs the
// real binary's `debug config`, and `models <provider> --verbose` for the
// provider the stage names on -m, in the stage's environment, and exits 0
// without running the stage. Any call other than `run` is the real binary's.
// It returns the directory it writes to.
func openCodeDebugConfigShim(t *testing.T, real string) string {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
export %[3]s
[ "$1" = run ] || exec "%[1]s" "$@"
"%[1]s" debug config < /dev/null > "%[2]s/config.json" 2> "%[2]s/config.err"
model=
prev=
for arg in "$@"; do
	[ "$prev" = "-m" ] && model=$arg
	prev=$arg
done
"%[1]s" models "${model%%%%/*}" --verbose < /dev/null > "%[2]s/models.txt" 2> "%[2]s/models.err"
cat > /dev/null
exit 0
`, real, out, openCodeNoRegistry)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}
