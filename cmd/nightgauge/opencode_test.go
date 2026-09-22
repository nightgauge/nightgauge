package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// openCodeVerbForge serves files as the head of main on a forge, or fails
// with err, and records the repositories it is asked for.
type openCodeVerbForge struct {
	files map[string]string
	err   error
	mu    *sync.Mutex
	asked *[]string
}

func (f openCodeVerbForge) DefaultBranchFiles(_ context.Context, owner, name string, paths []string) (*forgetypes.DefaultBranchFiles, error) {
	if f.asked != nil {
		f.mu.Lock()
		*f.asked = append(*f.asked, owner+"/"+name)
		f.mu.Unlock()
	}
	if f.err != nil {
		return nil, f.err
	}
	out := &forgetypes.DefaultBranchFiles{Branch: "main", Commit: "0123456789abcdef0123456789abcdef01234567", Files: map[string]forgetypes.RepoFile{}}
	for _, p := range paths {
		if content, ok := f.files[p]; ok {
			out.Files[p] = forgetypes.RepoFile{Regular: true, Content: []byte(content)}
		}
	}
	return out, nil
}

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
// run is resolved from, sets the adapter's enable switch, puts a fake opencode
// at the compat manifest's max-tested version first on PATH (the adapter's
// version policy reads it), and returns a worktree to run the verb against.
func isolateOpenCodeVerb(t *testing.T, machineConfig string) string {
	t.Helper()
	m, ok := adaptercompat.Get("opencode")
	if !ok {
		t.Fatal("no opencode compat manifest")
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1\" = --version ] && { echo " + m.MaxTested + "; exit 0; }\nexit 97\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "GH_CONFIG_DIR", "GOCACHE", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	machineDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(machineConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real, one-commit git repository: the project-config tamper gate
	// (#1638 fix round) now fails CLOSED, not open, on a worktree git
	// reports is not one, and PreDispatch runs it before every other check
	// this verb exercises.
	worktree := t.TempDir()
	gittest.InitRepo(t, worktree, "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, worktree, "add", "-A")
	gittest.Run(t, worktree, "commit", "-qm", "base")
	return worktree
}

// runOpenCodeVerb runs `nightgauge opencode config` with args and returns its
// stdout and error.
func runOpenCodeVerb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, _, err := runOpenCodeVerbStderr(t, args...)
	return out, err
}

// runOpenCodeVerbStderr runs the verb and also returns what the process wrote
// to stderr, where the adapter's warnings and notices go.
func runOpenCodeVerbStderr(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := opencodeCmd()
	var stdout, cobraErr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&cobraErr)
	cmd.SetArgs(append([]string{"config"}, args...))
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	runErr := cmd.Execute()
	os.Stderr = orig
	_ = w.Close()
	return stdout.String(), string(<-done), runErr
}

