package adapters

import (
	"fmt"
	"os"
	"strings"
)

// LmStudioAdapter implements SkillRunner for LM Studio via SDK CLI bridge.
// The Go adapter is a thin wrapper that sets NIGHTGAUGE_ADAPTER=lm-studio
// so the TypeScript SDK selects the LmStudioAdapter, which then makes HTTP
// calls to a local LM Studio server (OpenAI-compatible API).
type LmStudioAdapter struct{}

// NewLmStudioAdapter creates an LM Studio adapter.
func NewLmStudioAdapter() *LmStudioAdapter {
	return &LmStudioAdapter{}
}

// Name returns "lm-studio".
func (a *LmStudioAdapter) Name() string {
	return "lm-studio"
}

// Agentic reports false.
// The bridge bottoms out in the TypeScript LmStudioAdapter — fetch/SSE chat
// completion with zero tool handling. Barred from pipeline dispatch (#57);
// remains available for eval/judge surfaces.
func (a *LmStudioAdapter) Agentic() bool {
	return false
}

// UsesStdin returns true — LM Studio uses the claude CLI bridge (stdin-based).
func (a *LmStudioAdapter) UsesStdin() bool {
	return true
}

// BuildCommand constructs the CLI command for running a skill via LM Studio.
// LM Studio uses the claude CLI as an SDK bridge: the prompt is piped via stdin,
// and NIGHTGAUGE_ADAPTER=lm-studio signals the TypeScript SDK to route
// LLM calls to the local LM Studio server instead of the Anthropic API.
func (a *LmStudioAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "claude"

	args := []string{
		"-p",                             // Print mode (read prompt from stdin)
		"--output-format", "stream-json", // NDJSON stream for token tracking
		"--verbose", // Include detailed events in stream
	}

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
		"NIGHTGAUGE_ADAPTER":       "lm-studio",
	}

	if opts.ContextFile != "" {
		env["NIGHTGAUGE_CONTEXT_FILE"] = opts.ContextFile
	}
	if opts.OutputFile != "" {
		env["NIGHTGAUGE_OUTPUT_FILE"] = opts.OutputFile
	}
	if opts.TargetRepo != "" {
		env["NIGHTGAUGE_TARGET_REPO"] = opts.TargetRepo
	}

	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	// Pass through LM Studio-specific environment variables.
	// These are read by the TypeScript LmStudioAdapter (resolveModel, resolveBaseUrl, etc.).
	if model := os.Getenv("NIGHTGAUGE_LM_STUDIO_MODEL"); model != "" {
		env["NIGHTGAUGE_LM_STUDIO_MODEL"] = model
	}
	if baseURL := os.Getenv("NIGHTGAUGE_LM_STUDIO_BASE_URL"); baseURL != "" {
		env["NIGHTGAUGE_LM_STUDIO_BASE_URL"] = baseURL
	}
	if apiKey := os.Getenv("NIGHTGAUGE_LM_STUDIO_API_KEY"); apiKey != "" {
		env["NIGHTGAUGE_LM_STUDIO_API_KEY"] = apiKey
	}
	if timeoutMs := os.Getenv("NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS"); timeoutMs != "" {
		env["NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS"] = timeoutMs
	}

	return cmd, args, env
}
