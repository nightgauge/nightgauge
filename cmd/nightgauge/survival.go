package main

import (
	"fmt"
	"log"
	"os"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/spf13/cobra"
)

// survivalCmd groups the post-merge survival outcome model operations (#4151).
// Capture happens automatically on the pr-merge path; these commands finalize
// and inspect the captured records. Finalization normally folds into the
// autonomous reconcile sweep (poll-on-reconcile, no new cron) — `sweep` exposes
// the same pass for manual/CI invocation.
func survivalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "survival",
		Short: "Post-merge survival outcome model (capture/detect)",
	}
	cmd.AddCommand(survivalSweepCmd())
	cmd.AddCommand(survivalListCmd())
	return cmd
}

func survivalSweepCmd() *cobra.Command {
	var (
		workdir    string
		windowDays int
		nowRFC     string
	)

	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Finalize due pending survival records (revert/breakage detection)",
		Long: `Scan the survival store for pending records whose observation window has
elapsed, run deterministic revert/breakage detection against GitHub, and finalize
each to survived / reverted / broke / unobserved. Best-effort and non-blocking:
records with no negative evidence inside their window are left pending; a record
never re-observed by 2×window ages out to "unobserved" (no signal).`,
		SilenceUsage: true,
		Example: `  nightgauge survival sweep
  nightgauge survival sweep --window-days 7 --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			now := time.Now()
			if nowRFC != "" {
				parsed, err := time.Parse(time.RFC3339, nowRFC)
				if err != nil {
					return fmt.Errorf("--now must be RFC3339: %w", err)
				}
				now = parsed
			}
			if windowDays <= 0 {
				windowDays = survival.DefaultWindowDays
			}

			store := survival.NewStore(workdir)
			res, err := survival.Sweep(cmd.Context(), store, gh.NewSurvivalDetector(), now, windowDays)
			if err != nil {
				return err
			}

			// (#4152/#4153) Feed newly-finalized verdicts into bias-safe
			// calibration so a manual/CI `survival sweep` has the same
			// effect as the autonomous reconcile sweep. Best-effort: a
			// calibration error is logged, never fails the command or
			// changes the printed sweep result.
			if len(res.FinalizedRecords) > 0 {
				calRes := gh.NewOutcomeService(workdir).ApplySurvivalVerdicts(res.FinalizedRecords)
				if calRes.Error != "" {
					log.Printf("survival sweep: calibration error: %v", calRes.Error)
				}
			}

			return printJSON(res)
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().IntVar(&windowDays, "window-days", survival.DefaultWindowDays, "Post-merge observation window in days")
	cmd.Flags().StringVar(&nowRFC, "now", "", "Override 'now' as RFC3339 (testing/replay)")
	return cmd
}

func survivalListCmd() *cobra.Command {
	var (
		workdir string
		verdict string
	)

	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List captured survival records",
		SilenceUsage: true,
		Example: `  nightgauge survival list
  nightgauge survival list --verdict pending`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			store := survival.NewStore(workdir)
			records, err := store.Load()
			if err != nil {
				return err
			}
			if verdict != "" {
				filtered := records[:0:0]
				for _, r := range records {
					if string(r.Verdict) == verdict {
						filtered = append(filtered, r)
					}
				}
				records = filtered
			}
			return printJSON(records)
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().StringVar(&verdict, "verdict", "", "Filter by verdict (pending|survived|reverted|broke|unobserved)")
	return cmd
}
