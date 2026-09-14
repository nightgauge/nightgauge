package codexprovision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// OpenCodeProvision is what an OpenCode stage is given from its repository
// (ADR-022 § 8, § 11, § 15): the baseline steering Codex gets, the
// repository's own steering files, and the pipeline's MCP servers.
// ProvisionOpenCode reads it and writes nothing, in the worktree or anywhere
// else: adapters.BuildOpenCodeConfig, the one writer of an OpenCode run's
// config, writes Steering to a file in the run's own root, names that file
// after Instructions in the config's instructions, and puts MCP in its mcp
// block.
type OpenCodeProvision struct {
	// Root is the worktree with its symbolic links resolved. Every entry of
	// Instructions is inside it.
	Root string
	// Steering is the baseline steering, from the function Codex's managed
	// AGENTS.md block comes from (assembleSteeringContent).
	Steering string
	// Instructions are the absolute paths, symbolic links resolved, of the
	// repository's steering file (repositorySteering) and of every file it
	// imports (openCodeInstructions).
	Instructions []string
	// MCP are the pipeline's MCP servers read from the base branch, in
	// OpenCode's shape.
	MCP map[string]OpenCodeMcpServer
	// McpSource is the branch the servers were read from, such as
	// origin/main, or "" when none could be read.
	McpSource string
	// Warnings say what the stage is not given and why, one line each. They
	// name files and servers, never a value.
	Warnings []string
}

// ProvisionOpenCode reads what an OpenCode stage running in worktree is
// given from its repository. The MCP servers come from the base branch
// (ReadBaseBranchMcpServers): one the working tree alone defines, or defines
// differently, is not given, and a warning names it. When the servers cannot
// be read, the stage runs with none and a warning says why. An error means
// the worktree is not a directory.
func ProvisionOpenCode(ctx context.Context, worktree string) (OpenCodeProvision, error) {
	var p OpenCodeProvision
	if worktree == "" {
		return p, errors.New("the stage has no worktree to read the repository's steering and MCP servers from")
	}
	root, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return p, fmt.Errorf("the stage's worktree: %w", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return p, fmt.Errorf("the stage's worktree %s is not a directory", worktree)
	}
	p.Root = root
	p.Steering = assembleSteeringContent(root, openCodeSteering)
	p.Instructions, p.Warnings = openCodeInstructions(root)

	base, source, err := ReadBaseBranchMcpServers(ctx, root)
	if err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers: none are started, because they are read from the base branch and %v", err))
		p.MCP = map[string]OpenCodeMcpServer{}
		return p, nil
	}
	p.McpSource = source
	added, changed := worktreeOnlyServers(ReadPipelineMcpServers(root), base)
	if len(added) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers only the worktree defines, not %s, are not started: %s", source, strings.Join(added, ", ")))
	}
	if len(changed) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers the worktree defines differently are started as %s defines them: %s", source, strings.Join(changed, ", ")))
	}
	var warnings []string
	p.MCP, warnings = OpenCodeMcpServers(base)
	p.Warnings = append(p.Warnings, warnings...)
	return p, nil
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
