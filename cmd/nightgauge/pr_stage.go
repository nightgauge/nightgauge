package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
//	{ "stage": "pr-create"|"pr-merge", "path": "created"|"merged"|"punt",
//	  "pr_number": N, "pr_url": "...", "pr_state": "...", "reason": "...",
//	  "rate_limited": bool, "duration_ms": N }
//
// Exit codes:
//
//	0 — a result was produced (created / merged / punt / rate-limited). The
//	    caller reads `path` + `rate_limited` from the JSON, never the exit code,
//	    to decide skip-LLM vs LLM-fallthrough vs defer.
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
}

// resolvePrStageWorkspace resolves the workdir the runner reads context from and
// runs `gh` in. Defaults to the process cwd. The deterministic runners project
// issue/dev/validate context from `<workdir>/.nightgauge/pipeline/*-{N}.json`;
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
			// identity for the target repo. Falls back to env/CLI token when no
			// config is present. A typed-nil *config.Config must NOT be boxed into
			// the interface (it would read as non-nil and nil-deref), so pass a
			// nil resolver explicitly in that case.
			var resolver gh.TokenResolver
			owner := ""
			if cfg != nil {
				resolver = cfg
				owner = cfg.Owner
			}
			client, clientErr := gh.NewClientFromConfig(resolver, owner, globalToken)
			if clientErr != nil {
				return fmt.Errorf("build GitHub client: %w", clientErr)
			}

			ctx, cancel := prStageContext(cmd, timeoutSec)
			defer cancel()

			runner := orchestrator.NewDefaultPRCreateRunner(client)
			res, runErr := runner.Run(ctx, issueNumber, repo, workspace)

			out := prStageResultJSON{
				Stage:      "pr-create",
				Path:       string(res.Path),
				PRNumber:   res.PRNumber,
				PRURL:      res.PRURL,
				Reason:     res.Reason,
				DurationMs: res.DurationMs,
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
			authenticatePrStageGh(workspace)

			ctx, cancel := prStageContext(cmd, timeoutSec)
			defer cancel()

			runner := pmstages.NewDeterministicRunner()
			res, runErr := runner.Run(ctx, issueNumber, repo, workspace)

			out := prStageResultJSON{
				Stage:      "pr-merge",
				Path:       string(res.Path),
				PRNumber:   res.PRNumber,
				PRState:    res.PRState,
				Reason:     res.Reason,
				DurationMs: res.DurationMs,
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
	cmd.Flags().IntVar(&timeoutSec, "timeout", 1200, "Overall timeout in seconds (0 = no CLI timeout; bounds the CI-wait)")
	return cmd
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
