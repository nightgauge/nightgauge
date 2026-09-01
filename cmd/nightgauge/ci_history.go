package main

import (
	"fmt"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/triage"
	"github.com/spf13/cobra"
)

// ciHistoryCmd runs `nightgauge ci history` — has this check ever been green?
//
// It is a separate verb rather than a paragraph in a skill because the wrong
// answer silently reframes an entire investigation. A check that has never
// passed is not a regression: there is no "what changed" to find, and a session
// that assumes otherwise spends itself bisecting a history in which nothing was
// ever different. The nightly sweep behind #1262 had failed on every run since
// the day it was added, and two sessions treated it as something that broke.
//
// Exit code is always 0 — this reports, it does not gate.
func ciHistoryCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		workflow   string
		branch     string
		limit      int
		outputJSON bool
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Report whether a workflow has ever concluded successfully",
		Long: `Summarize a bounded window of completed runs for one workflow: whether it has
ever passed, when it last did, and how many consecutive failures precede now
(#1262).

"Never passed" and "regressed" are different failures with different
investigations, and only this distinguishes them.`,
		Example: `  nightgauge ci history --workflow e2e.yml --branch main
  nightgauge ci history --workflow ci.yml --limit 100 --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}
			ownerPart, repoPart := splitRepo(owner, repo)
			runs, err := gh.NewCIService(client).ListWorkflowRuns(cmd.Context(), ownerPart, repoPart, workflow, branch, limit)
			if err != nil {
				return err
			}
			sum := triage.SummarizeHistory(workflow, branch, runs)
			if outputJSON {
				return printJSON(sum)
			}
			fmt.Printf("%s (%s)\n", sum.Workflow, sum.Branch)
			fmt.Printf("  examined: %d completed run(s)\n", sum.Examined)
			fmt.Printf("  ever passed: %t\n", sum.EverPassed)
			if sum.LastSuccess != nil {
				fmt.Printf("  last success: %s  %s\n", sum.LastSuccess.CreatedAt, sum.LastSuccess.HTMLURL)
			}
			fmt.Printf("  consecutive failures: %d\n", sum.ConsecutiveFailures)
			fmt.Printf("  → %s\n", sum.Verdict)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	repoNameFlag(cmd, &repo, "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow file name or path, e.g. e2e.yml (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Restrict to one branch (default: any)")
	cmd.Flags().IntVar(&limit, "limit", 50, "How many completed runs to examine")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("workflow")
	return cmd
}
