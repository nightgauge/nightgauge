package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/spf13/cobra"
)

// gateTally totals gate spend over any group of stage exits.
type gateTally struct {
	Stages     int   `json:"stages"`
	Runs       int   `json:"gate_runs"`
	Seconds    int   `json:"gate_seconds"`
	Reused     int   `json:"gate_reused"`
	Unfinished int   `json:"gate_unfinished"`
	ElapsedMs  int64 `json:"elapsed_ms"`
}

func (t *gateTally) add(rec diagnostics.StageExitRecord) {
	t.Stages++
	t.Runs += rec.GateRuns
	t.Seconds += rec.GateSeconds
	t.Reused += rec.GateReused
	t.Unfinished += rec.GateUnfinished
	t.ElapsedMs += rec.ElapsedMs
}

// gateRunSummary is one pipeline run, or one issue's records with no run id.
type gateRunSummary struct {
	Issue  int                   `json:"issue"`
	RunID  string                `json:"run_id,omitempty"`
	Total  gateTally             `json:"total"`
	Stages map[string]*gateTally `json:"stages"`
}

// gateSummary is the JSON shape of `nightgauge exit-records gate-summary`.
type gateSummary struct {
	Runs []gateRunSummary `json:"runs"`
	// FeatureDevReview splits feature-dev by ReviewPhases, because a stage that
	// did its review takes longer for a reason that has nothing to do with gates.
	// The empty key is a record that could not say.
	FeatureDevReview map[string]*gateTally `json:"feature_dev_review"`
}

// summarizeGates totals records per run and per stage.
func summarizeGates(records []diagnostics.StageExitRecord) gateSummary {
	sum := gateSummary{FeatureDevReview: map[string]*gateTally{}}
	byRun := map[string]*gateRunSummary{}
	var keys []string
	for _, rec := range records {
		key := fmt.Sprintf("%012d/%s", rec.Issue, rec.RunID)
		run, ok := byRun[key]
		if !ok {
			run = &gateRunSummary{Issue: rec.Issue, RunID: rec.RunID, Stages: map[string]*gateTally{}}
			byRun[key] = run
			keys = append(keys, key)
		}
		run.Total.add(rec)
		if run.Stages[rec.Stage] == nil {
			run.Stages[rec.Stage] = &gateTally{}
		}
		run.Stages[rec.Stage].add(rec)
		if rec.Stage == "feature-dev" {
			if sum.FeatureDevReview[rec.ReviewPhases] == nil {
				sum.FeatureDevReview[rec.ReviewPhases] = &gateTally{}
			}
			sum.FeatureDevReview[rec.ReviewPhases].add(rec)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		sum.Runs = append(sum.Runs, *byRun[key])
	}
	return sum
}

// readExitRecordsSince reads every daily file on or after since (YYYY-MM-DD; an
// empty since reads them all). The file name is its UTC day, so the comparison
// is lexical.
func readExitRecordsSince(root string, issue int, since string) ([]diagnostics.StageExitRecord, error) {
	dir, err := diagnostics.ExitRecordsDir(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read exit-records dir: %w", err)
	}
	var out []diagnostics.StageExitRecord
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || strings.TrimSuffix(name, ".jsonl") < since {
			continue
		}
		day, err := readDailyFile(filepath.Join(dir, name), issue)
		if err != nil {
			return nil, err
		}
		out = append(out, day...)
	}
	return out, nil
}

func printGateSummary(sum gateSummary) {
	if len(sum.Runs) == 0 {
		fmt.Println("No stage-exit records found.")
		return
	}
	line := func(label string, t gateTally) {
		fmt.Printf("  %-22s gates=%d %s reused=%d unfinished=%d stages=%d stage-time=%s\n",
			label, t.Runs, fmtMs(int64(t.Seconds)*1000), t.Reused, t.Unfinished, t.Stages, fmtMs(t.ElapsedMs))
	}
	for _, run := range sum.Runs {
		fmt.Printf("#%d %s\n", run.Issue, run.RunID)
		names := make([]string, 0, len(run.Stages))
		for name := range run.Stages {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			line(name, *run.Stages[name])
		}
		line("total", run.Total)
	}
	if len(sum.FeatureDevReview) > 0 {
		fmt.Println("feature-dev by review phases")
		for _, key := range []string{diagnostics.ReviewPhasesRan, diagnostics.ReviewPhasesMissing, ""} {
			if t := sum.FeatureDevReview[key]; t != nil {
				label := key
				if label == "" {
					label = "unknown"
				}
				line(label, *t)
			}
		}
	}
}

// exitRecordsGateSummaryCmd implements `nightgauge exit-records gate-summary`.
func exitRecordsGateSummaryCmd() *cobra.Command {
	var (
		issue      int
		since      string
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "gate-summary",
		Short:        "Total full gates per run and per stage (#2784)",
		SilenceUsage: true,
		Example: `  nightgauge exit-records gate-summary --issue 2784
  nightgauge exit-records gate-summary --since 2026-11-01 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := gateWorkdir(workdir)
			if err != nil {
				return err
			}
			records, err := readExitRecordsSince(root, issue, since)
			if err != nil {
				return err
			}
			sum := summarizeGates(records)
			if jsonOutput {
				return printJSON(sum)
			}
			printGateSummary(sum)
			return nil
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Filter to a single issue number (0 = all)")
	cmd.Flags().StringVar(&since, "since", "", "Earliest UTC day to read, YYYY-MM-DD")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON instead of a table")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}
