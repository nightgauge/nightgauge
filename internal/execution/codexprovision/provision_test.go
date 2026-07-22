package codexprovision

import (
	"os"
	"path/filepath"
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
