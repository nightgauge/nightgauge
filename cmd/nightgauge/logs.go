package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/scanfailures"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/logretention"
	"github.com/spf13/cobra"
)

// logsCmd is the top-level "logs" command. It exposes deterministic
// operations over local pipeline session logs in the clone's logs directory
// (layout.CloneLogsDir).
//
// Distinct from `nightgauge ci logs <run-id>`, which downloads CI workflow
// run logs from GitHub. Cobra namespaces subcommands by parent, so the two
// coexist cleanly. See Issue #3087 (audit row B29).
func logsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Local pipeline session-log operations (scan-failures, prune)",
		Long: `Deterministic readers over ` + layout.CloneLogsDisplay() + `/. Replaces the inline-Python
regex scan duplicated in skills/nightgauge-retro/SKILL.md Phase 2.3 with a
single Go verb that emits a stable JSON schema (audit row B29).

Note: this command operates on local session logs. To download CI workflow run
logs, use ` + "`nightgauge ci logs <run-id>`" + ` (a separate, unrelated command).`,
	}
	cmd.AddCommand(logsScanFailuresCmd(), logsPruneCmd())
	return cmd
}

// logsPruneCmd applies log retention now (ADR-024 § 11, #2029). `serve` and
// CLI start already run it on their own schedule; this is the on-demand form
// doctor's NGD043 remedy names, and the manual verification's lever.
func logsPruneCmd() *cobra.Command {
	var (
		workdir    string
		dryRun     bool
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Apply the log size and age caps now",
		Long: `Prunes each Nightgauge log directory (the clone's ` + layout.CloneLogsDisplay() + `/ and the
machine state logs/) to its caps: machine-tier pipeline.logs.max_size_mb
(default 200) and pipeline.logs.max_age_days (default 30). Oldest files go
first. The live file (the current UTC day's ledger segment
github-api-YYYY-MM-DD.jsonl), files written in the last hour and files of a
run that is not terminal are never deleted; nor are symlinks, subdirectories
or anything outside the directory. The daemon's log,
` + layout.CheckoutDisplay(layout.CheckoutBackendLog) + `, is not in a pruned
directory; serve trims it to 1 MB when it passes 5 MB.`,
		Example: `  nightgauge logs prune --dry-run
  nightgauge logs prune --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := workdir
			if root == "" {
				root, _ = os.Getwd()
			}
			root = absPathOrEmpty(root)
			now := time.Now()
			var results []logretention.Result
			for _, t := range logretention.Targets(root) {
				res, err := logretention.PruneTarget(t, now, dryRun)
				if err != nil {
					return fmt.Errorf("%s logs: %w", t.Label, err)
				}
				if res.Dir == "" {
					continue
				}
				results = append(results, res)
				if !jsonOutput {
					verb := "pruned"
					if dryRun {
						verb = "would prune"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s logs %s: %s %d file(s), %s -> %s",
						t.Label, res.Dir, verb, len(res.Deleted),
						logretention.FormatBytes(res.SizeBefore), logretention.FormatBytes(res.SizeAfter))
					if res.OverCap {
						fmt.Fprint(cmd.OutOrStdout(), " — still over the cap: only live, recent or unfinished-run files remain")
					}
					fmt.Fprintln(cmd.OutOrStdout())
					for _, e := range res.Errors {
						fmt.Fprintf(cmd.OutOrStdout(), "  ! %s\n", e)
					}
				}
			}
			if jsonOutput {
				if results == nil {
					results = []logretention.Result{}
				}
				return printJSON(results)
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no log directory to prune")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be deleted without deleting it")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the per-directory results as JSON")
	return cmd
}

// pruneLogsAtCLIStart runs log retention for the command's workspace when the
// last prune is over a day old (ADR-024 § 11). Quiet by design: it runs ahead
// of every command, including hooks whose output is parsed, and doctor reports
// the directory's state.
func pruneLogsAtCLIStart(cmd *cobra.Command) {
	root := explicitWorkspaceRoot(cmd)
	if root == "" {
		root, _ = os.Getwd()
	}
	logretention.PruneAll(absPathOrEmpty(root), time.Now(), false, nil)
}

// absPathOrEmpty returns p made absolute, or "" when that fails, so a clone
// log directory is never resolved against an unknown working directory.
func absPathOrEmpty(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return abs
}

// logsScanFailuresCmd scans pipeline session logs for failure-signal patterns.
//
// Exit codes:
//
//	0 — scan completed (zero matches is not an error)
//	2 — hard error (invalid flag value, internal failure)
func logsScanFailuresCmd() *cobra.Command {
	var (
		issue      int
		since      string
		workdir    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "scan-failures",
		Short: "Scan pipeline session logs for failure-signal patterns",
		Long: `Walks ` + layout.CloneLogsDisplay() + `/*_session.log and emits matched lines using the
canonical 16-pattern regex set (case-insensitive). Replaces ~80 lines of inline
Python in retro Phase 2.3 (audit row B29). Output schema is stable v1 — field
names locked after first merge; additive fields allowed.

Filename pattern: YYYY-MM-DD[_NNN]_session.log. The optional NNN issue prefix
is parsed when present; logs without it have issue_number=null.

Exit codes:
  0  scan completed
  2  hard error (invalid workdir, internal failure)`,
		Example: `  nightgauge logs scan-failures --json
  nightgauge logs scan-failures --since 2026-04-01 --json
  nightgauge logs scan-failures --issue 3087 --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if since != "" && !isYYYYMMDD(since) {
				return fmt.Errorf("--since must be YYYY-MM-DD (got %q)", since)
			}
			result, err := scanfailures.Scan(scanfailures.Options{
				Workdir: workdir,
				Issue:   issue,
				Since:   since,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				return nil
			}
			printScanFailuresHuman(&result)
			return nil
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Filter to a single issue number (0 = all)")
	cmd.Flags().StringVar(&since, "since", "", "Lower bound YYYY-MM-DD (filename pre-filter)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printScanFailuresHuman(r *scanfailures.Result) {
	fmt.Printf("nightgauge logs scan-failures — schema v%d\n", r.V)
	fmt.Printf("logs scanned: %d  files with signals: %d\n",
		r.LogFilesScanned, r.FilesWithSignals)
	for _, lf := range r.LogSignals {
		issue := "—"
		if lf.IssueNumber != nil {
			issue = fmt.Sprintf("#%d", *lf.IssueNumber)
		}
		fmt.Printf("\n  %s  date=%s  issue=%s  signals=%d\n",
			lf.LogFile, lf.Date, issue, len(lf.FailureSignals))
		for _, s := range lf.FailureSignals {
			fmt.Printf("    L%d  %s\n", s.Line, s.Text)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// isYYYYMMDD reports whether s is a YYYY-MM-DD string. Local helper; mirrors
// the unexported helper of the same name in internal/pipeline/aggregator.go.
func isYYYYMMDD(s string) bool {
	if len(s) != 10 {
		return false
	}
	if s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
