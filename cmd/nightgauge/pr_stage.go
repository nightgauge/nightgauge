package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/spf13/cobra"
)

// prStageCmd exposes the deterministic-first pr-create / pr-merge runners as a
// standalone CLI seam (Issue #300).
//
// Motivation: the VSCode dogfood path executes the legacy TypeScript
// HeadlessOrchestrator.runPipeline (one instance per ConcurrentPipelineManager
// slot, inside each issue's worktree), NOT the Go scheduler's stage loop. The
// scheduler's deterministic-first hooks (tryDeterministicPRCreate /
// tryDeterministicPRMerge) therefore never fire on that path, so every dogfood
// run paid for an LLM pr-create + pr-merge session. Rather than reimplement the
// decision matrix, rich-body rendering, and bounded CI-wait a SECOND time in
// TypeScript (which would immediately drift from the Go source of truth), this
// verb lets the TS orchestrator invoke the EXACT runners the scheduler uses and
// react to a small, stable JSON contract.
//
// Contract (stdout, one line of JSON on a produced result):
//
//	{ "stage": "pr-create"|"pr-merge", "path": "created"|"merged"|"punt"|"refused",
//	  "pr_number": N, "pr_url": "...", "pr_state": "...", "reason": "...",
//	  "rate_limited": bool, "duration_ms": N }
//
// Exit codes:
//
//	0 — a result was produced (created / merged / punt / refused /
//	    rate-limited). The caller reads `path` + `rate_limited` from the JSON,
//	    never the exit code, to decide skip-LLM vs LLM-fallthrough vs defer.
//	    `refused` (pr-merge only, issue 1675) fails the stage: never fall
//	    through to the LLM skill.
//	1 — CLI/setup error (bad args, unresolved repo, client build failure). The
//	    caller falls through to the LLM path — the safe default.
func prStageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr-stage",
		Short: "Run a deterministic-first PR stage (create|merge) — the TS-path seam (#300)",
		Long: `Invoke the deterministic pr-create / pr-merge runners the Go scheduler uses,
from outside the scheduler. The legacy TypeScript HeadlessOrchestrator (the
VSCode dogfood path) shells out to these verbs to run pr-create/pr-merge
deterministic-FIRST — skipping the LLM skill when context is rich enough (create)
or the PR is cleanly mergeable, waiting out in-flight CI (merge) — mirroring the
scheduler contract instead of maintaining a second, divergent implementation.`,
	}
	cmd.AddCommand(prStageCreateCmd())
	cmd.AddCommand(prStageMergeCmd())
	cmd.AddCommand(prStageBudgetCmd())
	cmd.AddCommand(prStageReviewRoundCmd())
	cmd.AddCommand(prStageReviewGateCmd())
	cmd.AddCommand(prStageResumePointCmd())
	return cmd
}

// prStageResultJSON is the exact wire shape parsed by the TypeScript shim
// (HeadlessOrchestrator.runDeterministicPrStage). Keep the field names stable —
// they are an external contract.
type prStageResultJSON struct {
	Stage       string `json:"stage"`
	Path        string `json:"path"`
	PRNumber    int    `json:"pr_number,omitempty"`
	PRURL       string `json:"pr_url,omitempty"`
	PRState     string `json:"pr_state,omitempty"`
	Reason      string `json:"reason"`
	RateLimited bool   `json:"rate_limited"`
	DurationMs  int64  `json:"duration_ms"`
	// Phases are the transitions the deterministic runner reported, in order
	// (#1397). The scheduler path writes these straight to the run's
	// RuntimeState and to the live view; this path runs in a SEPARATE PROCESS
	// with neither, so it returns them for the caller to replay. Omitted when
	// empty so a punt before the first phase does not emit `"phases": null`.
	Phases []orchestrator.PhaseTransition `json:"phases,omitempty"`

	// Commit-owner outcome (#1179), additive — the TS shim ignores unknown
	// fields. pr-create is the pipeline's commit owner, so its verdict must be
	// observable from the path the extension actually drives.
	CommitMade   bool   `json:"commit_made,omitempty"`
	CommitSHA    string `json:"commit_sha,omitempty"`
	CommitReason string `json:"commit_reason,omitempty"`
}

