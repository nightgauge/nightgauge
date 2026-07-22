package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/graduation"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/spf13/cobra"
)

func knowledgeGraduateCandidatesCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
		minScore   int
	)

	cmd := &cobra.Command{
		Use:          "graduate-candidates <issue>",
		Short:        "Rank ADR graduation candidates for an issue (deterministic, read-only)",
		SilenceUsage: true,
		Long: `Score each ADR block in the per-issue decisions.md and print the
ranked subset that exceeds the graduation threshold.

The command is strictly read-only — it never edits decisions.md. Scoring
combines telemetry signals (recall_hit events for the file) with structural
heuristics (general-vs-specific language, RFC-2119 keywords, filled
Consequences, already-graduated marker, issue-specific title). See
docs/GO_BINARY.md for the full rubric and JSON schema.

Designed to be invoked from the retro skill: when at least one candidate
qualifies, the skill appends a "Graduation Candidates" section to the retro
summary. When no candidates qualify the section is omitted entirely.`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge knowledge graduate-candidates 3596
  nightgauge knowledge graduate-candidates 3596 --json
  nightgauge knowledge graduate-candidates 3596 --min-score 3
  nightgauge knowledge graduate-candidates 3596 --workdir /path/to/repo --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber := 0
			if _, err := fmt.Sscanf(args[0], "%d", &issueNumber); err != nil || issueNumber <= 0 {
				return fmt.Errorf("<issue> must be a positive integer, got %q", args[0])
			}

			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			start := time.Now()
			result, err := graduation.Candidates(workdir, issueNumber, graduation.Options{MinScore: minScore})
			if err != nil {
				return err
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventStats,
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				Path:        result.DecisionsPath,
				DurationMs:  time.Since(start).Milliseconds(),
				Status:      "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if len(result.Candidates) == 0 {
				threshold := minScore
				if threshold <= 0 {
					threshold = graduation.DefaultMinScore
				}
				fmt.Printf("Issue #%d: 0 graduation candidates (threshold %d).\n", issueNumber, threshold)
				return nil
			}

			fmt.Printf("Issue #%d graduation candidates: %d\n\n", issueNumber, len(result.Candidates))
			for _, c := range result.Candidates {
				fmt.Printf("  ADR-%03d %q  score=%d  %s\n", c.ADRIndex, c.ADRTitle, c.Score, strings.Join(c.Signals, ", "))
				fmt.Printf("                                          suggested: %s\n\n", c.SuggestedDest)
			}
			fmt.Println("Run: nightgauge knowledge graduate <issue> --section <docs-path> --adr ADR-NNN")
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().IntVar(&minScore, "min-score", graduation.DefaultMinScore, "Minimum score to qualify as a candidate")

	return cmd
}
