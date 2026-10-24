package forgecmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
	"os"
	"strconv"
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
			out := IssueFromForge(issue)
			withComments, _ := cmd.Flags().GetBool("comments")
			vouched, _ := cmd.Flags().GetBool("vouched")
			if withComments || vouched || isStageOwnIssue(number) {
				// Checked AFTER the issue was read, with nothing cached, so an
				// edit made at any point up to now is seen: an outsider's edit
				// after a writer approved the issue never reaches the stage that
				// reads it (#2130). The dispatch-time check cannot close that
				// window on its own.
				if err := requireVouched(cmd, client, owner, repo, number); err != nil {
					return emitError(cmd, err)
				}
			}
			if withComments {
				comments, removed, err := vouchedComments(cmd, client, owner, repo, number)
				if err != nil {
					return emitError(cmd, err)
				}
				out.Comments = comments
				if removed > 0 {
					// The count, never the text (#2130).
					fmt.Fprintf(cmd.ErrOrStderr(), "comments: %d removed: their authors lack write access to %s/%s\n",
						removed, owner, repo)
				}
			}
			return renderForCmd(cmd, out)
		},
	}
	cmd.Flags().Bool("vouched", false,
		"Refuse unless a user with write access vouches for the issue now (always on for a stage reading its own issue)")
	cmd.Flags().Bool("comments", false,
		"Include the issue's comments by users with write access (or a trusted bot); the rest are removed and counted on stderr")
	return cmd
}

// commentSourcer is a forge client that can list comments and answer
// permission lookups (the GitHub adapter).
type commentSourcer interface {
	TrustSource() trust.CommentSource
}

// trustedBotsFromConfig reads autonomous.trusted_bots from the working
// directory's config; without one, the gate's defaults apply.
var trustedBotsFromConfig = func() []string {
	cfg := workingConfig()
	if cfg == nil || cfg.Autonomous == nil {
		return nil
	}
	return cfg.Autonomous.TrustedBots
}

// appOwnersFromConfig reads the owners where the pipeline acts as a GitHub
// App, the only owners where label approval exists.
var appOwnersFromConfig = func() []string { return workingConfig().AppOwners() }

// stageIdentitiesFromConfig lists every login a stage on this machine can
// act as; a label any of them applied never approves.
var stageIdentitiesFromConfig = func(ctx context.Context) ([]string, error) {
	return gh.StageIdentities(ctx, workingConfig().ConfiguredGitHubLogins())
}

func workingConfig() *config.Config {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil
	}
	return cfg
}

// newCommandGate is the gate a forge command asks.
func newCommandGate(src trust.Forge) *trust.Gate {
	return trust.NewGate(src, trustedBotsFromConfig()).WithAppOwners(appOwnersFromConfig()...).
		WithStageIdentities(stageIdentitiesFromConfig)
}

// isStageOwnIssue reports whether this read is a pipeline stage reading the
// issue it was dispatched for: stage adapters set NIGHTGAUGE_ISSUE_NUMBER.
func isStageOwnIssue(number int) bool {
	v := strings.TrimSpace(os.Getenv("NIGHTGAUGE_ISSUE_NUMBER"))
	return v != "" && v == strconv.Itoa(number)
}

// requireVouched re-runs the eligibility gate on a fresh cache. A forge that
// cannot answer it refuses: fail closed.
func requireVouched(cmd *cobra.Command, client forge.ForgeClient, owner, repo string, number int) error {
	src, ok := client.(commentSourcer)
	if !ok {
		return fmt.Errorf("%w: this forge cannot check who vouches for %s/%s#%d", forge.ErrUnsupported, owner, repo, number)
	}
	d := newCommandGate(src.TrustSource()).Decide(cmd.Context(),
		trust.Subject{Owner: owner, Repo: repo, Number: number})
	if !d.Eligible {
		return errors.New(d.Reason)
	}
	return nil
}

// vouchedComments lists an issue's comments and keeps only those whose author
// may vouch for an issue: a user with write access or higher, or a trusted bot
// (#2130). This is the one place comments are assembled for a model, so a
// comment by anyone else never reaches one. A failed permission lookup removes
// the comment.
func vouchedComments(cmd *cobra.Command, client forge.ForgeClient, owner, repo string, number int) ([]CommentJSON, int, error) {
	src, ok := client.(commentSourcer)
	if !ok {
		return nil, 0, fmt.Errorf("--comments: %w: this forge cannot list comments with their authors' permissions", forge.ErrUnsupported)
	}
	ts := src.TrustSource()
	all, err := ts.IssueComments(cmd.Context(), owner, repo, number)
	if err != nil {
		return nil, 0, fmt.Errorf("list comments: %w", err)
	}
	kept, removed := newCommandGate(ts).FilterComments(cmd.Context(), owner, repo, all)
	out := make([]CommentJSON, 0, len(kept))
	for _, c := range kept {
		at := c.CreatedAt
		out = append(out, CommentJSON{Author: ActorJSON{Login: c.Author}, Body: c.Body, CreatedAt: &at})
	}
	return out, removed, nil
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
			// Names, not node IDs — the example on this command has always
			// shown names, and nothing but the forge knows the IDs (#1214).
			labelIDs, err := resolveLabelNames(cmd, client, splitCSV(labels))
			if err != nil {
				return emitError(cmd, err)
			}
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
	cmd.Flags().StringVar(&labels, "labels", "", "Comma-separated label names (e.g. type:bug,priority:high). Unknown names fail before the issue is created.")
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
