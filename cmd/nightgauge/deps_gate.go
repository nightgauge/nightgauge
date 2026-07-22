package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/spf13/cobra"
)

// depsGateCmd is the top-level "deps-gate" command. It mirrors baseline-gate
// (Issue #3004) for the native-blockedBy dependency case (Issue #231):
//
//   - check:   evaluate one issue's open `blockedBy` dependencies and decide
//     allow/defer. On defer it pauses the queue item with kind=
//     "blocked_dependency" and exits 1 so the pickup skill records a deferral
//     instead of a misclassified pipeline failure.
//   - promote: re-evaluate every queue item paused with kind=
//     "blocked_dependency" and resume those whose blockers have all closed.
//
// Determinism: like baseline-gate, this makes only the `gh` GraphQL calls that
// hooks.EvaluateIssueDeps already makes (internal/ MUST stay deterministic — no
// LLM calls). See docs/FAILURE_TAXONOMY.md for the `[blocked-dependency]`
// infrastructure pattern.
func depsGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deps-gate",
		Short: "Native blockedBy dependency preflight gate",
		Long: `Defers issue pickup when the issue has an OPEN native blockedBy dependency
(the blocker's PR is not yet merged). The deferral is a controlled hold, not a
failure — the item is paused and automatically resumed when its blockers close
(deps-gate promote sweep, or the autonomous cascade).`,
	}
	cmd.AddCommand(depsGateCheckCmd(), depsGatePromoteCmd())
	return cmd
}

