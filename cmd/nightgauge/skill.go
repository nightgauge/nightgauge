package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skillrender"
	"github.com/spf13/cobra"
)

// skillCmd groups skill-composition verbs.
func skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Compose executable skill text (render)",
		Long: `Skill composition verbs. ` + "`render`" + ` is the single composer for a stage's
executable skill text: frontmatter parsing, _includes expansion, model-overlay
resolution, and absolute-path rewriting (ADR 016).`,
	}
	cmd.AddCommand(skillRenderCmd())
	return cmd
}

// skillRenderCmd implements `nightgauge skill render` (#78).
func skillRenderCmd() *cobra.Command {
	var (
		stage      string
		model      string
		adapter    string
		roots      []string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Compose a stage's skill with includes and model overlays applied",
		Long: `Compose the executable skill text for a (stage, model) pair.

Resolution is additive and fail-open (ADR 016 §2-§4). Overlay keys are derived
from the model registry, general to specific — provider, then each capability
band, then the concrete id — and every matching fragment is appended in that
order, shared before skill-specific. Missing fragments are skipped silently:
absence is the norm, not an error.

An unknown model, a local provider (ollama/lm-studio, which have no registry
entries by design), or an unreadable fragment all render base-only and exit 0.
A malformed overlay must never take down a pipeline run.

Skill LOCATION is the caller's responsibility: pass --skills-root (repeatable,
first match wins). The binary cannot reproduce the extension's bundle
discovery, so it owns only parsing, expansion, resolution, and composition.

Composed text goes to stdout. With --json, the provenance envelope goes to
stdout instead — resolved keys, the fragments applied in order, the injection
site, and whether a whole-file override replaced the base.`,
		Example: `  # Base-only render (no model): exactly today's behavior
  nightgauge skill render --stage feature-dev --skills-root ./skills

  # Overlay-aware render for a concrete model
  nightgauge skill render --stage feature-dev --model claude-opus-5 --skills-root ./skills

  # What resolved, and why
  nightgauge skill render --stage pr-merge --model opus --skills-root ./skills --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stage == "" {
				return fmt.Errorf("--stage is required (one of: %s)", strings.Join(knownStages(), ", "))
			}
			res, err := skillrender.Render(skillrender.Options{
				Stage:       stage,
				Model:       model,
				Adapter:     adapter,
				SkillsRoots: roots,
				// Warnings go to stderr so they never corrupt piped stdout,
				// which is the composed prompt a caller feeds to an agent.
				Warn: func(msg string) { fmt.Fprintln(os.Stderr, "warning:", msg) },
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), res.Content)
			return err
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "Pipeline stage to render ("+strings.Join(knownStages(), ", ")+")")
	cmd.Flags().StringVar(&model, "model", "", "Concrete model id or tier band; empty renders base-only")
	cmd.Flags().StringVar(&adapter, "adapter", "", "Execution adapter, selects the provider for tier resolution (claude, codex, gemini, …)")
	cmd.Flags().StringArrayVar(&roots, "skills-root", nil, "Directory containing skill directories (repeatable; first match wins)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the provenance envelope instead of the composed text")
	return cmd
}

// knownStages lists renderable stages in a stable order for help text.
func knownStages() []string {
	out := make([]string, 0, len(skillrender.StageSkillDirs))
	for stage := range skillrender.StageSkillDirs {
		out = append(out, stage)
	}
	sort.Strings(out)
	return out
}
