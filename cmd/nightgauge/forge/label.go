package forgecmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func labelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Label CRUD plus add/remove on issues and PRs",
		Long:  longLabel,
	}
	cmd.AddCommand(labelListCmd(), labelCreateCmd(), labelDeleteCmd(),
		labelAddCmd(), labelRemoveCmd())
	return cmd
}

func labelListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List labels defined on the repository",
		Long:         longLabelList,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			labels, err := client.Labels().List(cmd.Context())
			if err != nil {
				return emitError(cmd, fmt.Errorf("list labels: %w", err))
			}
			out := make([]LabelJSON, 0, len(labels))
			for _, l := range labels {
				out = append(out, LabelFromForge(l))
			}
			return renderForCmd(cmd, out)
		},
	}
	return cmd
}

func labelCreateCmd() *cobra.Command {
	var (
		name        string
		description string
		color       string
	)
	cmd := &cobra.Command{
		Use:          "create",
		Short:        "Create a new repository label",
		Long:         longLabelCreate,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return emitError(cmd, fmt.Errorf("--name is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			label, err := client.Labels().Create(cmd.Context(), name, description, color)
			if err != nil {
				return emitError(cmd, fmt.Errorf("create label: %w", err))
			}
			return renderForCmd(cmd, LabelFromForge(label))
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Label name")
	cmd.Flags().StringVar(&description, "description", "", "Label description")
	cmd.Flags().StringVar(&color, "color", "", "Hex colour without leading #")
	return cmd
}

func labelDeleteCmd() *cobra.Command {
	var labelID string
	cmd := &cobra.Command{
		Use:          "delete",
		Short:        "Delete a repository label by id",
		Long:         longLabelDelete,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if labelID == "" {
				return emitError(cmd, fmt.Errorf("--id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			if err := client.Labels().Delete(cmd.Context(), labelID); err != nil {
				return emitError(cmd, fmt.Errorf("delete label: %w", err))
			}
			return renderForCmd(cmd, map[string]any{"v": 1, "deleted": true, "id": labelID})
		},
	}
	cmd.Flags().StringVar(&labelID, "id", "", "Label node ID")
	return cmd
}

func labelAddCmd() *cobra.Command {
	var (
		issueID  string
		labelIDs string
	)
	cmd := &cobra.Command{
		Use:          "add",
		Short:        "Add labels to an issue or PR",
		Long:         longLabelAdd,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueID == "" || labelIDs == "" {
				return emitError(cmd, fmt.Errorf("--issue-id and --labels are required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			ids := splitCSV(labelIDs)
			if err := client.Issues().AddLabels(cmd.Context(), issueID, ids); err != nil {
				return emitError(cmd, fmt.Errorf("add labels: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v": 1, "added": true, "issueId": issueID, "labels": ids,
			})
		},
	}
	cmd.Flags().StringVar(&issueID, "issue-id", "", "Issue or PR node ID")
	cmd.Flags().StringVar(&labelIDs, "labels", "", "Comma-separated label node IDs")
	return cmd
}

func labelRemoveCmd() *cobra.Command {
	var (
		issueID  string
		labelIDs string
	)
	cmd := &cobra.Command{
		Use:          "remove",
		Short:        "Remove labels from an issue or PR",
		Long:         longLabelRemove,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueID == "" || labelIDs == "" {
				return emitError(cmd, fmt.Errorf("--issue-id and --labels are required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			ids := splitCSV(labelIDs)
			if err := client.Issues().RemoveLabels(cmd.Context(), issueID, ids); err != nil {
				return emitError(cmd, fmt.Errorf("remove labels: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v": 1, "removed": true, "issueId": issueID, "labels": ids,
			})
		},
	}
	cmd.Flags().StringVar(&issueID, "issue-id", "", "Issue or PR node ID")
	cmd.Flags().StringVar(&labelIDs, "labels", "", "Comma-separated label node IDs")
	return cmd
}
