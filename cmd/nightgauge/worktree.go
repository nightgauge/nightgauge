package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/reclaim"
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
	cmd.AddCommand(worktreeRecoverCmd())
	return cmd
}

// worktreeRecoverCmd exposes the scheduler's uncommitted-work rescue (#3542) to
// any caller, which in practice means the extension's HeadlessOrchestrator
// (#223). The rescue existed only as an in-process call on the Go scheduler's
// failure path, so runs driven by the extension — most of them — had none: #221
// left a complete, passing implementation uncommitted in its worktree, one
// sweep away from deletion, while the halt card reported zero file changes.
func worktreeRecoverCmd() *cobra.Command {
	var (
		worktree string
		issue    int
		stage    string
		jsonOut  bool
	)

	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Commit a worktree's uncommitted work so a failed stage's output survives",
		Long: `Stage everything in a pipeline worktree, commit it as an auto-recovery
commit, and push. The pipeline's own UNTRACKED exhaust under .nightgauge/ and
.claude/ is excluded so the rescue never publishes it into the issue branch — but
a tracked bookkeeping file the stage changed IS the deliverable and is committed.

Idempotent: a clean worktree is a no-op, reported as recovered=false. The push
is best-effort — a push failure still leaves the recovery commit on the local
branch, which is what actually prevents the work being lost.`,
		SilenceUsage: true,
		Example: `  nightgauge worktree recover --worktree .worktrees/issue-221 --issue 221 --stage feature-dev
  nightgauge worktree recover --worktree .worktrees/issue-221 --issue 221 --stage feature-dev --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if worktree == "" {
				return fmt.Errorf("--worktree is required")
			}
			if issue <= 0 {
				return fmt.Errorf("--issue is required and must be positive")
			}
			if stage == "" {
				stage = "unknown"
			}

			dirty, err := worktreeHasChanges(worktree)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", worktree, err)
			}
			if !dirty {
				return emitRecoverResult(cmd, jsonOut, false, "worktree is clean — nothing to recover")
			}

			if err := orchestrator.RecoverUncommittedWork(worktree, issue, stage); err != nil {
				return fmt.Errorf("recover %s: %w", worktree, err)
			}
			return emitRecoverResult(cmd, jsonOut, true,
				fmt.Sprintf("committed uncommitted %s work on #%d", stage, issue))
		},
	}

	cmd.Flags().StringVar(&worktree, "worktree", "", "Path to the pipeline worktree (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number the worktree belongs to (required)")
	cmd.Flags().StringVar(&stage, "stage", "", "Stage whose work is being recovered (recorded in the commit message)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output the result as JSON")
	return cmd
}

// worktreeHasChanges reports whether the worktree holds anything worth
// rescuing. The pipeline's own untracked exhaust is excluded for the same
// reason RecoverUncommittedWork unstages it: a worktree whose only diff is the
// run's own state files has produced nothing, and treating that as recoverable
// would create an empty commit on every failed stage.
//
// A TRACKED bookkeeping change is not exhaust and counts (#332). This check
// used to run against ci.DeliverablePathspec(), which excludes `.nightgauge`
// wholesale — so a stage whose entire deliverable was untracking pipeline
// assessments read as "clean", and `recover` reported "nothing to recover" over
// 209 staged deletions. Gating the rescue on the same classifier the rescue
// itself uses is what keeps the two answers from disagreeing.
func worktreeHasChanges(path string) (bool, error) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return false, err
	}
	return reclaim.ClassifyStatus(string(out)).Blocked(), nil
}

func emitRecoverResult(cmd *cobra.Command, jsonOut, recovered bool, message string) error {
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"recovered": recovered,
			"message":   message,
		})
	}
	fmt.Fprintln(cmd.OutOrStdout(), message)
	return nil
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
