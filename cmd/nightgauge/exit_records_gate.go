package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/gateledger"
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

// firstGate is a run's first finished full gate (#2847): the gate a fast
// static failure costs a whole re-run of.
type firstGate struct {
	Stage       string   `json:"stage"`
	Verdict     string   `json:"verdict"`
	FailedSteps []string `json:"failed_steps,omitempty"`
}

// gateRunSummary is one pipeline run, or one issue's records with no run id.
type gateRunSummary struct {
	Issue  int                   `json:"issue"`
	RunID  string                `json:"run_id,omitempty"`
	Total  gateTally             `json:"total"`
	Stages map[string]*gateTally `json:"stages"`
	// FirstGate is nil when no stage of the run finished a full gate.
	FirstGate *firstGate `json:"first_gate,omitempty"`
}

// firstGateTotals counts the runs' first gates. Other is any verdict that is
// neither a pass nor an assertion failure (infra, superseded). PassRate is
// Passed/Runs, 0 when no run had a first gate.
type firstGateTotals struct {
	Runs     int     `json:"runs"`
	Passed   int     `json:"passed"`
	Failed   int     `json:"failed"`
	Other    int     `json:"other"`
	PassRate float64 `json:"pass_rate"`
}

// failingStep is one step's failures across runs. FirstGates counts runs whose
// first gate failed on the step; Runs counts runs where any stage's first gate
// failed on it.
type failingStep struct {
	Step       string `json:"step"`
	FirstGates int    `json:"first_gates"`
	Runs       int    `json:"runs"`
}

// gateSummary is the JSON shape of `nightgauge exit-records gate-summary`.
type gateSummary struct {
	Runs            []gateRunSummary `json:"runs"`
	FirstGateTotals firstGateTotals  `json:"first_gate_totals"`
	// FailingSteps is sorted by FirstGates, then Runs, descending, then by name.
	FailingSteps []failingStep `json:"failing_steps"`
	// FeatureDevReview splits feature-dev by ReviewPhases, because a stage that
	// did its review takes longer for a reason that has nothing to do with gates.
	// The empty key is a record that could not say.
	FeatureDevReview map[string]*gateTally `json:"feature_dev_review"`
}

// summarizeGates totals records per run and per stage.
func summarizeGates(records []diagnostics.StageExitRecord) gateSummary {
	sum := gateSummary{FeatureDevReview: map[string]*gateTally{}, FailingSteps: []failingStep{}}
	byRun := map[string]*gateRunSummary{}
	// stepRuns holds, per step, the runs any stage's first gate failed it in.
	stepRuns := map[string]map[string]bool{}
	var keys []string
	for _, rec := range records {
		key := gateRunKey(rec)
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
		if rec.GateFirstVerdict != "" {
			// Records are read in write order, so the first one with a gate is
			// the run's first gate.
			if run.FirstGate == nil {
				run.FirstGate = &firstGate{Stage: rec.Stage, Verdict: rec.GateFirstVerdict,
					FailedSteps: rec.GateFirstFailedSteps}
			}
			for _, step := range rec.GateFirstFailedSteps {
				if stepRuns[step] == nil {
					stepRuns[step] = map[string]bool{}
				}
				stepRuns[step][key] = true
			}
		}
		if rec.Stage == "feature-dev" {
			if sum.FeatureDevReview[rec.ReviewPhases] == nil {
				sum.FeatureDevReview[rec.ReviewPhases] = &gateTally{}
			}
			sum.FeatureDevReview[rec.ReviewPhases].add(rec)
		}
	}
	sort.Strings(keys)
	firstFails := map[string]int{}
	for _, key := range keys {
		run := byRun[key]
		sum.Runs = append(sum.Runs, *run)
		if run.FirstGate == nil {
			continue
		}
		t := &sum.FirstGateTotals
		t.Runs++
		switch run.FirstGate.Verdict {
		case gateledger.VerdictPass:
			t.Passed++
		case gateledger.VerdictFail:
			t.Failed++
		default:
			t.Other++
		}
		for _, step := range run.FirstGate.FailedSteps {
			firstFails[step]++
		}
	}
	if t := &sum.FirstGateTotals; t.Runs > 0 {
		t.PassRate = float64(t.Passed) / float64(t.Runs)
	}
	for step, runs := range stepRuns {
		sum.FailingSteps = append(sum.FailingSteps,
			failingStep{Step: step, FirstGates: firstFails[step], Runs: len(runs)})
	}
	sort.Slice(sum.FailingSteps, func(i, j int) bool {
		a, b := sum.FailingSteps[i], sum.FailingSteps[j]
		if a.FirstGates != b.FirstGates {
			return a.FirstGates > b.FirstGates
		}
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		return a.Step < b.Step
	})
	return sum
}

