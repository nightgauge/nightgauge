package forgecmd

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/spf13/cobra"
)

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Project board operations",
		Long:  longProject,
	}
	cmd.AddCommand(projectFieldListCmd(), projectFieldSetCmd(), projectFieldGetCmd(),
		projectItemListCmd(), projectItemAddCmd(), projectItemRemoveCmd())
	return cmd
}

func projectFieldListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "field-list",
		Short:        "List project fields with type metadata",
		Long:         longProjectFieldList,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			snap, err := client.Project().SnapshotFields(cmd.Context())
			if err != nil {
				return emitError(cmd, fmt.Errorf("snapshot fields: %w", err))
			}
			out := make([]ProjectFieldJSON, 0, len(snap.Fields))
			for name, info := range snap.Fields {
				out = append(out, ProjectFieldJSON{
					V:       1,
					Name:    name,
					Type:    info.Type,
					ID:      info.ID,
					Options: info.Options,
				})
			}
			return renderForCmd(cmd, out)
		},
	}
	return cmd
}

func projectFieldSetCmd() *cobra.Command {
	var (
		itemID    string
		fieldName string
		value     string
		fieldType string
	)
	cmd := &cobra.Command{
		Use:          "field-set",
		Short:        "Set a field on a project board item",
		Long:         longProjectFieldSet,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if itemID == "" || fieldName == "" || fieldType == "" {
				return emitError(cmd, fmt.Errorf("--item-id, --field, and --type are required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			ctx := cmd.Context()
			ps := client.Project()
			switch fieldType {
			case "single-select":
				err = ps.SetSingleSelectField(ctx, itemID, fieldName, value)
			case "text":
				err = ps.SetTextField(ctx, itemID, fieldName, value)
			case "number":
				var n float64
				if _, scanErr := fmt.Sscanf(value, "%f", &n); scanErr != nil {
					return emitError(cmd, fmt.Errorf("invalid --value %q for number field", value))
				}
				err = ps.SetNumberField(ctx, itemID, fieldName, n)
			case "date":
				err = ps.SetDateField(ctx, itemID, fieldName, value)
			default:
				return emitError(cmd, fmt.Errorf("unsupported field type %q (want single-select, text, number, date)", fieldType))
			}
			if err != nil {
				return emitError(cmd, fmt.Errorf("set field: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v":     1,
				"set":   true,
				"name":  fieldName,
				"type":  fieldType,
				"value": value,
			})
		},
	}
	cmd.Flags().StringVar(&itemID, "item-id", "", "Project item ID")
	cmd.Flags().StringVar(&fieldName, "field", "", "Field name")
	cmd.Flags().StringVar(&value, "value", "", "Field value")
	cmd.Flags().StringVar(&fieldType, "type", "", "Field type: single-select, text, number, date")
	return cmd
}

func projectFieldGetCmd() *cobra.Command {
	var (
		number    int
		fieldName string
	)
	cmd := &cobra.Command{
		Use:          "field-get",
		Short:        "Read a field value off a board item",
		Long:         longProjectFieldGet,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if number <= 0 || fieldName == "" {
				return emitError(cmd, fmt.Errorf("--number and --field are required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			item, err := client.Board().GetItem(cmd.Context(), owner, repo, number)
			if err != nil {
				return emitError(cmd, fmt.Errorf("get board item: %w", err))
			}
			snap, err := client.Project().SnapshotFields(cmd.Context())
			if err != nil {
				return emitError(cmd, fmt.Errorf("snapshot fields: %w", err))
			}
			info, ok := snap.Fields[fieldName]
			fieldType := "unknown"
			if ok {
				fieldType = info.Type
			}
			value := readFieldValue(item, fieldName)
			_ = info
			return renderForCmd(cmd, ProjectFieldValueJSON{
				V: 1, Name: fieldName, Type: fieldType, Value: value,
			})
		},
	}
	cmd.Flags().IntVar(&number, "number", 0, "Issue number")
	cmd.Flags().StringVar(&fieldName, "field", "", "Field name")
	return cmd
}

func projectItemListCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:          "item-list",
		Short:        "List items on the project board",
		Long:         longProjectItemList,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			items, err := client.Board().ListItems(cmd.Context(), status)
			if err != nil {
				return emitError(cmd, fmt.Errorf("list board items: %w", err))
			}
			out := make([]BoardItemJSON, 0, len(items))
			for i := range items {
				out = append(out, BoardItemFromForge(&items[i]))
			}
			return renderForCmd(cmd, out)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Filter by status (e.g. Ready, 'In Progress')")
	return cmd
}

func projectItemAddCmd() *cobra.Command {
	var number int
	cmd := &cobra.Command{
		Use:          "item-add",
		Short:        "Add an issue to the project board",
		Long:         longProjectItemAdd,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if number <= 0 {
				return emitError(cmd, fmt.Errorf("--number is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			itemID, err := client.Project().AddIssueByNumber(cmd.Context(), owner, repo, number)
			if err != nil {
				return emitError(cmd, fmt.Errorf("add item: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v":      1,
				"added":  true,
				"number": number,
				"itemId": itemID,
			})
		},
	}
	cmd.Flags().IntVar(&number, "number", 0, "Issue number")
	return cmd
}

func projectItemRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "item-remove",
		Short:        "Remove an issue from the project board (not yet supported)",
		Long:         longProjectItemRemove,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitError(cmd, fmt.Errorf("forge project item-remove: %w", forge.ErrUnsupported))
		},
	}
	return cmd
}

// readFieldValue extracts a named field value off a BoardItem. The
// BoardItem struct exposes a fixed set of well-known fields; this
// helper maps common --field names onto those fields. Unknown field
// names return the empty string — callers should rely on the field
// type from SnapshotFields rather than guess.
func readFieldValue(item *forgetypes.BoardItem, name string) string {
	if item == nil {
		return ""
	}
	switch strings.ToLower(name) {
	case "status":
		return item.Status
	case "priority":
		return string(item.Priority)
	case "size":
		return string(item.Size)
	case "title":
		return item.Title
	case "url":
		return item.URL
	case "repo", "repository":
		return item.Repo
	case "state":
		return item.State
	case "pipelinestage", "pipeline-stage":
		return item.PipelineStage
	}
	return ""
}
