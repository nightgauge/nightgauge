package adapters

import (
	"fmt"
	"os"
	"strings"
)

// OllamaAdapter implements SkillRunner for Ollama via SDK CLI bridge.
// The Go adapter is a thin wrapper that sets NIGHTGAUGE_ADAPTER=ollama
// so the TypeScript SDK selects the OllamaAdapter, which then makes HTTP
// calls to a local Ollama server.
type OllamaAdapter struct{}

// NewOllamaAdapter creates an Ollama adapter.
func NewOllamaAdapter() *OllamaAdapter {
	return &OllamaAdapter{}
}

// Name returns "ollama".
func (a *OllamaAdapter) Name() string {
	return "ollama"
}

// Agentic reports false.
// The bridge bottoms out in the TypeScript OllamaAdapter — fetch/SSE chat
// completion with zero tool handling. Barred from pipeline dispatch (#57);
// remains available for eval/judge surfaces.
func (a *OllamaAdapter) Agentic() bool {
	return false
}

// UsesStdin returns true — Ollama uses the claude CLI bridge (stdin-based).
func (a *OllamaAdapter) UsesStdin() bool {
	return true
}

// BuildCommand constructs the CLI command for running a skill via Ollama.
// Ollama uses the claude CLI as an SDK bridge: the prompt is piped via stdin,
// and NIGHTGAUGE_ADAPTER=ollama signals the TypeScript SDK to route
// LLM calls to the local Ollama server instead of the Anthropic API.
func (a *OllamaAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
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
		"NIGHTGAUGE_ADAPTER":       "ollama",
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

	// Pass through Ollama-specific environment variables.
	// These are read by the TypeScript OllamaAdapter (resolveModel, resolveBaseUrl, etc.).
	if model := os.Getenv("NIGHTGAUGE_OLLAMA_MODEL"); model != "" {
		env["NIGHTGAUGE_OLLAMA_MODEL"] = model
	}
	if baseURL := os.Getenv("NIGHTGAUGE_OLLAMA_BASE_URL"); baseURL != "" {
		env["NIGHTGAUGE_OLLAMA_BASE_URL"] = baseURL
	}
	if apiKey := os.Getenv("NIGHTGAUGE_OLLAMA_API_KEY"); apiKey != "" {
		env["NIGHTGAUGE_OLLAMA_API_KEY"] = apiKey
	}
	if timeoutMs := os.Getenv("NIGHTGAUGE_OLLAMA_TIMEOUT_MS"); timeoutMs != "" {
		env["NIGHTGAUGE_OLLAMA_TIMEOUT_MS"] = timeoutMs
	}

	return cmd, args, env
}
