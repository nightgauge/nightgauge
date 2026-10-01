package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/handoff"
	"github.com/spf13/cobra"
)

// handoffCmd rolls up session handoff header blocks (#1481).
//
// Handoff files were a documentation-only convention: nothing read their
// `<!-- nightgauge:handoff -->` headers except a script outside this
// repository. This verb owns that read: one deterministic pass that reports a
// header tip behind main, an `updated` date past its limit, and cross-repo
// needs with no issue behind them.
func handoffCmd() *cobra.Command {
	var (
		workspaceRoot string
		checkouts     map[string]string
		ref           string
		maxAgeDays    int
		jsonOut       bool
	)

	cmd := &cobra.Command{
		Use:   "handoff <file-or-dir>...",
		Short: "Roll up session handoff headers and report stale or unmaterialized ones",
		Long: `Parse the <!-- nightgauge:handoff --> YAML block of each handoff file and
report what it says that is no longer true.

A file argument must carry a header; a directory argument contributes its *.md
files and skips the ones without a header (an index, a README).

Findings:
  handoff-stale                    tip behind --ref, or updated older than --max-age-days
  handoff-tip-unresolvable         the header tip is not a commit in the repo's checkout
  handoff-header-missing           a file argument with no header block
  handoff-header-unparseable       the block is not a YAML mapping, or updated is not a date
  cross-repo-need-unmaterialized   a needs/provides row with no issue
  cross-repo-row-malformed         a needs/provides row that is not a mapping

Tip staleness is measured in a local checkout of the header's repo, found by
--checkout repo=path or as <workspace-root>/<repo>. Nothing is fetched: fetch
first for the forge's answer. A repo with no checkout is reported as info
(handoff-tip-unmeasured), never as fresh.

Exit: 0 no findings, 1 findings, 2 could not run.`,
		Example: `  nightgauge handoff ../internal/runbooks/handoffs/
  nightgauge handoff handoffs/api.md --checkout api=../api --json`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			inputs, err := handoff.CollectInputs(args)
			if err != nil {
				return err
			}
			root := workspaceRoot
			if !cmd.Flags().Changed("workspace-root") {
				// Default: the directory holding this checkout and its siblings.
				if gitRoot, gerr := getGitRoot(); gerr == nil {
					root = filepath.Dir(gitRoot)
				}
			}
			report := handoff.Assess(cmd.Context(), inputs, handoff.Options{
				Ref:           ref,
				MaxAgeDays:    maxAgeDays,
				Checkouts:     checkouts,
				WorkspaceRoot: root,
			})
			if jsonOut {
				if err := printJSON(report); err != nil {
					return err
				}
			} else {
				printHandoffReport(cmd.OutOrStdout(), report)
			}
			if report.HasFindings() {
				return verdictExit{code: 1}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workspaceRoot, "workspace-root", "", "Directory holding one checkout per repo (default: the parent of this checkout)")
	cmd.Flags().StringToStringVar(&checkouts, "checkout", nil, "Checkout for a repo, as repo=path (repeatable)")
	cmd.Flags().StringVar(&ref, "ref", "origin/main", "Ref the header tip is measured against")
	cmd.Flags().IntVar(&maxAgeDays, "max-age-days", handoff.DefaultMaxAgeDays, "Days after `updated` before a handoff is stale")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	markCouldNotRunExit(cmd)
	return cmd
}

func printHandoffReport(w io.Writer, r *handoff.Report) {
	if len(r.Handoffs) == 0 {
		fmt.Fprintln(w, "No handoff headers found.")
		return
	}
	fmt.Fprintf(w, "%-28s %-8s %-11s %-10s %7s  %s\n", "file", "session", "updated", "tip", "behind", "findings")
	for _, e := range r.Handoffs {
		session, updated, tip, behind := "-", "-", "-", "-"
		if e.Header != nil {
			session, updated, tip = orDash(e.Header.Session), orDash(e.Header.Updated), orDash(e.Header.Tip)
		}
		if e.TipBehind != nil {
			behind = fmt.Sprint(*e.TipBehind)
		}
		n := 0
		for _, f := range e.Findings {
			if f.Severity == handoff.SeverityFinding {
				n++
			}
		}
		fmt.Fprintf(w, "%-28s %-8s %-11s %-10s %7s  %d\n", filepath.Base(e.Path), session, updated, tip, behind, n)
	}
	if len(r.Findings) > 0 {
		fmt.Fprintln(w)
		for _, f := range r.Findings {
			fmt.Fprintf(w, "%-6s %s  %s: %s\n", f.Severity, filepath.Base(f.Path), f.Code, f.Message)
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
