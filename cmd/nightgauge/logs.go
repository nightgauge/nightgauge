package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/cmd/scanfailures"
	"github.com/spf13/cobra"
)

// logsCmd is the top-level "logs" command. It exposes deterministic
// operations over local pipeline session logs in .nightgauge/logs/.
//
// Distinct from `nightgauge ci logs <run-id>`, which downloads CI workflow
// run logs from GitHub. Cobra namespaces subcommands by parent, so the two
// coexist cleanly. See Issue #3087 (audit row B29).
func logsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Local pipeline session-log operations (scan-failures)",
		Long: `Deterministic readers over .nightgauge/logs/. Replaces the inline-Python
regex scan duplicated in skills/nightgauge-retro/SKILL.md Phase 2.3 with a
single Go verb that emits a stable JSON schema (audit row B29).

Note: this command operates on local session logs. To download CI workflow run
logs, use ` + "`nightgauge ci logs <run-id>`" + ` (a separate, unrelated command).`,
	}
	cmd.AddCommand(logsScanFailuresCmd())
	return cmd
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
		Long: `Walks .nightgauge/logs/*_session.log and emits matched lines using the
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
