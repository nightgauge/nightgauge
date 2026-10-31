package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/opencodeallow"
)

// ClaudeAdapter implements SkillRunner for Claude Code CLI.
type ClaudeAdapter struct{}

// NewClaudeAdapter creates a Claude CLI adapter.
func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{}
}

// Name returns "claude".
func (a *ClaudeAdapter) Name() string {
	return "claude"
}

// Agentic reports true.
// Claude Code CLI — full agentic tool loop.
func (a *ClaudeAdapter) Agentic() bool {
	return true
}

// UsesStdin returns true — Claude receives the prompt via stdin.
func (a *ClaudeAdapter) UsesStdin() bool {
	return true
}

// BuildCommand constructs the claude CLI command for running a skill.
// The prompt is passed via stdin (not --prompt-file) to match the TypeScript
// skillRunner implementation. Use RunOptions.Prompt for the stdin content.
func (a *ClaudeAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "claude"

	args := []string{
		"-p",                             // Print mode (read prompt from stdin)
		"--no-session-persistence",       // Don't persist sessions
		"--output-format", "stream-json", // NDJSON stream for token tracking
		"--verbose", // Include detailed events in stream
	}

	// Allowed tools from SKILL.md frontmatter
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}

	args = append(args, claudeKnowledgeDirArgs(opts)...)
	args = append(args, claudeSkillsDirArgs(opts)...)

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// opts.MaxTokens is not mapped: Claude Code has no --max-tokens option and
	// exits 1 on one (#1716). The run's bounds are --max-turns and
	// --max-budget-usd below.
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	} else {
		args = append(args, "--max-turns", "200") // Default
	}

	if opts.CostBudget > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", opts.CostBudget))
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER":  fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":          opts.Repo,
		"NIGHTGAUGE_STAGE":         opts.Stage,
		"NIGHTGAUGE_OUTPUT_FORMAT": "stream-json",
		"NIGHTGAUGE_ADAPTER":       "claude",
	}
	// NIGHTGAUGE_DISPATCH_MODEL is the model this stage was dispatched to run
	// on. It is what `knowledge stamp --stage` builds the provenance actor
	// from: the SERVED model is only observable after the stage exits, so it
	// cannot be an input to a stamp the stage itself makes.
	if opts.Model != "" {
		env["NIGHTGAUGE_DISPATCH_MODEL"] = opts.Model
	}
	// Thinking is deliberately NOT disabled here. The forced
	// CLAUDE_CODE_DISABLE_THINKING=1 workaround for #3801 (thinking-block
	// replay 400 on claude CLI 2.1.154) was removed after the bug stopped
	// reproducing on CLI 2.1.186 — three multi-turn replay runs with thinking
	// enabled (up to 26 turns / 9 replayed blocks) completed without a 400;
	// see docs/PIPELINE_EXECUTION.md § Spawn Environment Inheritance. The spawn
	// env is built on os.Environ() (manager.go), so an operator on an older
	// CLI can restore the workaround without a rebuild: export
	// CLAUDE_CODE_DISABLE_THINKING=1.

	if opts.ContextFile != "" {
		env["NIGHTGAUGE_CONTEXT_FILE"] = opts.ContextFile
	}
	if opts.OutputFile != "" {
		env["NIGHTGAUGE_OUTPUT_FILE"] = opts.OutputFile
	}
	if opts.KnowledgeDir != "" {
		env[KnowledgeDirEnvVar] = opts.KnowledgeDir
	}
	if opts.TargetRepo != "" {
		env["NIGHTGAUGE_TARGET_REPO"] = opts.TargetRepo
	}
	if opts.RunID != "" {
		env[RunIDEnvVar] = opts.RunID
	}

	// Pass through GITHUB_TOKEN
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	return cmd, args, env
}

// KnowledgeDirEnvVar is the variable every stage is handed the run's
// canonical knowledge base in (RunOptions.KnowledgeDir, #2194).
const KnowledgeDirEnvVar = "NIGHTGAUGE_KNOWLEDGE_DIR"

// claudeKnowledgeDirArgs makes the run's knowledge base a Claude Code working
// directory (#2194). A stage runs in a worktree, and Claude Code confines
// edits to its working directories, so the PRD/decisions/lessons writes into
// the main checkout's .nightgauge/knowledge need it added.
func claudeKnowledgeDirArgs(opts RunOptions) []string {
	if opts.KnowledgeDir == "" {
		return nil
	}
	return []string{"--add-dir", opts.KnowledgeDir}
}

// claudeSkillsDirArgs makes the skills tree the stage rendered from readable
// under a default user's permissions (#2718): over-budget includes are read
// from that tree at run time, and it lies outside the worktree. --add-dir also
// makes it a working directory, so file-tool writes are denied explicitly;
// deny rules hold under bypassPermissions too. Bash can still write there, the
// limit ADR-022 records for OpenCode.
//
// A tree inside the worktree gets neither flag. It is already reachable, and
// it is the checkout the stage was dispatched to change: a deny there would
// stop a stage editing the repository's own skills/.
func claudeSkillsDirArgs(opts RunOptions) []string {
	root := opencodeallow.SkillsRootOf(opts.SkillPath)
	if !filepath.IsAbs(root) || pathWithin(root, opts.WorktreeDir) {
		return nil
	}
	rule := "//" + strings.TrimLeft(filepath.ToSlash(root), "/") + "/**"
	return []string{
		"--add-dir", root,
		"--disallowedTools", "Edit(" + rule + "),Write(" + rule + ")",
	}
}

// pathWithin reports whether path is dir or lies under it, comparing resolved
// paths so a symlinked temp or home directory does not read as outside.
func pathWithin(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(resolvedPath(dir), resolvedPath(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
