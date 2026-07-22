package adapters

import (
	"fmt"
	"os"
)

// GeminiSdkAdapter implements SkillRunner for Gemini SDK mode.
// This invokes the Gemini CLI with flags optimized for SDK-style execution,
// using GEMINI_API_KEY or GOOGLE_API_KEY for direct API access.
type GeminiSdkAdapter struct{}

// NewGeminiSdkAdapter creates a Gemini SDK adapter.
func NewGeminiSdkAdapter() *GeminiSdkAdapter {
	return &GeminiSdkAdapter{}
}

// Name returns "gemini-sdk".
func (a *GeminiSdkAdapter) Name() string {
	return "gemini-sdk"
}

// Agentic reports true.
// Spawns the agentic gemini CLI (#60) — unlike the TypeScript gemini-sdk
// adapter, which is chat-completion-only and non-agentic.
func (a *GeminiSdkAdapter) Agentic() bool {
	return true
}

// UsesStdin returns false — the Gemini CLI takes the prompt as a positional
// argument (not stdin, not --prompt-file), same contract as gemini.go. #4032
func (a *GeminiSdkAdapter) UsesStdin() bool {
	return false
}

// BuildCommand constructs the gemini CLI command for SDK mode execution.
// SDK mode passes the API key directly and enables streaming output.
//
// The prompt is delivered positionally, exactly as in gemini.go: the Gemini
// CLI has no `--prompt-file` flag, and passing the raw SKILL.md path dropped
// the manager-built prompt entirely (the pre-#4032 broken pattern; fixed for
// this adapter in #53).
func (a *GeminiSdkAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "gemini"

	args := []string{}

	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}

	args = append(args, "--output-format", "stream-json") // Structured output for token tracking

	if opts.Model != "" {
		// Tiers and Claude escalation ids resolve to concrete Gemini models
		// via the registry (#56); the CLI rejects untranslated Claude ids.
		args = append(args, "--model", resolveGeminiModel(opts.Model))
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER":  fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":          opts.Repo,
		"NIGHTGAUGE_STAGE":         opts.Stage,
		"NIGHTGAUGE_OUTPUT_FORMAT": "stream-json",
		"NIGHTGAUGE_ADAPTER":       "gemini-sdk",
	}

	if opts.ContextFile != "" {
		env["NIGHTGAUGE_CONTEXT_FILE"] = opts.ContextFile
	}
	if opts.OutputFile != "" {
		env["NIGHTGAUGE_OUTPUT_FILE"] = opts.OutputFile
	}

	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	// Pass Gemini API keys — prefer GEMINI_API_KEY, fall back to GOOGLE_API_KEY
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		env["GEMINI_API_KEY"] = key
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		env["GOOGLE_API_KEY"] = key
	}

	return cmd, args, env
}
