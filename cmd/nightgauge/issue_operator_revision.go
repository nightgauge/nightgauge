package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
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

// partialCloseDepsFactory builds what `issue partial-noop --apply-close`
// writes with (#3423); tests replace it. boardErr reports a board that could
// not be resolved: the close still runs, and the error is surfaced.
var partialCloseDepsFactory = func(ctx context.Context, owner, repo string, number, pr int) (deps orchestrator.PartialCloseDeps, target orchestrator.PartialCloseTarget, boardErr, err error) {
	client, err := clientFromConfig()
	if err != nil {
		return deps, target, nil, err
	}
	svc := gh.NewIssueService(client)
	target, err = orchestrator.ReadPartialCloseTarget(ctx, svc, owner, repo, number, pr)
	if err != nil {
		return deps, target, nil, err
	}
	deps = orchestrator.PartialCloseDeps{Issues: svc, Owner: owner, Repo: repo}
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		return deps, target, fmt.Errorf("working directory: %w", wdErr), nil
	}
	cfg, cfgErr := config.Load(wd)
	if cfgErr != nil {
		return deps, target, fmt.Errorf("load config: %w", cfgErr), nil
	}
	project, projErr := resolveProjectNumber(cfg, false, cfg.ProjectNumber, owner, repo, wd)
	if projErr != nil {
		return deps, target, fmt.Errorf("resolve project: %w", projErr), nil
	}
	if project > 0 {
		deps.ProjectNumber = project
		deps.Board = gh.NewProjectService(client, owner, project, gh.ParseOwnerType(cfg.OwnerType))
	}
	return deps, target, nil, nil
}

