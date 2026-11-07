package followup

import (
	"context"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/filing"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// AuthorTrusted asks the trust gate whether the source issue is eligible
// (trusted author, trusted bot or a writer's approval). A lookup failure is
// untrusted: the materializer then reports instead of filing.
func AuthorTrusted(ctx context.Context, client *gh.Client, workdir, owner, repo string, number int) bool {
	var bots []string
	if cfg, err := config.Load(workdir); err == nil && cfg != nil && cfg.Autonomous != nil {
		bots = cfg.Autonomous.TrustedBots
	}
	d := trust.NewGate(gh.NewTrustForge(client), bots).Decide(ctx, trust.Subject{Owner: owner, Repo: repo, Number: number})
	return d.Eligible
}

// ReviewFiling returns the pr-merge enqueue gate's filer (#2730): Materialize
// against the live forge, under the same Rule 5 controls as the post-merge
// stage, for a capture of review findings. The gate calls it before the merge
// because the post-merge stage depends on the run surviving the merge.
//
// The gate decides report versus file. In report mode nothing reaches the forge.
func ReviewFiling(client *gh.Client, cfg *config.Config) func(ctx context.Context, workdir, repo string, capture *discoveredwork.Capture) (*Result, error) {
	return func(ctx context.Context, workdir, repo string, capture *discoveredwork.Capture) (*Result, error) {
		gate := filing.ResolveGate(workdir)
		if !gate.Files() {
			return Materialize(ctx, capture, gate, false, nil)
		}
		// A pr-{N}.json restored for a dispatched PR carries no pr_url.
		if repo == "" {
			repo = cfg.Owner + "/" + cfg.DefaultRepo
		}
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || owner == "" || name == "" {
			return nil, fmt.Errorf("repository %q is not owner/name", repo)
		}
		project, err := config.ResolveRepoProjectNumber(cfg, config.RepoProjectQuery{Owner: owner, Repo: name})
		if err != nil {
			return nil, err
		}
		filer := NewGitHubFiler(client, owner, name, project, gh.ParseOwnerType(cfg.OwnerType))
		trusted := AuthorTrusted(ctx, client, workdir, owner, name, capture.IssueNumber)
		return Materialize(ctx, capture, gate, trusted, filer)
	}
}

// GitHubFiler is the live Filer. The repository and project
// come from flags or config only, never from a record.
type GitHubFiler struct {
	client   *gh.Client
	owner    string
	repo     string
	issueSvc *gh.IssueService
	projSvc  *gh.ProjectService
	trustF   *gh.TrustForge
}

func NewGitHubFiler(client *gh.Client, owner, repo string, projectNumber int, ownerType gh.OwnerType) *GitHubFiler {
	return &GitHubFiler{
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
func (g *GitHubFiler) FindByMarker(ctx context.Context, marker string) (int, string, error) {
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
func (g *GitHubFiler) FindOpenByTitle(ctx context.Context, title string) (int, string, error) {
	issues, err := g.issueSvc.SearchIssues(ctx, g.owner, g.repo, title, 20)
	if err != nil {
		return 0, "", err
	}
	want := NormalizeTitle(title)
	for _, is := range issues {
		if NormalizeTitle(is.Title) == want {
			return is.Number, is.URL, nil
		}
	}
	return 0, "", nil
}

func (g *GitHubFiler) HasComment(ctx context.Context, number int, marker string) (bool, error) {
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

func (g *GitHubFiler) AddComment(ctx context.Context, number int, body string) error {
	is, err := g.issueSvc.GetIssue(ctx, g.owner, g.repo, number)
	if err != nil {
		return err
	}
	return g.issueSvc.AddComment(ctx, is.NodeID, body)
}

func (g *GitHubFiler) CreateIssue(ctx context.Context, title, body, typ, priority, size string) (int, string, error) {
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
