package adapters

import (
	"fmt"
	"os"
)

// ClaudeSdkAdapter implements SkillRunner for Claude Agent SDK mode.
// This invokes the Claude CLI with --sdk-mode flag, which uses the
// Anthropic API directly via ANTHROPIC_API_KEY rather than OAuth auth.
type ClaudeSdkAdapter struct{}

// NewClaudeSdkAdapter creates a Claude SDK adapter.
func NewClaudeSdkAdapter() *ClaudeSdkAdapter {
	return &ClaudeSdkAdapter{}
}

// Name returns "claude-sdk".
func (a *ClaudeSdkAdapter) Name() string {
	return "claude-sdk"
}

// Agentic reports true.
// Claude Agent SDK — full agentic tool loop.
func (a *ClaudeSdkAdapter) Agentic() bool {
	return true
}

// UsesStdin returns true — Claude SDK receives the prompt via stdin.
func (a *ClaudeSdkAdapter) UsesStdin() bool {
	return true
}

// BuildCommand constructs the claude CLI command for SDK mode execution.
// SDK mode uses ANTHROPIC_API_KEY for auth and enables interactive features
// like session resume and native token tracking.
func (a *ClaudeSdkAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "claude"

	args := []string{
		"-p",                             // Print mode (read prompt from stdin)
		"--output-format", "stream-json", // NDJSON stream for token tracking
		"--verbose", // Include detailed events in stream
	}

	// Allowed tools from SKILL.md frontmatter
	if len(opts.AllowedTools) > 0 {
		toolList := ""
		for i, t := range opts.AllowedTools {
			if i > 0 {
				toolList += ","
			}
			toolList += t
		}
		args = append(args, "--allowedTools", toolList)
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
		args = append(args, "--max-turns", "200")
	}

	if opts.CostBudget > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", opts.CostBudget))
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER":  fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":          opts.Repo,
		"NIGHTGAUGE_STAGE":         opts.Stage,
		"NIGHTGAUGE_OUTPUT_FORMAT": "stream-json",
		"NIGHTGAUGE_ADAPTER":       "claude-sdk",
	}
	// Thinking is deliberately NOT disabled — the #3801 replay-400 workaround
	// was removed after re-validation on CLI 2.1.186 (see claude.go and
	// docs/spikes/fable-5-behavior-porting.md §8.2). Operators can restore it
	// via their environment: export CLAUDE_CODE_DISABLE_THINKING=1.

	if opts.ContextFile != "" {
		env["NIGHTGAUGE_CONTEXT_FILE"] = opts.ContextFile
	}
	if opts.OutputFile != "" {
		env["NIGHTGAUGE_OUTPUT_FILE"] = opts.OutputFile
	}
	if opts.TargetRepo != "" {
		env["NIGHTGAUGE_TARGET_REPO"] = opts.TargetRepo
	}

	// Pass through ANTHROPIC_API_KEY for SDK mode
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		env["ANTHROPIC_API_KEY"] = key
	}

	// Pass through GITHUB_TOKEN
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	return cmd, args, env
}
