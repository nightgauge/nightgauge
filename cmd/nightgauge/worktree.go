package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/spf13/cobra"
)

// worktreeCmd groups worktree reclamation (#110). Removal normally happens
// inline when a run finishes and, for runs that never reached their cleanup
// step, folds into the autonomous reconcile pass — `sweep` exposes the same
// pass for manual/CI invocation.
func worktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Pipeline worktree reclamation",
	}
	cmd.AddCommand(worktreeSweepCmd())
	return cmd
}

func worktreeSweepCmd() *cobra.Command {
	var (
		workdir       string
		defaultBranch string
		dryRun        bool
		jsonOut       bool
	)

	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Reclaim pipeline worktrees whose branch already landed",
		Long: `Scan the worktrees git has registered for this repository and remove the
pipeline-created ones (issue-NNN / <repo>-issue-NNN) whose branch carries no
content the default branch is missing.

Merged-ness is decided by content diff against origin/<default>, not by
ancestry: a squash merge leaves the branch tip a non-ancestor of the default
branch, so an ancestry check reports a false negative for every merged branch.

Never removed: the primary checkout, locked worktrees, detached worktrees,
worktrees with uncommitted or untracked changes, branches carrying unmerged
work, and branches with no commits of their own (indistinguishable from a
worktree created for a run about to start).`,
		SilenceUsage: true,
		Example: `  nightgauge worktree sweep --dry-run
  nightgauge worktree sweep --json
  nightgauge worktree sweep --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
				RepoRoot:      workdir,
				DefaultBranch: defaultBranch,
				DryRun:        dryRun,
			})
			if err != nil {
				return err
			}

			if jsonOut {
				return printJSON(res)
			}

			if len(res.Reclaimed) == 0 {
				fmt.Printf("No reclaimable worktrees (scanned %d, base %s).\n", res.Scanned, res.BaseRef)
			}
			for _, wt := range res.Reclaimed {
				verb := "reclaimed"
				if res.DryRun {
					verb = "would reclaim"
				}
				fmt.Printf("  %-14s %s (branch %s)\n", verb, wt.Path, wt.Branch)
			}
			for _, wt := range res.Skipped {
				fmt.Printf("  %-14s %s (%s)\n", "skipped", wt.Path, wt.Reason)
			}
			for _, e := range res.Errors {
				fmt.Fprintf(os.Stderr, "[WARN] worktree sweep: %s\n", e)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Repository root to sweep (default: current directory)")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "Default branch to compare against (default: detected from origin/HEAD)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Classify without removing anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	return cmd
}
