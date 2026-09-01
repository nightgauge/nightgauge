package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/testexec"
	"github.com/spf13/cobra"
)

// gateCheckTestExecutionCmd runs `nightgauge gate check-test-execution` — the
// IN-STAGE half of the evidence-of-execution gate (#1261).
//
// It exists for the same reason check-deliverable does: a stage that fails its
// own check for free is strictly better than a post-condition gate that fails
// it after the spend. The stage can still fix this — run the excluded suite,
// record the execution, continue. The gate can only reject.
//
// Exit codes: 0 when every excluded test file carries a passing execution
// record (and when nothing is excluded at all, which is the ordinary case);
// 1 when one or more do not.
func gateCheckTestExecutionCmd() *cobra.Command {
	var (
		issueNumber int
		workdir     string
		base        string
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use:   "check-test-execution",
		Short: "Fail when the change adds test files the configured test command never runs",
		Long: `Detect test files added by this change that the repo's own test command
structurally does not execute — a Dart suite tagged for exclusion, or one
outside every path the command names — and require an execution record proving
each was actually run (#1261).

Silent and exit 0 in any repo whose test command excludes nothing. When it does
fail, it names each file, the exclusion mechanism, and the exact command that
would run it.`,
		Example: `  nightgauge gate check-test-execution --issue 1261
  nightgauge gate check-test-execution --issue 1261 --json`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			work := resolveWorkdir(workdir)

			var changed []string
			if base != "" {
				files, err := ci.ChangedFilesFromGit(work, base, "HEAD")
				if err != nil {
					return fmt.Errorf("resolve changed files against %s: %w", base, err)
				}
				changed = files
			} else {
				files, resolved := ci.ChangedFilesAgainstDefaultBaseResolved(work)
				if !resolved {
					// No base ref means no diff, which means no evidence —
					// and no evidence must never read as an accusation.
					if asJSON {
						emitJSON(map[string]any{"issue_number": issueNumber, "blocked": false,
							"skipped": "no default base ref resolved"})
					}
					return nil
				}
				changed = files
			}

			var configured string
			if cfg, err := config.LoadMerged(work); err == nil && cfg != nil {
				configured = cfg.Pipeline.ResolveTestExecutionCommand()
			}

			res, err := testexec.Check(testexec.Options{
				Workspace:         work,
				IssueNumber:       issueNumber,
				ChangedFiles:      changed,
				ConfiguredCommand: configured,
			})
			if err != nil {
				return err
			}
			// Best-effort: the validate artifact may not exist yet when this
			// runs in-stage. A missing artifact is not a reason to fail a check
			// about test execution.
			_ = testexec.ApplyToValidateContext(work, issueNumber, res)

			if asJSON {
				payload := res.Summary()
				payload["issue_number"] = issueNumber
				payload["blocked"] = res.Blocked()
				emitJSON(payload)
			} else if !res.Quiet() {
				for _, line := range res.Evidence() {
					fmt.Println("  " + line)
				}
			}

			if res.Blocked() {
				fmt.Fprintf(os.Stderr, "check-test-execution: %s\n", res.Reason())
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().StringVar(&base, "base", "", "Diff base ref (default: the repo's default branch)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the check result as JSON")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// gateRecordTestExecutionCmd runs `nightgauge gate record-test-execution` — the
// writer half of #1261.
//
// The record names the command as run, not the command that should have been
// run. The failure this gate exists to prevent is a claim about execution that
// nobody checked against an execution; a record carrying only an assertion
// would reproduce it one layer up.
func gateRecordTestExecutionCmd() *cobra.Command {
	var (
		issueNumber int
		files       []string
		outcome     string
		command     string
		detail      string
		workdir     string
	)
	cmd := &cobra.Command{
		Use:   "record-test-execution",
		Short: "Record that a specific test file was actually executed",
		Long: `Append an execution record for one or more test files (#1261). Only a
"pass" outcome satisfies check-test-execution and the FeatureValidateGate;
recording a "fail" is honest and still leaves the gate red.`,
		Example: `  nightgauge gate record-test-execution --issue 1261 \
    --file integration_test/app_e2e/setup_flow_test.dart \
    --outcome pass --command "flutter test --tags=app-e2e integration_test/app_e2e/setup_flow_test.dart"`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			work := resolveWorkdir(workdir)
			if len(files) == 0 {
				return fmt.Errorf("at least one --file is required")
			}
			for _, f := range files {
				if err := testexec.AppendRecord(work, issueNumber, testexec.Record{
					File:    f,
					Outcome: strings.ToLower(strings.TrimSpace(outcome)),
					Command: command,
					Detail:  detail,
				}); err != nil {
					return err
				}
				fmt.Printf("recorded %s execution of %s\n", outcome, f)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringSliceVar(&files, "file", nil, "Test file that was executed (repeatable, required)")
	cmd.Flags().StringVar(&outcome, "outcome", "", "Outcome: pass | fail (required)")
	cmd.Flags().StringVar(&command, "command", "", "The command that was actually run")
	cmd.Flags().StringVar(&detail, "detail", "", "Optional note (e.g. how the environment was stood up)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("outcome")
	return cmd
}

func resolveWorkdir(workdir string) string {
	if workdir != "" {
		return workdir
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func emitJSON(payload map[string]any) {
	enc, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Println(string(enc))
}
