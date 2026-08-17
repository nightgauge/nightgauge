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
	if band, ok := models.ClaudeIDTier(m); ok {
		tier = band
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
// entries (research previews included — accepted when explicit) that are
// also reachable through the cli transport (#579). A model with no declared
// cli transport fact (unexpressed/pending) still counts as known — additive
// enforcement, #579 AC4. resolveCodexModel remaps deprecated ids to a live
// replacement before validation (#4018, #4021, #56).
func knownCodexModels() map[string]bool {
	return knownTransportServedModels(models.All(), "openai", codexTransport())
}

// codexTransport resolves the single-authority transport axis (#600) the
// codex adapter's preflight consults. The "codex" entry is a mandatory member
// of adapter_transports (mustLoad asserts it), so a miss here is a
// programming error, not a runtime condition — panicking surfaces it at the
// first call instead of silently mis-gating every codex model.
func codexTransport() string {
	t, ok := models.TransportForAdapter("codex")
	if !ok {
		panic("model registry: adapter_transports has no entry for \"codex\"")
	}
	return t
}

// ValidateCodexModel fails fast when the configured model does not resolve to a
// known, cli-transport-reachable Codex model id — the Go-side mirror of the
// SDK validateModelForAdapter preflight (#4021), generalized with the
// registry's transport facts (#579). It lets the standalone `nightgauge run
// --adapter codex` path reject an invalid model BEFORE spawning the CLI,
// instead of surfacing an opaque CLI error. An empty model is allowed
// (BuildCommand omits --model and the CLI uses its own default). Tier
// aliases and deprecated ids are resolved first, so they validate as their
// concrete replacement.
//
// models.CheckTransportServed is consulted first so a model that IS in the
// registry but explicitly unreachable through the cli transport fails closed
// with an error naming provider, model, and transport, distinct from the
// generic "unknown model" case handled by the closed-set fallback below.
func ValidateCodexModel(model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	resolved := resolveCodexModel(trimmed)
	m, ok, err := models.CheckTransportServed("openai", codexTransport(), resolved)
	if err != nil {
		return err
	}
	// The provider check guards CheckTransportServed's exact-id lookup, which
	// (like Resolve) is deliberately provider-agnostic: a concrete id from a
	// DIFFERENT provider (e.g. copilot's "gpt-4o") must still be rejected,
	// matching the pre-#579 closed-set behavior.
	if ok && !m.Deprecated && m.Provider == "openai" {
		return nil
	}

	known := knownCodexModels()
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
		"model %q is not valid for the codex adapter%s; valid models: %s, or a tier (%s)",
		trimmed, note, strings.Join(valid, ", "), models.BandAlternation(),
	)
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

	// NIGHTGAUGE_OUTPUT_FORMAT is intentionally NOT exported below (#630, per
	// #416 AC3). The codex CLI has no --output-format flag at all — it selects
	// NDJSON with the boolean `--json` set above, consumed by
	// ParseCodexStreamLine. There is no format value to mirror, so exporting a
	// `stream-json` string would invent a format name the codex CLI never
	// accepts and that no codex code path reads. This is NOT the gemini
	// copy-paste drift #416 fixed (see gemini.go): gemini already passed
	// --output-format stream-json on the command line and simply failed to
	// export the matching env var, whereas codex has no such flag to mirror in
	// the first place. Posture is recorded as data — not just here — in
	// outputFormatPosture["codex"] in adapters_test.go, and
	// TestOutputFormatEnvVar_AllAdapters asserts this BuildCommand output
	// against it; change both together or the test fails.
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
	if opts.RunID != "" {
		env[RunIDEnvVar] = opts.RunID
	}

	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	return cmd, args, env
}