// issuePartialNoOpCmd prints the decision for a zero-change feature-dev on a
// partial-delivery re-run (#3163): relap once on a binding operator-revised
// plan, end as a named PLAN_REVISION_NEEDED dispute, close an issue whose
// remainder is met on the earlier PR's merged diff (#3423), or book a no-op.
// The extension's stage loop acts on it, so it decides exactly as the Go
// scheduler does. Without --apply-close it reads only local files.
func issuePartialNoOpCmd() *cobra.Command {
	var pr, criteria int
	var relapped, applyClose bool
	var owner, repo string
	cmd := &cobra.Command{
		Use:   "partial-noop [number]",
		Short: "Decide what a zero-change feature-dev means on a partial-delivery re-run",
		Long: `Print, as JSON, the decision for a feature-dev that wrote no changes on a run
scoped to a partial-delivery remainder: action "relap" (re-dispatch feature-dev
once with steer), "dispute" (end as the named PLAN_REVISION_NEEDED dispute),
"close" (every remaining criterion is met on the earlier PR's merged diff),
"book" (end not_pipeline_actionable) or "not_scoped" (--pr unset). Reads
planning-{N}.json, dev-{N}.json and partial-delivery-{N}.json — whose criteria
and merge_sha the remainder is re-judged with — in this clone's pipeline state
directory.

With --apply-close and action "close", also post the evidence comment, tick
the met boxes, close the issue as completed and move its board row to Done.
"applied" reports the close; on a failure the action becomes "book" with the
failure in "detail".`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge issue partial-noop 3163 --pr 3200 --criteria 2
  nightgauge issue partial-noop 3423 --pr 3422 --apply-close --repo nightgauge/nightgauge`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			ownerPart, repoPart := "", ""
			if applyClose {
				ownerPart, repoPart = splitRepo(owner, repo)
			}
			return runIssuePartialNoOp(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), partialNoOpArgs{
				number: number, pr: pr, criteria: criteria, relapped: relapped,
				applyClose: applyClose, owner: ownerPart, repo: repoPart,
			})
		},
	}
	cmd.Flags().IntVar(&pr, "pr", 0, "PR whose partial-delivery remainder scopes this run")
	cmd.Flags().IntVar(&criteria, "criteria", 0, "number of remaining criteria in scope when no partial-delivery record exists")
	cmd.Flags().BoolVar(&relapped, "relapped", false, "this run already spent its one extra feature-dev lap")
	cmd.Flags().BoolVar(&applyClose, "apply-close", false, "close the issue when the action is close")
	ownerFlag(cmd, &owner, "GitHub organization (with --apply-close)")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository, name or owner/name (with --apply-close)")
	return cmd
}

type partialNoOpArgs struct {
	number, pr, criteria int
	relapped, applyClose bool
	owner, repo          string
}

// partialNoOpOutput is DecidePartialNoOp's result plus what --apply-close did.
type partialNoOpOutput struct {
	orchestrator.PartialNoOpResult
	Applied    bool                             `json:"applied,omitempty"`
	Close      *orchestrator.PartialCloseResult `json:"close,omitempty"`
	BoardError string                           `json:"board_error,omitempty"`
}

func runIssuePartialNoOp(ctx context.Context, stdout, stderr io.Writer, a partialNoOpArgs) error {
	ws := partialDeliveryWorkspace(ctx)
	var scope *state.PartialRerun
	mergeSHA := ""
	if a.pr > 0 {
		criteria := a.criteria
		if criteria < 0 {
			criteria = 0
		}
		scope = &state.PartialRerun{PR: a.pr, Criteria: make([]string, criteria)}
		// The record holds the criteria texts and the merge commit; counts
		// alone can never be judged met.
		if rec, err := orchestrator.ReadPartialDeliveryContext(ws, a.number); err != nil {
			fmt.Fprintf(stderr, "issue partial-noop: no partial-delivery record, the remainder is not judged: %v\n", err)
		} else if rec.PR != a.pr {
			fmt.Fprintf(stderr, "issue partial-noop: the partial-delivery record names PR #%d, not #%d; the remainder is not judged\n", rec.PR, a.pr)
		} else {
			scope.Criteria = rec.Criteria
			mergeSHA = rec.MergeSHA
		}
	}
	var remainder orchestrator.RemainderVerdict
	if scope != nil {
		remainder = orchestrator.JudgeRemainder(ctx, ws, mergeSHA, scope.Criteria)
	}
	out := partialNoOpOutput{PartialNoOpResult: orchestrator.DecidePartialNoOp(ws, a.number, scope, a.relapped, remainder)}
	if a.applyClose && out.Action == orchestrator.PartialNoOpActionClose {
		applyPartialClose(ctx, stderr, a, remainder, &out)
	}
	return json.NewEncoder(stdout).Encode(out)
}

// applyPartialClose runs the close for a "close" decision, downgrading the
// action to "book" with the shared failure or held detail when it fails or
// holds.
func applyPartialClose(ctx context.Context, stderr io.Writer, a partialNoOpArgs, v orchestrator.RemainderVerdict, out *partialNoOpOutput) {
	fail := func(err error) {
		fmt.Fprintf(stderr, "issue partial-noop: close failed: %v\n", err)
		out.Action = orchestrator.PartialNoOpActionBook
		out.Detail = orchestrator.PartialCloseFailedDetail(a.number, a.pr, err)
	}
	if a.owner == "" || a.repo == "" {
		fail(errors.New("--apply-close needs the owner and repo"))
		return
	}
	deps, target, boardErr, err := partialCloseDepsFactory(ctx, a.owner, a.repo, a.number, a.pr)
	if err != nil {
		fail(err)
		return
	}
	if boardErr != nil {
		out.BoardError = boardErr.Error()
		fmt.Fprintf(stderr, "issue partial-noop: board not synced on close: %v\n", boardErr)
	}
	res, err := orchestrator.CloseMetRemainder(ctx, deps, target, v)
	if err != nil {
		fail(err)
		return
	}
	out.Close = &res
	if res.Held != "" {
		// The issue's Delivery order says refs, or its body was unread
		// (#3510): nothing was written, so the run books as a held close.
		fmt.Fprintf(stderr, "issue partial-noop: close held: %s\n", res.Held)
		out.Action = orchestrator.PartialNoOpActionBook
		out.Detail = orchestrator.PartialCloseHeldDetail(a.number, a.pr, res.Held)
		return
	}
	out.Applied = true
}
