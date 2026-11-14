package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// issueIntakeStatus is the board column a new issue lands in when --status is
// not given: Backlog, the intake column every board in the workspace carries
// and the one the issue-creation rules start issues in.
const issueIntakeStatus = "Backlog"

// issueCreator is what `issue create` does to the forge. The GitHub-backed
// implementation is ghIssueCreator; tests substitute a fake.
type issueCreator interface {
	// ResolveLabels resolves label names to canonical names and node IDs. An
	// unknown name fails before anything is created.
	ResolveLabels(ctx context.Context, owner, repo string, names []string) (canonical, ids []string, err error)
	Create(ctx context.Context, owner, repo, title, body string, labelIDs []string) (*types.Issue, error)
	// AddToBoard adds the issue to the board and sets its Status.
	AddToBoard(ctx context.Context, owner, repo string, number, project int, status string) error
}

type ghIssueCreator struct{ client *gh.Client }

func (c ghIssueCreator) ResolveLabels(ctx context.Context, owner, repo string, names []string) ([]string, []string, error) {
	labels, ids, err := resolveLabelNames(ctx, c.client, owner, repo, names)
	if err != nil {
		return nil, nil, err
	}
	canonical := make([]string, len(labels))
	for i, l := range labels {
		canonical[i] = l.Name
	}
	return canonical, ids, nil
}

