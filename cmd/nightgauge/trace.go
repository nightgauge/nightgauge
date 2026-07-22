package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/spf13/cobra"
)

// traceCmd is the top-level "trace" command: deterministic readers over the
// per-run lifecycle decision trace written during pipeline execution (#179).
//
// Per-run files live at `.nightgauge/pipeline/trace/<run_id>.jsonl`. Each
// line is one trace event — see internal/trace and ADR 013
// (docs/decisions/013-run-lifecycle-trace-schema.md) for the schema.
func traceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Inspect per-run lifecycle decision traces (#179)",
		Long: `Run lifecycle decision traces (#179 / ADR 013) — one JSONL file per run
in .nightgauge/pipeline/trace/<run_id>.jsonl capturing every stage boundary
and every decision with its rationale and rejected alternatives.

Use 'show' to print a run's ordered timeline (by issue number or run id) and
'export' to emit one joined JSON document combining the trace with the run's
RunRecord and stage-exit forensic records.`,
	}
	cmd.AddCommand(traceShowCmd())
	cmd.AddCommand(traceExportCmd())
	return cmd
}

// traceShowCmd implements `nightgauge trace show <issue-or-run-id>`.
func traceShowCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "show <issue-or-run-id>",
		Short:        "Show a run's decision trace as an ordered timeline",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge trace show 179
  nightgauge trace show 01890a5d-ac96-774b-bcce-b302099a8057
  nightgauge trace show 179 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveTraceWorkdir(workdir)
			if err != nil {
				return err
			}
			runID, err := resolveTraceRunID(root, args[0])
			if err != nil {
				return err
			}
			events, err := trace.ReadRun(root, runID)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return fmt.Errorf("no trace recorded for run %s", runID)
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(events)
			}
			printTraceHuman(runID, events)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the ordered events as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

// traceExportCmd implements `nightgauge trace export <run-id> --json`.
func traceExportCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "export <run-id>",
		Short:        "Export one joined document: trace + RunRecord + exit records",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example:      `  nightgauge trace export 01890a5d-ac96-774b-bcce-b302099a8057 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveTraceWorkdir(workdir)
			if err != nil {
				return err
			}
			// The argument is a run id; accept an issue number too for
			// operator convenience (resolves to the latest traced run).
			runID, err := resolveTraceRunID(root, args[0])
			if err != nil {
				return err
			}
			doc, err := trace.Export(root, runID)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			if !jsonOutput {
				// Export is inherently machine-readable; without --json emit
				// indented JSON for human reading.
				enc.SetIndent("", "  ")
			}
			return enc.Encode(doc)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Compact single-line JSON output")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

// resolveTraceWorkdir resolves the project root from --workdir or CWD.
func resolveTraceWorkdir(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getcwd: %w", err)
	}
	return cwd, nil
}

// resolveTraceRunID maps a CLI argument — an issue number or a literal run
// id — to a run id with a trace file.
func resolveTraceRunID(root, arg string) (string, error) {
	if issue, err := strconv.Atoi(arg); err == nil && issue > 0 {
		runID, err := trace.FindLatestRunIDForIssue(root, issue)
		if err != nil {
			return "", err
		}
		if runID == "" {
			return "", fmt.Errorf("no trace found for issue #%d", issue)
		}
		return runID, nil
	}
	return arg, nil
}

// printTraceHuman renders the ordered trace as one line per event with a
// kind-aware summary column so an operator can read the run's story top to
// bottom.
func printTraceHuman(runID string, events []trace.Event) {
	first := events[0]
	header := fmt.Sprintf("run %s", runID)
	if first.Repo != "" || first.Issue > 0 {
		header += fmt.Sprintf("  (%s#%d)", first.Repo, first.Issue)
	}
	fmt.Println(header)
	for _, ev := range events {
		ts := ev.Ts
		if t, err := time.Parse(time.RFC3339Nano, ev.Ts); err == nil {
			ts = t.Local().Format("15:04:05.000")
		}
		stage := ev.Stage
		if stage == "" {
			stage = "-"
		}
		fmt.Printf("%s  %-4d %-22s %-18s %s\n", ts, ev.Seq, ev.Kind, stage, summarizeTracePayload(ev))
	}
	fmt.Printf("\n%d event(s).\n", len(events))
}

// summarizeTracePayload produces the one-line human summary for an event.
// Payloads decode as map[string]any after a JSONL round trip.
func summarizeTracePayload(ev trace.Event) string {
	p, _ := ev.Payload.(map[string]any)
	get := func(key string) string {
		if p == nil {
			return ""
		}
		if v, ok := p[key]; ok {
			switch t := v.(type) {
			case string:
				return t
			case bool:
				return strconv.FormatBool(t)
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			}
		}
		return ""
	}
	switch ev.Kind {
	case trace.KindStageStart:
		return "model=" + get("model")
	case trace.KindStageExit:
		s := "success=" + get("success")
		if tk := get("terminal_kind"); tk != "" {
			s += " terminal_kind=" + tk
		}
		return s
	case trace.KindPhaseTransition:
		return ev.Phase
	case trace.KindModelRouting:
		return fmt.Sprintf("%s for %s — %s", get("model"), get("for_stage"), get("reasoning"))
	case trace.KindChangeClass:
		return fmt.Sprintf("route=%s rule=%s", get("suggested_route"), get("matched_change_rule"))
	case trace.KindStageSkip:
		return fmt.Sprintf("source=%s %s", get("source"), get("reason"))
	case trace.KindComplexityEscalation:
		return fmt.Sprintf("%s→%s (%s)", get("from_model"), get("to_model"), get("trigger"))
	case trace.KindBacktrack:
		return fmt.Sprintf("%s→%s signal=%s", get("from_stage"), get("target_stage"), get("signal_type"))
	case trace.KindRecoveryRetry:
		return fmt.Sprintf("action=%s recovered=%s", get("action"), get("recovered"))
	case trace.KindGateResult:
		return fmt.Sprintf("%s passed=%s %s", get("gate_name"), get("passed"), get("reason"))
	case trace.KindOutcome:
		s := "success=" + get("success")
		if tk := get("terminal_failure_kind"); tk != "" {
			s += " terminal_failure_kind=" + tk
		}
		return s
	}
	return ""
}
