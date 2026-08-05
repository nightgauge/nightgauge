package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/spf13/cobra"
)

// stashCmd groups pipeline stash reclamation (#330). Reclamation normally
// happens inline when a stage finishes (ResetPipeline drops the stashes the
// run created); `sweep` is the reconcile pass for stages that never reached
// that point, which is every stage killed by a window reload, a crash, or a
// timeout.
func stashCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stash",
		Short: "Pipeline stash reclamation",
	}
	cmd.AddCommand(stashSweepCmd())
	return cmd
}

func stashSweepCmd() *cobra.Command {
	var (
		workdir string
		issue   int
		drop    bool
		dryRun  bool
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Reclaim git stashes the pipeline created and never took back",
		Long: `Scan this repository's stash stack and reclaim the entries the pipeline
created, identified by the "nightgauge:" marker their message carries.

Default action is RESTORE (git stash pop): a baseline stash holds work a stage
moved out of the way and never moved back, so discarding it silently is the
failure this command exists to end. --drop discards instead, and is an explicit
opt-in.

Never touched: any stash without the marker. Ownership cannot be proven for a
free-form message, an operator's own "wip before the refactor" is
indistinguishable from a pre-marker pipeline stash, and the conservative answer
is the only safe one when the action destroys content. Those are reported as
"unowned" so they are visible without being acted on.

A stash is also left alone when restoring it would collide with uncommitted
work already in the tree; the report says so rather than forcing a conflict.`,
		SilenceUsage: true,
		Example: `  nightgauge stash sweep --dry-run
  nightgauge stash sweep --issue 289
  nightgauge stash sweep --drop --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			action := reclaim.StashRestore
			if drop {
				action = reclaim.StashDrop
			}

			res, err := reclaim.SweepPipelineStashes(reclaim.StashSweepOptions{
				RepoRoot: workdir,
				Issue:    issue,
				Action:   action,
				DryRun:   dryRun,
			})
			if err != nil {
				return err
			}

			if jsonOut {
				return printJSON(res)
			}

			out := cmd.OutOrStdout()
			if len(res.Reclaimed) == 0 {
				fmt.Fprintf(out, "No reclaimable pipeline stashes (scanned %d).\n", res.Scanned)
			}
			for _, s := range res.Reclaimed {
				// Spelled out rather than suffixed: `string(action) + "d"`
				// renders "dropd".
				verb := "restored"
				if s.Action == reclaim.StashDrop {
					verb = "dropped"
				}
				if res.DryRun {
					verb = "would " + string(s.Action)
				}
				fmt.Fprintf(out, "  %-14s %s  #%d %s (%dd old)\n", verb, s.Ref, s.Issue, s.Stage, s.AgeDays)
			}
			for _, s := range res.Skipped {
				fmt.Fprintf(out, "  %-14s %s  %s (%dd old)\n", "skipped", s.Ref, s.Reason, s.AgeDays)
			}
			for _, e := range res.Errors {
				fmt.Fprintf(cmd.ErrOrStderr(), "[WARN] stash sweep: %s\n", e)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Repository root to sweep (default: current directory)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Only reclaim stashes belonging to this issue (default: all)")
	cmd.Flags().BoolVar(&drop, "drop", false, "Discard the stashes instead of restoring them")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Classify without touching the stash stack")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	return cmd
}
