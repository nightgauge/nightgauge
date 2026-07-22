package forgecmd

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge/output"
	"github.com/spf13/cobra"
)

// issueCmd is the `forge issue` group.
func issueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue operations",
		Long:  longIssue,
	}
	cmd.AddCommand(issueListCmd(), issueViewCmd(), issueCreateCmd(),
		issueEditCmd(), issueCloseCmd(), issueReopenCmd(), issueCommentCmd())
	return cmd
}

// --- list ---

func issueListCmd() *cobra.Command {
	var labels string
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List issues in a repo",
		Long:         longIssueList,
		SilenceUsage: true,
		Example:      `  nightgauge forge issue list --repo nightgauge/nightgauge --labels type:bug --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			labelList := splitCSV(labels)
			issues, err := client.Issues().ListIssues(cmd.Context(), owner, repo, labelList)
			if err != nil {
				return emitError(cmd, fmt.Errorf("list issues: %w", err))
			}
			out := make([]IssueJSON, 0, len(issues))
			for i := range issues {
				out = append(out, IssueFromForge(&issues[i]))
			}
			return renderForCmd(cmd, out)
		},
	}
	cmd.Flags().StringVar(&labels, "labels", "", "Comma-separated label filter")
	return cmd
}

// --- view ---

func issueViewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "view <number>",
		Short:        "View a single issue by number",
		Long:         longIssueView,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example:      `  nightgauge forge issue view 3362 --repo nightgauge/nightgauge --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := parseNumber(args[0])
			if err != nil {
				return emitError(cmd, err)
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			issue, err := client.Issues().GetIssue(cmd.Context(), owner, repo, number)
			if err != nil {
				return emitError(cmd, fmt.Errorf("get issue: %w", err))
			}
			return renderForCmd(cmd, IssueFromForge(issue))
		},
	}
	return cmd
}

// --- create ---

func issueCreateCmd() *cobra.Command {
	var (
		repoID string
		title  string
		body   string
		labels string
	)
	cmd := &cobra.Command{
		Use:          "create",
		Short:        "Create a new issue",
		Long:         longIssueCreate,
		SilenceUsage: true,
		Example:      `  nightgauge forge issue create --repo-id <node> --title "..." --body "..." --labels type:bug`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return emitError(cmd, fmt.Errorf("--title is required"))
			}
			if repoID == "" {
				return emitError(cmd, fmt.Errorf("--repo-id is required (the repository node id)"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			labelIDs := splitCSV(labels)
			issue, err := client.Issues().CreateIssue(cmd.Context(), repoID, title, body, labelIDs)
			if err != nil {
				return emitError(cmd, fmt.Errorf("create issue: %w", err))
			}
			return renderForCmd(cmd, IssueFromForge(issue))
		},
	}
	cmd.Flags().StringVar(&repoID, "repo-id", "", "Repository node ID (GraphQL)")
	cmd.Flags().StringVar(&title, "title", "", "Issue title")
	cmd.Flags().StringVar(&body, "body", "", "Issue body (Markdown)")
	cmd.Flags().StringVar(&labels, "labels", "", "Comma-separated label IDs")
	return cmd
}

// --- edit ---

func issueEditCmd() *cobra.Command {
	var (
		nodeID string
		body   string
	)
	cmd := &cobra.Command{
		Use:          "edit",
		Short:        "Edit an existing issue's body",
		Long:         longIssueEdit,
		SilenceUsage: true,
		Example:      `  nightgauge forge issue edit --node-id I_xxx --body "Updated body"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return emitError(cmd, fmt.Errorf("--node-id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			issue, err := client.Issues().EditIssue(cmd.Context(), nodeID, body)
			if err != nil {
				return emitError(cmd, fmt.Errorf("edit issue: %w", err))
			}
			return renderForCmd(cmd, IssueFromForge(issue))
		},
	}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "Issue node ID")
	cmd.Flags().StringVar(&body, "body", "", "New issue body")
	return cmd
}

// --- close / reopen ---

func issueCloseCmd() *cobra.Command {
	var nodeID string
	cmd := &cobra.Command{
		Use:          "close",
		Short:        "Close an issue by node id",
		Long:         longIssueClose,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return emitError(cmd, fmt.Errorf("--node-id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			if err := client.Issues().CloseIssue(cmd.Context(), nodeID); err != nil {
				return emitError(cmd, fmt.Errorf("close issue: %w", err))
			}
			return renderForCmd(cmd, map[string]any{"v": 1, "closed": true, "nodeId": nodeID})
		},
	}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "Issue node ID")
	return cmd
}

func issueReopenCmd() *cobra.Command {
	var nodeID string
	cmd := &cobra.Command{
		Use:          "reopen",
		Short:        "Reopen a closed issue",
		Long:         longIssueReopen,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return emitError(cmd, fmt.Errorf("--node-id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			if err := client.Issues().ReopenIssue(cmd.Context(), nodeID); err != nil {
				return emitError(cmd, fmt.Errorf("reopen issue: %w", err))
			}
			return renderForCmd(cmd, map[string]any{"v": 1, "reopened": true, "nodeId": nodeID})
		},
	}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "Issue node ID")
	return cmd
}

// --- comment ---

func issueCommentCmd() *cobra.Command {
	var (
		subjectID string
		body      string
	)
	cmd := &cobra.Command{
		Use:          "comment",
		Short:        "Add a comment to an issue",
		Long:         longIssueComment,
		SilenceUsage: true,
		Example:      `  nightgauge forge issue comment --subject-id I_xxx --body "..."`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if subjectID == "" || body == "" {
				return emitError(cmd, fmt.Errorf("--subject-id and --body are required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			if err := client.Issues().AddComment(cmd.Context(), subjectID, body); err != nil {
				return emitError(cmd, fmt.Errorf("add comment: %w", err))
			}
			return renderForCmd(cmd, map[string]any{"v": 1, "commented": true, "subjectId": subjectID})
		},
	}
	cmd.Flags().StringVar(&subjectID, "subject-id", "", "Issue or PR node ID")
	cmd.Flags().StringVar(&body, "body", "", "Comment body (Markdown)")
	return cmd
}

// --- helpers shared across the forge package ---

// parseRepo extracts owner/repo from --repo on the root forge command.
// The flag is stored as "owner/name"; we split it once and return both.
func parseRepo(cmd *cobra.Command) (owner, repo string, err error) {
	repoSpec, _ := flagString(cmd, "repo")
	if repoSpec == "" {
		return "", "", fmt.Errorf("--repo is required (owner/name)")
	}
	parts := strings.SplitN(repoSpec, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid --repo %q (want owner/name)", repoSpec)
	}
	return parts[0], parts[1], nil
}

func parseNumber(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return n, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// renderForCmd reads --json and --template off the root command and
// dispatches to internal/forge/output. Output goes through cmd.OutOrStdout
// when possible, falling back to os.Stdout for printJSON-style callers.
func renderForCmd(cmd *cobra.Command, v any) error {
	jsonFlag := flagBool(cmd, "json")
	tpl, _ := flagString(cmd, "template")
	mode := output.Resolve(jsonFlag, tpl)
	return output.Render(v, mode, tpl, cmd.OutOrStdout())
}
