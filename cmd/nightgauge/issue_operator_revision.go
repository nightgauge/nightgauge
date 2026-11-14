package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// issueOperatorRevisionCmd prints the feature-dev prompt block for an
// operator-revised plan (#3163): the Action Center steer and the rule that the
// revised plan binds, rendered from this clone's planning-{N}.json. The
// extension's stage loop appends it to the feature-dev prompt, so the text has
// one source shared with the Go scheduler. It reads only local files.
func issueOperatorRevisionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "operator-revision [number]",
		Short: "Print the feature-dev prompt block for an operator-revised plan",
		Long: `Render the feature-dev prompt block from planning-{N}.json in this clone's
pipeline state directory: the operator steer spliced as operator_steer, and,
when revision_reasons carries an operator-origin: entry, the rule that the
revised plan binds feature-dev. Prints nothing when the plan carries neither.`,
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge issue operator-revision 3163`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			return runIssueOperatorRevision(cmd.Context(), cmd.OutOrStdout(), number)
		},
	}
}

func runIssueOperatorRevision(ctx context.Context, stdout io.Writer, number int) error {
	_, err := io.WriteString(stdout, orchestrator.OperatorRevisionPromptBlock(partialDeliveryWorkspace(ctx), number))
	return err
}

// issuePartialNoOpCmd prints the decision for a zero-change feature-dev on a
// partial-delivery re-run (#3163): relap once on a binding operator-revised
// plan, end as a named PLAN_REVISION_NEEDED dispute, or book a no-op. The
// extension's stage loop acts on it, so it decides exactly as the Go
// scheduler does. It reads only local files.
func issuePartialNoOpCmd() *cobra.Command {
	var pr, criteria int
	var relapped bool
	cmd := &cobra.Command{
		Use:   "partial-noop [number]",
		Short: "Decide what a zero-change feature-dev means on a partial-delivery re-run",
		Long: `Print, as JSON, the decision for a feature-dev that wrote no changes on a run
scoped to a partial-delivery remainder: action "relap" (re-dispatch feature-dev
once with steer), "dispute" (end as the named PLAN_REVISION_NEEDED dispute),
"book" (end not_pipeline_actionable) or "not_scoped" (--pr unset). Reads
planning-{N}.json and dev-{N}.json in this clone's pipeline state directory.`,
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge issue partial-noop 3163 --pr 3200 --criteria 2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			return runIssuePartialNoOp(cmd.Context(), cmd.OutOrStdout(), number, pr, criteria, relapped)
		},
	}
	cmd.Flags().IntVar(&pr, "pr", 0, "PR whose partial-delivery remainder scopes this run")
	cmd.Flags().IntVar(&criteria, "criteria", 0, "number of remaining criteria in scope")
	cmd.Flags().BoolVar(&relapped, "relapped", false, "this run already spent its one extra feature-dev lap")
	return cmd
}

func runIssuePartialNoOp(ctx context.Context, stdout io.Writer, number, pr, criteria int, relapped bool) error {
	var scope *state.PartialRerun
	if pr > 0 {
		if criteria < 0 {
			criteria = 0
		}
		scope = &state.PartialRerun{PR: pr, Criteria: make([]string, criteria)}
	}
	res := orchestrator.DecidePartialNoOp(partialDeliveryWorkspace(ctx), number, scope, relapped)
	return json.NewEncoder(stdout).Encode(res)
}
