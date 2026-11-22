package followup

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/filing"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
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
// The gate decides report versus file. In report mode the forge is only read,
// for the parent's criteria, so tracked-by-parent applies there too. That read
// needs no project board, so report mode tolerates an unresolved project, and
// when the repository is not owner/name the report runs without the read.
func ReviewFiling(client *gh.Client, cfg *config.Config) func(ctx context.Context, workdir, repo string, capture *discoveredwork.Capture) (*Result, error) {
	return func(ctx context.Context, workdir, repo string, capture *discoveredwork.Capture) (*Result, error) {
		gate := filing.ResolveGate(workdir)
		// A pr-{N}.json restored for a dispatched PR carries no pr_url.
		if repo == "" {
			repo = cfg.Owner + "/" + cfg.DefaultRepo
		}
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || owner == "" || name == "" {
			if !gate.Files() {
				return Materialize(ctx, capture, gate, false, Tracking{}, nil)
			}
			return nil, fmt.Errorf("repository %q is not owner/name", repo)
		}
		project, err := config.ResolveRepoProjectNumber(cfg, config.RepoProjectQuery{Owner: owner, Repo: name})
		if err != nil && gate.Files() {
			return nil, err
		}
		// Report mode makes no board call, so an unresolved project (0) is fine.
		filer := NewGitHubFiler(client, owner, name, project, gh.ParseOwnerType(cfg.OwnerType))
		if !gate.Files() {
			return Materialize(ctx, capture, gate, false, Tracking{}, filer)
		}
		trusted := AuthorTrusted(ctx, client, workdir, owner, name, capture.IssueNumber)
		return Materialize(ctx, capture, gate, trusted, Tracking{}, filer)
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
	project  int
	// subs memoizes the per-parent subsystem inputs across the records of one
	// Materialize run: records in one capture normally share their parent.
	subs map[int]*parentSubsystem
	// issues and placements memoize issue reads and board placements across
	// the records of one Materialize run (#2872 Part E): records share their
	// parent, epic, siblings and candidates. A GitHubFiler serves one run, so
	// nothing outlives it; AppendPart writes its edit back into issues.
	issues     map[int]*types.Issue
	placements map[int]gh.BoardPlacement
	// fetchIssue, fetchPlacement and editIssue reach the forge; tests replace
	// them to count calls without HTTP.
	fetchIssue     func(ctx context.Context, number int) (*types.Issue, error)
	fetchPlacement func(ctx context.Context, number int) (gh.BoardPlacement, error)
	editIssue      func(ctx context.Context, nodeID, body string) error
}

// parentSubsystem is what FindOpenInSubsystem reads once per parent.
type parentSubsystem struct {
	sub      Subsystem
	siblings map[int]bool
	extra    []types.Issue // open siblings the searches may not return
}

func NewGitHubFiler(client *gh.Client, owner, repo string, projectNumber int, ownerType gh.OwnerType) *GitHubFiler {
	g := &GitHubFiler{
		client:     client,
		owner:      owner,
		repo:       repo,
		issueSvc:   gh.NewIssueService(client),
		projSvc:    gh.NewProjectService(client, owner, projectNumber, ownerType),
		trustF:     gh.NewTrustForge(client),
		project:    projectNumber,
		subs:       map[int]*parentSubsystem{},
		issues:     map[int]*types.Issue{},
		placements: map[int]gh.BoardPlacement{},
	}
	g.fetchIssue = func(ctx context.Context, n int) (*types.Issue, error) {
		return g.issueSvc.GetIssue(ctx, g.owner, g.repo, n)
	}
	g.fetchPlacement = func(ctx context.Context, n int) (gh.BoardPlacement, error) {
		return gh.IssueBoardPlacement(ctx, g.client, g.owner, g.repo, n, g.project)
	}
	g.editIssue = func(ctx context.Context, nodeID, body string) error {
		_, err := g.issueSvc.EditIssue(ctx, nodeID, body)
		return err
	}
	return g
}

// getIssue reads issue number once per run. A failed read is not cached.
func (g *GitHubFiler) getIssue(ctx context.Context, number int) (*types.Issue, error) {
	if is, ok := g.issues[number]; ok {
		return is, nil
	}
	is, err := g.fetchIssue(ctx, number)
	if err != nil {
		return nil, err
	}
	g.issues[number] = is
	return is, nil
}

// placement reads issue number's board placement once per run. A failed read
// is not cached.
func (g *GitHubFiler) placement(ctx context.Context, number int) (gh.BoardPlacement, error) {
	if p, ok := g.placements[number]; ok {
		return p, nil
	}
	p, err := g.fetchPlacement(ctx, number)
	if err != nil {
		return gh.BoardPlacement{}, err
	}
	g.placements[number] = p
	return p, nil
}

// markerIssueRe reads the issue a marker names.
var markerIssueRe = regexp.MustCompile(`issue=#(\d+)`)

// FindByMarker looks for marker in the body of the issue the marker names
// first (the parent may already carry its own record), then searches open and
// closed issues for the record id and confirms the whole marker is in the
// body. Reading the parent directly does not depend on the search index. That
// read may come from the run's memo, which is current for this run's own
// folds because AppendPart writes each edit back into it.
func (g *GitHubFiler) FindByMarker(ctx context.Context, marker string) (Found, error) {
	if m := markerIssueRe.FindStringSubmatch(marker); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			is, err := g.getIssue(ctx, n)
			if err != nil {
				return Found{}, fmt.Errorf("read #%d: %w", n, err)
			}
			if strings.Contains(is.Body, marker) {
				return foundOf(is), nil
			}
		}
	}
	id := marker
	if i := strings.Index(marker, "id="); i >= 0 {
		id = strings.Fields(marker[i+3:])[0]
	}
	issues, err := g.issueSvc.SearchIssuesAnyState(ctx, g.owner, g.repo, `"`+id+`"`, markerSearchLimit)
	if err != nil {
		return Found{}, err
	}
	for i := range issues {
		if strings.Contains(issues[i].Body, marker) {
			return foundOf(&issues[i]), nil
		}
	}
	return Found{}, nil
}

