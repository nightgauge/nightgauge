package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/metrics"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/spf13/cobra"
)

func knowledgeMetricsCmd() *cobra.Command {
	var (
		workdir    string
		windowDays int
		staleDays  int
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "metrics",
		Short:        "Aggregate knowledge-events.jsonl into the KB Value dashboard payload",
		SilenceUsage: true,
		Long: `Stream .nightgauge/pipeline/history/knowledge-events.jsonl over a
sliding window and emit the typed Result used by the KB Value dashboard.

The same aggregator backs the knowledge.metrics IPC method so the dashboard
and CLI display identical numbers (#3600).`,
		Example: `  nightgauge knowledge metrics --window 7 --json
  nightgauge knowledge metrics --window 30 --stale-days 14 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}
			if windowDays <= 0 {
				windowDays = 7
			}
			if staleDays < 0 {
				staleDays = 30
			}

			start := time.Now()
			result, err := metrics.Aggregate(workdir, windowDays, staleDays)
			if err != nil {
				return fmt.Errorf("aggregate knowledge metrics: %w", err)
			}

			rc := result.Totals.EventsInRange
			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventStats,
				DurationMs:  time.Since(start).Milliseconds(),
				ResultCount: &rc,
				Status:      "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			return renderMetricsHuman(result)
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().IntVar(&windowDays, "window", 7, "Window size in days (must be > 0)")
	cmd.Flags().IntVar(&staleDays, "stale-days", 30, "Staleness threshold in days")
	cmd.Flags().BoolVar(&outputJSON, "json", true, "Emit JSON to stdout (default true)")

	return cmd
}

func renderMetricsHuman(r metrics.Result) error {
	fmt.Printf("Knowledge metrics (window: %dd, stale: %dd, status: %s)\n",
		r.WindowDays, r.StaleDays, r.Status)
	fmt.Printf("  events_in_range: %d\n", r.Totals.EventsInRange)
	fmt.Printf("  writes=%d reads=%d recalls=%d hits=%d graduations=%d\n",
		r.Totals.Writes, r.Totals.Reads, r.Totals.Recalls,
		r.Totals.RecallHits, r.Totals.Graduations)
	if r.HitRate != nil {
		fmt.Printf("  hit_rate: %.2f%%\n", *r.HitRate*100)
	}
	if len(r.TopRecalled) > 0 {
		fmt.Println("  top recalled:")
		for _, e := range r.TopRecalled {
			fmt.Printf("    %3d  %s\n", e.Hits, e.Path)
		}
	}
	if len(r.StaleEntries) > 0 {
		fmt.Println("  stale entries:")
		for _, e := range r.StaleEntries {
			fmt.Printf("    %4dd  %s\n", e.DaysSinceTouch, e.Path)
		}
	}
	return nil
}
