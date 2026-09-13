package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// openCodeVerbMachineConfig is the reference machine's `opencode:` block: one
// LM Studio on loopback with a 131072-token window loaded.
const openCodeVerbMachineConfig = `opencode:
  model: lmstudio/qwen/qwen3.8-27b
  provider: lm-studio
  base_url: http://127.0.0.1:1234/v1
  limit:
    context: 131072
    output: 8192
`

// openCodeVerbRunID is the run identity the verb tests name their root by.
const openCodeVerbRunID = "01890a5d-ac96-774b-bcce-b30209a81625"

// isolateOpenCodeVerb points HOME and the machine tier at fresh directories,
// writes machineConfig as the machine-tier config, clears every variable the
// run is resolved from, and returns a worktree to run the verb against.
func isolateOpenCodeVerb(t *testing.T, machineConfig string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "GH_CONFIG_DIR", "GOCACHE", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	machineDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(machineConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return t.TempDir()
}

// runOpenCodeVerb runs `nightgauge opencode config` with args and returns its
// stdout and error.
func runOpenCodeVerb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := opencodeCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"config"}, args...))
	err := cmd.Execute()
	return stdout.String(), err
}

// TestOpenCodeConfigVerbShape: the verb's JSON carries every field an SDK
// caller needs so it never re-derives one (#1648): schema_version, which is
// the builder's constant, config_content, env (the isolation variables and
// the config itself), plugin_dir and run_dir.
func TestOpenCodeConfigVerbShape(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID, "--json")
	if err != nil {
		t.Fatalf("the verb failed: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("the verb's output is not a JSON object: %v\n%s", err, out)
	}
	for _, key := range []string{"schema_version", "config_content", "env", "plugin_dir", "run_dir"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("the verb's output has no %q:\n%s", key, out)
		}
	}
	var run adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != adapters.OpenCodeConfigSchemaVersion {
		t.Errorf("schema_version = %q, want %q", run.SchemaVersion, adapters.OpenCodeConfigSchemaVersion)
	}
	if run.Env["OPENCODE_CONFIG_CONTENT"] != run.ConfigContent || run.Env["XDG_CONFIG_HOME"] != filepath.Join(run.RunDir, "config") {
		t.Errorf("env does not carry the config and the isolation variables: %v", run.Env)
	}
	if fi, err := os.Stat(run.RunDir); err != nil || !fi.IsDir() {
		t.Errorf("run_dir %s was not created: %v", run.RunDir, err)
	}
}

// TestOpenCodeConfigVerbMatchesTheAdapter: for the same inputs the verb prints
// exactly what the Go adapter hands a spawn, byte for byte: config_content is
// the child's OPENCODE_CONFIG_CONTENT, every env entry is the value the child
// gets, env names exactly the run root's variables, and run_dir is the root.
// So a stage the SDK launches and one the Go path launches run under the same
// config.
func TestOpenCodeConfigVerbMatchesTheAdapter(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	const model = "lmstudio/qwen/qwen3.8-27b"
	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID,
		"--model", model, "--max-turns", "40", "--json")
	if err != nil {
		t.Fatalf("the verb failed: %v", err)
	}
	var verb adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &verb); err != nil {
		t.Fatal(err)
	}

	machineDir, err := config.MachineConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	run := adapters.RunOptions{Stage: "feature-dev", WorktreeDir: worktree, Model: model, MaxTurns: 40}
	a := adapters.NewOpenCodeAdapter()
	root, err := a.PrepareRunRoot(adapters.RunRootRequest{ID: openCodeVerbRunID, MachineConfigDir: machineDir, Run: run})
	if err != nil {
		t.Fatalf("the adapter refused what the verb accepted: %v", err)
	}
	run.RunRoot = root
	_, _, env := a.BuildCommand(run)

	if verb.ConfigContent != env["OPENCODE_CONFIG_CONTENT"] {
		t.Errorf("config_content differs from the adapter's OPENCODE_CONFIG_CONTENT:\n  verb:    %s\n  adapter: %s", verb.ConfigContent, env["OPENCODE_CONFIG_CONTENT"])
	}
	if len(verb.Env) != len(root.Env) {
		t.Errorf("the verb's env has %d variables, the adapter's run root %d", len(verb.Env), len(root.Env))
	}
	for k, v := range verb.Env {
		if env[k] != v {
			t.Errorf("env[%s]: the verb prints %q, the adapter spawns with %q", k, v, env[k])
		}
	}
	if verb.RunDir != root.Dir {
		t.Errorf("run_dir = %q, the adapter's root is %q", verb.RunDir, root.Dir)
	}
	if !strings.Contains(verb.ConfigContent, `"steps":40`) {
		t.Errorf("--max-turns did not reach the steps cap: %s", verb.ConfigContent)
	}
}