func (c ghIssueCreator) Create(ctx context.Context, owner, repo, title, body string, labelIDs []string) (*types.Issue, error) {
	repoID, err := c.client.GetRepositoryID(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	return gh.NewIssueService(c.client).CreateIssue(ctx, repoID, title, body, labelIDs)
}

func (c ghIssueCreator) AddToBoard(ctx context.Context, owner, repo string, number, project int, status string) error {
	svc := gh.NewProjectService(c.client, owner, project)
	if _, err := svc.AddIssueByNumber(ctx, owner, repo, number); err != nil {
		return err
	}
	return svc.SetFields(ctx, owner, repo, number, map[string]string{"Status": status})
}

// issueCreateOptions are the resolved inputs of one `issue create`.
type issueCreateOptions struct {
	Owner, Repo string
	Title, Body string
	Labels      []string
	Refined     bool
	// Project is the board to place the issue on; 0 leaves it off every board.
	Project int
	Status  string
}

// issueCreateResult is the --json output: the issue, where it landed, and
// every reason the scheduler would not dispatch it.
type issueCreateResult struct {
	*types.Issue
	Project             int                       `json:"project"`
	Status              string                    `json:"status,omitempty"`
	BoardError          string                    `json:"board_error,omitempty"`
	DispatchSkipReasons []orchestrator.SkipReason `json:"dispatch_skip_reasons"`
}

// runIssueCreate creates the issue, places it on the board, and evaluates it
// with the scheduler's own dispatch rules (#3278). Before this an issue was
// created with labels only: off every board, so the scheduler never saw it,
// and nothing said so.
func runIssueCreate(ctx context.Context, c issueCreator, ready readinessChecker, o issueCreateOptions) (issueCreateResult, error) {
	names := append([]string{}, o.Labels...)
	if o.Refined && !hasLabelFold(names, gh.LabelRefined) {
		names = append(names, gh.LabelRefined)
	}
	// Resolve names first: an unknown label must fail before anything is
	// created, not leave a half-labelled issue behind.
	canonical, ids, err := c.ResolveLabels(ctx, o.Owner, o.Repo, names)
	if err != nil {
		return issueCreateResult{}, err
	}
	issue, err := c.Create(ctx, o.Owner, o.Repo, o.Title, o.Body, ids)
	if err != nil {
		return issueCreateResult{}, err
	}
	// Report the canonical names the mutation applied, not the caller's input.
	issue.Labels = append(issue.Labels, canonical...)

	res := issueCreateResult{Issue: issue, Project: o.Project}
	placement := gh.BoardPlacement{Labels: issue.Labels}
	if o.Project > 0 {
		// A failed board add is reported as not-on-board below, never as a
		// silent success: the issue exists either way.
		if err := c.AddToBoard(ctx, o.Owner, o.Repo, issue.Number, o.Project, o.Status); err != nil {
			res.BoardError = err.Error()
		} else {
			placement.OnBoard = true
			placement.Status = o.Status
			res.Status = o.Status
		}
	}
	res.DispatchSkipReasons = ready.evaluate(o.Owner, o.Repo, issue.Number, placement)
	if res.DispatchSkipReasons == nil {
		res.DispatchSkipReasons = []orchestrator.SkipReason{}
	}
	return res, nil
}

// printIssueCreateWarnings writes one warning per skip reason, with the
// command that clears it.
func printIssueCreateWarnings(w io.Writer, res issueCreateResult) {
	if res.BoardError != "" {
		fmt.Fprintf(w, "warning: #%d was created but not added to board %d: %s\n", res.Number, res.Project, res.BoardError)
	}
	for _, r := range res.DispatchSkipReasons {
		fmt.Fprintf(w, "warning: #%d will not be dispatched: %s: %s\n  fix: %s\n", res.Number, r.Code, r.Detail, r.Remedy)
	}
}

func issueCreateCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		title         string
		body          string
		labels        []string
		status        string
		projectNumber int
		refined       bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new issue and place it on the repository's board",
		Long: `Create an issue, add it to the repository's project board with a Status
(default: ` + issueIntakeStatus + `, the intake column), and report every reason the autonomous
scheduler would not dispatch it, with the command that clears each one.

--refined applies pipeline:refined for a body that already has acceptance
criteria. --project 0 leaves the issue off every board.`,
		Example: `  nightgauge issue create --title "Fix bug" --body "Description"
  nightgauge issue create --title "Feature" --labels type:feature --status ready --refined
  nightgauge issue create --title "Note" --project 0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			resolvedStatus, err := validateStatusFlag(status)
			if err != nil {
				return err
			}
			ownerPart, repoPart := splitRepo(owner, repo)

			// Resolve the board before any network call, as `project add` does.
			workdir, _ := os.Getwd()
			var cfg *config.Config
			if c, cfgErr := config.Load(workdir); cfgErr == nil {
				cfg = c
			}
			explicit := cmd.Flags().Changed("project")
			project := projectNumber
			if !(explicit && projectNumber == 0) {
				project, err = resolveProjectNumber(cfg, explicit, projectNumber, ownerPart, repoPart, workdir)
				if err != nil {
					return fmt.Errorf("resolve the board for %s/%s: %w (pass --project N, or --project 0 to leave the issue off every board)", ownerPart, repoPart, err)
				}
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}
			res, err := runIssueCreate(cmd.Context(), ghIssueCreator{client: client},
				newReadinessChecker(cfg, nil, project), issueCreateOptions{
					Owner: ownerPart, Repo: repoPart, Title: title, Body: body,
					Labels: labels, Refined: refined, Project: project, Status: resolvedStatus,
				})
			if err != nil {
				return err
			}
			printIssueCreateWarnings(os.Stderr, res)
			if outputJSON {
				return printJSON(res)
			}
			where := "off every board"
			if res.Status != "" {
				where = fmt.Sprintf("board %d, %s", res.Project, res.Status)
			}
			fmt.Printf("Created #%d: %s (%s)\n", res.Number, res.Title, where)
			return nil
		},
	}

	ownerFlag(cmd, &owner, "GitHub organization")
	requiredRepoNameFlag(cmd, &repo, "Repository (owner/name or name)")
	cmd.Flags().StringVar(&title, "title", "", "Issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Issue body")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "Label names to apply, comma-separated (e.g. type:bug,component:sdk). Unknown names fail before anything is created.")
	cmd.Flags().StringVar(&status, "status", issueIntakeStatus, "Board Status for the new issue (backlog, ready, in-progress, in-review, done)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (default: the repository's board; 0 leaves the issue off every board)")
	cmd.Flags().BoolVar(&refined, "refined", false, "Apply pipeline:refined: the body already has acceptance criteria")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}
