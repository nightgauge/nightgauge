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
	// AGENTS.md block comes from (assembleSteeringContent), its sources read
	// only inside the worktree (worktreeReader).
	Steering string
	// Instructions are the absolute paths, symbolic links resolved, of the
	// repository's steering file (repositorySteering) and of every file it
	// imports (openCodeInstructions).
	Instructions []string
	// MCP are the pipeline's MCP servers read from the run's repository on
	// its forge (ReadForgeMcpServers), in OpenCode's shape.
	MCP map[string]OpenCodeMcpServer
	// McpSource is where the servers were read from, the repository, its
	// default branch and the commit at its head, such as owner/name@main
	// (0123abc), or "" when none could be read.
	McpSource string
	// Warnings say what the stage is not given and why, one line each. They
	// name files and servers, never a value.
	Warnings []string
}

// ProvisionOpenCode reads what an OpenCode stage running in worktree is
// given from its repository. Every file it reads there is read through a
// worktreeReader, so nothing outside the worktree is read, and a warning
// names each file refused. The MCP servers are read from mcp.Repo's default
// branch as its forge serves it (ReadForgeMcpServers), never from the
// repository on this machine: one the working tree alone defines, or defines
// differently, is not given, and a warning names it, as it names one only the
// default branch defines. lookup reads the environment OpenCode is spawned
// with, in which a server's variables are checked (openCodePastableMcpServers)
// and never recorded. When the servers cannot be read, as when the forge does
// not answer, the stage runs with none and one warning says why; nothing
// falls back to a ref of the repository. An error means the worktree is not a
// directory or there is no environment to check.
func ProvisionOpenCode(ctx context.Context, worktree string, mcp McpSource, lookup func(string) (string, bool)) (OpenCodeProvision, error) {
	var p OpenCodeProvision
	if worktree == "" {
		return p, errors.New("the stage has no worktree to read the repository's steering and MCP servers from")
	}
	if lookup == nil {
		return p, errors.New("no environment to check the MCP servers' variables against")
	}
	root, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return p, fmt.Errorf("the stage's worktree: %w", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return p, fmt.Errorf("the stage's worktree %s is not a directory", worktree)
	}
	p.Root = root
	files := newWorktreeReader(root)
	p.Steering = assembleSteeringContent(root, openCodeSteering, files.read)
	instructions, walkWarnings := openCodeInstructions(root, files.read)
	p.Instructions = instructions
	worktreeServers := readPipelineMcpServers(root, files.read)
	p.Warnings = append(append(p.Warnings, files.warnings...), walkWarnings...)

	base, source, err := ReadForgeMcpServers(ctx, mcp)
	if err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers: none are started, because they are read from the repository's default branch on its forge and %v", err))
		p.MCP = map[string]OpenCodeMcpServer{}
		return p, nil
	}
	p.McpSource = source
	worktreeOnly, changed, baseOnly := compareMcpServers(worktreeServers, base)
	if len(worktreeOnly) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers only the worktree defines, not %s, are not started: %s", source, strings.Join(worktreeOnly, ", ")))
	}
	if len(changed) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers the worktree defines differently are started as %s defines them: %s", source, strings.Join(changed, ", ")))
	}
	if len(baseOnly) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("MCP servers only %s defines, not the worktree, are started: %s", source, strings.Join(baseOnly, ", ")))
	}
	servers, warnings := OpenCodeMcpServers(base)
	p.Warnings = append(p.Warnings, warnings...)
	p.MCP, warnings = openCodePastableMcpServers(servers, lookup)
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
