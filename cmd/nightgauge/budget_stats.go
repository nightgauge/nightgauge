package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/spf13/cobra"
)

// budgetStatsCmd implements `nightgauge budget-stats`.
//
// Reads all exit-record JSONL files (or those within the last N days),
// groups by (repo, stage, size_label), and emits p50/p75/p95 cost +
// duration + ok_rate — either as a human-readable table or JSON.
func budgetStatsCmd() *cobra.Command {
	var (
		workdir    string
		filterRepo string
		filterStage string
		jsonOutput bool
		limitDays  int
	)
	cmd := &cobra.Command{
		Use:   "budget-stats",
		Short: "Aggregate stage-exit records into budget statistics (#3667)",
		Long: `Reads per-stage exit-record JSONL files under
.nightgauge/pipeline/exit-records/<UTC-day>.jsonl and computes p50/p75/p95
cost statistics grouped by (repo, stage, size_label).

Use --json for machine-readable output (one JSON array). Use --repo / --stage
to narrow the result to a single repo or stage. Use --days to limit the window
(default: all available history).`,
		Example: `  nightgauge budget-stats
  nightgauge budget-stats --repo nightgauge/nightgauge --stage pr-create
  nightgauge budget-stats --days 30 --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := workdir
			if root == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("getcwd: %w", err)
				}
				root = cwd
			}

			records, err := loadExitRecordsForStats(root, limitDays)
			if err != nil {
				return err
			}

			// Apply optional repo / stage filters.
			if filterRepo != "" || filterStage != "" {
				filtered := records[:0]
				for _, r := range records {
					if filterRepo != "" && r.Repo != filterRepo {
						continue
					}
					if filterStage != "" && r.Stage != filterStage {
						continue
					}
					filtered = append(filtered, r)
				}
				records = filtered
			}

			stats := diagnostics.ComputeStats(records)

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(stats)
			}
			printBudgetStatsHuman(stats)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: CWD)")
	cmd.Flags().StringVar(&filterRepo, "repo", "", "Filter to a single repo (owner/name)")
	cmd.Flags().StringVar(&filterStage, "stage", "", "Filter to a single pipeline stage")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON array instead of a table")
	cmd.Flags().IntVar(&limitDays, "days", 0, "Limit to records from the last N days (0 = all)")
	return cmd
}

// loadExitRecordsForStats reads all daily files (or those within the last N
// days) and returns the flattened record slice. Records are read in any order
// because ComputeStats groups before sorting.
func loadExitRecordsForStats(root string, limitDays int) ([]diagnostics.StageExitRecord, error) {
	dir := diagnostics.ExitRecordsDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read exit-records dir: %w", err)
	}

	var cutoff time.Time
	if limitDays > 0 {
		cutoff = time.Now().UTC().AddDate(0, 0, -limitDays)
	}

	var out []diagnostics.StageExitRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if limitDays > 0 {
			// Filename is YYYY-MM-DD.jsonl — parse day and skip if before cutoff.
			dayStr := strings.TrimSuffix(e.Name(), ".jsonl")
			if t, err := time.Parse("2006-01-02", dayStr); err == nil {
				if t.Before(cutoff) {
					continue
				}
			}
		}
		path := filepath.Join(dir, e.Name())
		recs, err := readDailyFileStats(path)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// readDailyFileStats parses a single daily JSONL file with no filtering.
func readDailyFileStats(path string) ([]diagnostics.StageExitRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []diagnostics.StageExitRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec diagnostics.StageExitRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping malformed line in %s: %v\n", path, err)
			continue
		}
		out = append(out, rec)
	}
	return out, scanner.Err()
}

// printBudgetStatsHuman renders the stats as a fixed-width table.
func printBudgetStatsHuman(stats []diagnostics.StageStats) {
	if len(stats) == 0 {
		fmt.Println("No exit records found.")
		return
	}

	// Sort: repo → stage → size_label (already sorted by ComputeStats, but
	// be explicit for reader confidence).
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Repo != stats[j].Repo {
			return stats[i].Repo < stats[j].Repo
		}
		if stats[i].Stage != stats[j].Stage {
			return stats[i].Stage < stats[j].Stage
		}
		return stats[i].SizeLabel < stats[j].SizeLabel
	})

	fmt.Printf("%-30s  %-20s  %-4s  %5s  %8s  %8s  %8s  %9s  %7s\n",
		"repo", "stage", "size", "n", "p50_cost", "p75_cost", "p95_cost", "med_dur", "ok_rate")
	fmt.Println(strings.Repeat("-", 110))
	for _, s := range stats {
		size := s.SizeLabel
		if size == "" {
			size = "?"
		}
		fmt.Printf("%-30s  %-20s  %-4s  %5d  $%7.3f  $%7.3f  $%7.3f  %9s  %6.0f%%\n",
			truncStr(s.Repo, 30),
			truncStr(s.Stage, 20),
			size,
			s.N,
			s.P50Cost,
			s.P75Cost,
			s.P95Cost,
			fmtMs(s.MedianDurMs),
			s.OkRate*100,
		)
	}
	fmt.Printf("\n%d group(s).\n", len(stats))
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
