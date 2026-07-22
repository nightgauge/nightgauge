package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/spf13/cobra"
)

func knowledgeTelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Emit and inspect knowledge telemetry events",
		Long: `Operations on the knowledge-events.jsonl stream written to
.nightgauge/pipeline/history/knowledge-events.jsonl.

Sub-commands let skills and downstream stages emit events that happen outside
the binary (e.g., a skill reading an ADR via cat, a stage reporting a
recall_hit). All in-binary knowledge subcommands emit telemetry automatically
at their success path; this command is for external callers.`,
	}
	cmd.AddCommand(knowledgeTelemetryRecordCmd())
	return cmd
}

func knowledgeTelemetryRecordCmd() *cobra.Command {
	var (
		eventType   string
		scope       string
		issueNumber int
		path        string
		query       string
		recallID    string
		hitIndex    int
		hasHit      bool
		resultCount int
		hasCount    bool
		durationMs  int64
		status      string
		errorKind   string
		stage       string
		workdir     string
		outputJSON  bool
	)

	cmd := &cobra.Command{
		Use:          "record",
		Short:        "Record a knowledge telemetry event",
		SilenceUsage: true,
		Long: `Record a single knowledge telemetry event to the JSONL stream.

Valid --type values: ` + strings.Join(telemetry.AllEventTypes(), ", ") + `

Used by skills and downstream stages to emit events that happen outside the
binary, such as a skill reading an ADR via cat (--type=read) or a stage
reporting that it actually used recall result N (--type=recall_hit).

Telemetry write failures are surfaced as a non-zero exit; for skill use the
exit code can be ignored because telemetry must never fail a user-facing op.`,
		Example: `  nightgauge knowledge telemetry record --type=read --scope=issue:42 --path=decisions.md --issue=42
  nightgauge knowledge telemetry record --type=recall_hit --recall-id=r-2026-05 --hit-index=2 --issue=42
  nightgauge knowledge telemetry record --type=read --scope=workspace --path=architecture/sse.md --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			et := telemetry.EventType(eventType)
			if !telemetry.IsValidEventType(et) {
				return fmt.Errorf("--type %q is not valid; must be one of: %s",
					eventType, strings.Join(telemetry.AllEventTypes(), ", "))
			}

			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			ev := telemetry.Event{
				Type:         et,
				Stage:        stage,
				Scope:        scope,
				IssueNumber:  issueNumber,
				Path:         path,
				QuerySummary: query,
				RecallID:     recallID,
				DurationMs:   durationMs,
				Status:       status,
				ErrorKind:    errorKind,
			}
			if hasHit {
				h := hitIndex
				ev.HitIndex = &h
			}
			if hasCount {
				c := resultCount
				ev.ResultCount = &c
			}

			if err := telemetry.Emit(workdir, ev); err != nil {
				return fmt.Errorf("emit event: %w", err)
			}

			if outputJSON {
				out := map[string]any{
					"recorded": true,
					"type":     string(et),
					"path":     telemetry.Path(workdir),
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Printf("Recorded %s event → %s\n", et, telemetry.Path(workdir))
			return nil
		},
	}

	cmd.Flags().StringVar(&eventType, "type", "", "Event type (required); one of: "+strings.Join(telemetry.AllEventTypes(), ", "))
	cmd.Flags().StringVar(&scope, "scope", "", `Scope identifier, e.g. "issue:42", "workspace", "repo:architecture"`)
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (numeric)")
	cmd.Flags().StringVar(&path, "path", "", "Knowledge file path (kept as-is; not normalized)")
	cmd.Flags().StringVar(&query, "query", "", "Query summary (truncated to 200 chars; redacted when NIGHTGAUGE_TELEMETRY_REDACT_QUERIES=1)")
	cmd.Flags().StringVar(&recallID, "recall-id", "", "Correlator ID returned by a prior recall (for recall_hit events)")
	cmd.Flags().IntVar(&hitIndex, "hit-index", 0, "Zero-based index of the recall result that was used")
	cmd.Flags().IntVar(&resultCount, "result-count", 0, "Result count for the operation")
	cmd.Flags().Int64Var(&durationMs, "duration-ms", 0, "Operation duration in milliseconds")
	cmd.Flags().StringVar(&status, "status", "", "success or failure")
	cmd.Flags().StringVar(&errorKind, "error-kind", "", "Error class when status=failure")
	cmd.Flags().StringVar(&stage, "stage", "", "Override pipeline stage (default: $NIGHTGAUGE_STAGE)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Print confirmation as JSON")
	_ = cmd.MarkFlagRequired("type")

	// Track explicit hit-index / result-count assignment so a flag value of 0
	// can be preserved when the user explicitly passes --hit-index=0 (a valid
	// "first result" answer) rather than treated as absent.
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		hasHit = cmd.Flags().Changed("hit-index")
		hasCount = cmd.Flags().Changed("result-count")
		return nil
	}

	return cmd
}