// TestOpenCodeConfigVerbShape: the verb's JSON carries every field an SDK
// caller needs so it never re-derives one (#1648): schema_version, which is
// the builder's constant, config_content, env (the isolation variables and
// the config itself), env_withhold (the inherited variables the spawn must
// not get), plugin_dir, run_dir and non_loopback.
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
	for _, key := range []string{"schema_version", "config_content", "env", "env_withhold", "plugin_dir", "run_dir", "non_loopback"} {
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

// TestOpenCodeConfigVerbResolvesStageTools is #1638 fix round finding 5's own
// probe: the verb had no flag or lookup for --stage's AllowedTools/SkillPath,
// so openCodeConfigForStage's RunOptions always carried neither, and
// openCodePermissionMap generated a deny-everything map regardless of the
// stage's real SKILL.md — read/edit/bash all "*": "deny", no
// NIGHTGAUGE_SKILL_DIR allow-list entry, no matter --stage. --skills-root
// points the verb at a fixture skill so it can resolve one.
func TestOpenCodeConfigVerbResolvesStageTools(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)

	// "feature-dev" is a real pipeline stage (skillrender.StageSkillDirs), so
	// it resolves to "nightgauge-feature-dev" the same way a real dispatch's
	// SkillsRoots lookup does — an arbitrary stage name skillrender does not
	// know would fail Locate for a reason unrelated to this fix (Locate is
	// this verb's own dependency, not something #1638 changes).
	skillsRoot := t.TempDir()
	skillDir := filepath.Join(skillsRoot, "skills", "nightgauge-feature-dev")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: nightgauge-feature-dev\nallowed-tools: Read Write Edit Bash\n---\n\nProbe stage body.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID,
		"--skills-root", skillsRoot, "--json")
	if err != nil {
		t.Fatalf("the verb failed: %v", err)
	}
	var run adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Permission struct {
			Read map[string]string `json:"read"`
			Edit map[string]string `json:"edit"`
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	if err := json.Unmarshal([]byte(run.ConfigContent), &config); err != nil {
		t.Fatalf("config_content is not the expected shape: %v\n%s", err, run.ConfigContent)
	}
	for key, m := range map[string]map[string]string{"read": config.Permission.Read, "edit": config.Permission.Edit, "bash": config.Permission.Bash} {
		if m["*"] != "allow" {
			t.Errorf("permission.%s[\"*\"] = %q, want \"allow\": the fixture SKILL.md grants Read/Write/Edit/Bash, so the verb should resolve them, not print the deny-everything default", key, m["*"])
		}
	}
	skillDirPattern := filepath.Clean(skillDir) + "/**"
	if !strings.Contains(run.ConfigContent, skillDirPattern) {
		t.Errorf("config_content has no external_directory allow-list entry for the resolved skill dir %q:\n%s", skillDirPattern, run.ConfigContent)
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
		// The plugin handshake nonce (#1635) is freshly minted by each of
		// the two independent InstallNightgaugePlugin calls this test makes
		// (crypto/rand, opencodeplugin.NewNonce) — it can never be byte
		// identical across them, unlike every other entry, which is a
		// deterministic function of the same inputs (id, worktree, model).
		// Presence and non-emptiness are what the verb/adapter parity
		// contract actually promises for this one key.
		if k == opencodeplugin.EnvNonce {
			if v == "" || env[k] == "" {
				t.Errorf("env[%s]: expected a non-empty handshake nonce from both the verb (%q) and the adapter (%q)", k, v, env[k])
			}
			continue
		}
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

	// The inherited variables the verb says to withhold are the ones the Go
	// path keeps from the child, so a caller composes the same environment.
	var withhold struct {
		EnvWithhold struct {
			Prefixes []string `json:"prefixes"`
			Names    []string `json:"names"`
		} `json:"env_withhold"`
	}
	if err := json.Unmarshal([]byte(out), &withhold); err != nil {
		t.Fatal(err)
	}
	covers := func(name string) bool {
		for _, p := range withhold.EnvWithhold.Prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return slices.Contains(withhold.EnvWithhold.Names, name)
	}
	names := append([]string{"OPENCODE_AUTH_CONTENT", "OPENCODE_CONFIG_DIR", "ANTHROPIC_API_KEY", "OPENAI_BASE_URL", "LMSTUDIO_API_KEY", "GITHUB_TOKEN", "AWS_REGION", "PATH"},
		withhold.EnvWithhold.Names...)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	for _, name := range names {
		if got, want := covers(name), a.WithholdsEnv(run, name); got != want {
			t.Errorf("env_withhold covers %s = %v; the adapter withholds it = %v", name, got, want)
		}
	}
	if len(withhold.EnvWithhold.Names) == 0 {
		t.Error("env_withhold names no variable; a local dispatch withholds every hosted model service's key")
	}
}

// TestOpenCodeConfigVerbEnvKeysMatchGoldenFixture (#1804) is the drift guard
// two independent reviews of this fix round both landed on: a parity test
// that only reads opencodeplugin's exported `Env*` constants is structurally
// blind to a variable set by a bare string literal — OPENCODE_DISABLE_PROJECT_CONFIG
// is set inline at InstallNightgaugePlugin's call site (opencode.go), not one
// of those constants — or set somewhere else in the isolation env entirely
// (HOME, OpenCodeIsolationEnv, opencode_isolation.go). Both slipped past a
// constants-only parity test once already. This test runs the real verb, the
// same one an SDK caller runs, and pins the exact key set it prints against
// internal/execution/testdata/opencode_config_verb_env_keys.golden.json —
// the file tests/cli/childEnv.test.ts reads on the TS side to assert every
// one of these keys is accepted by the TS allowlist. A key this test does not
// know about is exactly as invisible to that TS assertion, so keeping this
// test green is what keeps the two lists from drifting apart again.
//
// NIGHTGAUGE_OPENCODE_OPERATOR_INSTALL_RISK is deliberately not in the
// fixture: operatorInstallRisk (opencode_plugin_deps.go) only sets it when an
// operator's OpenCode install directory is at risk, which this bare
// invocation's fresh, empty $HOME never is. Its own accept-but-withhold
// contract is pinned by TestOpenCodeBuildCommandWithholdsOperatorInstallRiskFromTheChild
// on this side and by dedicated tests in opencodeAdapter.test.ts on the TS
// side, not by this fixture.
func TestOpenCodeConfigVerbEnvKeysMatchGoldenFixture(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID, "--json")
	if err != nil {
		t.Fatalf("the verb failed: %v", err)
	}
	var run adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(run.Env))
	for k := range run.Env {
		got = append(got, k)
	}
	slices.Sort(got)

	goldenPath := filepath.Join("..", "..", "internal", "execution", "testdata", "opencode_config_verb_env_keys.golden.json")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading the golden fixture: %v", err)
	}
	var golden struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parsing the golden fixture: %v", err)
	}

	if !slices.Equal(got, golden.Keys) {
		t.Errorf(
			"the verb's env keys drifted from %s.\n  golden: %v\n  got:    %v\n"+
				"If this is a deliberate new variable, add it to childEnv.ts's "+
				"OPENCODE_RUN_ENV_NAMES (or OPENCODE_RUN_ENV_WITHHELD_NAMES if it "+
				"must never reach the child) first, then regenerate this fixture "+
				"with the sorted key set above.",
			goldenPath, golden.Keys, got,
		)
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
		"a forge provider":          {openCodeVerbMachineConfig, []string{"--model", "github-copilot/claude-sonnet-5"}, "subscription or OAuth"},
		"a cloud platform provider": {openCodeVerbMachineConfig, []string{"--model", "google-vertex-anthropic/claude-sonnet-5"}, "subscription or OAuth"},
		"an undeclared endpoint id": {openCodeVerbMachineConfig, []string{"--model", "lmstudio-remote/qwen/qwen3.8-27b"}, "neither an endpoint"},
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

// TestOpenCodeConfigVerbCarriesTheRepository: the verb's config carries what
// the Go path's does from the stage's repository (#1626), so the SDK path
// (#1648) gets it through the verb: the repository's CLAUDE.md as an
// instructions entry, and the MCP servers of --repo's default branch as the
// forge serves them, whose credential is an {env:VAR} reference. The forge
// is asked for --repo. The variable's value is nowhere in the output, and a
// server the worktree alone defines is not in the config. Without --repo the
// stage gets no MCP server.
func TestOpenCodeConfigVerbCarriesTheRepository(t *testing.T) {
	isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	const token = "fake-mcp-credential-1626"
	t.Setenv("MCP_FIXTURE_TOKEN", token)
	servers := `{"mcpServers": {"r": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${MCP_FIXTURE_TOKEN}"}}}}`
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	for name, content := range map[string]string{"CLAUDE.md": "# Rules\n\nSENTINEL-7Q\n", ".mcp.json": servers} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "base")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)
	parent := t.TempDir()
	gittest.Run(t, parent, "clone", "-q", origin, "wt")
	worktree := filepath.Join(parent, "wt")
	if err := os.WriteFile(filepath.Join(worktree, ".mcp.json"), []byte(strings.Replace(servers, `"r":`, `"evil": {"command": "/bin/sh"}, "r":`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	var asked []string
	t.Cleanup(adapters.SwapOpenCodeMcpForgeForTest(openCodeVerbForge{files: map[string]string{".mcp.json": servers}, mu: &sync.Mutex{}, asked: &asked}))
	out, stderr, err := runOpenCodeVerbStderr(t, "--stage", "feature-dev", "--worktree", worktree, "--repo", "fixture-owner/fixture-repo", "--run-id", openCodeVerbRunID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"fixture-owner/fixture-repo"}) {
		t.Errorf("the forge was asked for %v, want --repo's fixture-owner/fixture-repo", asked)
	}
	if strings.Contains(out, token) {
		t.Fatalf("the verb's output holds the MCP credential's value:\n%s", out)
	}
	var run adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Instructions []string                  `json:"instructions"`
		MCP          map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(run.ConfigContent), &cfg); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instructions) != 2 || cfg.Instructions[0] != filepath.Join(resolved, "CLAUDE.md") || cfg.Instructions[1] != filepath.Join(run.RunDir, "nightgauge", "steering.md") {
		t.Errorf("instructions = %q; want the worktree's CLAUDE.md, then the run's steering file", cfg.Instructions)
	}
	if len(cfg.MCP) != 1 || cfg.MCP["r"]["headers"].(map[string]any)["Authorization"] != "Bearer {env:MCP_FIXTURE_TOKEN}" {
		t.Errorf("mcp = %v; want r alone, its credential a {env:MCP_FIXTURE_TOKEN} reference", cfg.MCP)
	}
	if !strings.Contains(stderr, "not started: evil") {
		t.Errorf("stderr does not name the server left out:\n%s", stderr)
	}

	out, stderr, err = runOpenCodeVerbStderr(t, "--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	cfg.MCP = nil
	if err := json.Unmarshal([]byte(run.ConfigContent), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCP) != 0 || !strings.Contains(stderr, "records no repository") {
		t.Errorf("without --repo, mcp = %v and stderr:\n%s", cfg.MCP, stderr)
	}
}

