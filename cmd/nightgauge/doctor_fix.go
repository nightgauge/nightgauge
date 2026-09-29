package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// `nightgauge doctor --fix`, `--dry-run` and `--history` (#2093): the CLI
// face of the remedy engine in internal/doctor/remedy.go. The engine decides
// every outcome; this file parses flags and renders the report.

// doctorFixFlags are the remedy-engine flags on `nightgauge doctor`.
type doctorFixFlags struct {
	fix      bool
	yes      bool
	dryRun   bool
	history  bool
	only     []string
	severity []string
}

func addDoctorFixFlags(cmd *cobra.Command, fl *doctorFixFlags) {
	cmd.Flags().BoolVar(&fl.fix, "fix", false, "Apply remedies: every auto remedy, confirm remedies with --yes; each is verified by re-running its check")
	cmd.Flags().BoolVar(&fl.yes, "yes", false, "With --fix, also apply confirm remedies (destructive or visible to others)")
	cmd.Flags().BoolVar(&fl.dryRun, "dry-run", false, "Print every remedy's preview and change nothing (implies --fix planning)")
	cmd.Flags().BoolVar(&fl.history, "history", false, "Print the local fix log of applied remedies")
	cmd.Flags().StringSliceVar(&fl.only, "only", nil, "Run only the checks owning these finding codes (NGD017) or check IDs (worktree_leaks), plus their dependencies; with --fix or --dry-run, act only on those findings")
	cmd.Flags().StringSliceVar(&fl.severity, "severity", nil, "With --fix or --dry-run, act only on findings of these severities (blocker, warning, housekeeping)")
}

// active reports whether the invocation runs the remedy engine.
func (fl doctorFixFlags) active() bool { return fl.fix || fl.dryRun }

// options validates the flags and builds the engine's options.
func (fl doctorFixFlags) options() (doctor.FixOptions, error) {
	if fl.history && (fl.active() || fl.yes || len(fl.only) > 0 || len(fl.severity) > 0) {
		return doctor.FixOptions{}, errors.New("--history prints the fix log and takes no other remedy flag")
	}
	if !fl.active() && (fl.yes || len(fl.severity) > 0) {
		return doctor.FixOptions{}, errors.New("--yes and --severity need --fix or --dry-run")
	}
	opts := doctor.FixOptions{DryRun: fl.dryRun, Yes: fl.yes && !fl.dryRun}
	for _, o := range fl.only {
		if o = strings.TrimSpace(o); o != "" {
			opts.Only = append(opts.Only, o)
		}
	}
	for _, s := range fl.severity {
		sev, err := doctor.ParseSeverity(strings.TrimSpace(s))
		if err != nil {
			return doctor.FixOptions{}, err
		}
		opts.Severities = append(opts.Severities, sev)
	}
	return opts, nil
}

// runDoctorFix runs one pass and renders it; the return is the exit code.
func runDoctorFix(ctx context.Context, w io.Writer, fx *doctor.Fixer, opts doctor.FixOptions, jsonOut bool) int {
	rep := fx.Run(ctx, opts)
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(w, "warning: failed to encode JSON output: %v\n", err)
		}
		return rep.ExitCode
	}
	renderFixReport(w, rep)
	return rep.ExitCode
}

// fixLabel is the text label every result carries, so neither colour nor a
// symbol is ever the only signal (ADR-025 § 6).
func fixLabel(r doctor.FixResult) string {
	switch r.Action {
	case doctor.ActionPreviewed:
		return "DRY RUN"
	case doctor.ActionAwaitingConsent:
		return "NEEDS --yes"
	case doctor.ActionManual:
		return "MANUAL"
	}
	switch r.Outcome {
	case doctor.OutcomeFixed:
		return "FIXED"
	case doctor.OutcomeStillPresent:
		return "STILL PRESENT"
	case doctor.OutcomeBlocked:
		return "BLOCKED"
	case doctor.OutcomeConflict:
		return "CONFLICT"
	case doctor.OutcomeStale:
		return "STALE"
	}
	return strings.ToUpper(string(r.Outcome))
}

func renderFixReport(w io.Writer, rep doctor.FixReport) {
	if rep.DryRun {
		fmt.Fprintln(w, "nightgauge doctor --dry-run: previews only, nothing is changed")
	} else {
		fmt.Fprintln(w, "nightgauge doctor --fix")
	}
	listed := 0
	for _, r := range rep.Results {
		if r.Action == doctor.ActionNoRemedy {
			continue
		}
		listed++
		f := r.Finding
		fmt.Fprintf(w, "\n[%s] %s %s  %s\n", fixLabel(r), f.Code, strings.ToUpper(string(f.Severity)), f.Title)
		if r.Remedy != nil {
			line := fmt.Sprintf("    %s", r.Remedy.Kind)
			if r.Remedy.Verb != "" {
				line += " " + r.Remedy.Verb
			}
			fmt.Fprintf(w, "%s: %s\n", line, r.Remedy.Summary)
		}
		if r.Preview != "" {
			fmt.Fprintf(w, "    preview: %s\n", r.Preview)
		}
		if r.Action == doctor.ActionManual && r.Remedy != nil {
			for _, s := range r.Remedy.Steps {
				fmt.Fprintf(w, "    - %s\n", s)
			}
		}
		if r.Detail != "" && r.Action != doctor.ActionManual {
			fmt.Fprintf(w, "    %s\n", r.Detail)
		}
		if len(r.Evidence) > 0 {
			fmt.Fprintf(w, "    evidence now: %s\n", evidenceLine(r.Evidence))
		}
	}
	if listed == 0 {
		fmt.Fprintln(w, "\nNothing to fix: no finding in scope declares a remedy.")
	}
	c := rep.Counts
	fmt.Fprintf(w, "\n%d fixed, %d still present, %d blocked, %d conflict, %d stale, %d awaiting --yes, %d manual, %d previewed\n",
		c.Fixed, c.StillPresent, c.Blocked, c.Conflict, c.Stale, c.AwaitingConsent, c.Manual, c.Previewed)
	s := rep.Doctor.Summary
	state := "After verification"
	if rep.DryRun {
		state = "Current state"
	}
	fmt.Fprintf(w, "%s: %d blocker, %d warning, %d housekeeping, %d info (exit %d)\n",
		state, s.Blocker, s.Warning, s.Housekeeping, s.Info, rep.ExitCode)
}

func evidenceLine(ev map[string]string) string {
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+ev[k])
	}
	return strings.Join(parts, ", ")
}

// runDoctorHistory prints the fix log at path.
func runDoctorHistory(w io.Writer, path string, jsonOut bool) error {
	entries, malformed, err := doctor.ReadFixLog(path)
	if err != nil {
		return err
	}
	if jsonOut {
		if entries == nil {
			entries = []doctor.FixLogEntry{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintf(w, "No remedies applied yet (%s)\n", path)
	} else {
		fmt.Fprintf(w, "Fix log: %s\n", path)
		for _, e := range entries {
			fmt.Fprintf(w, "%s  %-13s  %s  %-20s  %s  %s\n",
				e.Time.Format("2006-01-02T15:04:05Z07:00"), strings.ToUpper(string(e.Outcome)), e.Code, e.Verb, e.Fingerprint, e.Detail)
		}
	}
	if malformed > 0 {
		fmt.Fprintf(w, "(%d unreadable line(s) skipped)\n", malformed)
	}
	return nil
}
