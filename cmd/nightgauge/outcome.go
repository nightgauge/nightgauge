package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

func outcomeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outcome",
		Short: "Outcome recording operations",
	}
	cmd.AddCommand(outcomeRecordCmd())
	cmd.AddCommand(outcomeRecordSelfHealCmd())
	return cmd
}

func outcomeRecordCmd() *cobra.Command {
	var (
		issueNumber   int
		prNumber      int
		modelID       string
		predictedSize string
		actualLines   int
		issueType     string
		workdir       string
	)

	cmd := &cobra.Command{
		Use:          "record",
		Short:        "Record a pipeline execution outcome to the complexity model",
		SilenceUsage: true,
		Example: `  nightgauge outcome record --issue 42 --pr 57 --model claude-sonnet-4-6 --predicted-size M --actual-lines 450
  nightgauge outcome record --issue 100 --pr 120 --model claude-opus-4-8 --predicted-size L --actual-lines 1200 --type feature`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNumber == 0 {
				return fmt.Errorf("--issue is required")
			}
			if prNumber == 0 {
				return fmt.Errorf("--pr is required")
			}

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			svc := gh.NewOutcomeService(workdir)
			result := svc.RecordOutcome(gh.OutcomeParams{
				IssueNumber:   issueNumber,
				PRNumber:      prNumber,
				ModelID:       modelID,
				PredictedSize: predictedSize,
				ActualLines:   actualLines,
				IssueType:     issueType,
			})

			return printJSON(result)
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number (required)")
	cmd.Flags().StringVar(&modelID, "model", "claude-sonnet-4-6", "Model ID used during pipeline")
	cmd.Flags().StringVar(&predictedSize, "predicted-size", "M", "Predicted size label (XS|S|M|L|XL)")
	cmd.Flags().IntVar(&actualLines, "actual-lines", 0, "Actual lines changed in PR")
	cmd.Flags().StringVar(&issueType, "type", "feature", "Issue type (feature|bug|docs|refactor|chore)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")

	// Support --actual-lines from env (for shell script integration)
	if envLines := os.Getenv("OUTCOME_ACTUAL_LINES"); envLines != "" {
		if v, err := strconv.Atoi(envLines); err == nil {
			actualLines = v
		}
	}

	return cmd
}

func outcomeRecordSelfHealCmd() *cobra.Command {
	var (
		issueNumber int
		category    string
		stage       string
		workdir     string
	)

	cmd := &cobra.Command{
		Use:          "record-self-heal",
		Short:        "Record a pipeline self-heal event to the complexity model",
		SilenceUsage: true,
		Example: `  nightgauge outcome record-self-heal --issue 42 --category stale_sdk_dist --stage feature-validate
  nightgauge outcome record-self-heal --issue 100 --category stale_sdk_dist --stage feature-dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNumber == 0 {
				return fmt.Errorf("--issue is required")
			}
			if category == "" {
				return fmt.Errorf("--category is required")
			}
			if stage == "" {
				return fmt.Errorf("--stage is required")
			}

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			svc := gh.NewOutcomeService(workdir)
			result := svc.RecordSelfHealEvent(issueNumber, category, stage)
			return printJSON(result)
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&category, "category", "", "Self-heal category (e.g. stale_sdk_dist) (required)")
	cmd.Flags().StringVar(&stage, "stage", "", "Pipeline stage where self-heal occurred (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")

	return cmd
}

// printOutcomeJSON is an alias used in tests — delegates to the shared printJSON helper.
func printOutcomeJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
