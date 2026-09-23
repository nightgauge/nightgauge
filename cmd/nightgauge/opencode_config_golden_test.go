package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// openCodeConfigGoldenPath is the SDK/Go parity golden (#1648). This test
// generates it from the real verb and the real Go spawn path; the SDK's
// tests/cli/adapters/opencodeRunConfig.test.ts feeds its "verb" through
// opencodeRunConfig.ts and asserts the SDK child gets exactly "go_spawn_env".
// Drift on either side turns that side red.
var openCodeConfigGoldenPath = filepath.Join("..", "..", "internal", "execution", "adapters", "testdata", "opencode_config_golden.json")

// openCodeConfigGoldenUpdateEnv regenerates the golden instead of comparing:
//
//	NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./cmd/nightgauge -run TestOpenCodeConfigGolden
const openCodeConfigGoldenUpdateEnv = "NIGHTGAUGE_UPDATE_GOLDEN"

// openCodeConfigGolden is the golden file's shape. The test's base directory
// and the per-spawn handshake nonce are replaced by "@BASE@" and "@NONCE@",
// the same on both sides; the TS test substitutes a directory and a nonce of
// its own.
type openCodeConfigGolden struct {
	Comment      string            `json:"_comment"`
	Placeholders []string          `json:"placeholders"`
	Inputs       map[string]any    `json:"inputs"`
	Verb         json.RawMessage   `json:"verb"`
	GoSpawnEnv   map[string]string `json:"go_spawn_env"`
	// NightgaugeEnvAllow is adapters.OpenCodeNightgaugeEnvAllow: the
	// NIGHTGAUGE_* names an opencode child inherits (#1657). The SDK's
	// OPENCODE_NIGHTGAUGE_ALLOW is compared to it.
	NightgaugeEnvAllow []string `json:"nightgauge_env_allow"`
}

// openCodeGoldenSpawnEnvName selects what AC4 of #1648 compares: every XDG
// and OPENCODE_* variable, the isolated HOME and the plugin/handshake
// variables. OPENCODE_SERVER_PASSWORD is minted fresh per spawn on both paths.
func openCodeGoldenSpawnEnvName(name string) bool {
	if name == "OPENCODE_SERVER_PASSWORD" {
		return false
	}
	return name == "HOME" || strings.HasPrefix(name, "XDG_") ||
		strings.HasPrefix(name, "OPENCODE_") || strings.HasPrefix(name, "NIGHTGAUGE_OPENCODE_")
}

