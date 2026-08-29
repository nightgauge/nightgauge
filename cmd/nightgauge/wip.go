package main

import (
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/spf13/cobra"
)

// wipCmd groups preserved work-in-progress reclamation (#1105).
//
// `preserveWorkInProgress` (#128) commits a killed stage's dirty tree and
// anchors it under refs/nightgauge/wip/ so re-dispatch cannot orphan it. Until
// this command existed that namespace had exactly one writer and zero readers:
// the work was preserved and then unfindable by anyone who did not already
// know the namespace was there.
func wipCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wip",
		Short: "Work preserved from stages the pipeline killed",
	}
	cmd.AddCommand(wipListCmd(), wipPruneCmd())
	return cmd
}

func wipListCmd() *cobra.Command {
	var (
		workdir string
		issue   int
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List work preserved from killed stages",
		Long: `List the preserved-work anchors under refs/nightgauge/wip/.

When a guard terminates a stage with uncommitted work, Nightgauge commits the
worktree in place and anchors that commit outside refs/heads/, so the
re-dispatch teardown (worktree remove --force, branch -D, fresh worktree add)
cannot orphan it. That anchor is frequently the ONLY remaining path back to
the work: the branch and the worktree are both gone by the time anyone looks.

Each row names the issue, the stage branch, the commit to inspect, how many
paths it touches and how long it has been waiting. Restore one with:

  git checkout -b salvage-<issue> <commit>`,
		SilenceUsage: true,
		Example: `  nightgauge wip list
  nightgauge wip list --issue 338 --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := resolveWipWorkdir(workdir)
			if err != nil {
				return err
			}
			refs, err := reclaim.ListWipRefs(root)
			if err != nil {
				return err
			}
			refs = reclaim.WipRefsForIssue(refs, issue)

			if jsonOut {
				// Never `null` for "none": a JSON consumer must be able to
				// tell an empty listing from a missing field.
				if refs == nil {
					refs = []reclaim.WipRef{}
				}
				return printJSON(refs)
			}

			out := cmd.OutOrStdout()
			if len(refs) == 0 {
				fmt.Fprintln(out, "No preserved work-in-progress refs.")
				return nil
			}
			now := time.Now()
			fmt.Fprintf(out, "%d preserved work-in-progress ref(s) in %s:\n", len(refs), root)
			for _, r := range refs {
				issueLabel := "unknown issue"
				if r.Issue > 0 {
					issueLabel = fmt.Sprintf("#%d", r.Issue)
				}
				branch := r.Branch
				if !r.BranchExists {
					// The branch is gone, so this ref is the last copy. Say so
					// on the row rather than rendering a name that no longer
					// resolves as if it were checkoutable.
					branch += " (deleted)"
				}
				fmt.Fprintf(out, "  %-14s %-12s %s  %d path(s)  %dd old\n",
					issueLabel, shortWipSHA(r.Commit), branch, r.FilesChanged, r.AgeDays(now))
				if r.Stage != "" {
					fmt.Fprintf(out, "  %-14s killed in %s\n", "", r.Stage)
				}
				fmt.Fprintf(out, "  %-14s %s\n", "", r.Ref)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Repository root to scan (default: current directory)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Only list refs belonging to this issue (default: all)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	return cmd
}

func wipPruneCmd() *cobra.Command {
	var (
		workdir string
		issue   int
		ref     string
		baseRef string
		discard bool
		dryRun  bool
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove preserved-WIP refs whose work has landed",
		Long: `Remove anchors under refs/nightgauge/wip/ that no longer hold anything.

A ref pins its whole object graph against git gc forever, so the namespace
grows without bound. But it is also the last copy of work the pipeline itself
destroyed, so the default is deliberately narrow: a ref is removed only when
every path its commit touches already reads identically in the base ref —
proof that deleting the anchor loses nothing main does not already have.

Merged-ness is decided by CONTENT, never ancestry: squash merge is this
workspace's only merge shape, and an ancestry test would report that nothing
has ever landed while looking correct doing it.

Everything else is KEPT and reported. There is no age-based expiry: preserved
work does not become less valuable by being old. To remove an anchor whose
work you are choosing to abandon, name it explicitly:

  nightgauge wip prune --issue 338 --discard`,
		SilenceUsage: true,
		Example: `  nightgauge wip prune --dry-run
  nightgauge wip prune
  nightgauge wip prune --ref refs/nightgauge/wip/feat-338-x-1787939337 --discard`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := resolveWipWorkdir(workdir)
			if err != nil {
				return err
			}
			res, err := reclaim.PruneWipRefs(reclaim.WipPruneOptions{
				RepoRoot: root,
				BaseRef:  baseRef,
				Issue:    issue,
				Ref:      ref,
				Discard:  discard,
				DryRun:   dryRun,
			})
			if err != nil {
				return err
			}

			if jsonOut {
				return printJSON(res)
			}

			out := cmd.OutOrStdout()
			if res.Scanned == 0 {
				fmt.Fprintln(out, "No preserved work-in-progress refs matched.")
				return nil
			}
			fmt.Fprintf(out, "Scanned %d preserved ref(s) against %s.\n", res.Scanned, res.BaseRef)
			for _, p := range res.Pruned {
				verb := "pruned"
				if res.DryRun {
					verb = "would prune"
				}
				fmt.Fprintf(out, "  %-14s %s  (%s, %s)\n", verb, p.Ref, shortWipSHA(p.Commit), p.Reason)
			}
			for _, k := range res.Kept {
				fmt.Fprintf(out, "  %-14s %s  (%s, %s, %d path(s))\n",
					"kept", k.Ref, shortWipSHA(k.Commit), k.Reason, k.FilesChanged)
			}
			for _, e := range res.Errors {
				fmt.Fprintf(cmd.ErrOrStderr(), "[WARN] wip prune: %s\n", e)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Repository root to prune (default: current directory)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Only consider refs belonging to this issue (default: all)")
	cmd.Flags().StringVar(&ref, "ref", "", "Only consider this exact ref")
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "Ref to measure landed-ness against (default: origin/<default branch>)")
	cmd.Flags().BoolVar(&discard, "discard", false, "Remove the selected refs even if their work has not landed (requires --issue or --ref)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Classify without deleting any refs")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	return cmd
}

func resolveWipWorkdir(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return cwd, nil
}

func shortWipSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
