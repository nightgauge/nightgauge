package codexprovision

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProvision_NonCodexIsNoOp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-home"))
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"fs": {"command": "npx"}}}`)

	res, err := Provision("claude", dir)
	if err != nil {
		t.Fatalf("Provision(claude) error: %v", err)
	}
	if res.AgentsMdPath != "" || res.ConfigTomlPath != "" {
		t.Errorf("non-codex adapter must be a no-op, got %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); statErr == nil {
		t.Error("non-codex adapter must not write AGENTS.md")
	}
}

func TestProvision_CodexWritesAgentsMdAndConfig(t *testing.T) {
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "# Proj\nA project.\n")
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"fs": {"command": "npx", "args": ["-y", "srv"]}}}`)

	res, err := Provision("codex", dir)
	if err != nil {
		t.Fatalf("Provision(codex) error: %v", err)
	}

	// AGENTS.md written with the managed steering block + project context.
	agents := readFileOrFail(t, filepath.Join(dir, "AGENTS.md"))
	assertContains(t, agents, steeringManagedBegin)
	assertContains(t, agents, "Proj")
	if res.AgentsMdPath == "" {
		t.Error("AgentsMdPath should be set")
	}

	// config.toml written under $CODEX_HOME with the MCP server.
	cfg := readFileOrFail(t, filepath.Join(codexHome, "config.toml"))
	assertContains(t, cfg, mcpManagedBegin)
	assertContains(t, cfg, "[mcp_servers.fs]")
	assertContains(t, cfg, `command = "npx"`)
	if len(res.Provisioned) != 1 || res.Provisioned[0] != "fs" {
		t.Errorf("Provisioned = %v, want [fs]", res.Provisioned)
	}
}

func TestProvision_Idempotent(t *testing.T) {
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"fs": {"command": "npx"}}}`)

	if _, err := Provision("codex", dir); err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	agents1 := readFileOrFail(t, filepath.Join(dir, "AGENTS.md"))
	cfg1 := readFileOrFail(t, filepath.Join(codexHome, "config.toml"))

	if _, err := Provision("codex", dir); err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	agents2 := readFileOrFail(t, filepath.Join(dir, "AGENTS.md"))
	cfg2 := readFileOrFail(t, filepath.Join(codexHome, "config.toml"))

	if agents1 != agents2 {
		t.Errorf("AGENTS.md not idempotent across two Provision runs")
	}
	if cfg1 != cfg2 {
		t.Errorf("config.toml not idempotent across two Provision runs")
	}
}

func TestProvision_PreservesUserAgentsMd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-home"))
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# My Own Steering\nDo not delete this.\n")

	if _, err := Provision("codex", dir); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	agents := readFileOrFail(t, filepath.Join(dir, "AGENTS.md"))
	assertContains(t, agents, "# My Own Steering")
	assertContains(t, agents, "Do not delete this.")
	assertContains(t, agents, steeringManagedBegin)
}

func TestProvision_NoMcpServersSkipsConfigButStillWritesAgentsMd(t *testing.T) {
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	// No .mcp.json and no existing config.toml.

	res, err := Provision("codex", dir)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// AGENTS.md baseline steering is always provisioned.
	if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); statErr != nil {
		t.Error("AGENTS.md should still be written with no MCP servers")
	}
	// No servers + no existing config → no config.toml created.
	if _, statErr := os.Stat(filepath.Join(codexHome, "config.toml")); statErr == nil {
		t.Error("config.toml should not be created when there is nothing to provision")
	}
	if res.ConfigTomlPath != "" {
		t.Errorf("ConfigTomlPath should be empty, got %q", res.ConfigTomlPath)
	}
}

// writeSteeringFixture writes a repository with every source the steering
// reads and both kinds of MCP server into dir. The files are written here,
// not kept under testdata/, because an AGENTS.md or CLAUDE.md in the tree is
// an instruction file every agent tool would load.
func writeSteeringFixture(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# Fixture Contract\n\n## Scope\n\nThe fixture's rules.\n\n## Commands\n\n```bash\nmake test\n```\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "@AGENTS.md\n\n# Claude Code adapter\n\nClaude-only notes.\n")
	writeFile(t, filepath.Join(dir, "standards", "code-standards.md"), "# Code Standards\n\nUse tabs.\n")
	writeFile(t, filepath.Join(dir, "standards", "security.md"), "# Security\n\nNo secrets in code.\n")
	writeFile(t, filepath.Join(dir, "docs", "GIT_WORKFLOW.md"), "# Git Workflow\n\nBranch first.\n")
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {
  "fs": {"command": "npx", "args": ["-y", "srv"], "env": {"LEVEL": "debug"}},
  "remote": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${FIXTURE_TOKEN}", "X-Team": "core"}}
}}`)
}

