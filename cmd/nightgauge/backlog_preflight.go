package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/backlogpreflight"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

// backlogCmd is the top-level "backlog" command. It exposes subcommands for
// backlog management operations. See Issue #3084.
func backlogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backlog",
		Short: "Backlog management operations",
	}
	cmd.AddCommand(backlogPreflightCmd())
	return cmd
}

// backlogPreflightCmd validates backlog issues are pipeline-ready.
// Exit codes:
//
//	0 — all checks pass (no findings)
//	1 — findings found (one or more validation failures)
//	2 — config or IO error
func backlogPreflightCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		status        string
		focus         string
		issueNum      int
		projectNumber int
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Validate backlog issues are pipeline-ready",
		Long: `Run deterministic validation checks on project board items to confirm they
meet pipeline entry requirements. Checks: required type:* label, board fields
(Size/Priority), acceptance criteria quality, dependency cycles, and greenfield
project structure.

Exit codes:
  0 — all checks pass (no findings)
  1 — findings found
  2 — config or IO error`,
		Example: `  nightgauge backlog preflight --status Ready --json
  nightgauge backlog preflight --status Ready --focus labels
  nightgauge backlog preflight --status Ready --issue 42 --json
  nightgauge backlog preflight --focus greenfield`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}

			ownerPart, repoPart := splitRepo(owner, repo)

			boardSvc := gh.NewBoardService(client, ownerPart, projectNumber, getOwnerType(cmd))
			items, err := boardSvc.ListItems(cmd.Context(), status)
			if err != nil {
				return fmt.Errorf("fetch board items (status=%q): %w", status, enrichError(err))
			}

			// Filter to a single issue when --issue is set.
			if issueNum > 0 {
				filtered := items[:0]
				for _, item := range items {
					if item.Number == issueNum {
						filtered = append(filtered, item)
					}
				}
				if len(filtered) == 0 {
					return fmt.Errorf("issue #%d not found in project board with status=%q", issueNum, status)
				}
				items = filtered
			}

			issueSvc := gh.NewIssueService(client)
			v := backlogpreflight.New(boardSvc, issueSvc, ownerPart, repoPart)

			var findings []backlogpreflight.BacklogFinding
			normalizedFocus := strings.ToLower(focus)

			if normalizedFocus == "all" || normalizedFocus == "labels" {
				findings = append(findings, v.CheckLabels(items)...)
				findings = append(findings, v.CheckBoardFields(items)...)
			}
			if normalizedFocus == "all" || normalizedFocus == "criteria" {
				findings = append(findings, v.CheckAcceptanceCriteria(cmd.Context(), items)...)
			}
			if normalizedFocus == "all" || normalizedFocus == "dependencies" {
				findings = append(findings, v.CheckDependencyCycles(items)...)
			}
			if normalizedFocus == "all" || normalizedFocus == "greenfield" {
				workdir, _ := os.Getwd()
				findings = append(findings, v.CheckGreenfield(workdir)...)
			}

			report := backlogpreflight.BuildReport(ownerPart, repoPart, status, focus, items, findings)
			report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

			if outputJSON {
				if err := printJSON(report); err != nil {
					return err
				}
				if len(findings) > 0 {
					os.Exit(1)
				}
				return nil
			}

			// Human-readable output.
			renderPreflightHuman(report)

			if len(findings) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
	cmd.Flags().StringVar(&status, "status", "Ready", "Project board status filter (e.g. Ready, Backlog)")
	cmd.Flags().StringVar(&focus, "focus", "all", "Checks to run: all|labels|criteria|dependencies|greenfield")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "Single issue number to validate (0 = all issues in --status)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (defaults to config)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output results as JSON")
	return cmd
}

// renderPreflightHuman prints a human-readable summary of the preflight report.
func renderPreflightHuman(report backlogpreflight.BacklogPreflightReport) {
	s := report.Summary
	fmt.Printf("Backlog Preflight — %s (%d issues)\n", report.Status, s.TotalIssues)
	if len(report.Findings) == 0 {
		fmt.Printf("✓ All %d issues ready for pipeline\n", s.TotalIssues)
		return
	}
	fmt.Printf("✓ %d issues ready for pipeline\n", s.IssuesClean)
	fmt.Printf("✗ %d issues need attention\n\n", s.IssuesFlagged)
	for _, f := range report.Findings {
		if f.IssueNumber > 0 {
			fmt.Printf("  #%d %s — %s\n", f.IssueNumber, f.IssueTitle, f.Detail)
		} else {
			fmt.Printf("  [project] %s\n", f.Detail)
		}
	}
}