// resolvePrStageWorkspace resolves the workdir the runner reads context from and
// runs `gh` in. Defaults to the process cwd. The deterministic runners project
// issue/dev/validate context from `*-{N}.json` in <workdir>'s pipeline state directory;
// on worktree-isolated dogfood runs those files live ONLY in the worktree (they
// are gitignored per-worktree local state, #288), so the caller MUST pass the
// worktree path via --workdir.
func resolvePrStageWorkspace(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return wd, nil
}

// authenticatePrStageGh exports the pipeline's configured GitHub token as
// GH_TOKEN/GITHUB_TOKEN so the runners' deterministic `gh`/git subprocesses and
// the in-process client authenticate as the pipeline identity — not the ambient
// active `gh` account (mirrors gate.go's rationale, #3890). Best-effort.
func authenticatePrStageGh(workspace string) *config.Config {
	cfg, err := config.Load(workspace)
	if err != nil || cfg == nil {
		return nil
	}
	exportConfiguredGitHubToken(cfg, cfg.Owner)
	return cfg
}

func emitPrStageResult(result prStageResultJSON, outputJSON bool) error {
	if outputJSON {
		data, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("marshal pr-stage result: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("%s: %s", result.Stage, result.Path)
	if result.PRNumber > 0 {
		fmt.Printf(" (PR #%d)", result.PRNumber)
	}
	if result.Reason != "" {
		fmt.Printf(" — %s", result.Reason)
	}
	if result.RateLimited {
		fmt.Print(" [rate-limited: defer]")
	}
	fmt.Printf(" (%dms)\n", result.DurationMs)
	return nil
}

// prStageCreateCmd runs `nightgauge pr-stage create <issue>`.
func prStageCreateCmd() *cobra.Command {
	var (
		repo       string
		workdir    string
		outputJSON bool
		timeoutSec int
	)
	cmd := &cobra.Command{
		Use:          "create <issue-number>",
		Short:        "Deterministic-first pr-create (renders a rich PR from context, or punts)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber, err := parseIssueNumberArg(args[0])
			if err != nil {
				return err
			}
			if repo == "" {
				return fmt.Errorf("--repo owner/name is required for pr-stage create")
			}
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			cfg := authenticatePrStageGh(workspace)

			// Build the client scoped to the run's workspace config (not the
			// process cwd) so worktree-mode invocations resolve the pipeline
			// identity for the target repo. Falls back to the env token when no
			// config is present. A typed-nil *config.Config must NOT be boxed into
			// the interface (it would read as non-nil and nil-deref), so pass a
			// nil resolver explicitly in that case.
			var resolver gh.TokenResolver
			owner := ""
			if cfg != nil {
				resolver = cfg
				owner = cfg.Owner
			}
			client, clientErr := gh.NewClientFromConfig(resolver, owner, "")
			if clientErr != nil {
				return fmt.Errorf("build GitHub client: %w", clientErr)
			}

			ctx, cancel := prStageContext(cmd, timeoutSec)
			defer cancel()

			// #1397: the runners report phases through a reporter on the
			// context. The scheduler attaches one backed by RuntimeState and
			// the live IPC events; out of process there is neither, so record
			// the transitions, stream them to stderr as they happen, and hand
			// the full array back in the result. The array alone would leave a
			// caller at 0/14 until the process exits.
			phases := newPrStagePhaseRecorder(cmd, outputJSON)
			runner := orchestrator.NewDefaultPRCreateRunner(client)
			res, runErr := runner.Run(pmstages.WithPhaseReporter(ctx, phases), issueNumber, repo, workspace)

			out := prStageResultJSON{
				Stage:      "pr-create",
				Path:       string(res.Path),
				PRNumber:   res.PRNumber,
				PRURL:      res.PRURL,
				Reason:     res.Reason,
				DurationMs: res.DurationMs,

				CommitMade:   res.CommitPerformed,
				CommitSHA:    res.CommitSHA,
				CommitReason: res.CommitReason,

				Phases: phases.Transitions(),
			}
			if runErr != nil {
				// Mirror the scheduler: an unexpected runner error is a punt to
				// the LLM path, never a rate-limit defer.
				out.Path = string(pmstages.CreatePathPunt)
				out.Reason = fmt.Sprintf("%s: %v", pmstages.ReasonUnexpected, runErr)
			} else if out.Path == string(pmstages.CreatePathPunt) {
				out.RateLimited = orchestrator.ReasonIndicatesRateLimit(res.Reason)
			}
			return emitPrStageResult(out, outputJSON)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Target repository as owner/name (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	cmd.Flags().IntVar(&timeoutSec, "timeout", 120, "Overall timeout in seconds (0 = no CLI timeout)")
	return cmd
}

// prStageMergeCmd runs `nightgauge pr-stage merge <issue>`.
func prStageMergeCmd() *cobra.Command {
	var (
		repo       string
		workdir    string
		outputJSON bool
		timeoutSec int
	)
	cmd := &cobra.Command{
		Use:   "merge <issue-number>",
		Short: "Deterministic-first pr-merge (waits out in-flight CI, then merges, or punts)",
		Long: `Read pr-{N}.json from --workdir, evaluate the merge decision matrix, and — when
the ONLY blocker is in-flight CI — poll the bounded CI-wait budget before merging
rather than punting to the LLM. A structural blocker (conflict, failed check,
blocking review) punts. A GitHub rate limit reports rate_limited=true so the
caller DEFERS instead of running the LLM into an exhausted bucket (#3976).`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber, err := parseIssueNumberArg(args[0])
			if err != nil {
				return err
			}
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			cfg := authenticatePrStageGh(workspace)

			if !cmd.Flags().Changed("timeout") {
				timeoutSec = prMergeDefaultTimeoutSec(workspace)
			}
			ctx, cancel := prStageContext(cmd, timeoutSec)
			defer cancel()

			// #1397 — see the pr-create verb above.
			phases := newPrStagePhaseRecorder(cmd, outputJSON)
			runner := newPrStageMergeRunner(cfg)
			res, runErr := runner.Run(pmstages.WithPhaseReporter(ctx, phases), issueNumber, repo, workspace)

			out := prStageResultJSON{
				Stage:      "pr-merge",
				Path:       string(res.Path),
				PRNumber:   res.PRNumber,
				PRState:    res.PRState,
				Reason:     res.Reason,
				DurationMs: res.DurationMs,

				Phases: phases.Transitions(),
			}
			if runErr != nil {
				out.Path = string(pmstages.PathPunt)
				out.Reason = fmt.Sprintf("%s: %v", pmstages.ReasonUnexpected, runErr)
			} else {
				// pr-merge sets the canonical ReasonRateLimited on a rate-limit
				// punt (matched exactly, like the scheduler does).
				out.RateLimited = res.Reason == pmstages.ReasonRateLimited
			}
			return emitPrStageResult(out, outputJSON)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Target repository as owner/name (informational; merge reads pr-{N}.json)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	cmd.Flags().IntVar(&timeoutSec, "timeout", prMergeCITimeoutSec, "Overall timeout in seconds (0 = no CLI timeout). When unset, raised to cover pipeline.merge_queue.wait_timeout plus 10m (#2214)")
	return cmd
}

// newPrStageMergeRunner builds the runner the scheduler would build for cfg, so
// the queue and CI budgets the budget verb reports are the ones the runner uses.
func newPrStageMergeRunner(cfg *config.Config) *pmstages.DeterministicRunner {
	runner := pmstages.NewDeterministicRunner()
	var pipeline *config.PipelineConfig
	var pr *config.PRConfig
	if cfg != nil {
		pipeline, pr = cfg.Pipeline, cfg.PR
		var resolver gh.TokenResolver = cfg
		if client, err := gh.NewClientFromConfig(resolver, cfg.Owner, ""); err == nil {
			runner.SetReviewFiling(followup.ReviewFiling(client, cfg))
		}
	}
	runner.SetMergeQueueWaitTimeout(pipeline.ResolveMergeQueueWaitTimeout())
	runner.SetCIWaitTimeout(pr.ResolveCICheckTimeout())
	return runner
}

// newPrStagePhaseRecorder builds the runner's phase sink.
//
// In --json mode (how the VS Code extension always invokes these verbs) the
// transitions are ALSO streamed to stderr as they happen, sentinel-prefixed, so
// the caller's phase count advances during the run rather than jumping at exit.
// stdout stays exactly one JSON object — that contract is what the extension's
// parser depends on — so the live channel is stderr.
//
// Without --json there is a human at the terminal and no consumer for the
// sentinel lines, so the recorder only accumulates.
func newPrStagePhaseRecorder(cmd *cobra.Command, outputJSON bool) *orchestrator.PhaseRecorder {
	if !outputJSON {
		return orchestrator.NewPhaseRecorder()
	}
	return orchestrator.NewStreamingPhaseRecorder(cmd.ErrOrStderr())
}

// prStageContext derives the runner context, applying a CLI-level timeout
// ceiling when timeoutSec > 0. The pr-merge runner also enforces its own bounded
// CI-wait budget internally; whichever fires first ends the wait.
func prStageContext(cmd *cobra.Command, timeoutSec int) (context.Context, context.CancelFunc) {
	base := cmd.Context()
	if timeoutSec <= 0 {
		return base, func() {}
	}
	return context.WithTimeout(base, time.Duration(timeoutSec)*time.Second)
}

// prMergeCITimeoutSec is the pr-merge verb's default ceiling for a CI wait.
const prMergeCITimeoutSec = 1200

// prStageClientMarginSec is how far the caller's kill sits past the CLI's own
// timeout, so the CLI's context always fires first and returns a structured
// result instead of dying to SIGKILL (#2730).
const prStageClientMarginSec = 60

// prStageBudgetJSON is the wire shape of `pr-stage budget`.
type prStageBudgetJSON struct {
	QueueWaitMs     int64 `json:"queue_wait_ms"`
	CLITimeoutMs    int64 `json:"cli_timeout_ms"`
	ClientTimeoutMs int64 `json:"client_timeout_ms"`
}

// prMergeQueueWait is the resolved pipeline.merge_queue.wait_timeout.
func prMergeQueueWait(workspace string) time.Duration {
	var pipeline *config.PipelineConfig
	if cfg, err := config.Load(workspace); err == nil && cfg != nil {
		pipeline = cfg.Pipeline
	}
	return pipeline.ResolveMergeQueueWaitTimeout()
}

// prMergeDefaultTimeoutSec returns the pr-merge verb's default --timeout. A
// merge-queue wait (#2214) runs up to pipeline.merge_queue.wait_timeout, so the
// CLI ceiling must not cut it short at the plain CI-wait default: it is the
// larger of the two, with ten minutes' margin over the queue wait.
func prMergeDefaultTimeoutSec(workspace string) int {
	queue := int((prMergeQueueWait(workspace) + 10*time.Minute).Seconds())
	if queue > prMergeCITimeoutSec {
		return queue
	}
	return prMergeCITimeoutSec
}

// prMergeBudget derives every pr-merge time limit from the one resolved queue
// wait (#2730). The binary owns this: the caller asks instead of holding a
// second constant, which is how a 20-minute client came to kill a 90-minute wait.
func prMergeBudget(workspace string) prStageBudgetJSON {
	cliSec := int64(prMergeDefaultTimeoutSec(workspace))
	return prStageBudgetJSON{
		QueueWaitMs:     prMergeQueueWait(workspace).Milliseconds(),
		CLITimeoutMs:    cliSec * 1000,
		ClientTimeoutMs: (cliSec + prStageClientMarginSec) * 1000,
	}
}

// prStageBudgetCmd runs `nightgauge pr-stage budget`.
func prStageBudgetCmd() *cobra.Command {
	var workdir string
	var outputJSON bool
	cmd := &cobra.Command{
		Use:          "budget",
		Short:        "Report the pr-merge time budget derived from pipeline.merge_queue.wait_timeout",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			budget := prMergeBudget(workspace)
			if outputJSON {
				data, err := json.Marshal(budget)
				if err != nil {
					return fmt.Errorf("marshal pr-stage budget: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queue wait %s, CLI timeout %s, client timeout %s\n",
				time.Duration(budget.QueueWaitMs)*time.Millisecond,
				time.Duration(budget.CLITimeoutMs)*time.Millisecond,
				time.Duration(budget.ClientTimeoutMs)*time.Millisecond)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	return cmd
}

// prStageReviewRoundCmd runs `nightgauge pr-stage review-round begin|end <N>`.
func prStageReviewRoundCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review-round",
		Short: "Open or close a pr-merge review-fix round (capped; #2730)",
	}
	verb := func(use, short string, apply func(workdir string, issue int, now time.Time) (pmstages.ReviewLedger, error)) *cobra.Command {
		var workdir string
		sub := &cobra.Command{
			Use:          use + " <issue-number>",
			Short:        short,
			Args:         cobra.ExactArgs(1),
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				issue, err := parseIssueNumberArg(args[0])
				if err != nil {
					return err
				}
				workspace, err := resolvePrStageWorkspace(workdir)
				if err != nil {
					return err
				}
				ledger, err := apply(workspace, issue, time.Now())
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "#%d: review round %d of %d, in progress: %t\n",
					issue, ledger.Rounds, pmstages.MaxReviewFixRounds, ledger.InProgress)
				return nil
			},
		}
		sub.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
		return sub
	}
	cmd.AddCommand(verb("begin", "Open a fix round; refuses past the cap or beside an open round", pmstages.BeginReviewRound))
	cmd.AddCommand(verb("end", "Close the open fix round", pmstages.EndReviewRound))
	return cmd
}

// prStageResumePointCmd runs `nightgauge pr-stage resume-point <N>`: whether a
// re-dispatch can start at feature-validate, restoring preserved work if it can
// (#2730). The decision lives in Go so both runtimes make the same one.
func prStageResumePointCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:          "resume-point <issue-number>",
		Short:        "Report whether a re-dispatch can resume at feature-validate from preserved work",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			issue, err := parseIssueNumberArg(args[0])
			if err != nil {
				return err
			}
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			authenticatePrStageGh(workspace)
			res := pmstages.ResolvePreservedResume(cmd.Context(), workspace, issue, pmstages.ExecIssueBodyReader(workspace))
			data, err := json.Marshal(res)
			if err != nil {
				return fmt.Errorf("marshal resume point: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
	return cmd
}

// prStageReviewGateCmd runs `nightgauge pr-stage review-gate <N>`: the gate
// DeterministicRunner.Run applies, for the LLM stage to run before it merges.
// Exit 0 with a JSON verdict either way; the caller reads `proceed`.
func prStageReviewGateCmd() *cobra.Command {
	var workdir string
	var outputJSON bool
	cmd := &cobra.Command{
		Use:          "review-gate <issue-number>",
		Short:        "Evaluate the enqueue review gate, filing open findings (#2730)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			issue, err := parseIssueNumberArg(args[0])
			if err != nil {
				return err
			}
			workspace, err := resolvePrStageWorkspace(workdir)
			if err != nil {
				return err
			}
			cfg := authenticatePrStageGh(workspace)
			out := newPrStageMergeRunner(cfg).ReviewGate(cmd.Context(), issue, workspace)
			if outputJSON {
				data, err := json.Marshal(out)
				if err != nil {
					return fmt.Errorf("marshal review gate: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d: %s, proceed=%t %s\n", issue, out.Verdict, out.Proceed, out.Reason)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace/worktree root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of human output")
	return cmd
}
