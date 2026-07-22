package forgecmd

import (
	"fmt"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/spf13/cobra"
)

// updatePRTitleBody calls UpdatePR with only the title/body fields the
// caller chose to set. Empty strings stay nil so the underlying adapter
// preserves the prior value.
func updatePRTitleBody(cmd *cobra.Command, client forge.ForgeClient, nodeID, title, body string) (*forgetypes.PullRequest, error) {
	opts := forge.UpdatePROptions{}
	if title != "" {
		t := title
		opts.Title = &t
	}
	if body != "" {
		b := body
		opts.Body = &b
	}
	pr, err := client.PRs().UpdatePR(cmd.Context(), nodeID, opts)
	if err != nil {
		return nil, fmt.Errorf("edit PR: %w", err)
	}
	return pr, nil
}

// prCmd is the `forge pr` group (alias `mr`).
func prCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pr",
		Aliases: []string{"mr"},
		Short:   "Pull/merge request operations",
		Long:    longPR,
	}
	cmd.AddCommand(prListCmd(), prViewCmd(), prCreateCmd(), prEditCmd(),
		prMergeCmd(), prCloseCmd(), prCommentCmd(), prChecksCmd())
	return cmd
}

func prListCmd() *cobra.Command {
	var (
		state   string
		headRef string
	)
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List PRs / MRs in a repo",
		Long:         longPRList,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			prs, err := client.PRs().ListPRs(cmd.Context(), owner, repo, state, headRef)
			if err != nil {
				return emitError(cmd, fmt.Errorf("list PRs: %w", err))
			}
			out := make([]PRJSON, 0, len(prs))
			for i := range prs {
				out = append(out, PRFromForge(&prs[i]))
			}
			return renderForCmd(cmd, out)
		},
	}
	cmd.Flags().StringVar(&state, "state", "open", "Filter by state (open, closed, merged, all)")
	cmd.Flags().StringVar(&headRef, "head", "", "Filter by head ref (branch name)")
	return cmd
}

func prViewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "view <number>",
		Short:        "View a single PR by number",
		Long:         longPRView,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
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
			pr, err := client.PRs().GetPR(cmd.Context(), owner, repo, number)
			if err != nil {
				return emitError(cmd, fmt.Errorf("get PR: %w", err))
			}
			return renderForCmd(cmd, PRFromForge(pr))
		},
	}
	return cmd
}

func prCreateCmd() *cobra.Command {
	var (
		repoID  string
		title   string
		body    string
		headRef string
		baseRef string
	)
	cmd := &cobra.Command{
		Use:          "create",
		Short:        "Create a new PR / MR",
		Long:         longPRCreate,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" || repoID == "" || headRef == "" || baseRef == "" {
				return emitError(cmd, fmt.Errorf("--repo-id, --title, --head, --base are required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			pr, err := client.PRs().CreatePR(cmd.Context(), repoID, title, body, headRef, baseRef)
			if err != nil {
				return emitError(cmd, fmt.Errorf("create PR: %w", err))
			}
			return renderForCmd(cmd, PRFromForge(pr))
		},
	}
	cmd.Flags().StringVar(&repoID, "repo-id", "", "Repository node ID (GraphQL)")
	cmd.Flags().StringVar(&title, "title", "", "PR title")
	cmd.Flags().StringVar(&body, "body", "", "PR body (Markdown)")
	cmd.Flags().StringVar(&headRef, "head", "", "Source branch")
	cmd.Flags().StringVar(&baseRef, "base", "", "Target branch")
	return cmd
}

func prEditCmd() *cobra.Command {
	var (
		nodeID string
		title  string
		body   string
	)
	cmd := &cobra.Command{
		Use:          "edit",
		Short:        "Edit a PR's title or body",
		Long:         longPREdit,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return emitError(cmd, fmt.Errorf("--node-id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			// Build a minimal UpdatePROptions with only the fields the user set.
			// We use the forge package's UpdatePROptions via the service interface.
			// Pointers to nil leaves the field unchanged.
			pr, err := updatePRTitleBody(cmd, client, nodeID, title, body)
			if err != nil {
				return emitError(cmd, err)
			}
			return renderForCmd(cmd, PRFromForge(pr))
		},
	}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "PR node ID")
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&body, "body", "", "New body")
	return cmd
}

func prMergeCmd() *cobra.Command {
	var (
		nodeID   string
		strategy string
	)
	cmd := &cobra.Command{
		Use:          "merge",
		Short:        "Merge a PR / MR",
		Long:         longPRMerge,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return emitError(cmd, fmt.Errorf("--node-id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			if strategy != "" {
				sha, err := client.PRs().MergePRWithStrategy(cmd.Context(), nodeID, strategy)
				if err != nil {
					return emitError(cmd, fmt.Errorf("merge PR: %w", err))
				}
				return renderForCmd(cmd, map[string]any{
					"v":        1,
					"merged":   true,
					"nodeId":   nodeID,
					"strategy": strategy,
					"sha":      sha,
				})
			}
			if err := client.PRs().MergePR(cmd.Context(), nodeID); err != nil {
				return emitError(cmd, fmt.Errorf("merge PR: %w", err))
			}
			return renderForCmd(cmd, map[string]any{"v": 1, "merged": true, "nodeId": nodeID})
		},
	}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "PR node ID")
	cmd.Flags().StringVar(&strategy, "strategy", "", "Merge strategy: squash, merge, rebase")
	return cmd
}

func prCloseCmd() *cobra.Command {
	var nodeID string
	cmd := &cobra.Command{
		Use:          "close",
		Short:        "Close a PR / MR without merging",
		Long:         longPRClose,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return emitError(cmd, fmt.Errorf("--node-id is required"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			if err := client.PRs().ClosePR(cmd.Context(), nodeID); err != nil {
				return emitError(cmd, fmt.Errorf("close PR: %w", err))
			}
			return renderForCmd(cmd, map[string]any{"v": 1, "closed": true, "nodeId": nodeID})
		},
	}
	cmd.Flags().StringVar(&nodeID, "node-id", "", "PR node ID")
	return cmd
}

func prCommentCmd() *cobra.Command {
	var (
		subjectID string
		body      string
	)
	cmd := &cobra.Command{
		Use:          "comment",
		Short:        "Add a comment to a PR / MR",
		Long:         longPRComment,
		SilenceUsage: true,
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
	cmd.Flags().StringVar(&subjectID, "subject-id", "", "PR node ID")
	cmd.Flags().StringVar(&body, "body", "", "Comment body (Markdown)")
	return cmd
}

func prChecksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "checks <number>",
		Short:        "CI/check rollup for a PR",
		Long:         longPRChecks,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
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
			cs, err := client.CI().GetCheckStatus(cmd.Context(), owner, repo, number)
			if err != nil {
				return emitError(cmd, fmt.Errorf("get check status: %w", err))
			}
			return renderForCmd(cmd, CheckRollupFromForge(cs))
		},
	}
	return cmd
}
