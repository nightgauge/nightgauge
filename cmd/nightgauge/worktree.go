package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/nightgauge/nightgauge/internal/state"
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

// worktreeSweepOutput is what `sweep --json` emits. An ARRAY of per-root
// results, because the command's default is now the whole workspace (#410) — a
// single bare object could only ever describe one of them.
type worktreeSweepOutput struct {
	Results []execution.WorktreeSweepResult `json:"results"`
	// Warnings carries the in-flight scan's own uncertainty per root, so a
	// reclaim decision made against an incomplete in-flight set is auditable
	// from the command's output instead of only from the operator's memory.
	Warnings []string `json:"warnings,omitempty"`
	// SkippedRoots names roots whose in-flight set could not be read and which
	// were therefore NOT swept at all.
	SkippedRoots []string `json:"skippedRoots,omitempty"`
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
		Long: `Scan the worktrees git has registered across the workspace and remove the
pipeline-created ones (issue-NNN / <repo>-issue-NNN) whose branch carries no
content the default branch is missing.

Every repo root in the workspace is swept by default — a run's worktree lives in
its TARGET repo, so a single-root sweep is blind to exactly the cross-repo runs
that leak most. Pass --workdir to sweep one repository instead.

Merged-ness is decided by content diff against origin/<default>, not by
ancestry: a squash merge leaves the branch tip a non-ancestor of the default
branch, so an ancestry check reports a false negative for every merged branch.

Runs in flight are protected by their runtime snapshots
(.nightgauge/pipeline/runtime-<issue>-<runId>.json), read per root: an issue with
a live snapshot is never reclaimed however merged its branch looks. A root whose
snapshot directory cannot be read is skipped entirely rather than swept blind.

Never removed: the primary checkout, locked worktrees, detached worktrees,
worktrees with uncommitted or untracked changes, branches carrying unmerged
work, branches with no commits of their own (indistinguishable from a worktree
created for a run about to start), and any issue with a run in flight.`,
		SilenceUsage: true,
		Example: `  nightgauge worktree sweep --dry-run
  nightgauge worktree sweep --json
  nightgauge worktree sweep --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			roots, err := worktreeSweepRoots(workdir)
			if err != nil {
				return err
			}

			result := worktreeSweepOutput{}
			for _, root := range roots {
				// THE IN-FLIGHT SET IS RESOLVED PER ROOT, from that root's own
				// snapshot directory (#410). A run's state is rooted in its
				// target repo since #229, so one shared set would answer with a
				// sibling repo's runs — and this command deletes directories.
				active, scanErr := state.ActiveIssuesFromSnapshots(state.PipelineStateDir(root))
				if scanErr != nil {
					// "I could not look" is never "nothing is running" (#296).
					// The only protection this command has against destroying a
					// live run's worktree is this set, so an unreadable snapshot
					// dir disqualifies the root rather than downgrading to an
					// unprotected sweep.
					if len(roots) == 1 {
						// Nothing else to report and nothing was swept: exit
						// non-zero rather than printing a warning that reads
						// like a clean pass.
						return fmt.Errorf("worktree sweep: %s: in-flight set unreadable, refusing to sweep blind: %w", root, scanErr)
					}
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: in-flight set unreadable (%v) — root NOT swept\n", root, scanErr)
					result.SkippedRoots = append(result.SkippedRoots, root)
					continue
				}
				for _, w := range active.Warnings {
					result.Warnings = append(result.Warnings, root+": "+w)
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: %s\n", root, w)
				}

				res, sweepErr := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
					RepoRoot:      root,
					DefaultBranch: defaultBranch,
					ActiveIssues:  active.Issues,
					DryRun:        dryRun,
				})
				if sweepErr != nil {
					// Best-effort per root, like the autonomous sweep: one
					// unreadable sibling must not hide the other roots' leaks.
					if len(roots) == 1 {
						return sweepErr
					}
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: %v\n", root, sweepErr)
					result.SkippedRoots = append(result.SkippedRoots, root)
					continue
				}
				result.Results = append(result.Results, res)
			}

			if jsonOut {
				return printJSON(result)
			}

			multi := len(roots) > 1
			for _, res := range result.Results {
				if multi {
					fmt.Fprintf(out, "%s\n", res.RepoRoot)
				}
				if len(res.Reclaimed) == 0 {
					fmt.Fprintf(out, "  No reclaimable worktrees (scanned %d, base %s).\n", res.Scanned, res.BaseRef)
				}
				for _, wt := range res.Reclaimed {
					verb := "reclaimed"
					if res.DryRun {
						verb = "would reclaim"
					}
					// The DOOR is printed, not inferred (#410): the sweep has two
					// reclaim rules and the default-branch one compares no
					// content at all, so an operator auditing a removal needs to
					// know which rule authorized it.
					fmt.Fprintf(out, "  %-14s %s (branch %s, door %s)\n", verb, wt.Path, wt.Branch, wt.Door)
				}
				for _, wt := range res.Skipped {
					fmt.Fprintf(out, "  %-14s %s (%s)\n", "skipped", wt.Path, wt.Reason)
				}
				for _, e := range res.Errors {
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s\n", e)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Sweep this repository only (default: every repo root in the workspace)")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "Default branch to compare against (default: detected from origin/HEAD)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Classify without removing anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	return cmd
}

// worktreeSweepRoots resolves which repositories the sweep covers.
//
// --workdir is the explicit SINGLE-root override. Without it the default is
// every repo root the workspace resolves (config.WorkspaceRepoRoots — the same
// resolver `doctor`'s leaked-worktree report walks), because since #229 a run's
// worktree is created in its TARGET repo: a sweep rooted only at the invocation
// directory cannot see a cross-repo run's leftovers at all, which is the
// population that accumulates fastest.
//
// Zero resolved roots is an ERROR, not an empty sweep. Even a single-repo
// workspace resolves its primary root, so an empty set means the lookup failed —
// and this is the leak-detection pass: reporting "nothing to reclaim" from a scan
// that never ran is how worktree accumulation stays invisible (#302).
func worktreeSweepRoots(workdir string) ([]string, error) {
	if workdir != "" {
		return []string{workdir}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	roots := config.WorkspaceRepoRoots(cwd)
	if len(roots) == 0 {
		return nil, fmt.Errorf("no repo roots resolved from %s — run inside a git repository or pass --workdir", cwd)
	}
	return roots, nil
}
