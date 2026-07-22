package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/baselineGate"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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

			cfg := loadBaselineGateConfigFromYAML(configPath)
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
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
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
			cfg := loadBaselineGateConfigFromYAML(configPath)

			ownerPart, repoPart := splitRepo(owner, repo)
			sched, err := getQueueScheduler(ownerPart, 0)
			if err != nil {
				return fmt.Errorf("init scheduler: %w", err)
			}

			items := sched.ListPausedByKind("baseline_ci_red")
			summary := promoteSummary{
				Owner:       ownerPart,
				Repo:        repoPart,
				Branch:      branch,
				Total:       len(items),
				Promoted:    []promoteEntry{},
				StillPaused: []promoteEntry{},
				Errors:      []promoteEntry{},
				EvaluatedAt: time.Now().UTC().Format(time.RFC3339),
			}

			if !cfg.Enabled {
				summary.Disabled = true
				if outputJSON {
					return printJSON(summary)
				}
				fmt.Println("Baseline gate: DISABLED — skipping promote sweep")
				return nil
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}
			runner := gh.NewCIService(client)
			eval := baselineGate.NewEvaluator(cfg, runner)

			for _, item := range items {
				if item.PausedReason == nil || item.PausedReason.Workflow == "" {
					continue
				}
				green, runIDs, err := eval.IsLastNGreen(cmd.Context(),
					ownerPart, repoPart,
					item.PausedReason.Workflow, branch, item.PausedReason.Job,
					cfg.GreenThreshold)

				entry := promoteEntry{
					IssueNumber: item.IssueNumber,
					Workflow:    item.PausedReason.Workflow,
					Job:         item.PausedReason.Job,
					RunIDs:      runIDs,
				}
				if err != nil {
					entry.Error = err.Error()
					summary.Errors = append(summary.Errors, entry)
					continue
				}
				if !green {
					summary.StillPaused = append(summary.StillPaused, entry)
					continue
				}
				if sched.ResumeByIssueNumber(item.IssueNumber) {
					summary.Promoted = append(summary.Promoted, entry)
				} else {
					entry.Error = "resume failed: queue entry not found or already resumed"
					summary.Errors = append(summary.Errors, entry)
				}
			}

			if outputJSON {
				return printJSON(summary)
			}
			renderPromoteHuman(summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
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

// promoteSummary is the JSON shape for `baseline-gate promote --json`.
type promoteSummary struct {
	Owner       string         `json:"owner"`
	Repo        string         `json:"repo"`
	Branch      string         `json:"branch"`
	Total       int            `json:"total"`
	Promoted    []promoteEntry `json:"promoted"`
	StillPaused []promoteEntry `json:"still_paused"`
	Errors      []promoteEntry `json:"errors"`
	Disabled    bool           `json:"disabled,omitempty"`
	EvaluatedAt string         `json:"evaluated_at"`
}

// promoteEntry is one row in a promoteSummary list.
type promoteEntry struct {
	IssueNumber int     `json:"issue_number"`
	Workflow    string  `json:"workflow,omitempty"`
	Job         string  `json:"job,omitempty"`
	RunIDs      []int64 `json:"run_ids,omitempty"`
	Error       string  `json:"error,omitempty"`
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

func renderPromoteHuman(s promoteSummary) {
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

// baselineGateYAML is the YAML shape for the pipeline.baseline_ci_gate config
// section. Same convention as `loadSizeGateConfigFromYAML`.
type baselineGateYAML struct {
	Pipeline struct {
		BaselineCIGate struct {
			Enabled        *bool `yaml:"enabled"`
			LookbackRuns   *int  `yaml:"lookback_runs"`
			RedThreshold   *int  `yaml:"red_threshold"`
			GreenThreshold *int  `yaml:"green_threshold"`
		} `yaml:"baseline_ci_gate"`
	} `yaml:"pipeline"`
}

// loadBaselineGateConfigFromYAML reads pipeline.baseline_ci_gate from the
// YAML config file, applying defaults for missing fields. When the file is
// absent, defaults are used; the gate is never disabled by a missing config.
func loadBaselineGateConfigFromYAML(configPath string) baselineGate.GateConfig {
	cfg := baselineGate.DefaultGateConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg
	}
	var y baselineGateYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return cfg
	}
	bg := y.Pipeline.BaselineCIGate
	if bg.Enabled != nil {
		cfg.Enabled = *bg.Enabled
	}
	if bg.LookbackRuns != nil {
		cfg.LookbackRuns = *bg.LookbackRuns
	}
	if bg.RedThreshold != nil {
		cfg.RedThreshold = *bg.RedThreshold
	}
	if bg.GreenThreshold != nil {
		cfg.GreenThreshold = *bg.GreenThreshold
	}
	return cfg
}

// ensure json package is referenced (used elsewhere in the binary; keep
// import here to avoid future drift if printJSON moves).
var _ = json.Marshal
