package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/deliverable"
	"github.com/spf13/cobra"
)

// gateCheckDeliverableCmd runs `nightgauge gate check-deliverable` — the
// IN-STAGE half of the deliverable-schema policy (#1177).
//
// The dev deliverable's shape was prescriptive text in a Markdown include: an
// exact `jq -n` template, followed by `jq . "$FILE" >/dev/null` to confirm the
// result was well-formed JSON. Well-formed is not the same as well-shaped, and
// nothing verified the stage had used the template at all. #1177's run wrote
// `files_changed` as a flat array against `schema_version: "1.5"` — a contract
// three versions old — and the emitted object contained `"committed": False`
// and `"commit_sha": None`, Python literals that no `jq` invocation can
// produce. The model hand-wrote the deliverable from a remembered schema, and
// the only detector was the post-condition gate, $6.12 later.
//
// This verb replaces the well-formedness check with the real one. It runs the
// same closed rule table the gate runs, in-stage, while the stage still has the
// context to correct itself — a stage that fails its own check for free is
// strictly better than a gate that fails it after the spend. It also STAMPS
// `schema_version` from the binary's own registry rather than trusting the
// number the skill wrote: a skill authoring its own version is making a claim,
// and #1177 is what a false claim looks like.
//
// Exit codes: 0 when the deliverable is usable (clean, repaired, or with an
// advisory field quarantined); 1 when no total repair exists and the stage must
// fix its own output.
func gateCheckDeliverableCmd() *cobra.Command {
	var (
		stage       string
		issueNumber int
		workdir     string
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use:   "check-deliverable",
		Short: "Validate and deterministically repair a stage deliverable in-stage",
		Long: `Apply the deliverable-schema policy to a stage's context file BEFORE the
stage exits (#1177). Repairable shape and vocabulary defects are rewritten in
place and recorded in the file; an advisory field with a genuinely missing value
is quarantined and marked untrustworthy; anything the closed rule table cannot
repair totally exits 1 so the stage can correct itself while it still has
context — instead of the post-condition gate discarding the work later.

schema_version is stamped from this binary's contract registry, not read from
the file.`,
		Example: `  nightgauge gate check-deliverable --stage dev --issue 210
  nightgauge gate check-deliverable --stage validate --issue 210 --json`,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, known := deliverable.CanonicalSchemaVersion(stage); !known {
				return fmt.Errorf("unknown stage %q (known: %s)", stage, strings.Join(deliverable.KnownStages(), ", "))
			}
			work := workdir
			if work == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				work = wd
			}
			path := filepath.Join(work, ".nightgauge", "pipeline",
				fmt.Sprintf("%s-%d.json", stage, issueNumber))

			outcome, err := deliverable.ApplyPolicyToFile(stage, path, time.Now())
			if err != nil {
				fmt.Fprintf(os.Stderr, "check-deliverable: %v\n", err)
				os.Exit(1)
			}

			if asJSON {
				payload := map[string]any{
					"stage":          stage,
					"issue_number":   issueNumber,
					"path":           path,
					"verdict":        outcome.Verdict(),
					"ok":             outcome.OK(),
					"policy_version": deliverable.PolicyVersion,
					"notes":          outcome.Notes,
					"untrustworthy":  outcome.Untrustworthy(),
				}
				enc, _ := json.MarshalIndent(payload, "", "  ")
				fmt.Println(string(enc))
			} else {
				fmt.Printf("%s deliverable %s: %s\n", stage, path, outcome.Verdict())
				for _, line := range outcome.Summary() {
					fmt.Println("  " + line)
				}
				if fields := outcome.Untrustworthy(); len(fields) > 0 {
					fmt.Printf("  untrustworthy (entries dropped): %s\n", strings.Join(fields, ", "))
				}
			}

			if !outcome.OK() {
				fmt.Fprintf(os.Stderr,
					"check-deliverable: %s-%d.json does not match the contract and no total repair exists — fix the emission before exiting the stage\n",
					stage, issueNumber)
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "Deliverable kind: dev | validate (required)")
	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the policy outcome as JSON")
	_ = cmd.MarkFlagRequired("stage")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}
