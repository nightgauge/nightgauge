package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skills"
	"github.com/spf13/cobra"
)

// skillsCmd is the top-level "skills" command group: telemetry over the skill
// catalog (which skills are popular, under-triggering, or never triggered).
func skillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Skill-catalog telemetry",
	}
	cmd.AddCommand(skillsUsageCmd())
	return cmd
}

func skillsUsageCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
		neverOnly  bool
	)
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Aggregate skill-usage telemetry (.nightgauge/skills/usage.jsonl)",
		Long: `Aggregate the skill-usage log written by the PreToolUse(Skill) hook into
per-skill trigger counts and last-seen timestamps, and (against the skills/
catalog) flag never-triggered skills — usually a sign a skill's description is
not triggering. Missing log → empty report, never an error.`,
		Example:      "  nightgauge skills usage\n  nightgauge skills usage --json\n  nightgauge skills usage --never-triggered",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := workdir
			if root == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("getcwd: %w", err)
				}
				root = wd
			}

			records, err := skills.ReadUsage(root)
			if err != nil {
				return err
			}
			catalog, err := skills.CatalogNames(root)
			if err != nil {
				return err
			}
			stats := skills.Aggregate(records, catalog)
			if neverOnly {
				filtered := stats[:0]
				for _, s := range stats {
					if s.NeverSeen {
						filtered = append(filtered, s)
					}
				}
				stats = filtered
			}

			if jsonOutput {
				return printJSON(stats)
			}
			printSkillsUsage(stats)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON instead of a table")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	cmd.Flags().BoolVar(&neverOnly, "never-triggered", false, "Show only catalog skills with zero recorded invocations")
	return cmd
}

func printSkillsUsage(stats []skills.Stats) {
	if len(stats) == 0 {
		fmt.Println("No skill-usage data found (and no skills/ catalog to compare against).")
		return
	}
	fmt.Printf("%-44s %10s  %s\n", "Skill", "Triggers", "Last seen")
	fmt.Println(strings.Repeat("-", 78))
	var triggered, never int
	for _, s := range stats {
		last := s.LastSeen
		if s.NeverSeen {
			last = "(never)"
			never++
		} else {
			triggered++
		}
		fmt.Printf("%-44s %10d  %s\n", s.Skill, s.TriggerCount, last)
	}
	fmt.Printf("\n%d triggered, %d never-triggered.\n", triggered, never)
}