func foundOf(is *types.Issue) Found {
	return Found{Number: is.Number, URL: is.URL, Closed: !strings.EqualFold(is.State, issueOpen)}
}

// issueOpen is GitHub's state for an open issue.
const issueOpen = "OPEN"

// markerSearchLimit bounds the results FindByMarker reads.
const markerSearchLimit = 20

// ParentCriteria returns the unchecked criteria in issue number's body.
func (g *GitHubFiler) ParentCriteria(ctx context.Context, number int) ([]string, error) {
	is, err := g.getIssue(ctx, number)
	if err != nil {
		return nil, err
	}
	return UncheckedCriteria(is.Body), nil
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
	is, err := g.getIssue(ctx, number)
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

// Bounds on the subsystem search: files, components and programs searched,
// results read per search, and board placements read.
const (
	maxSubsystemFiles      = 5
	maxSubsystemComponents = 3
	maxSubsystemPrograms   = 3
	subsystemSearchLimit   = 20
	maxPlacementReads      = 10
)

// broadLabelShare is the share of all open issues above which a component
// label is broad: sharing it says nothing about the subsystem.
const broadLabelShare = 0.15

// maxSiblingReads bounds the open epic siblings read per parent.
const maxSiblingReads = 10

// parentInputs reads, once per parent, the parent's component:* and program:*
// labels, its same-repository epic and that epic's open siblings (at most
// maxSiblingReads), and which component labels are broad. An unreadable
// open-issue count marks every component label broad: the safe direction
// files instead of folding.
func (g *GitHubFiler) parentInputs(ctx context.Context, parent int) (*parentSubsystem, error) {
	if ps, ok := g.subs[parent]; ok {
		return ps, nil
	}
	ps := &parentSubsystem{sub: Subsystem{Parent: parent, Broad: map[string]bool{}}, siblings: map[int]bool{}}
	if parent > 0 {
		p, err := g.getIssue(ctx, parent)
		if err != nil {
			return nil, fmt.Errorf("read parent #%d: %w", parent, err)
		}
		ps.sub.Components = ComponentLabels(p.Labels)
		if len(ps.sub.Components) > maxSubsystemComponents {
			ps.sub.Components = ps.sub.Components[:maxSubsystemComponents]
		}
		ps.sub.Programs = ProgramLabels(p.Labels)
		if len(ps.sub.Programs) > maxSubsystemPrograms {
			ps.sub.Programs = ps.sub.Programs[:maxSubsystemPrograms]
		}
		if p.ParentIssueNumber > 0 && sameRepo(p.ParentIssueRepo, g.owner, g.repo) {
			ps.sub.Epic = p.ParentIssueNumber
			epic, err := g.getIssue(ctx, ps.sub.Epic)
			if err != nil {
				return nil, fmt.Errorf("read epic #%d: %w", ps.sub.Epic, err)
			}
			for _, si := range epic.SubIssues {
				if si.Number == parent || !strings.EqualFold(si.State, issueOpen) ||
					!sameRepo(si.Repo, g.owner, g.repo) || len(ps.siblings) == maxSiblingReads {
					continue
				}
				ps.siblings[si.Number] = true
				is, err := g.getIssue(ctx, si.Number)
				if err != nil {
					return nil, fmt.Errorf("read sibling #%d: %w", si.Number, err)
				}
				ps.extra = append(ps.extra, *is)
			}
		}
	}
	if len(ps.sub.Components) > 0 {
		total, err := g.issueSvc.CountOpenMatching(ctx, g.owner, g.repo, "")
		for _, c := range ps.sub.Components {
			n := 0
			if err == nil {
				n, err = g.issueSvc.CountOpenMatching(ctx, g.owner, g.repo, `label:"`+c+`"`)
			}
			if err != nil || total == 0 || float64(n) > broadLabelShare*float64(total) {
				ps.sub.Broad[strings.ToLower(c)] = true
			}
		}
	}
	g.subs[parent] = ps
	return ps, nil
}

// FindOpenInSubsystem gathers candidates (open issues naming one of files,
// carrying one of the parent's non-broad component:* or program:* labels, or
// open under the parent's epic), reads the board placement of the qualifying
// ones (at most maxPlacementReads; a qualifying candidate past that bound is
// dropped, never folded into blind), and picks with PickSubsystemIssue. It
// returns 0 when nothing qualifies, and also when the pick's board Priority is
// one priority outranks: that follow-up is filed on its own.
func (g *GitHubFiler) FindOpenInSubsystem(ctx context.Context, parent int, files []string, priority string) (int, string, error) {
	ps, err := g.parentInputs(ctx, parent)
	if err != nil {
		return 0, "", err
	}
	sub := ps.sub
	sub.Files = files
	var queries []string
	for i, f := range files {
		if i == maxSubsystemFiles {
			break
		}
		if f = strings.TrimSpace(f); f != "" && !Ubiquitous(f) && !strings.ContainsAny(f, `"`) {
			queries = append(queries, `"`+f+`" in:body`)
		}
	}
	for _, c := range append(append([]string{}, sub.Components...), sub.Programs...) {
		if !sub.Broad[strings.ToLower(c)] && !strings.ContainsAny(c, `"`) {
			queries = append(queries, `label:"`+c+`"`)
		}
	}
	seen := map[int]int{}
	var cands []SubsystemCandidate
	add := func(is types.Issue) {
		if _, ok := seen[is.Number]; ok {
			return
		}
		seen[is.Number] = len(cands)
		c := SubsystemCandidate{Number: is.Number, URL: is.URL, Body: is.Body, Labels: is.Labels}
		if ps.siblings[is.Number] {
			c.Parent = sub.Epic
		}
		cands = append(cands, c)
	}
	for _, is := range ps.extra {
		add(is)
	}
	for _, q := range queries {
		issues, err := g.issueSvc.SearchIssues(ctx, g.owner, g.repo, q, subsystemSearchLimit)
		if err != nil {
			return 0, "", err
		}
		for _, is := range issues {
			add(is)
		}
	}
	reads := 0
	for i := range cands {
		c := &cands[i]
		if signal, sibling := qualify(sub, *c); signal == noMatch && !sibling {
			continue
		}
		if reads == maxPlacementReads {
			c.Number = 0
			continue
		}
		reads++
		placement, err := g.placement(ctx, c.Number)
		if err != nil {
			return 0, "", err
		}
		c.BoardStatus, c.Priority = placement.Status, placement.Priority
	}
	n, url := PickSubsystemIssue(sub, cands)
	if n == 0 || Outranks(priority, cands[seen[n]].Priority) {
		return 0, "", nil
	}
	return n, url, nil
}

// sameRepo reports whether full ("owner/name") is this repository. An empty
// name is taken as this repository: GitHub omits it for a same-repository link.
func sameRepo(full, owner, repo string) bool {
	return full == "" || strings.EqualFold(full, owner+"/"+repo)
}

// AppendPart appends rec to issue number as a new part. A body already
// carrying rec's marker is left alone. It reads the body fresh, never from the
// run's memo, so the edit never starts from a stale body, and stores the
// result in the memo, so a later FindByMarker on number sees the new marker.
func (g *GitHubFiler) AppendPart(ctx context.Context, number int, rec discoveredwork.Record) (string, error) {
	is, err := g.fetchIssue(ctx, number)
	if err != nil {
		return "", fmt.Errorf("read #%d: %w", number, err)
	}
	g.issues[number] = is
	body, label, changed := AppendPartToBody(is.Body, rec)
	if !changed {
		return "", nil
	}
	if err := g.editIssue(ctx, is.NodeID, body); err != nil {
		return "", fmt.Errorf("edit #%d: %w", number, err)
	}
	edited := *is
	edited.Body = body
	g.issues[number] = &edited
	return label, nil
}
