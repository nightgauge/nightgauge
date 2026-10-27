package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/filing"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/spf13/cobra"
)

// exitCapRefused is the exit code of a filer whose per-run cap refused
// records (#480, Rule 5): loud, and distinct from a failure to run.
const exitCapRefused = 3

// followupCmd is the "followup" command family (#480).
func followupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "followup",
		Short: "File discovered follow-up work as issues (followup-materialize stage)",
	}
	cmd.AddCommand(followupMaterializeCmd())
	return cmd
}

// followupMaterializeCmd implements `nightgauge followup materialize <N>`.
func followupMaterializeCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		workdir       string
		runDirFlag    string
		dryRun        bool
		outputJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "materialize <issue-number>",
		Short: "File the run's discovered-work records as follow-up issues",
		Long: `Reads discovered-work-<N>.json from the pipeline state directory and files each
record as an issue with its type label, Priority, Size, Status=Backlog and a
provenance line, under the follow_up_filing gate (fail closed to report-only,
per-run cap, untrusted-author check). Records already filed are skipped;
records matching an open issue's title become a provenance comment instead.
Filed, deduped and reported records are removed from the capture file.

Exit 0: done (including a silent no-op on zero records). Exit 3: the per-run
cap refused records; they stay in the capture file. Exit 1: error.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			if workdir == "" {
				if workdir, err = os.Getwd(); err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			runDir := runDirFlag
			if runDir == "" {
				if runDir, err = layout.PipelineStateDir(workdir); err != nil {
					return fmt.Errorf("pipeline state dir: %w", err)
				}
			}
			capture, err := discoveredwork.Load(runDir, number)
			if err != nil {
				return err
			}
			if capture == nil || len(capture.Records) == 0 {
				return nil // zero records: a silent no-op
			}

			gate := filing.ResolveGate(workdir)
			if dryRun {
				gate.Mode, gate.Reason = filing.ModeReport, "--dry-run"
			}
			var filer followup.Filer
			trusted := false
			if gate.Files() {
				ownerPart, repoPart := splitRepo(owner, repo)
				client, err := clientFromConfig()
				if err != nil {
					return fmt.Errorf("create GitHub client: %w", err)
				}
				filer = newGitHubFollowupFiler(client, ownerPart, repoPart, projectNumber, getOwnerType(cmd))
				trusted = followupAuthorTrusted(cmd.Context(), client, workdir, ownerPart, repoPart, number)
			}

			res, matErr := followup.Materialize(cmd.Context(), capture, gate, trusted, filer)
			if matErr != nil && !followup.IsCapRefusal(matErr) {
				// Consume what was decided before the error so a retry does
				// not redo it; undecided records stay.
				_ = discoveredwork.Consume(runDir, number, func(r discoveredwork.Record) bool { return !res.Consumed(r.ID) })
				return enrichError(matErr)
			}
			if !dryRun {
				if err := discoveredwork.Consume(runDir, number, func(r discoveredwork.Record) bool {
					return !res.Consumed(r.ID)
				}); err != nil {
					return fmt.Errorf("consume capture: %w", err)
				}
			}
			if outputJSON {
				if err := printJSON(res); err != nil {
					return err
				}
			} else {
				renderFollowupHuman(cmd.OutOrStdout(), res)
			}
			if matErr != nil {
				fmt.Fprintf(os.Stderr, "followup materialize: %v\n", matErr)
				return verdictExit{code: exitCapRefused}
			}
			return nil
		},
	}
	ownerFlag(cmd, &owner, "GitHub repository owner (defaults to config)")
	requiredRepoNameFlag(cmd, &repo, "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (defaults to config)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().StringVar(&runDirFlag, "run-dir", "", "Directory holding discovered-work-<N>.json (default: the pipeline state dir)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report the would-file list; no forge calls and the capture is kept")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

// followupAuthorTrusted asks the trust gate whether the source issue is
// eligible (trusted author, trusted bot or a writer's approval). A lookup
// failure is untrusted: the materializer then reports instead of filing.
func followupAuthorTrusted(ctx context.Context, client *gh.Client, workdir, owner, repo string, number int) bool {
	var bots []string
	if cfg, err := config.Load(workdir); err == nil && cfg != nil && cfg.Autonomous != nil {
		bots = cfg.Autonomous.TrustedBots
	}
	d := trust.NewGate(gh.NewTrustForge(client), bots).Decide(ctx, trust.Subject{Owner: owner, Repo: repo, Number: number})
	return d.Eligible
}

func renderFollowupHuman(w io.Writer, res *followup.Result) {
	mode := "file"
	if res.ReportOnly {
		mode = "report-only: " + res.Reason
	}
	fmt.Fprintf(w, "Follow-up materialize (%s) — issue #%d\n", mode, res.Issue)
	for _, it := range res.Items {
		switch it.Outcome {
		case followup.OutcomeFiled, followup.OutcomeAlreadyExists, followup.OutcomeDeduped:
			fmt.Fprintf(w, "  %-14s %s  #%d %s\n", it.Outcome, it.ID, it.IssueNumber, it.Title)
		case followup.OutcomeInvalid:
			fmt.Fprintf(w, "  %-14s %s  %s (%s)\n", it.Outcome, it.ID, it.Title, it.Detail)
		default:
			fmt.Fprintf(w, "  %-14s %s  [%s %s %s] %s\n", it.Outcome, it.ID, it.Type, it.Priority, it.Size, it.Title)
		}
	}
	if res.Refused > 0 {
		fmt.Fprintf(w, "  refused: %d of %d by the per-run cap (kept in the capture file)\n",
			res.Refused, res.Refused+countOutcome(res, followup.OutcomeFiled))
	}
}

func countOutcome(res *followup.Result, outcome string) int {
	n := 0
	for _, it := range res.Items {
		if it.Outcome == outcome {
			n++
		}
	}
	return n
}

// githubFollowupFiler is the live followup.Filer. The repository and project
// come from flags or config only, never from a record.
type githubFollowupFiler struct {
	client   *gh.Client
	owner    string
	repo     string
	issueSvc *gh.IssueService
	projSvc  *gh.ProjectService
	trustF   *gh.TrustForge
}

func newGitHubFollowupFiler(client *gh.Client, owner, repo string, projectNumber int, ownerType gh.OwnerType) *githubFollowupFiler {
	return &githubFollowupFiler{
		client:   client,
		owner:    owner,
		repo:     repo,
		issueSvc: gh.NewIssueService(client),
		projSvc:  gh.NewProjectService(client, owner, projectNumber, ownerType),
		trustF:   gh.NewTrustForge(client),
	}
}

// FindByMarker searches open issues for the record id, then confirms the
// whole marker is in the body. A closed follow-up is not found here; the
// capture is consumed once a record is filed, so a re-run has nothing to
// re-file.
func (g *githubFollowupFiler) FindByMarker(ctx context.Context, marker string) (int, string, error) {
	id := marker
	if i := strings.Index(marker, "id="); i >= 0 {
		id = strings.Fields(marker[i+3:])[0]
	}
	issues, err := g.issueSvc.SearchIssues(ctx, g.owner, g.repo, `"`+id+`"`, 20)
	if err != nil {
		return 0, "", err
	}
	for _, is := range issues {
		if strings.Contains(is.Body, marker) {
			return is.Number, is.URL, nil
		}
	}
	return 0, "", nil
}

// FindOpenByTitle matches the normalized title exactly; a search hit with a
// merely similar title is not a duplicate.
func (g *githubFollowupFiler) FindOpenByTitle(ctx context.Context, title string) (int, string, error) {
	issues, err := g.issueSvc.SearchIssues(ctx, g.owner, g.repo, title, 20)
	if err != nil {
		return 0, "", err
	}
	want := followup.NormalizeTitle(title)
	for _, is := range issues {
		if followup.NormalizeTitle(is.Title) == want {
			return is.Number, is.URL, nil
		}
	}
	return 0, "", nil
}

func (g *githubFollowupFiler) HasComment(ctx context.Context, number int, marker string) (bool, error) {
	comments, err := g.trustF.IssueComments(ctx, g.owner, g.repo, number)
	if err != nil {
		return false, err
	}
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (g *githubFollowupFiler) AddComment(ctx context.Context, number int, body string) error {
	is, err := g.issueSvc.GetIssue(ctx, g.owner, g.repo, number)
	if err != nil {
		return err
	}
	return g.issueSvc.AddComment(ctx, is.NodeID, body)
}

func (g *githubFollowupFiler) CreateIssue(ctx context.Context, title, body, typ, priority, size string) (int, string, error) {
	if err := filing.ValidateFields(typ, priority, size); err != nil {
		return 0, "", err
	}
	repoLabels, err := g.issueSvc.GetRepoLabels(ctx, g.owner, g.repo)
	if err != nil {
		return 0, "", fmt.Errorf("fetch repo labels: %w", err)
	}
	var labelIDs []string
	if id, ok := repoLabels["type:"+typ]; ok {
		labelIDs = append(labelIDs, id)
	}
	repoID, err := g.client.GetRepositoryID(ctx, g.owner, g.repo)
	if err != nil {
		return 0, "", fmt.Errorf("fetch repo id: %w", err)
	}
	issue, err := g.issueSvc.CreateIssue(ctx, repoID, title, body, labelIDs)
	if err != nil {
		return 0, "", err
	}
	// A filed issue off the board is invisible to the workflow, so a board
	// failure is an error, not a tolerated miss.
	itemID, err := g.projSvc.AddItem(ctx, issue.NodeID)
	if err != nil {
		return issue.Number, issue.URL, fmt.Errorf("add #%d to project: %w", issue.Number, err)
	}
	for _, f := range [][2]string{
		{"Priority", filing.PriorityToBoardOption(priority)},
		{"Size", size},
		{"Status", "Backlog"},
	} {
		if err := g.projSvc.SetSingleSelectField(ctx, itemID, f[0], f[1]); err != nil {
			return issue.Number, issue.URL, fmt.Errorf("set %s on #%d: %w", f[0], issue.Number, err)
		}
	}
	return issue.Number, issue.URL, nil
}
