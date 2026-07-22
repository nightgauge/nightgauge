package codexprovision

import (
	"fmt"
	"os"
	"path/filepath"
)

// Result reports what Provision did, for logging.
type Result struct {
	AgentsMdPath      string   // AGENTS.md path written (empty if not written)
	ConfigTomlPath    string   // config.toml path written (empty if not written)
	Provisioned       []string // MCP server names provisioned into config.toml
	SkippedCollisions []string // MCP names skipped because the user defined them
}

// Provision provisions Codex provider context for a stage on the Go-direct
// spawn path, at parity with the TypeScript StageExecutor:
//
//   - AGENTS.md baseline steering written into workspaceRoot (#4028).
//   - the pipeline's MCP servers (.mcp.json / .claude/settings.json) translated
//     into $CODEX_HOME/config.toml `[mcp_servers.*]` (#4025).
//
// No-op for non-codex adapters. Idempotent: re-running with the same inputs
// rewrites the same bytes (and skips the write when unchanged). The managed
// blocks preserve user content outside the markers; user-defined MCP servers win
// on a name collision. #4041
func Provision(adapterName, workspaceRoot string) (Result, error) {
	var res Result
	if adapterName != "codex" {
		return res, nil
	}

	// 1. AGENTS.md steering (always provisioned — baseline guidance).
	agentsPath := filepath.Join(workspaceRoot, "AGENTS.md")
	existing, has := readFileGracefully(agentsPath)
	next := computeNextAgentsMd(existing, has, workspaceRoot)
	if !has || next != existing {
		if err := os.WriteFile(agentsPath, []byte(next), 0o644); err != nil {
			return res, fmt.Errorf("write AGENTS.md: %w", err)
		}
	}
	res.AgentsMdPath = agentsPath

	// 2. MCP config.toml.
	servers := ReadPipelineMcpServers(workspaceRoot)
	configPath := codexConfigTomlPath()
	cfgExisting, cfgHas := readFileGracefully(configPath)
	// Nothing to provision and no existing managed block to clean up → skip.
	if len(servers) == 0 && !cfgHas {
		return res, nil
	}
	nextCfg, provisioned, skipped := ComputeNextCodexConfig(cfgExisting, cfgHas, servers)
	res.Provisioned = provisioned
	res.SkippedCollisions = skipped
	if cfgHas && nextCfg == cfgExisting {
		return res, nil // already up to date — no write
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return res, fmt.Errorf("mkdir codex home: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(nextCfg), 0o644); err != nil {
		return res, fmt.Errorf("write config.toml: %w", err)
	}
	res.ConfigTomlPath = configPath
	return res, nil
}

// codexConfigTomlPath resolves $CODEX_HOME/config.toml, defaulting to
// ~/.codex/config.toml — the location the Codex CLI reads (and the same
// resolution the TS CodexMcpProvisioner uses). #4025
func codexConfigTomlPath() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		} else {
			home = ".codex"
		}
	}
	return filepath.Join(home, "config.toml")
}
