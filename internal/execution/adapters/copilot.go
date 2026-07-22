package adapters

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// CopilotAdapter implements SkillRunner for GitHub Copilot CLI.
// Prompts are delivered via stdin; auth uses env var cascade:
// COPILOT_GITHUB_TOKEN → GH_TOKEN → GITHUB_TOKEN.
type CopilotAdapter struct{}

// NewCopilotAdapter creates a GitHub Copilot CLI adapter.
func NewCopilotAdapter() *CopilotAdapter {
	return &CopilotAdapter{}
}

// Name returns "copilot".
func (a *CopilotAdapter) Name() string {
	return "copilot"
}

// Agentic reports true.
// copilot CLI — agentic coding-agent tool loop.
func (a *CopilotAdapter) Agentic() bool {
	return true
}

// UsesStdin returns true — Copilot CLI receives the prompt via stdin.
func (a *CopilotAdapter) UsesStdin() bool {
	return true
}

// resolveCopilotModel maps Claude-style routing tiers and Claude model ids to a
// concrete Copilot model id via the embedded model registry (internal/models —
// the same data the SDK modelPreflight copilot policy resolves through, giving
// automatic Go↔SDK parity like codex #56). The Go scheduler emits Claude tiers
// ("sonnet"/"opus"/…) and escalation ids ("claude-sonnet-4-6"); the Copilot CLI
// `--model` flag wants a concrete id it hosts (e.g. "gpt-4o", "claude-sonnet-4.5").
// Concrete ids and unknown values pass through unchanged (copilot is an OPEN set
// — its live catalog is larger than the registry band assignments and the CLI
// validates server-side), so an operator can pin any model copilot supports via
// NIGHTGAUGE_COPILOT_MODEL. Deprecated registry ids remap to their replacement.
func resolveCopilotModel(model string) string {
	m := strings.TrimSpace(model)
	// Claude-id PREFIX matching (not registry-exact) so future dated ids like
	// "claude-sonnet-9" still land on the matching band — mirrors
	// resolveCodexModel and the SDK resolver.
	tier := m
	switch {
	case strings.HasPrefix(m, "claude-haiku"):
		tier = "haiku"
	case strings.HasPrefix(m, "claude-sonnet"):
		tier = "sonnet"
	case strings.HasPrefix(m, "claude-opus"), strings.HasPrefix(m, "claude-fable"):
		tier = "opus"
	}
	if resolved, ok := models.Resolve("copilot", tier); ok && resolved.Provider == "copilot" {
		if !resolved.Deprecated {
			return resolved.ID
		}
		if resolved.Replacement != "" {
			return resolved.Replacement
		}
	}
	return m
}

// BuildCommand constructs the copilot CLI command for running a skill.
//
// Contract (verified against the GitHub Copilot CLI command reference,
// https://docs.github.com/en/copilot/reference/copilot-cli-reference):
//   - prompt delivered via stdin (UsesStdin() == true)
//   - `--allow-all-tools` grants unrestricted tool access for autonomous
//     pipeline stage execution (the documented flag; the prior `--allow-all`
//     was not the tool-permission flag). Mirrors codex's bypass-sandbox default.
//   - `--model <id>` selects the AI model; Claude-style tiers/ids from the
//     scheduler are translated to a concrete copilot model id by
//     resolveCopilotModel. Omitted when no model override is set (CLI default).
//   - the default (non-`-s`) output carries a stats footer with the real
//     premium-request count, which ParseCopilotStreamLine reads for accounting;
//     `-s` is deliberately NOT passed so that footer is available.
func (a *CopilotAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "copilot"

	args := []string{
		"--allow-all-tools", // Grant full tool permissions for pipeline execution
	}

	// Inject model routing — the scheduler emits Claude tiers/ids that copilot
	// would reject, so translate to a concrete hosted model id (#52). An empty
	// model omits --model and the CLI uses its own default.
	if opts.Model != "" {
		args = append(args, "--model", resolveCopilotModel(opts.Model))
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER":  fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":          opts.Repo,
		"NIGHTGAUGE_STAGE":         opts.Stage,
		"NIGHTGAUGE_OUTPUT_FORMAT": "stream-json",
		"NIGHTGAUGE_ADAPTER":       "copilot",
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

	// Pass through all Copilot auth tokens independently.
	// The TypeScript CopilotCliAdapter checks these in order: GH_TOKEN → GITHUB_TOKEN → COPILOT_GITHUB_TOKEN.
	// Passing all available tokens preserves the TypeScript side's priority logic.
	if token := os.Getenv("GH_TOKEN"); token != "" {
		env["GH_TOKEN"] = token
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}
	if token := os.Getenv("COPILOT_GITHUB_TOKEN"); token != "" {
		env["COPILOT_GITHUB_TOKEN"] = token
	}

	return cmd, args, env
}