// TestProvision_CodexGolden pins the bytes the Codex path writes, AGENTS.md
// and config.toml, for a fixture repository. The goldens were generated from
// the code before OpenCode shared the steering function (#1626), so a change
// to the shared function that moves one Codex byte fails here.
//
//	NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/codexprovision/ -run TestProvision_CodexGolden
func TestProvision_CodexGolden(t *testing.T) {
	dir := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	writeSteeringFixture(t, dir)

	if _, err := Provision("codex", dir); err != nil {
		t.Fatalf("Provision(codex): %v", err)
	}
	for _, c := range []struct{ got, golden string }{
		{filepath.Join(dir, "AGENTS.md"), "codex-agents-md.golden"},
		{filepath.Join(codexHome, "config.toml"), "codex-config-toml.golden"},
	} {
		got := readFileOrFail(t, c.got)
		golden := filepath.Join("testdata", "provision", c.golden)
		if os.Getenv("NIGHTGAUGE_UPDATE_GOLDEN") == "1" {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if want := readFileOrFail(t, golden); got != want {
			t.Errorf("%s differs from %s\n--- got ---\n%s\n--- want ---\n%s", c.got, golden, got, want)
		}
	}
}

// snapshotTree maps every path under dir, apart from the repository's .git,
// to its mode and, for a file, its content, so a test can show that a call
// wrote nothing there.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			entry += "\n" + string(b)
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestProvisionOpenCode_SharesSteeringAndWritesNothing: an OpenCode stage's
// steering comes from the function Codex's managed AGENTS.md block comes
// from, under its own title and notice, and its delivery writes nothing into
// the worktree: Provision, which the manager calls for every adapter, is a
// no-op for opencode, and ProvisionOpenCode only reads. The repository's
// steering reaches OpenCode as an instructions path instead.
func TestProvisionOpenCode_SharesSteeringAndWritesNothing(t *testing.T) {
	wt := openCodeRepo(t, nil)
	writeSteeringFixture(t, wt)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex-home"))
	before := snapshotTree(t, wt)

	if res, err := Provision("opencode", wt); err != nil || res.AgentsMdPath != "" || res.ConfigTomlPath != "" {
		t.Fatalf("Provision(opencode) = %+v, %v; want a no-op", res, err)
	}
	p, err := provision(wt)
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshotTree(t, wt); !maps.Equal(before, after) {
		for path, entry := range after {
			if before[path] != entry {
				t.Errorf("the worktree changed at %s", path)
			}
		}
		for path := range before {
			if _, ok := after[path]; !ok {
				t.Errorf("the worktree lost %s", path)
			}
		}
	}

	root, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Instructions, []string{filepath.Join(root, "AGENTS.md")}) {
		t.Errorf("Instructions = %v, want the worktree's AGENTS.md", p.Instructions)
	}
	head := func(h steeringHost) string {
		return "# Nightgauge Pipeline Steering (" + h.name + ")\n\n" + h.notice + "\n"
	}
	codex := assembleSteeringContent(root, codexSteering, readFileGracefully)
	if !strings.HasPrefix(p.Steering, head(openCodeSteering)) || !strings.HasPrefix(codex, head(codexSteering)) {
		t.Fatalf("a steering does not open with its host's title and notice:\n%s", p.Steering)
	}
	if strings.TrimPrefix(p.Steering, head(openCodeSteering)) != strings.TrimPrefix(codex, head(codexSteering)) {
		t.Errorf("the OpenCode steering's body differs from Codex's:\n--- opencode ---\n%s\n--- codex ---\n%s", p.Steering, codex)
	}
	for _, want := range []string{"Fixture Contract", "Use tabs.", "No secrets in code.", "Branch first.", "Never push directly to main"} {
		assertContains(t, p.Steering, want)
	}
	if strings.Contains(p.Steering, "markers") {
		t.Error("the OpenCode steering speaks of a managed block's markers, which it has none of")
	}
}

func TestCodexConfigTomlPath_RespectsCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex")
	if got := codexConfigTomlPath(); got != filepath.Join("/custom/codex", "config.toml") {
		t.Errorf("codexConfigTomlPath = %q, want /custom/codex/config.toml", got)
	}
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
