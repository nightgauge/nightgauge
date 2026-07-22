package adapters

import (
	"fmt"
	"os"
	"strings"
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

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if opts.MaxTokens > 0 {
		args = append(args, "--max-tokens", fmt.Sprintf("%d", opts.MaxTokens))
	}

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
	// Thinking is deliberately NOT disabled here. The forced
	// CLAUDE_CODE_DISABLE_THINKING=1 workaround for #3801 (thinking-block
	// replay 400 on claude CLI 2.1.154) was removed after the bug stopped
	// reproducing on CLI 2.1.186 — three multi-turn replay runs with thinking
	// enabled (up to 26 turns / 9 replayed blocks) completed without a 400;
	// see docs/spikes/fable-5-behavior-porting.md §8.2. The spawn env is built
	// on os.Environ() (manager.go), so an operator on an older CLI can restore
	// the workaround without a rebuild: export CLAUDE_CODE_DISABLE_THINKING=1.

	if opts.ContextFile != "" {
		env["NIGHTGAUGE_CONTEXT_FILE"] = opts.ContextFile
	}
	if opts.OutputFile != "" {
		env["NIGHTGAUGE_OUTPUT_FILE"] = opts.OutputFile
	}
	if opts.TargetRepo != "" {
		env["NIGHTGAUGE_TARGET_REPO"] = opts.TargetRepo
	}

	// Pass through GITHUB_TOKEN
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	return cmd, args, env
}
