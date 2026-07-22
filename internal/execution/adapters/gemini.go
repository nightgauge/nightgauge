package adapters

import (
	"fmt"
	"os"
)

// GeminiAdapter implements SkillRunner for Gemini CLI.
type GeminiAdapter struct{}

// NewGeminiAdapter creates a Gemini CLI adapter.
func NewGeminiAdapter() *GeminiAdapter {
	return &GeminiAdapter{}
}

// Name returns "gemini".
func (a *GeminiAdapter) Name() string {
	return "gemini"
}

// Agentic reports true.
// gemini CLI — agentic tool loop.
func (a *GeminiAdapter) Agentic() bool {
	return true
}

// UsesStdin returns false — the Gemini CLI takes the prompt as a positional
// argument (not stdin, not --prompt-file). The execution manager therefore does
// not pipe stdin for this adapter; the prompt is carried in BuildCommand's args.
// @see Issue #4032
func (a *GeminiAdapter) UsesStdin() bool {
	return false
}

// BuildCommand constructs the gemini CLI command for running a skill.
//
// The current Gemini CLI has NO `--prompt-file` flag (the same broken pattern
// the Go Codex adapter had pre-#4019). The prompt is delivered as a positional
// argument — prepended — exactly as the TypeScript GeminiAdapter does
// (`promptDelivery: "positional"`: `gemini "<prompt>" --output-format
// stream-json`). Output is structured NDJSON via `--output-format stream-json`
// so the Gemini stream parser (StreamFormatGemini) receives token-usage events,
// mirroring claude.go. #4032
func (a *GeminiAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "gemini"

	args := []string{}

	// Positional prompt first (matches the verified TS contract). The built
	// prompt arrives via RunOptions.Prompt; SkillPath is no longer passed as a
	// file because the CLI cannot read a prompt from one.
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}

	args = append(args, "--output-format", "stream-json")

	if opts.Model != "" {
		// Tiers and Claude escalation ids resolve to concrete Gemini models
		// via the registry (#56); the CLI rejects untranslated Claude ids.
		args = append(args, "--model", resolveGeminiModel(opts.Model))
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER": fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":         opts.Repo,
		"NIGHTGAUGE_STAGE":        opts.Stage,
		"NIGHTGAUGE_ADAPTER":      "gemini",
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

	// Pass Gemini API key
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		env["GEMINI_API_KEY"] = key
	}

	return cmd, args, env
}