// TestOpenCodeConfigVerbFlagsANonLoopbackEndpoint: an endpoint that is not on
// this machine is accepted, flagged non_loopback so no one claims the run
// stays offline, and its address is not printed. A hosted provider's model is
// flagged the same way.
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

	// A hosted provider's model runs on its servers, never on this machine.
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	t.Setenv("ANTHROPIC_API_KEY", "fake-anthropic-credential-1625")
	for _, model := range []string{"anthropic/claude-sonnet-5", "openai/gpt-5.5"} {
		out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--model", model, "--json")
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var run struct {
			NonLoopback *bool `json:"non_loopback"`
		}
		if err := json.Unmarshal([]byte(out), &run); err != nil {
			t.Fatal(err)
		}
		if run.NonLoopback == nil {
			t.Errorf("%s: the output has no non_loopback", model)
		} else if !*run.NonLoopback {
			t.Errorf("%s: non_loopback = false, want true: a hosted model does not run on this machine", model)
		}
	}
}

// TestOpenCodeConfigVerbRefusesWhatTheAdapterRefusesBeforeSpawn: the verb is
// the SDK path's one authority, so it refuses every dispatch the Go adapter
// refuses before spawning, not only the config's own refusals: without the
// enable switch. Since #1787, a populated ~/.opencode no longer refuses the
// verb: a non-inheriting dispatch gets its own per-run HOME (home/), which
// never contains .opencode, so the verb goes ahead and the operator's real
// ~/.opencode is left untouched. With the opt-in the verb also goes ahead,
// layers the operator's config in instead, and says so on stderr.
func TestOpenCodeConfigVerbRefusesWhatTheAdapterRefusesBeforeSpawn(t *testing.T) {
	worktree := isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--stage", "feature-dev", "--worktree", worktree, "--run-id", openCodeVerbRunID, "--json"}

	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "")
	if out, err := runOpenCodeVerb(t, args...); err == nil || !strings.Contains(err.Error(), "is experimental") {
		t.Errorf("without %s=1 the verb = %v, want the adapter's gate refusal; printed:\n%s", adapters.ExperimentalOpenCodeEnvVar, err, out)
	} else if out != "" {
		t.Errorf("a refusal printed on stdout:\n%s", out)
	}
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	dotDir := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(dotDir, "plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "opencode.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dotDir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runOpenCodeVerb(t, args...)
	if err != nil {
		t.Fatalf("with ~/.opencode holding config the verb = %v; #1787's per-run HOME must never refuse or wait on it", err)
	}
	var run adapters.OpenCodeRun
	if jsonErr := json.Unmarshal([]byte(out), &run); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if want := filepath.Join(run.RunDir, "home"); run.Env["HOME"] != want {
		t.Errorf("Env[HOME] = %q, want %q", run.Env["HOME"], want)
	}
	if _, err := os.Lstat(filepath.Join(run.RunDir, "home", ".opencode")); !os.IsNotExist(err) {
		t.Errorf("the run's own home/.opencode exists (%v); it must never be created", err)
	}
	after, err := os.Stat(filepath.Join(dotDir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("the operator's ~/.opencode/opencode.json mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}

	machineDir := os.Getenv("NIGHTGAUGE_CONFIG_HOME")
	if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(openCodeVerbMachineConfig+"  inherit_user_config: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runOpenCodeVerbStderr(t, args...)
	if err != nil {
		t.Fatalf("with the opt-in the verb refused: %v", err)
	}
	// A fresh struct: json.Unmarshal merges into an existing non-nil map
	// rather than replacing it, so reusing `run` here would carry the first
	// call's "HOME" entry forward even though this response's JSON omits it.
	var inheritRun adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &inheritRun); err != nil {
		t.Fatal(err)
	}
	if inheritRun.Env["OPENCODE_CONFIG_DIR"] == "" {
		t.Error("with the opt-in the verb's env does not layer the operator's OpenCode config in")
	}
	if inheritRun.Env["HOME"] != "" {
		t.Errorf("with the opt-in the verb's env still sets a per-run HOME: %q, want it left untouched", inheritRun.Env["HOME"])
	}
	if !strings.Contains(stderr, "opencode.inherit_user_config is on") {
		t.Errorf("with the opt-in the verb did not say so on stderr:\n%s", stderr)
	}
}