// TestOpenCodeConfigGolden runs `nightgauge opencode config` and the Go
// adapter's PrepareRunRoot + BuildCommand for the same fixed inputs (stage
// feature-dev, a fixture SKILL.md granting Read/Write/Edit/Bash/Grep,
// lmstudio/qwen/qwen3.8-27b, one MCP server on the repository's default
// branch) and compares both, normalized, to the committed golden.
func TestOpenCodeConfigGolden(t *testing.T) {
	isolateOpenCodeVerb(t, openCodeVerbMachineConfig)
	const model = "lmstudio/qwen/qwen3.8-27b"
	const repo = "fixture-owner/fixture-repo"
	const maxTurns = 40

	// Every path either side can print lives under one symlink-free base, so
	// the golden holds one placeholder for all of them and is the same on
	// every OS: no /private/var twin of a /var path, no OS cache directory,
	// no randomly named go-build directory.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := func(name string) string {
		d := filepath.Join(base, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	t.Setenv("HOME", dir("home"))
	t.Setenv("GOCACHE", dir("gocache"))
	t.Setenv("GH_CONFIG_DIR", dir("gh"))
	machine := dir("machine")
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machine)
	if err := os.WriteFile(filepath.Join(machine, "config.yaml"), []byte(openCodeVerbMachineConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ok := adaptercompat.Get("opencode")
	if !ok {
		t.Fatal("no opencode compat manifest")
	}
	ocBin := dir("opencode-bin")
	fake := "#!/bin/sh\n[ \"$1\" = --version ] && { echo " + m.MaxTested + "; exit 0; }\nexit 97\n"
	if err := os.WriteFile(filepath.Join(ocBin, "opencode"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", ocBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(adapters.SwapOpenCodeBinDirForTest(dir("nightgauge-bin")))
	worktree := dir("worktree")
	gittest.InitRepo(t, worktree, "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, worktree, "add", "-A")
	gittest.Run(t, worktree, "commit", "-qm", "base")

	skillsRoot := dir("skills-root")
	skillDir := filepath.Join(skillsRoot, "skills", "nightgauge-feature-dev")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: nightgauge-feature-dev\nallowed-tools: Read Write Edit Bash Grep\n---\n\nGolden stage body.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	servers := `{"mcpServers": {"nightgauge": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${NIGHTGAUGE_MCP_FIXTURE_TOKEN}"}}}}`
	t.Setenv("NIGHTGAUGE_MCP_FIXTURE_TOKEN", "fake-mcp-credential-1648")
	t.Cleanup(adapters.SwapOpenCodeMcpForgeForTest(openCodeVerbForge{files: map[string]string{".mcp.json": servers}, mu: &sync.Mutex{}}))

	out, err := runOpenCodeVerb(t, "--stage", "feature-dev", "--worktree", worktree, "--repo", repo,
		"--run-id", openCodeVerbRunID, "--model", model, "--max-turns", "40", "--skills-root", skillsRoot, "--json")
	if err != nil {
		t.Fatalf("the verb failed: %v", err)
	}
	var verb adapters.OpenCodeRun
	if err := json.Unmarshal([]byte(out), &verb); err != nil {
		t.Fatal(err)
	}

	// The Go path: the adapter's own PrepareRunRoot and BuildCommand, with
	// the stage tools resolved the way the pipeline resolves them.
	allowedTools, skillPath, err := openCodeVerbStageTools("feature-dev", skillsRoot)
	if err != nil {
		t.Fatal(err)
	}
	machineDir, err := config.MachineConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	run := adapters.RunOptions{Stage: "feature-dev", WorktreeDir: worktree, TargetRepo: repo, Model: model,
		MaxTurns: maxTurns, AllowedTools: allowedTools, SkillPath: skillPath}
	a := adapters.NewOpenCodeAdapter()
	root, err := a.PrepareRunRoot(adapters.RunRootRequest{ID: openCodeVerbRunID, MachineConfigDir: machineDir, Run: run})
	if err != nil {
		t.Fatalf("the adapter refused what the verb accepted: %v", err)
	}
	run.RunRoot = root
	_, _, env := a.BuildCommand(run)

	// The base and the two nonces (each path mints its own) are the only
	// values that differ between runs.
	repls := []struct{ from, to string }{{base, "@BASE@"}}
	for _, n := range []string{verb.Env[opencodeplugin.EnvNonce], env[opencodeplugin.EnvNonce]} {
		if n != "" {
			repls = append(repls, struct{ from, to string }{n, "@NONCE@"})
		}
	}
	// A JSON-valued string (config_content) was marshalled with its keys
	// sorted while they still held the real base, so its key order depends
	// on where the OS puts temp dirs (/tmp/X on Linux sorts after "/tmp/?",
	// /private/var/X on macOS before it). Re-sorting after substitution
	// makes the golden the same on every OS.
	normalize := func(s string) string {
		for _, r := range repls {
			s = strings.ReplaceAll(s, r.from, r.to)
		}
		var obj map[string]any
		if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &obj) == nil {
			if b, err := json.Marshal(obj); err == nil {
				return string(b)
			}
		}
		return s
	}
	var normalizeStrings func(v any) any
	normalizeStrings = func(v any) any {
		switch x := v.(type) {
		case string:
			return normalize(x)
		case map[string]any:
			for k, e := range x {
				x[k] = normalizeStrings(e)
			}
		case []any:
			for i, e := range x {
				x[i] = normalizeStrings(e)
			}
		}
		return v
	}

	var verbObj any
	if err := json.Unmarshal([]byte(out), &verbObj); err != nil {
		t.Fatalf("the verb output is not JSON: %v", err)
	}
	verbObj = normalizeStrings(verbObj)
	verbJSON, err := json.MarshalIndent(verbObj, "  ", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// Every variable the verb's env carries is compared too (GH_CONFIG_DIR,
	// NIGHTGAUGE_CONFIG_HOME, …), not only the XDG/OPENCODE_* families.
	goEnv := map[string]string{}
	for k, v := range env {
		if _, inVerb := verb.Env[k]; openCodeGoldenSpawnEnvName(k) || inVerb {
			goEnv[k] = normalize(v)
		}
	}

	// The two paths must already agree before the golden is consulted.
	if verb.ConfigContent != env["OPENCODE_CONFIG_CONTENT"] {
		t.Errorf("config_content differs from the Go spawn's OPENCODE_CONFIG_CONTENT")
	}
	for k, v := range verb.Env {
		// The one variable the Go spawn deliberately withholds from the child.
		if k == opencodeplugin.EnvOperatorInstallRisk {
			continue
		}
		if normalize(v) != goEnv[k] {
			t.Errorf("env[%s]: the verb prints %q, the Go spawn gets %q", k, normalize(v), goEnv[k])
		}
	}
	for k := range goEnv {
		if _, ok := verb.Env[k]; !ok {
			t.Errorf("the Go spawn sets %s, which the verb's env does not carry", k)
		}
	}
	if verb.PluginVersion != opencodeplugin.PluginVersion {
		t.Errorf("plugin_version = %q, want %q", verb.PluginVersion, opencodeplugin.PluginVersion)
	}
	if !filepath.IsAbs(verb.Binary) {
		t.Errorf("binary = %q, want the absolute path of the vetted opencode", verb.Binary)
	}

	golden := openCodeConfigGolden{
		Comment: "Generated by TestOpenCodeConfigGolden (cmd/nightgauge/opencode_config_golden_test.go) from the real `nightgauge opencode config` verb and the Go adapter's PrepareRunRoot + BuildCommand for the same inputs; never edit by hand. Regenerate: " +
			openCodeConfigGoldenUpdateEnv + "=1 go test ./cmd/nightgauge -run TestOpenCodeConfigGolden. " +
			"verb is the verb's JSON; go_spawn_env is every variable the verb's env names, plus every HOME, XDG_*, OPENCODE_* (less the per-spawn OPENCODE_SERVER_PASSWORD) and NIGHTGAUGE_OPENCODE_* variable, as the Go spawn gets it. " +
			"packages/nightgauge-sdk/tests/cli/adapters/opencodeRunConfig.test.ts feeds verb through the SDK and asserts its child gets go_spawn_env (#1648). " +
			"nightgauge_env_allow is adapters.OpenCodeNightgaugeEnvAllow, which packages/nightgauge-sdk/tests/cli/childEnv.test.ts compares OPENCODE_NIGHTGAUGE_ALLOW to (#1657).",
		Placeholders: []string{"@BASE@", "@NONCE@"},
		Inputs: map[string]any{
			"stage": "feature-dev", "model": model, "repo": repo, "max_turns": maxTurns,
			"run_id": openCodeVerbRunID, "allowed_tools": allowedTools, "machine_config": openCodeVerbMachineConfig,
			"mcp_json": servers,
		},
		Verb:               verbJSON,
		GoSpawnEnv:         goEnv,
		NightgaugeEnvAllow: adapters.OpenCodeNightgaugeEnvAllow,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(golden); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"fake-mcp-credential-1648", base} {
		if strings.Contains(buf.String(), leak) {
			t.Fatalf("the golden would carry %q", leak)
		}
	}

	if os.Getenv(openCodeConfigGoldenUpdateEnv) == "1" {
		if err := os.WriteFile(openCodeConfigGoldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(openCodeConfigGoldenPath)
	if err != nil {
		t.Fatalf("reading the golden: %v", err)
	}
	// Compared as JSON values, not bytes: the repository's formatter may
	// re-wrap the committed file without changing a value.
	var wantV, gotV any
	if err := json.Unmarshal(want, &wantV); err != nil {
		t.Fatalf("the golden is not JSON: %v", err)
	}
	if err := json.Unmarshal(buf.Bytes(), &gotV); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantV, gotV) {
		t.Errorf("the verb or the Go spawn env drifted from %s. If the change is deliberate, regenerate it with %s=1 go test ./cmd/nightgauge -run TestOpenCodeConfigGolden and let the SDK test prove the SDK follows.\n--- got ---\n%s",
			openCodeConfigGoldenPath, openCodeConfigGoldenUpdateEnv, buf.String())
	}
}
