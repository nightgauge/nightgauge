package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/spf13/cobra"
)

// stampResult is the --json shape of `knowledge stamp`.
type stampResult struct {
	Path    string                `json:"path"`
	Changed bool                  `json:"changed"`
	Block   *okf.FrontmatterBlock `json:"frontmatter"`
}

// dispatchModelEnvVar carries the model a stage was dispatched to run on. The
// adapters export it alongside NIGHTGAUGE_STAGE.
const dispatchModelEnvVar = "NIGHTGAUGE_DISPATCH_MODEL"

// stageActorFromEnv builds the provenance actor for a stage from its name and
// the dispatched model, both of which the binary controls. Neither is ever
// taken from model output.
func stageActorFromEnv(stage string) (string, error) {
	model := strings.TrimSpace(os.Getenv(dispatchModelEnvVar))
	if model == "" {
		return "", fmt.Errorf("--stage needs %s to build the actor; it is exported by the adapters, so this is an out-of-pipeline invocation — pass --generated-by explicitly", dispatchModelEnvVar)
	}
	actor, err := okf.StageActor(stage, model)
	if err != nil {
		return "", err
	}
	return actor, nil
}

func knowledgeStampCmd() *cobra.Command {
	var (
		entryType   string
		stage       string
		generatedBy string
		sources     []string
		verifiedBy  string
		staleAfter  string
		status      string
		workdir     string
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:          "stamp <path>",
		Short:        "Record provenance on a knowledge entry",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Long: `Merge provenance fields into a knowledge entry's YAML frontmatter.

This is the ONLY writer of the provenance fields. Skills and stages call it
instead of editing frontmatter with sed or a heredoc, so every value that
lands in the base has been through the same validation.

The body is never inspected or modified — the file is split, only the
frontmatter block is rebuilt, and the two are re-joined.

Merge rules:

  type         set only when the entry has none; never overwrites
  generated    replaced; it names the last producer, it is not a log
  verified     appended, unless an event with the same actor already exists
  sources      appended, unless an entry with the same resource already exists
  status       set only when --status is given; never cleared
  stale_after  set only when --stale-after is given; never cleared

Actors take one of three forms: <producer>/<version> for an agent stage and
its model, human:<id> for a person, process:<id> for a deterministic writer.

A stage records its own provenance with --stage rather than --generated-by:
the binary reads the dispatched model out of NIGHTGAUGE_DISPATCH_MODEL and
builds the actor itself, so no model-authored string can become a provenance
claim. NIGHTGAUGE_DISPATCH_MODEL is the model the stage was DISPATCHED to run
on — the served model is only observable after the stage exits, so it cannot
be an input to a stamp the stage itself makes.

A --source is an https:// URL, a bundle-absolute path beginning with "/", or a
repository-relative path that resolves inside the repository. Everything else
is rejected and nothing is written.

<path> must resolve inside .nightgauge/knowledge/.`,
		Example: `  # From a pipeline stage
  nightgauge knowledge stamp features/42-photo-upload/PRD.md \
    --stage "$NIGHTGAUGE_STAGE" \
    --source https://github.com/nightgauge/nightgauge/issues/42

  nightgauge knowledge stamp features/42-photo-upload/PRD.md \
    --generated-by feature-planning/claude-sonnet-5 \
    --source https://github.com/nightgauge/nightgauge/issues/42

  nightgauge knowledge stamp features/42-photo-upload/decisions.md \
    --verified-by human:octocat --status stable --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			entryPath, err := okf.ResolveEntryPath(args[0], workdir)
			if err != nil {
				return err
			}

			// --stage is the form skills use: the binary constructs the actor
			// from the stage name and the dispatched model, so no model-
			// authored string can ever become a provenance claim.
			if stage != "" {
				if generatedBy != "" {
					return fmt.Errorf("--stage and --generated-by are alternatives; pass one")
				}
				generatedBy, err = stageActorFromEnv(stage)
				if err != nil {
					return err
				}
			}

			// Validate every source before touching the file, so a rejected
			// stamp leaves the entry byte-identical.
			in := okf.StampInput{
				Type:        entryType,
				GeneratedBy: generatedBy,
				VerifiedBy:  verifiedBy,
				StaleAfter:  staleAfter,
				Status:      status,
			}
			for _, raw := range sources {
				resource, err := okf.ValidateSource(raw, workdir)
				if err != nil {
					return err
				}
				in.Sources = append(in.Sources, okf.Source{Resource: resource})
			}

			if in.Empty() {
				return fmt.Errorf("nothing to stamp: give at least one of --type, --stage, --generated-by, --verified-by, --source, --status, --stale-after")
			}

			start := time.Now()
			block, changed, err := okf.Stamp(entryPath, in)
			if err != nil {
				return err
			}

			relPath, relErr := filepath.Rel(workdir, entryPath)
			if relErr != nil {
				relPath = entryPath
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:       telemetry.EventWrite,
				Scope:      "stamp",
				Path:       relPath,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(stampResult{Path: relPath, Changed: changed, Block: block})
			}

			if !changed {
				fmt.Printf("Unchanged (already stamped): %s\n", relPath)
				return nil
			}
			fmt.Printf("Stamped: %s\n", relPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&stage, "stage", "", "Pipeline stage; the actor is built from this and the dispatched model (NIGHTGAUGE_DISPATCH_MODEL)")
	cmd.Flags().StringVar(&entryType, "type", "", "Entry type, set only when the entry has none (never overwrites an existing type)")
	cmd.Flags().StringVar(&generatedBy, "generated-by", "", "Actor that produced this entry (<producer>/<version>, human:<id> or process:<id>)")
	cmd.Flags().StringArrayVar(&sources, "source", nil, "Material this entry was derived from; repeatable")
	cmd.Flags().StringVar(&verifiedBy, "verified-by", "", "Actor confirming this entry; appended to the verified log")
	cmd.Flags().StringVar(&staleAfter, "stale-after", "", "RFC3339 instant past which this entry stops being current guidance")
	cmd.Flags().StringVar(&status, "status", "", "Lifecycle status: draft, stable or deprecated")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output the merged frontmatter as JSON")

	return cmd
}
