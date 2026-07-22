package main

import (
	"fmt"
	"strconv"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/spf13/cobra"
)

func sizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "size",
		Short: "Size operations (prediction)",
	}
	cmd.AddCommand(sizePredictCmd())
	return cmd
}

func sizePredictCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "predict [issue-number]",
		Short:        "Predict complexity size label for an issue from its metadata",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge size predict 3081
  nightgauge size predict 3081 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, number)
			if err != nil {
				return err
			}

			score := complexity.NewEstimator().Estimate(complexity.Input{
				Title:         issue.Title,
				Body:          issue.Body,
				Labels:        issue.Labels,
				SubIssueCount: len(issue.SubIssues),
			})

			if outputJSON {
				return printJSON(score)
			}

			reasoning := score.Reasoning
			if reasoning == "" {
				reasoning = "no strong signals"
			}
			fmt.Printf("Issue #%d: %s (score=%d/10, confidence=%s) — %s\n",
				number, score.SizeLabel, score.Value, score.Confidence,
				strings.ToLower(reasoning))
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}
