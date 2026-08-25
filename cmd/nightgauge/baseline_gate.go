package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/baselineGate"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/spf13/cobra"
)

// baselineGateCmd is the top-level "baseline-gate" command. It exposes two
// subcommands:
//   - check: evaluate one issue body against `main`'s recent CI runs and
//     decide allow/defer.
//   - promote: re-evaluate every queue item paused with kind=baseline_ci_red
//     and resume those whose baseline has gone green.
//
// See Issue #3004.
func baselineGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baseline-gate",
		Short: "Baseline-CI dependency preflight gate",
		Long: `Defers issues whose acceptance criteria require promoting a CI check on main
when main's recent runs of that check are failing. Daily cron promotes
deferred items back to the queue when the baseline goes green.`,
	}
	cmd.AddCommand(baselineGateCheckCmd(), baselineGatePromoteCmd())
	return cmd
}

// baselineGateCheckCmd evaluates whether the issue at --issue should be
// dispatched or deferred. Exit codes:
//
//	0 — allow dispatch (or unparseable; treated as allow)
//	1 — defer dispatch
//	2 — config or IO error
func baselineGateCheckCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		issueNum   int
		branch     string
		configPath string
		outputJSON bool
		pauseQueue bool
	)

	cmd := &cobra.Command{
		Use:          "check",
		Short:        "Evaluate an issue against the baseline-CI gate",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNum <= 0 {
				return fmt.Errorf("--issue must be a positive integer")
			}

			cfg := baselineGate.LoadGateConfigFromYAML(configPath)
			if !cfg.Enabled {
				if outputJSON {
					return printJSON(checkJSONResult{
						Decision: string(baselineGate.DecisionAllow),
						Reason:   "baseline_ci_gate disabled in config",
					})
				}
				fmt.Println("Baseline gate: DISABLED")
				return nil
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}
			ownerPart, repoPart := splitRepo(owner, repo)
			issueSvc := gh.NewIssueService(client)
			issue, err := issueSvc.GetIssue(cmd.Context(), ownerPart, repoPart, issueNum)
			if err != nil {
				return fmt.Errorf("fetch issue #%d: %w", issueNum, enrichError(err))
			}

			runner := gh.NewCIService(client)
			eval := baselineGate.NewEvaluator(cfg, runner)
			res, err := eval.EvaluateForBody(cmd.Context(), issue.Body, ownerPart, repoPart, branch)
			if err != nil {
				return fmt.Errorf("evaluate baseline gate: %w", err)
			}

			if outputJSON {
				out := checkJSONResult{
					Decision:    string(res.Decision),
					Reason:      res.Reason,
					Workflow:    res.Workflow,
					Job:         res.Job,
					FailedRuns:  res.FailedRuns,
					SampledRuns: res.SampledRuns,
					RunIDs:      res.RunIDs,
					TriggerText: res.TriggerText,
					IssueNumber: issueNum,
				}
				if err := printJSON(out); err != nil {
					return err
				}
			} else {
				renderCheckHuman(res, issueNum)
			}

			if res.Decision != baselineGate.DecisionDefer {
				return nil
			}

			// Defer: optionally pause the queue item via the orchestrator scheduler.
			if pauseQueue {
				if err := pauseDeferredQueueItem(ownerPart, repoPart, issueNum, issue.Title, res, cfg.LookbackRuns); err != nil {
					fmt.Fprintf(os.Stderr, "warning: pause-deferred failed: %v\n", err)
				}
			}

			return fmt.Errorf("baseline-ci red: %s", res.Reason)
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	repoNameFlag(cmd, &repo, "", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "GitHub issue number to evaluate (required)")
	cmd.Flags().StringVar(&branch, "branch", "main", "Branch to check baseline runs against")
	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config.yaml")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&pauseQueue, "pause-queue", true, "When deferred, pause/insert the queue item with kind=baseline_ci_red")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// baselineGatePromoteCmd re-evaluates every queue item with paused kind=
// "baseline_ci_red" and resumes those whose last GreenThreshold runs on
// `main` are all `success`.
func baselineGatePromoteCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		branch     string
		configPath string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "promote",
		Short:        "Promote deferred queue items back to the queue when baseline is green",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := baselineGate.LoadGateConfigFromYAML(configPath)

			ownerPart, repoPart := splitRepo(owner, repo)
			sched, err := getQueueScheduler(ownerPart, 0)
			if err != nil {
				return fmt.Errorf("init scheduler: %w", err)
			}

			// A disabled gate still reports, and must do so WITHOUT building a
			// forge client — the sweep is a no-op and paying for auth to say
			// so would be the wrong shape. PromoteBaselineDeferrals returns
			// the disabled summary before touching the evaluator, so a nil one
			// is safe on that path.
			var eval orchestrator.BaselinePromoteEvaluator
			if cfg.Enabled {
				client, cerr := clientFromConfig()
				if cerr != nil {
					return fmt.Errorf("create GitHub client: %w", cerr)
				}
				eval = baselineGate.NewEvaluator(cfg, gh.NewCIService(client))
			}

			// THE sweep lives in internal/orchestrator (#885) so this verb and
			// the autonomous daemon share one implementation. This function is
			// now only argument marshalling and rendering.
			summary := orchestrator.PromoteBaselineDeferrals(
				cmd.Context(), sched, eval,
				ownerPart, repoPart, branch,
				cfg.GreenThreshold, cfg.Enabled)

			if outputJSON {
				return printJSON(summary)
			}
			renderPromoteHuman(summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	repoNameFlag(cmd, &repo, "", "GitHub repository name (defaults to config)")
	cmd.Flags().StringVar(&branch, "branch", "main", "Branch to check baseline runs against")
	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config.yaml")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

// pauseDeferredQueueItem inserts (or marks paused) the issue in the queue
// with the baseline_ci_red reason. Best-effort — errors are surfaced via the
// returned error but the gate still exits 1 to signal defer to the skill.
func pauseDeferredQueueItem(owner, repo string, issueNumber int, title string, res *baselineGate.GateResult, lookback int) error {
	sched, err := getQueueScheduler(owner, 0)
	if err != nil {
		return err
	}
	repoFull := repo
	if !strings.Contains(repoFull, "/") {
		repoFull = owner + "/" + repo
	}
	jobNote := ""
	if res.Job != "" {
		jobNote = " " + res.Job
	}
	summary := fmt.Sprintf("baseline-ci red: %s%s failed %d/%d recent runs",
		res.Workflow, jobNote, res.FailedRuns, res.SampledRuns)
	sched.PauseDeferred(orchestrator.QueueItem{
		Repo:        repoFull,
		IssueNumber: issueNumber,
		Title:       title,
	}, orchestrator.QueuePausedReason{
		Kind:         "baseline_ci_red",
		Summary:      summary,
		Workflow:     res.Workflow,
		Job:          res.Job,
		FailedRuns:   res.FailedRuns,
		LookbackRuns: lookback,
	})
	return nil
}

// checkJSONResult is the JSON shape for `baseline-gate check --json`.
type checkJSONResult struct {
	IssueNumber int     `json:"issue_number,omitempty"`
	Decision    string  `json:"decision"`
	Reason      string  `json:"reason"`
	Workflow    string  `json:"workflow,omitempty"`
	Job         string  `json:"job,omitempty"`
	FailedRuns  int     `json:"failed_runs,omitempty"`
	SampledRuns int     `json:"sampled_runs,omitempty"`
	RunIDs      []int64 `json:"run_ids,omitempty"`
	TriggerText string  `json:"trigger_text,omitempty"`
}

func renderCheckHuman(res *baselineGate.GateResult, issueNum int) {
	switch res.Decision {
	case baselineGate.DecisionAllow:
		fmt.Printf("Baseline gate: PASSED\n")
		fmt.Printf("Issue #%d: %s\n", issueNum, res.Reason)
	case baselineGate.DecisionUnparseable:
		fmt.Printf("Baseline gate: UNPARSEABLE (allowing dispatch)\n")
		fmt.Printf("Issue #%d: %s\n", issueNum, res.Reason)
	case baselineGate.DecisionDefer:
		fmt.Fprintf(os.Stderr, "Baseline gate: DEFERRED\n")
		fmt.Fprintf(os.Stderr, "Issue #%d: %s\n", issueNum, res.Reason)
		if res.Workflow != "" {
			fmt.Fprintf(os.Stderr, "Workflow: %s", res.Workflow)
			if res.Job != "" {
				fmt.Fprintf(os.Stderr, " job=%s", res.Job)
			}
			fmt.Fprintf(os.Stderr, " — failed %d/%d recent runs\n", res.FailedRuns, res.SampledRuns)
		}
	}
}

func renderPromoteHuman(s orchestrator.BaselinePromoteSummary) {
	fmt.Printf("Baseline-defer promote sweep — %s/%s @%s\n", s.Owner, s.Repo, s.Branch)
	fmt.Printf("  Total deferred:  %d\n", s.Total)
	fmt.Printf("  Promoted:        %d\n", len(s.Promoted))
	fmt.Printf("  Still paused:    %d\n", len(s.StillPaused))
	fmt.Printf("  Errors:          %d\n", len(s.Errors))
	for _, p := range s.Promoted {
		fmt.Printf("  ✓ #%d (%s)\n", p.IssueNumber, p.Workflow)
	}
	for _, p := range s.Errors {
		fmt.Printf("  ✗ #%d: %s\n", p.IssueNumber, p.Error)
	}
}

// ensure json package is referenced (used elsewhere in the binary; keep
// import here to avoid future drift if printJSON moves).
var _ = json.Marshal
