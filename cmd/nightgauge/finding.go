package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/finding"
	"github.com/spf13/cobra"
)

// findingCmd is the `finding` family (#102): record a learning signal found
// outside a pipeline run, and list findings for the skills that consume them.
// Separate from `discovered-work` by ADR 027.
func findingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finding",
		Short: "Record and list out-of-band findings (learning signals)",
		Long: `Out-of-band findings are environment gotchas, preconditions and API shapes an
operator learned while working beside the pipeline. They are stored one per line
in <main-checkout>/.nightgauge/findings.jsonl (schemas/finding-v1.json) and read
by continuous-improvement and feature-planning. A finding that warrants an issue
goes through ` + "`nightgauge discovered-work record`" + ` instead (ADR 027).`,
	}
	cmd.AddCommand(findingRecordCmd(), findingListCmd())
	return cmd
}

// findingFile is --file when given, else the main checkout's findings file.
func findingFile(cmd *cobra.Command) (string, error) {
	if v, _ := cmd.Flags().GetString("file"); v != "" {
		return v, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve the working directory: %w", err)
	}
	return finding.Path(cwd)
}

func findingRecordCmd() *cobra.Command {
	var rec finding.Record
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Append one validated finding; prints its id",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			file, err := findingFile(cmd)
			if err != nil {
				return err
			}
			got, added, err := finding.Append(file, rec, time.Now())
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
	f.String("file", "", "findings file (default: <main-checkout>/.nightgauge/findings.jsonl)")
	f.StringVar(&rec.Kind, "kind", "", "environment|precondition|api-shape|gotcha")
	f.StringVar(&rec.Scope, "scope", "", "project|pipeline")
	f.StringVar(&rec.Summary, "summary", "", "one-line summary (≤200 chars)")
	f.StringVar(&rec.Remedy, "remedy", "", "what to do about it (optional)")
	f.StringArrayVar(&rec.Evidence, "evidence", nil, "evidence line (repeatable, ≤10)")
	f.StringArrayVar(&rec.Paths, "path", nil, "repository-relative path or glob it concerns (repeatable, ≤20)")
	return cmd
}

func findingListCmd() *cobra.Command {
	var (
		kind, scope, matchPaths string
		asJSON                  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Load, validate, filter and print findings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			file, err := findingFile(cmd)
			if err != nil {
				return err
			}
			recs, err := finding.List(file)
			if err != nil {
				return err
			}
			var targets []string
			for _, p := range strings.Split(matchPaths, ",") {
				if p = strings.TrimSpace(p); p != "" {
					targets = append(targets, p)
				}
			}
			if matchPaths != "" && len(targets) == 0 {
				recs = []finding.Record{}
			} else {
				recs = finding.Filter(recs, kind, scope, targets)
			}
			w := cmd.OutOrStdout()
			if asJSON {
				out, err := json.MarshalIndent(recs, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(w, string(out))
				return nil
			}
			if len(recs) == 0 {
				fmt.Fprintln(w, "no findings recorded")
				return nil
			}
			for _, r := range recs {
				fmt.Fprintf(w, "%s  %s/%s  %s\n", r.ID, r.Kind, r.Scope, r.Summary)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("file", "", "findings file (default: <main-checkout>/.nightgauge/findings.jsonl)")
	f.StringVar(&kind, "kind", "", "keep only this kind")
	f.StringVar(&scope, "scope", "", "keep only this scope")
	f.StringVar(&matchPaths, "match-paths", "", "comma-separated target paths; keep findings that concern any of them")
	f.BoolVar(&asJSON, "json", false, "print as a JSON array ([] when none)")
	return cmd
}