// depsGateCheckCmd evaluates whether the issue at --issue should be dispatched
// or deferred. Exit codes:
//
//	0 — allow dispatch (no open blockers)
//	1 — defer dispatch (one or more open blockers)
//	2 — config or IO error
func depsGateCheckCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		issueNum   int
		outputJSON bool
		pauseQueue bool
	)

	cmd := &cobra.Command{
		Use:          "check",
		Short:        "Evaluate an issue against the native blockedBy dependency gate",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNum <= 0 {
				return fmt.Errorf("--issue must be a positive integer")
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}
			ownerPart, repoPart := splitRepo(owner, repo)
			issueSvc := gh.NewIssueService(client)

			res, err := evaluateDepsGate(cmd.Context(), issueSvc, ownerPart, repoPart, issueNum)
			if err != nil {
				return fmt.Errorf("evaluate deps gate for #%d: %w", issueNum, enrichError(err))
			}

			if outputJSON {
				if err := printJSON(res); err != nil {
					return err
				}
			} else {
				renderDepsCheckHuman(res)
			}

			if res.Decision != depsDecisionDeferred {
				return nil
			}

			// Defer: optionally pause the queue item via the orchestrator scheduler.
			if pauseQueue {
				sched, serr := getQueueScheduler(ownerPart, 0)
				if serr != nil {
					fmt.Fprintf(os.Stderr, "warning: pause-deferred failed: %v\n", serr)
				} else {
					title := ""
					if issue, ierr := issueSvc.GetIssue(cmd.Context(), ownerPart, repoPart, issueNum); ierr == nil {
						title = issue.Title
					}
					pauseBlockedDependencyItem(sched, ownerPart, repoPart, issueNum, title, res)
				}
			}

			return fmt.Errorf("blocked-dependency: %s", res.Reason)
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "GitHub issue number to evaluate (required)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&pauseQueue, "pause-queue", true, "When deferred, pause/insert the queue item with kind=blocked_dependency")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// depsGatePromoteCmd re-evaluates every queue item with paused kind=
// "blocked_dependency" and resumes those whose blockers have all closed.
func depsGatePromoteCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "promote",
		Short:        "Promote deferred queue items back to the queue when their blockers close",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ownerPart, _ := splitRepo(owner, repo)
			sched, err := getQueueScheduler(ownerPart, 0)
			if err != nil {
				return fmt.Errorf("init scheduler: %w", err)
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}
			issueSvc := gh.NewIssueService(client)

			summary := depsGatePromoteSweep(cmd.Context(), sched, issueSvc, ownerPart)

			if outputJSON {
				return printJSON(summary)
			}
			renderDepsPromoteHuman(summary)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

const (
	depsDecisionAllow    = "allow"
	depsDecisionDeferred = "deferred"
)

// depsGateCheckResult is the JSON shape for `deps-gate check --json`.
type depsGateCheckResult struct {
	IssueNumber      int                    `json:"issue_number"`
	Decision         string                 `json:"decision"` // "allow" | "deferred"
	OpenDependencies []hooks.OpenDependency `json:"open_dependencies,omitempty"`
	OpenCount        int                    `json:"open_count"`
	Reason           string                 `json:"reason,omitempty"`
}

// evaluateDepsGate runs the deterministic blockedBy check and maps it to an
// allow/defer decision. Extracted from the RunE closure so it is unit-testable
// with a fake IssueFetcher (no network).
func evaluateDepsGate(ctx context.Context, fetcher hooks.IssueFetcher, owner, repo string, issueNum int) (depsGateCheckResult, error) {
	res, err := hooks.EvaluateIssueDeps(ctx, fetcher, owner, repo, issueNum)
	if err != nil {
		return depsGateCheckResult{}, err
	}
	out := depsGateCheckResult{
		IssueNumber:      issueNum,
		OpenDependencies: res.OpenDependencies,
		OpenCount:        res.OpenCount,
	}
	if res.HasOpenDependencies {
		out.Decision = depsDecisionDeferred
		out.Reason = depsDeferReason(issueNum, res.OpenDependencies)
	} else {
		out.Decision = depsDecisionAllow
	}
	return out, nil
}

// depsDeferReason builds the human summary naming the open blockers.
func depsDeferReason(issueNum int, blockers []hooks.OpenDependency) string {
	nums := make([]string, 0, len(blockers))
	for _, b := range blockers {
		nums = append(nums, fmt.Sprintf("#%d", b.Number))
	}
	return fmt.Sprintf("blocked by open dependency %s (PR not merged)", strings.Join(nums, ", "))
}

// pauseBlockedDependencyItem inserts (or marks paused) the issue in the queue
// with the blocked_dependency reason, naming its open blockers so the promote
// sweep can re-evaluate without re-parsing the issue.
func pauseBlockedDependencyItem(sched *orchestrator.Scheduler, owner, repo string, issueNumber int, title string, res depsGateCheckResult) {
	repoFull := repo
	if repoFull != "" && !strings.Contains(repoFull, "/") {
		repoFull = owner + "/" + repo
	}
	refs := make([]orchestrator.QueueBlockingRef, 0, len(res.OpenDependencies))
	for _, d := range res.OpenDependencies {
		refs = append(refs, orchestrator.QueueBlockingRef{
			Number: d.Number,
			Title:  d.Title,
			State:  d.State,
		})
	}
	sched.PauseDeferred(orchestrator.QueueItem{
		Repo:        repoFull,
		IssueNumber: issueNumber,
		Title:       title,
	}, orchestrator.QueuePausedReason{
		Kind:           "blocked_dependency",
		Summary:        res.Reason,
		BlockingIssues: refs,
	})
}

// depsGatePromoteSummary is the JSON shape for `deps-gate promote --json`.
type depsGatePromoteSummary struct {
	Owner       string             `json:"owner"`
	Total       int                `json:"total"`
	Promoted    []depsPromoteEntry `json:"promoted"`
	StillPaused []depsPromoteEntry `json:"still_paused"`
	Errors      []depsPromoteEntry `json:"errors"`
	EvaluatedAt string             `json:"evaluated_at"`
}

// depsPromoteEntry is one row in a depsGatePromoteSummary list.
type depsPromoteEntry struct {
	IssueNumber int    `json:"issue_number"`
	OpenCount   int    `json:"open_count,omitempty"`
	Error       string `json:"error,omitempty"`
}

// depsGatePromoteSweep re-evaluates every blocked_dependency-paused item and
// resumes those whose blockers have all closed. Extracted from the RunE closure
// so it is unit-testable with a fake IssueFetcher (no network).
func depsGatePromoteSweep(ctx context.Context, sched *orchestrator.Scheduler, fetcher hooks.IssueFetcher, defaultOwner string) depsGatePromoteSummary {
	items := sched.ListPausedByKind("blocked_dependency")
	summary := depsGatePromoteSummary{
		Owner:       defaultOwner,
		Total:       len(items),
		Promoted:    []depsPromoteEntry{},
		StillPaused: []depsPromoteEntry{},
		Errors:      []depsPromoteEntry{},
		EvaluatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	for _, item := range items {
		o, r := ownerRepoForItem(item.Repo, defaultOwner)
		res, err := hooks.EvaluateIssueDeps(ctx, fetcher, o, r, item.IssueNumber)
		entry := depsPromoteEntry{IssueNumber: item.IssueNumber, OpenCount: res.OpenCount}
		if err != nil {
			entry.Error = err.Error()
			summary.Errors = append(summary.Errors, entry)
			continue
		}
		if res.OpenCount > 0 {
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
	return summary
}

// ownerRepoForItem splits a queue item's "owner/repo" string, falling back to
// defaultOwner when the item carries only a bare repo name (or nothing).
func ownerRepoForItem(itemRepo, defaultOwner string) (string, string) {
	if strings.Contains(itemRepo, "/") {
		parts := strings.SplitN(itemRepo, "/", 2)
		return parts[0], parts[1]
	}
	return defaultOwner, itemRepo
}

func renderDepsCheckHuman(res depsGateCheckResult) {
	if res.Decision == depsDecisionAllow {
		fmt.Printf("Dependency gate: PASSED\n")
		fmt.Printf("Issue #%d: no open blockers\n", res.IssueNumber)
		return
	}
	fmt.Fprintf(os.Stderr, "Dependency gate: DEFERRED\n")
	fmt.Fprintf(os.Stderr, "Issue #%d: %s\n", res.IssueNumber, res.Reason)
	for _, b := range res.OpenDependencies {
		fmt.Fprintf(os.Stderr, "  - #%d %s (%s)\n", b.Number, b.Title, b.State)
	}
}

func renderDepsPromoteHuman(s depsGatePromoteSummary) {
	fmt.Printf("Blocked-dependency promote sweep — %s\n", s.Owner)
	fmt.Printf("  Total deferred:  %d\n", s.Total)
	fmt.Printf("  Promoted:        %d\n", len(s.Promoted))
	fmt.Printf("  Still paused:    %d\n", len(s.StillPaused))
	fmt.Printf("  Errors:          %d\n", len(s.Errors))
	for _, p := range s.Promoted {
		fmt.Printf("  ✓ #%d (blockers all closed)\n", p.IssueNumber)
	}
	for _, p := range s.Errors {
		fmt.Printf("  ✗ #%d: %s\n", p.IssueNumber, p.Error)
	}
}
