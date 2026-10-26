package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/spf13/cobra"
)

// discoveredWorkCmd is the `discovered-work` family (#478, ADR 027): record
// work a stage found but must not do in its own run, list it, and report
// capture files no run will materialize. Nothing here deletes a capture file;
// the #480 materializer is the only deleter.
func discoveredWorkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discovered-work",
		Short: "Record, list and report per-run discovered work (ADR 027)",
		Long: `Per-run capture of follow-up work found during a pipeline run, stored as
discovered-work-{N}.json in the clone's pipeline state directory
(` + "`nightgauge layout path pipeline`" + `). Records are validated before they are
written; a refused record leaves the file unchanged. See
docs/decisions/027-discovered-work-substrate.md.`,
	}
	cmd.AddCommand(discoveredWorkRecordCmd(), discoveredWorkListCmd(), discoveredWorkOrphansCmd())
	return cmd
}

// discoveredWorkRunDir is --run-dir when given, else the clone's pipeline
// state directory.
func discoveredWorkRunDir(cmd *cobra.Command) (string, error) {
	if v, _ := cmd.Flags().GetString("run-dir"); v != "" {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve the working directory: %w", err)
	}
	return layout.PipelineStateDir(cwd)
}

// discoveredWorkRunID is the checkout's live run id for issue, or nil when no
// run-state names that issue. A caller never invents one.
func discoveredWorkRunID(issue int) *string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	dir, err := layout.CheckoutDir(cwd)
	if err != nil {
		return nil
	}
	rs, err := runstate.Load(dir)
	if err != nil || rs == nil || rs.IssueNumber != issue || rs.RunID == "" {
		return nil
	}
	id := rs.RunID
	return &id
}

func discoveredWorkRecordCmd() *cobra.Command {
	var (
		issue int
		rec   discoveredwork.Record
		runID string
	)
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Append one validated discovered-work record; prints its id",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			dir, err := discoveredWorkRunDir(cmd)
			if err != nil {
				return err
			}
			rec.IssueNumber = issue
			if cmd.Flags().Changed("run-id") {
				rec.RunID = &runID
			} else {
				rec.RunID = discoveredWorkRunID(issue)
			}
			got, added, err := discoveredwork.Append(dir, rec, time.Now())
			if err != nil {
				return err
			}
			if added {
				fmt.Fprintln(cmd.OutOrStdout(), got.ID)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already recorded\n", got.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&issue, "issue", 0, "issue number the run is for (required)")
	f.String("run-dir", "", "capture directory (default: the clone's pipeline state directory)")
	f.StringVar(&rec.Summary, "summary", "", "one-line summary, becomes the issue title (≤200 chars)")
	f.StringArrayVar(&rec.Evidence, "evidence", nil, "evidence line (repeatable, 1-10)")
	f.StringVar(&rec.SourceStage, "source-stage", "", "pipeline stage, or producer:<kebab-name>")
	f.StringVar(&rec.SuggestedType, "suggested-type", "", "feature|bug|docs|chore|spike")
	f.StringVar(&rec.SuggestedPriority, "suggested-priority", "", "critical|high|medium|low")
	f.StringVar(&rec.SuggestedSize, "suggested-size", "", "XS|S|M|L|XL")
	f.StringArrayVar(&rec.CandidateTargetFiles, "candidate-file", nil, "repository-relative file hint (repeatable, ≤20)")
	f.StringVar(&runID, "run-id", "", "run id (default: the checkout's run-state for --issue, else null)")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func discoveredWorkListCmd() *cobra.Command {
	var (
		issue  int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Load, validate and print an issue's discovered-work records",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			dir, err := discoveredWorkRunDir(cmd)
			if err != nil {
				return err
			}
			c, err := discoveredwork.Load(dir, issue)
			if err != nil {
				return err
			}
			if c == nil {
				c = &discoveredwork.Capture{SchemaVersion: discoveredwork.SchemaVersion, IssueNumber: issue, Records: []discoveredwork.Record{}}
			}
			return printDiscoveredWork(cmd.OutOrStdout(), c, asJSON)
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().String("run-dir", "", "capture directory (default: the clone's pipeline state directory)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the capture envelope as JSON")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func printDiscoveredWork(w io.Writer, c *discoveredwork.Capture, asJSON bool) error {
	if asJSON {
		out, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(out))
		return nil
	}
	if len(c.Records) == 0 {
		fmt.Fprintf(w, "no discovered work recorded for #%d\n", c.IssueNumber)
		return nil
	}
	for _, r := range c.Records {
		fmt.Fprintf(w, "%s  [%s/%s/%s]  %s  (from %s)\n", r.ID, r.SuggestedType, r.SuggestedPriority, r.SuggestedSize, r.Summary, r.SourceStage)
	}
	return nil
}

func discoveredWorkOrphansCmd() *cobra.Command {
	var (
		olderThan time.Duration
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "orphans",
		Short: "Report capture files no run will materialize (never deletes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			dir, err := discoveredWorkRunDir(cmd)
			if err != nil {
				return err
			}
			orphans, err := discoveredwork.Orphans(dir, time.Now(), olderThan)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				if orphans == nil {
					orphans = []discoveredwork.Orphan{}
				}
				out, err := json.MarshalIndent(orphans, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(w, string(out))
				return nil
			}
			if len(orphans) == 0 {
				fmt.Fprintln(w, "no orphaned discovered-work files")
				return nil
			}
			for _, o := range orphans {
				fmt.Fprintf(w, "#%d  %s  %s\n", o.IssueNumber, o.Reason, o.Path)
			}
			return nil
		},
	}
	cmd.Flags().String("run-dir", "", "capture directory (default: the clone's pipeline state directory)")
	cmd.Flags().DurationVar(&olderThan, "older-than", discoveredwork.DefaultOrphanAge, "report files older than this even while their run is live")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}
