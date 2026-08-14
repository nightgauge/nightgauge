package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// GrokAdapter implements SkillRunner for the Grok Build CLI.
type GrokAdapter struct{}

// NewGrokAdapter creates a Grok CLI adapter.
func NewGrokAdapter() *GrokAdapter {
	return &GrokAdapter{}
}

func (a *GrokAdapter) Name() string { return "grok" }

func (a *GrokAdapter) Agentic() bool { return true }

// UsesStdin is false — Grok headless ignores piped stdin. The prompt is
// delivered with --prompt-file (or -p as a fallback).
func (a *GrokAdapter) UsesStdin() bool { return false }

func resolveGrokModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if m, ok := models.Resolve(models.ProviderForAdapter("grok"), model); ok {
		return m.ID
	}
	return model
}

func (a *GrokAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "grok"
	if override := os.Getenv("NIGHTGAUGE_GROK_CLI_COMMAND"); override != "" {
		cmd = override
	}

	args := []string{
		"--output-format", "streaming-json",
		"--always-approve",
		"--no-auto-update",
	}

	if opts.Prompt != "" {
		path := writeGrokPromptFile(opts.Prompt)
		if path != "" {
			args = append(args, "--prompt-file", path)
		} else {
			args = append(args, "-p", opts.Prompt)
		}
	}

	if model := resolveGrokModel(opts.Model); model != "" {
		args = append(args, "--model", model)
	}

	if effort := grokCliEffortFlag(os.Getenv("NIGHTGAUGE_GROK_EFFORT")); effort != "" {
		args = append(args, "--effort", effort)
	}

	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	} else {
		args = append(args, "--max-turns", "200")
	}

	if opts.WorktreeDir != "" {
		args = append(args, "--cwd", opts.WorktreeDir)
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER": fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":         opts.Repo,
		"NIGHTGAUGE_STAGE":        opts.Stage,
		"NIGHTGAUGE_ADAPTER":      "grok",
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
	if opts.RunID != "" {
		env[RunIDEnvVar] = opts.RunID
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}
	if key := os.Getenv("XAI_API_KEY"); key != "" {
		env["XAI_API_KEY"] = key
	}

	return cmd, args, env
}

func writeGrokPromptFile(prompt string) string {
	dir := os.TempDir()
	f, err := os.CreateTemp(dir, "nightgauge-grok-prompt-*.txt")
	if err != nil {
		return ""
	}
	path := f.Name()
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return ""
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return ""
	}
	return filepath.Clean(path)
}
