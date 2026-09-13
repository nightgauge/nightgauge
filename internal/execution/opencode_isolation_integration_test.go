//go:build opencode_integration

package execution

// Run isolation (ADR-022 § 8, § 22) observed against the real opencode binary,
// pinned to the version the observations were made on:
//
//	go test -tags opencode_integration ./internal/execution/ -run OpenCodeIntegration -count=1
//
// Each case dispatches through Manager.RunStage, so the environment under test
// is the one the manager composes for a real stage. A shim named opencode, first
// on PATH, runs the real binary's `debug paths`, `debug config` and, after the
// stage's own `run`, `session list`, all in that environment. The stage names
// a provider key no config defines, so the run exits 1 before any model
// request, and every opencode call runs with a throwaway HOME: no network
// request is made and the operator's real config is never read.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
)

// openCodeIntegrationVersion is the version every assertion here was observed
// on. A different binary fails rather than skips: its behaviour is unverified,
// and ADR-022 § 20 re-captures the evidence before max-tested moves.
const openCodeIntegrationVersion = "1.18.30"

// openCodeIntegrationModel names a provider key nothing defines, so a run
// fails before it sends any request.
const openCodeIntegrationModel = "nosuchprovider/x"

// openCodeInheritConfig is the reference machine-tier config with the opt-in
// into the operator's own OpenCode config (ADR-022 § 8).
const openCodeInheritConfig = openCodeMachineConfig + "  inherit_user_config: true\n"

// openCodeInheritNotice is the stderr line an opted-in dispatch prints.
const openCodeInheritNotice = "opencode.inherit_user_config is on: this dispatch also reads your own OpenCode config"

// realOpenCode resolves the opencode binary before any shim shadows it and
// checks its version under a throwaway HOME.
func realOpenCode(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode is not on PATH")
	}
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	if v := strings.TrimSpace(string(out)); v != openCodeIntegrationVersion {
		t.Fatalf("opencode %s is installed; these cases were observed on %s. Re-verify ADR-022's observations before changing the pin", v, openCodeIntegrationVersion)
	}
	return path
}

// openCodeShim installs the shim and returns the directory it writes to.
func openCodeShim(t *testing.T, real string) string {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
"%[1]s" debug paths < /dev/null > "%[2]s/paths.txt" 2>&1
"%[1]s" debug config < /dev/null > "%[2]s/config.json" 2> "%[2]s/config.err"
"%[1]s" "$@"
code=$?
"%[1]s" session list < /dev/null > "%[2]s/sessions.txt" 2>&1
exit $code
`, real, out)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}

// runOpenCodeIntegrationStage dispatches one stage with no run identity, so
// its root is minted and gone when RunStage returns.
func runOpenCodeIntegrationStage(t *testing.T) (*adapters.RunResult, string, error) {
	t.Helper()
	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		opts := openCodeStageOptions(openCodeIntegrationModel, nil)
		opts.Timeout = 120 * time.Second
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
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_AUTOUPDATE=1"}
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
		t.Errorf("the stage exited %d; a provider key nothing defines exits 1", result.ExitCode)
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

	out := openCodeShim(t, real)
	_, stderr, err := runOpenCodeIntegrationStage(t)
	if err != nil {
		t.Fatalf("RunStage: %v", err)
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
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "OPENCODE_DISABLE_MODELS_FETCH=1"}
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
	if _, _, err := runOpenCodeIntegrationStage(t); err != nil {
		t.Fatalf("RunStage with the opt-in: %v", err)
	}
	if config := string(readShimFile(t, out, "config.json")); !strings.Contains(config, "home-dotdir-agent") {
		t.Errorf("with the opt-in the ~/.opencode agent is not in the run's config:\n%s", config)
	}
}

// TestOpenCodeIntegrationPerRunConfigReachesOpenCode: the per-run config is
// what the real binary resolves in the environment a stage runs in. A shim
// runs `opencode debug config` there instead of the stage, so no request is
// sent. The endpoint's base URL, which is in no variable, resolves from the
// private file the config refers to; the limits, the steps cap, the pinned
// models and the locked keys are all in the resolved config; and a config
// file in the run's own XDG directory cannot change them.
func TestOpenCodeIntegrationPerRunConfigReachesOpenCode(t *testing.T) {
	real := realOpenCode(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	// A closed loopback port: nothing may answer even if the shim ran a stage.
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "127.0.0.1:1234", "127.0.0.1:9", 1))

	bin, out := t.TempDir(), t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n\"%[1]s\" debug config < /dev/null > \"%[2]s/config.json\" 2> \"%[2]s/config.err\"\ncat > /dev/null\nexit 0\n", real, out)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	const runID = "01890a5d-ac96-774b-bcce-b30209a81625"
	// A file layer below the per-run config tries to lift every locked key.
	xdgConfig := filepath.Join(home, ".nightgauge", "opencode", "runs", runID, "config", "opencode")
	if err := os.MkdirAll(xdgConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	below := `{"share":"auto","small_model":"opencode/free-model","enabled_providers":["lmstudio","opencode"],` +
		`"provider":{"lmstudio":{"options":{"baseURL":"http://127.0.0.1:8/v1"},"models":{"qwen/qwen3.8-27b":{"limit":{"context":0,"output":0}}}}},` +
		`"agent":{"build":{"steps":9999}}}`
	if err := os.WriteFile(filepath.Join(xdgConfig, "opencode.json"), []byte(below), 0o600); err != nil {
		t.Fatal(err)
	}

	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", state.NewRuntimeState("nightgauge/nightgauge", 1625, "item-1625", runID))
		opts.MaxTurns = 7
		opts.Timeout = 120 * time.Second
		if _, err := NewManager(openCodeWorkspace(t), adapters.NewOpenCodeAdapter()).RunStage(ctx, opts); err != nil {
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
				Limit struct{ Context, Output int } `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
		Agent map[string]struct {
			Model   string `json:"model"`
			Steps   int    `json:"steps"`
			Disable bool   `json:"disable"`
		} `json:"agent"`
		Compaction struct {
			Auto bool `json:"auto"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("`opencode debug config` in the stage's environment printed no config: %v\n%s\n%s", err, raw, readShimFile(t, out, "config.err"))
	}
	lm := cfg.Provider["lmstudio"]
	if lm.Options["baseURL"] != "http://127.0.0.1:9/v1" {
		t.Errorf("baseURL resolved to %v; want the machine-tier base_url, read from the run's file", lm.Options["baseURL"])
	}
	if l := lm.Models["qwen/qwen3.8-27b"].Limit; l.Context != 131072 || l.Output != 8192 {
		t.Errorf("limit = %+v, want 131072/8192", l)
	}
	if cfg.Agent["build"].Steps != 7 || cfg.Agent["general"].Steps != 7 || !cfg.Agent["title"].Disable {
		t.Errorf("agents = %+v; want steps 7 on build and general and the title agent disabled", cfg.Agent)
	}
	if cfg.Share != "disabled" || cfg.Autoupdate || cfg.SmallModel != "lmstudio/qwen/qwen3.8-27b" ||
		len(cfg.EnabledProviders) != 1 || cfg.EnabledProviders[0] != "lmstudio" || !cfg.Compaction.Auto {
		t.Errorf("a locked key did not hold: share %q, autoupdate %v, small_model %q, enabled_providers %v, compaction.auto %v",
			cfg.Share, cfg.Autoupdate, cfg.SmallModel, cfg.EnabledProviders, cfg.Compaction.Auto)
	}
}
