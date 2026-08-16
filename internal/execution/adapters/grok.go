package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// ValidateModel implements the optional pre-spawn validation hook the
// execution manager checks before BuildCommand (#4021). Two independent
// gates, both fail-closed BEFORE any process is spawned:
//   - ValidateGrokModel (#579): the resolved model must be a known,
//     transport-reachable, non-deprecated xai model — the closed-set gate
//     codex/gemini already have, absorbing #552's silent band-name
//     fallthrough to `grok --model`.
//   - validateGrokEffort (#569): the provider-global NIGHTGAUGE_GROK_EFFORT
//     must sit on the ladder the resolved model declares.
//
// Reads the same env var BuildCommand forwards so the value that is
// validated is exactly the value that would be dispatched.
func (a *GrokAdapter) ValidateModel(model string) error {
	if err := ValidateGrokModel(model); err != nil {
		return err
	}
	return validateGrokEffort(model, os.Getenv("NIGHTGAUGE_GROK_EFFORT"))
}

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

// knownGrokModels returns the CLOSED set of concrete xai model ids the
// pipeline may dispatch through the grok CLI: the registry's non-deprecated
// `provider: "xai"` entries that are also reachable through the cli
// transport (#579) — the same rule ValidateCodexModel/ValidateGeminiModel
// apply for their providers, generalized with the transport fact. A model
// with no declared cli transport fact (unexpressed/pending) still counts as
// known — additive enforcement, #579 AC4.
func knownGrokModels() map[string]bool {
	return knownTransportServedModels(models.All(), "xai", grokTransport())
}

// grokTransport resolves the single-authority transport axis (#600) the grok
// adapter's preflight consults. See codexTransport's doc for why a miss here
// panics rather than degrading silently.
func grokTransport() string {
	t, ok := models.TransportForAdapter("grok")
	if !ok {
		panic("model registry: adapter_transports has no entry for \"grok\"")
	}
	return t
}

// ValidateGrokModel fails fast when the configured model does not resolve to
// a known, cli-transport-reachable Grok/xai model id — the Go-side mirror of
// the SDK validateModelForAdapter preflight (#4021), generalized with the
// registry's transport facts (#579).
//
// Absorbs #552: resolveGrokModel's bare 'return model' on a Resolve miss used
// to hand the raw band name straight to `grok --model`, reaching the CLI
// unchecked — the exact #532 signature. Grok becomes a CLOSED set here, like
// codex/gemini: an unknown id, a deprecated id, or a registry-known id whose
// transports.cli.served is explicitly false are all rejected before spawn.
//
// The transport-unreachable case is checked FIRST via
// models.CheckTransportServed so the error names provider, model, AND
// transport explicitly (rather than only appearing in a generic "valid
// models" list) — e.g. grok-build-0.1 is unselectable for two INDEPENDENT
// reasons: deprecated:true (caught by the closed-set fallback below) and
// transports.cli.served:false (caught here, first).
//
// An empty model is allowed (BuildCommand omits --model and the CLI uses its
// own default).
func ValidateGrokModel(model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	provider := models.ProviderForAdapter("grok")
	resolved := resolveGrokModel(trimmed)
	m, ok, err := models.CheckTransportServed(provider, grokTransport(), resolved)
	if err != nil {
		return err
	}
	// The provider check guards CheckTransportServed's exact-id lookup, which
	// (like Resolve) is deliberately provider-agnostic: a concrete id from a
	// DIFFERENT provider (e.g. a codex "gpt-5.5" mistakenly routed to grok)
	// must still be rejected, matching the pre-#579 closed-set behavior.
	if ok && !m.Deprecated && m.Provider == provider {
		return nil
	}

	known := knownGrokModels()
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
		"model %q is not valid for the grok adapter%s; valid models: %s, or a tier (%s)",
		trimmed, note, strings.Join(valid, ", "), models.BandAlternation(),
	)
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