// TestOpenCodeConfigVerbRefuses: every refusal the adapter makes before
// spawning fails the verb with a reason, and nothing is printed on stdout for
// a caller to spawn with.
func TestOpenCodeConfigVerbRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		machine string
		args    []string
		want    string
	}{
		"zero context limit":        {strings.Replace(openCodeVerbMachineConfig, "context: 131072", "context: 0", 1), nil, "opencode.limit.context"},
		"missing output limit":      {strings.Replace(openCodeVerbMachineConfig, "    output: 8192\n", "", 1), nil, "opencode.limit.output"},
		"ftp base_url":              {strings.Replace(openCodeVerbMachineConfig, "http://127.0.0.1:1234/v1", "ftp://127.0.0.1:1234/v1", 1), nil, "http or https"},
		"base_url with a password":  {strings.Replace(openCodeVerbMachineConfig, "http://127.0.0.1:1234/v1", "http://u:p@127.0.0.1:1234", 1), nil, "user name or password"},
		"anthropic without its key": {openCodeVerbMachineConfig, []string{"--model", "anthropic/claude-sonnet-5"}, "ANTHROPIC_API_KEY is not set"},
		"a bare model":              {openCodeVerbMachineConfig, []string{"--model", "sonnet"}, "names no provider"},
		"no model":                  {strings.Replace(openCodeVerbMachineConfig, "  model: lmstudio/qwen/qwen3.8-27b\n", "", 1), nil, "no model"},
		"no --json":                 {openCodeVerbMachineConfig, []string{"--json=false"}, "--json"},
	} {
		t.Run(name, func(t *testing.T) {
			worktree := isolateOpenCodeVerb(t, tc.machine)
			args := append([]string{"--stage", "feature-dev", "--worktree", worktree, "--json"}, tc.args...)
			out, err := runOpenCodeVerb(t, args...)
			if err == nil {
				t.Fatalf("the verb succeeded:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not say %q: %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "u:p@") {
				t.Errorf("the error quotes the base URL: %v", err)
			}
			if out != "" {
				t.Errorf("a refusal printed on stdout:\n%s", out)
			}
		})
	}

	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	if err := os.MkdirAll(filepath.Join(worktree, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".nightgauge", "config.yaml"), []byte("owner: nightgauge\nopencode:\n  base_url: http://127.0.0.1:9/v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--json"); err == nil || !strings.Contains(err.Error(), "read only from the machine-tier config") {
		t.Errorf("a worktree committing opencode: was accepted: %v", err)
	}
}

// TestOpenCodeConfigVerbHoldsNoCredential: credentials reach OpenCode only as
// {env:VAR} references, so neither the config nor the verb's env ever holds
// one: not the dispatched provider's key, and not the forge token.
func TestOpenCodeConfigVerbHoldsNoCredential(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	const anthropicKey = "fake-anthropic-credential-1625"
	const forgeToken = "fake-forge-credential-1625"
	t.Setenv("ANTHROPIC_API_KEY", anthropicKey)
	t.Setenv("GITHUB_TOKEN", forgeToken)
	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--model", "anthropic/claude-sonnet-5", "--json")
	if err != nil {
		t.Fatalf("the verb refused anthropic/ with ANTHROPIC_API_KEY set: %v", err)
	}
	for _, secret := range []string{anthropicKey, forgeToken} {
		if strings.Contains(out, secret) {
			t.Errorf("the verb's output holds a credential's value:\n%s", out)
		}
	}
	var run adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.ConfigContent, `"apiKey":"{env:ANTHROPIC_API_KEY}"`) {
		t.Errorf("the anthropic key is not read through {env:ANTHROPIC_API_KEY}: %s", run.ConfigContent)
	}
}

// TestOpenCodeConfigVerbFlagsANonLoopbackEndpoint: an endpoint that is not on
// this machine is accepted, flagged non_loopback so no one claims the run
// stays offline, and its address is not printed.
func TestOpenCodeConfigVerbFlagsANonLoopbackEndpoint(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:1234/v1":  false,
		"http://192.0.2.10:1234/v1": true,
	} {
		worktree := isolateOpenCodeVerb(t, strings.Replace(openCodeVerbMachineConfig, "http://127.0.0.1:1234/v1", url, 1))
		out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--json")
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		var run adapters.OpenCodeRun
		if err := json.Unmarshal([]byte(out), &run); err != nil {
			t.Fatal(err)
		}
		if run.NonLoopback != want {
			t.Errorf("%s: non_loopback = %v, want %v", url, run.NonLoopback, want)
		}
		if strings.Contains(out, strings.TrimSuffix(url, "/v1")) {
			t.Errorf("%s: the verb's output carries the endpoint's address:\n%s", url, out)
		}
	}
}
