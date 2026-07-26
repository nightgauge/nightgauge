package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/spf13/cobra"
)

// pipelineRepairHistoryCmd collapses duplicate run records in a workspace's
// pipeline history (#141).
//
// Reports by default and rewrites only under --apply: collapsing records is
// destructive and irreversible, and the operator needs to see the shape of the
// damage — how many runs, how badly duplicated, whether the directory is
// holding another repository's runs — before agreeing to it.
func pipelineRepairHistoryCmd() *cobra.Command {
	var (
		workdir  string
		apply    bool
		asJSON   bool
		topGroup int
	)
	cmd := &cobra.Command{
		Use:   "repair-history",
		Short: "Collapse duplicate run records in pipeline history (dry run by default)",
		Long: `De-duplicates .nightgauge/pipeline/history/*.jsonl so each pipeline run
occupies exactly one record.

Records are grouped by run identity: run_id when present, otherwise
(repo, issue, started_at) with started_at compared as an instant bucketed to the
second — the duplicates carry the same instant formatted differently by each
writer, so exact string matching does not collapse them. Within a group the
richest record survives (most stages carrying per-stage token data); skeletons
are discarded. Non-run records and any unparseable line are preserved verbatim.

WITHOUT --apply nothing is written: the command reports what it WOULD do and
exits 0. With --apply each daily file is rewritten via temp-file + rename and
index.json is rebuilt from the repaired records.

Records with no repo are counted but never relocated — nothing on such a record
can say which repository it belongs to, and guessing would compound the error
that produced it.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			root := workdir
			if root == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve workdir: %w", err)
				}
				root = cwd
			}

			report, err := state.RepairHistory(root, apply)
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}

			verb := "would remove"
			if report.Applied {
				verb = "removed"
			}
			fmt.Printf("history dir:   %s\n", report.Dir)
			fmt.Printf("lines scanned: %d (%d run records, %d other)\n",
				report.LinesScanned, report.RunRecords, report.NonRunRecords)
			fmt.Printf("distinct runs: %d\n", report.DistinctRuns)
			fmt.Printf("duplicates:    %d (%s)\n", report.Duplicates, verb)
			if report.RunRecords > 0 && report.DistinctRuns > 0 {
				fmt.Printf("inflation:     %.1fx\n",
					float64(report.RunRecords)/float64(report.DistinctRuns))
			}
			if report.Unattributed > 0 {
				fmt.Printf("no repo field: %d run record(s) — cannot be attributed to a repository\n",
					report.Unattributed)
			}
			for repo, n := range report.ForeignRepos {
				fmt.Printf("foreign repo:  %s (%d record(s) in this repository's history)\n", repo, n)
			}

			if topGroup > 0 && len(report.Groups) > 0 {
				fmt.Printf("\nworst duplicated runs:\n")
				for i, g := range report.Groups {
					if i >= topGroup || g.Dropped == 0 {
						break
					}
					fmt.Printf("  #%-6d %-40s %4d records → 1 (%d stages, %d with tokens)\n",
						g.IssueNumber, g.Key, g.Records, g.KeptStages, g.KeptStagesWithTokens)
				}
			}

			if !report.Applied && report.Duplicates > 0 {
				fmt.Printf("\nDry run — nothing written. Re-run with --apply to rewrite.\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Repository root holding .nightgauge/pipeline/history (default: cwd)")
	cmd.Flags().BoolVar(&apply, "apply", false, "Rewrite the history files (without this the command only reports)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the full report as JSON")
	cmd.Flags().IntVar(&topGroup, "top", 10, "Show this many worst-duplicated runs (0 to hide)")
	return cmd
}
