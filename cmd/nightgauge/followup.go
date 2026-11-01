package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/spf13/cobra"
)

// exitCapRefused is the exit code of a filer whose per-run cap refused
// records (#480, Rule 5): loud, and distinct from a failure to run.
const exitCapRefused = 3

// followupEmptyCaptureReason is the Result.Reason of a run with no records.
const followupEmptyCaptureReason = "empty capture: no discovered-work records"

// followupCmd is the "followup" command family (#480).
func followupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "followup",
		Short: "File discovered follow-up work as issues (followup-materialize stage)",
	}
	cmd.AddCommand(followupMaterializeCmd())
	return cmd
}

// followupMaterializeCmd implements `nightgauge followup materialize <N>`.
func followupMaterializeCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		workdir       string
		runDirFlag    string
		dryRun        bool
		outputJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "materialize <issue-number>",
		Short: "File the run's discovered-work records as follow-up issues",
		Long: `Reads discovered-work-<N>.json from the pipeline state directory and files each
record as an issue with its type label, Priority, Size, Status=Backlog and a
provenance line, under the follow_up_filing gate (fail closed to report-only,
per-run cap, untrusted-author check). Records already filed are skipped;
records matching an open issue's title become a provenance comment instead.
Filed, deduped and reported records are removed from the capture file.

Exit 0: done (including zero records, reported as an empty capture). Exit 3: the per-run
cap refused records; they stay in the capture file. Exit 1: error.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			if workdir == "" {
				if workdir, err = os.Getwd(); err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			runDir := runDirFlag
			if runDir == "" {
				if runDir, err = layout.PipelineStateDir(workdir); err != nil {
					return fmt.Errorf("pipeline state dir: %w", err)
				}
			}
			capture, err := discoveredwork.Load(runDir, number)
			if err != nil {
				return err
			}
			if capture == nil || len(capture.Records) == 0 {
				// Say so explicitly (#2638): a silent no-op cannot be told
				// apart from a materializer that never ran.
				empty := &followup.Result{Issue: number, Reason: followupEmptyCaptureReason, Items: []followup.Item{}}
				if outputJSON {
					data, err := json.MarshalIndent(empty, "", "  ")
					if err != nil {
						return err
					}
					fmt.Fprintln(cmd.OutOrStdout(), string(data))
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "#%d: no discovered-work records — nothing to file\n", number)
				return nil
			}

			gate := filing.ResolveGate(workdir)
			if dryRun {
				gate.Mode, gate.Reason = filing.ModeReport, "--dry-run"
			}
			var filer followup.Filer
			trusted := false
			if gate.Files() {
				ownerPart, repoPart := splitRepo(owner, repo)
				client, err := clientFromConfig()
				if err != nil {
					return fmt.Errorf("create GitHub client: %w", err)
				}
				filer = followup.NewGitHubFiler(client, ownerPart, repoPart, projectNumber, getOwnerType(cmd))
				trusted = followup.AuthorTrusted(cmd.Context(), client, workdir, ownerPart, repoPart, number)
			}

			res, matErr := followup.Materialize(cmd.Context(), capture, gate, trusted, filer)
			if matErr != nil && !followup.IsCapRefusal(matErr) {
				// Consume what was decided before the error so a retry does
				// not redo it; undecided records stay.
				_ = discoveredwork.Consume(runDir, number, func(r discoveredwork.Record) bool { return !res.Consumed(r.ID) })
				return enrichError(matErr)
			}
			if !dryRun {
				if err := discoveredwork.Consume(runDir, number, func(r discoveredwork.Record) bool {
					return !res.Consumed(r.ID)
				}); err != nil {
					return fmt.Errorf("consume capture: %w", err)
				}
			}
			if outputJSON {
				if err := printJSON(res); err != nil {
					return err
				}
			} else {
				renderFollowupHuman(cmd.OutOrStdout(), res)
			}
			if matErr != nil {
				fmt.Fprintf(os.Stderr, "followup materialize: %v\n", matErr)
				return verdictExit{code: exitCapRefused}
			}
			return nil
		},
	}
	ownerFlag(cmd, &owner, "GitHub repository owner (defaults to config)")
	requiredRepoNameFlag(cmd, &repo, "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (defaults to config)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().StringVar(&runDirFlag, "run-dir", "", "Directory holding discovered-work-<N>.json (default: the pipeline state dir)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report the would-file list; no forge calls and the capture is kept")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

func renderFollowupHuman(w io.Writer, res *followup.Result) {
	mode := "file"
	if res.ReportOnly {
		mode = "report-only: " + res.Reason
	}
	fmt.Fprintf(w, "Follow-up materialize (%s) — issue #%d\n", mode, res.Issue)
	for _, it := range res.Items {
		switch it.Outcome {
		case followup.OutcomeFiled, followup.OutcomeAlreadyExists, followup.OutcomeDeduped:
			fmt.Fprintf(w, "  %-14s %s  #%d %s\n", it.Outcome, it.ID, it.IssueNumber, it.Title)
		case followup.OutcomeInvalid:
			fmt.Fprintf(w, "  %-14s %s  %s (%s)\n", it.Outcome, it.ID, it.Title, it.Detail)
		default:
			fmt.Fprintf(w, "  %-14s %s  [%s %s %s] %s\n", it.Outcome, it.ID, it.Type, it.Priority, it.Size, it.Title)
		}
	}
	if res.Refused > 0 {
		fmt.Fprintf(w, "  refused: %d of %d by the per-run cap (kept in the capture file)\n",
			res.Refused, res.Refused+countOutcome(res, followup.OutcomeFiled))
	}
}

func countOutcome(res *followup.Result, outcome string) int {
	n := 0
	for _, it := range res.Items {
		if it.Outcome == outcome {
			n++
		}
	}
	return n
}