// readExitRecordsSince reads the stage-exit records on or after since. since is
// empty (every record), a UTC day YYYY-MM-DD (every daily file from that day),
// or an RFC 3339 instant. An instant filters whole runs, not records: a run
// (issue and run_id) is kept only when every record it has, in any daily file,
// has a ts at or after the instant. Filtering records alone would drop a run's
// earlier failed gate and report a later pass as its first gate (#2847). A file
// name is its UTC day, so the day comparison is lexical.
func readExitRecordsSince(root string, issue int, since string) ([]diagnostics.StageExitRecord, error) {
	day, instant, err := parseGateSince(since)
	if err != nil {
		return nil, err
	}
	if !instant.IsZero() {
		// A run can span days, so its earlier records may sit in any file.
		day = ""
	}
	dir, err := diagnostics.ExitRecordsDir(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []diagnostics.StageExitRecord
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || strings.TrimSuffix(name, ".jsonl") < day {
			continue
		}
		recs, err := readDailyFile(filepath.Join(dir, name), issue)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	if instant.IsZero() {
		return out, nil
	}
	// A record whose ts does not parse cannot be shown to follow the instant,
	// so its run is not counted toward the build's runs.
	early := map[string]bool{}
	for _, rec := range out {
		ts, perr := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if perr != nil || ts.Before(instant) {
			early[gateRunKey(rec)] = true
		}
	}
	kept := out[:0]
	for _, rec := range out {
		if !early[gateRunKey(rec)] {
			kept = append(kept, rec)
		}
	}
	return kept, nil
}

// gateRunKey identifies a run: its issue and run_id.
func gateRunKey(rec diagnostics.StageExitRecord) string {
	return fmt.Sprintf("%012d/%s", rec.Issue, rec.RunID)
}

// parseGateSince splits a --since value into the earliest daily file to read
// and, for an RFC 3339 instant, the instant records must not precede.
func parseGateSince(since string) (string, time.Time, error) {
	if since == "" {
		return "", time.Time{}, nil
	}
	if _, err := time.Parse("2006-01-02", since); err == nil {
		return since, time.Time{}, nil
	}
	instant, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("--since %q: want a UTC day YYYY-MM-DD or an RFC 3339 instant", since)
	}
	instant = instant.UTC()
	return instant.Format("2006-01-02"), instant, nil
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
		if fg := run.FirstGate; fg != nil {
			steps := ""
			if len(fg.FailedSteps) > 0 {
				steps = " (" + strings.Join(fg.FailedSteps, ", ") + ")"
			}
			fmt.Printf("  first gate: %s in %s%s\n", fg.Verdict, fg.Stage, steps)
		}
	}
	if t := sum.FirstGateTotals; t.Runs > 0 {
		fmt.Printf("first gates: %d of %d passed (%.0f%%), %d failed, %d other\n",
			t.Passed, t.Runs, t.PassRate*100, t.Failed, t.Other)
		for _, s := range sum.FailingSteps {
			fmt.Printf("  %-40s first-gates=%d runs=%d\n", s.Step, s.FirstGates, s.Runs)
		}
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
		Short:        "Total full gates per run and per stage, and first-gate failures (#2784, #2847)",
		SilenceUsage: true,
		Example: `  nightgauge exit-records gate-summary --issue 2784
  nightgauge exit-records gate-summary --since 2026-11-01 --json
  nightgauge exit-records gate-summary --since 2026-11-15T03:04:05Z --json`,
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
	cmd.Flags().StringVar(&since, "since", "", "Earliest record: a UTC day YYYY-MM-DD, or an RFC 3339 instant")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON instead of a table")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}
