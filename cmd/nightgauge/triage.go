package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/triage"
	"github.com/spf13/cobra"
)

// triageCmd is the record half of ad-hoc failure triage (#1262). The skill
// does the investigating; this decides whether the investigation may call
// itself done.
//
// The split matters. "Reproduce, then observe, then fix" was already the
// intent, and it did not hold — twice a session read the source, formed a
// confident theory, and shipped code encoding it, once in the form of a probe
// that reported the exact opposite of the truth and misdirected the next
// session for its entire duration. Prose could not tell that report apart from
// a grounded one. A schema can, because the one thing a plausible guess never
// has is a rival it ruled out by observation.
func triageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Ad-hoc failure triage records (#1262)",
		Long: `Record and validate an ad-hoc triage of a failing check — a red CI job that
no issue exists for, so no pipeline stage can act on it.

The record is the discipline: it must answer whether the check has ever passed,
say how the failure was reproduced (or that it was not, in which case a fix is
refused outright), and name at least one hypothesis that was ruled OUT together
with the observation that ruled it out.`,
	}
	cmd.AddCommand(triageRecordCmd())
	cmd.AddCommand(triageCheckCmd())
	cmd.AddCommand(triageListCmd())
	return cmd
}

// triageRecordCmd writes a triage record from a JSON document.
//
// It takes a document rather than thirty flags because the record is a
// structured claim, not a set of switches, and because the skill assembles it
// incrementally as the investigation proceeds.
//
// Exit codes: 0 when the record is written AND valid; 1 when it is written but
// violates the contract. It is always written — refusing to persist a failing
// investigation would destroy the record of what was tried, which is the part
// the next session needs most.
func triageRecordCmd() *cobra.Command {
	var (
		file    string
		workdir string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Write a triage record and validate it against the contract",
		Long: `Read a triage record as JSON (from --file, or stdin with "-") and write it to
.nightgauge/triage/<id>.json. Exits 1 when the record is written but does not
meet the contract — the file is kept either way.`,
		Example:      `  nightgauge triage record --file /tmp/triage.json`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			work := resolveWorkdir(workdir)
			raw, err := readTriageInput(file)
			if err != nil {
				return err
			}
			var rec triage.Record
			if err := json.Unmarshal(raw, &rec); err != nil {
				return fmt.Errorf("parse triage record: %w", err)
			}
			path, violations, err := triage.Write(work, rec)
			if err != nil {
				return err
			}
			if asJSON {
				emitJSON(map[string]any{
					"path": path, "valid": len(violations) == 0,
					"violations": violationStrings(violations),
				})
			} else {
				fmt.Printf("wrote %s\n", path)
				printViolations(violations)
			}
			if len(violations) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "-", `Triage record JSON path, or "-" for stdin`)
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON")
	return cmd
}

// triageCheckCmd re-validates a record already on disk.
func triageCheckCmd() *cobra.Command {
	var (
		id      string
		workdir string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:          "check",
		Short:        "Validate an existing triage record against the contract",
		Example:      `  nightgauge triage check --id e2e-sweep-20260901T120000Z`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			work := resolveWorkdir(workdir)
			rec, err := triage.Read(work, id)
			if err != nil {
				return err
			}
			violations := rec.Validate()
			if asJSON {
				emitJSON(map[string]any{
					"id": rec.ID, "valid": len(violations) == 0,
					"violations": violationStrings(violations),
				})
			} else if len(violations) == 0 {
				fmt.Printf("%s: meets the triage contract\n", rec.ID)
			} else {
				fmt.Printf("%s: does not meet the triage contract\n", rec.ID)
				printViolations(violations)
			}
			if len(violations) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Triage record id (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func triageListCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List triage record ids in this workspace",
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			ids, err := triage.List(resolveWorkdir(workdir))
			if err != nil {
				return err
			}
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	return cmd
}

func readTriageInput(file string) ([]byte, error) {
	if file == "" || file == "-" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return nil, fmt.Errorf("read triage record from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read triage record: %w", err)
	}
	return data, nil
}

func violationStrings(in []triage.Violation) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, v.String())
	}
	return out
}

func printViolations(in []triage.Violation) {
	for _, v := range in {
		fmt.Fprintf(os.Stderr, "  %s\n", strings.TrimSpace(v.String()))
	}
}
