package adapters

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// CodexAdapter implements SkillRunner for OpenAI Codex CLI.
type CodexAdapter struct{}

// NewCodexAdapter creates a Codex CLI adapter.
func NewCodexAdapter() *CodexAdapter {
	return &CodexAdapter{}
}

// Name returns "codex".
func (a *CodexAdapter) Name() string {
	return "codex"
}

// Agentic reports true.
// codex exec — sandbox-scoped agentic tool loop (#4026).
func (a *CodexAdapter) Agentic() bool {
	return true
}

// UsesStdin returns true — Codex reads the prompt from stdin via the `-`
// positional argument (mirrors the Claude adapter and the TypeScript
// CodexAdapter). The current Codex CLI has no --prompt-file flag.
func (a *CodexAdapter) UsesStdin() bool {
	return true
}

// resolveCodexModel maps Claude-style routing tiers and Claude model ids to a
// concrete Codex/OpenAI model id via the embedded model registry
// (internal/models — same data the SDK codexModelRegistry.ts derives from,
// parity-tested, #56). The Go scheduler emits Claude tiers ("sonnet"/"opus"/…)
// and escalation ids ("claude-sonnet-4-6"), which the Claude adapters accept
// natively but Codex rejects. Concrete `gpt-5.*` ids and unknown values pass
// through unchanged; deprecated ids remap to their registry replacement.
func resolveCodexModel(model string) string {
	m := strings.TrimSpace(model)
	// Claude-id PREFIX matching (not registry-exact) so future dated ids like
	// "claude-sonnet-9" still land on the matching band — mirrors
	// resolveCodexModelAlias in the SDK.
	tier := m
	switch {
	case strings.HasPrefix(m, "claude-haiku"):
		tier = "haiku"
	case strings.HasPrefix(m, "claude-sonnet"):
		tier = "sonnet"
	case strings.HasPrefix(m, "claude-opus"), strings.HasPrefix(m, "claude-fable"):
		tier = "opus"
	}
	if resolved, ok := models.Resolve("openai", tier); ok && resolved.Provider == "openai" {
		if !resolved.Deprecated {
			return resolved.ID
		}
		if resolved.Replacement != "" {
			return resolved.Replacement
		}
	}
	return m
}

// knownCodexModels returns the CLOSED set of concrete Codex/OpenAI model ids
// the pipeline supports: the registry's non-deprecated `provider: "openai"`
// entries (research previews included — accepted when explicit).
// resolveCodexModel remaps deprecated ids to a live replacement before
// validation (#4018, #4021, #56).
func knownCodexModels() map[string]bool {
	known := make(map[string]bool)
	for _, m := range models.All() {
		if m.Provider == "openai" && !m.Deprecated {
			known[m.ID] = true
		}
	}
	return known
}

// ValidateCodexModel fails fast when the configured model does not resolve to a
// known Codex model id — the Go-side mirror of the SDK validateModelForAdapter
// preflight (#4021). It lets the standalone `nightgauge run --adapter
// codex` path reject an invalid model BEFORE spawning the CLI, instead of
// surfacing an opaque CLI error. An empty model is allowed (BuildCommand omits
// --model and the CLI uses its own default). Tier aliases and deprecated ids
// are resolved first, so they validate as their concrete replacement.
func ValidateCodexModel(model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	resolved := resolveCodexModel(trimmed)
	known := knownCodexModels()
	if !known[resolved] {
		note := ""
		if resolved != trimmed {
			note = fmt.Sprintf(" (resolved to %q)", resolved)
		}
		valid := make([]string, 0, len(known))
		for id := range known {
			valid = append(valid, id)
		}
		sort.Strings(valid)
		return fmt.Errorf(
			"model %q is not valid for the codex adapter%s; valid models: %s, or a tier (haiku|sonnet|opus|fable)",
			trimmed, note, strings.Join(valid, ", "),
		)
	}
	return nil
}

// ValidateModel implements the optional model-validation interface the
// execution manager checks before BuildCommand (#4021).
func (a *CodexAdapter) ValidateModel(model string) error {
	return ValidateCodexModel(model)
}

// BuildCommand constructs the `codex exec` CLI command for running a skill.
//
// Uses the modern non-interactive contract verified against the live Codex CLI
// reference (https://developers.openai.com/codex/cli/reference):
//   - `exec` subcommand for non-interactive runs
//   - `--dangerously-bypass-approvals-and-sandbox` for autonomous, externally
//     isolated CI-style runs — disables BOTH the filesystem sandbox and
//     approval prompts in one flag, replacing the now-deprecated `--full-auto`.
//     Matches the SDK CodexAdapter base args.
//   - `--json` for NDJSON event output (consumed by ParseCodexStreamLine)
//   - prompt via stdin using the `-` positional argument (no --prompt-file)
//
// Claude-style tier aliases and Claude model ids in opts.Model are translated to
// concrete Codex ids by resolveCodexModel — the scheduler emits tiers like
// "sonnet"/"opus" and escalation ids like "claude-sonnet-4-6" that Codex would
// otherwise reject.
func (a *CodexAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	cmd := "codex"

	// Scope the filesystem sandbox to what the stage's allowed-tools justify
	// (#4026). Defaults to `--dangerously-bypass-approvals-and-sandbox` (the prior
	// behavior) when tools imply shell/network or are absent, so autonomous runs
	// are never locked out; tightens to `--sandbox <mode> --ask-for-approval never`
	// for read-only / file-edit-only stages.
	args := []string{"exec"}
	args = append(args, codexSandboxFlags(resolveCodexSandboxMode(opts.AllowedTools))...)
	args = append(args, "--json")

	if opts.Model != "" {
		args = append(args, "--model", resolveCodexModel(opts.Model))
	}

	// `-` tells Codex to read the prompt from stdin, which the execution
	// manager pipes from RunOptions.Prompt when UsesStdin() is true.
	args = append(args, "-")

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER": fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":         opts.Repo,
		"NIGHTGAUGE_STAGE":        opts.Stage,
		"NIGHTGAUGE_ADAPTER":      "codex",
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

	return cmd, args, env
}
