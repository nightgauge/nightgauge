package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Forge is the forge as the conversation tools use it.
type Forge interface {
	GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error)
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*types.PullRequest, error)
	SearchIssues(ctx context.Context, owner, repo, query string, limit int) ([]types.Issue, error)
	// CreateIssue files an issue through the existing issue-creation path.
	// asOperator selects the credential: true is the host operator's own
	// credential chain, for a turn the operator posted; false is the GitHub
	// App installation and nothing else, and an error carrying
	// CodeAppUnavailable when the App cannot be used.
	CreateIssue(ctx context.Context, owner, repo, title, body string, asOperator bool) (*types.Issue, error)
}

// forgeTimeout bounds one forge request a tool makes.
const forgeTimeout = 30 * time.Second

// gitHubForge is the GitHub forge, resolved on the workspace's config.
type gitHubForge struct {
	cfg gh.TokenResolver
}

// NewGitHubForge is the GitHub forge for a workspace's config, which may be
// nil when it did not load.
func NewGitHubForge(cfg *config.Config) Forge {
	if cfg == nil {
		return gitHubForge{} // a nil *Config inside the interface would not read as nil
	}
	return gitHubForge{cfg: cfg}
}

// reader is the client reads go through: the workspace's ordinary chain,
// which prefers the App installation. Reads are confined to the poster's
// readable repositories before one is made.
func (f gitHubForge) reader(ctx context.Context, owner string) (*gh.Client, error) {
	return gh.NewClientFromConfigContext(ctx, f.cfg, owner)
}

func (f gitHubForge) GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error) {
	c, err := f.reader(ctx, owner)
	if err != nil {
		return nil, err
	}
	return gh.NewIssueService(c).GetIssueWithRelations(ctx, owner, repo, number, gh.NoRelations)
}

func (f gitHubForge) GetPullRequest(ctx context.Context, owner, repo string, number int) (*types.PullRequest, error) {
	c, err := f.reader(ctx, owner)
	if err != nil {
		return nil, err
	}
	return gh.NewPRService(c).GetPR(ctx, owner, repo, number)
}

func (f gitHubForge) SearchIssues(ctx context.Context, owner, repo, query string, limit int) ([]types.Issue, error) {
	c, err := f.reader(ctx, owner)
	if err != nil {
		return nil, err
	}
	return gh.NewIssueService(c).SearchIssues(ctx, owner, repo, query, limit)
}

// CreateIssue resolves the write's credential, then files the issue as the
// IPC issue.create method does: the repository's node id, then
// IssueService.CreateIssue. A teammate's write resolves the App and nothing
// else (gh.NewAppOnlyClient); when the App is unavailable nothing is sent.
func (f gitHubForge) CreateIssue(ctx context.Context, owner, repo, title, body string, asOperator bool) (*types.Issue, error) {
	var (
		c   *gh.Client
		err error
	)
	if asOperator {
		c, err = gh.NewClientFromConfigContext(ctx, f.cfg, owner)
	} else {
		c, err = gh.NewAppOnlyClient(ctx, f.cfg, owner)
	}
	if err != nil {
		if errors.Is(err, gh.ErrAppUnavailable) {
			return nil, fail(CodeAppUnavailable, appUnavailableMessage(owner))
		}
		return nil, err
	}
	repoID, err := c.GetRepositoryID(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	return gh.NewIssueService(c).CreateIssue(ctx, repoID, title, body, nil)
}

func appUnavailableMessage(owner string) string {
	return "this workspace has no usable GitHub App installation for " + owner +
		"; an issue a teammate asks for is filed only through the App, never with another account's token"
}

// forgeErr is the error a failed forge request shows the model: a typed
// error as it is, anything else as a forge error without its detail, which
// can carry a request URL or a response body.
func forgeErr(err error) *Error {
	var te *Error
	if errors.As(err, &te) {
		return te
	}
	return fail(CodeForgeError, "the forge request failed")
}

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	// idPattern is what a hosted-service id may be (conversation.idPattern).
	idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
)

type repoArgs struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// check validates a repository argument and the poster's read access to it.
func (s *Toolset) checkRepo(a repoArgs) *Error {
	if !ownerPattern.MatchString(a.Owner) || !repoPattern.MatchString(a.Repo) || a.Repo == "." || a.Repo == ".." {
		return fail(CodeInvalidArguments, "owner and repo must be a GitHub owner and repository name")
	}
	if !s.canReadRepo(a.Owner, a.Repo) {
		return errRepoNotReadable
	}
	return nil
}

func (s *Toolset) forge() (Forge, *Error) {
	if s.env.Forge == nil {
		return nil, fail(CodeForgeError, "no forge is configured on this host")
	}
	return s.env.Forge, nil
}

func (s *Toolset) getIssue(ctx context.Context, raw json.RawMessage) (string, *Error) {
	var a struct {
		repoArgs
		Number int `json:"number"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	if err := s.checkRepo(a.repoArgs); err != nil {
		return "", err
	}
	if a.Number <= 0 {
		return "", fail(CodeInvalidArguments, "number must be a positive issue number")
	}
	f, ferr := s.forge()
	if ferr != nil {
		return "", ferr
	}
	ctx, cancel := context.WithTimeout(ctx, forgeTimeout)
	defer cancel()
	issue, err := f.GetIssue(ctx, a.Owner, a.Repo, a.Number)
	if err != nil {
		return "", forgeErr(err)
	}
	return untrusted(fmt.Sprintf("issue %s/%s#%d", a.Owner, a.Repo, a.Number), issueView(issue)), nil
}

func issueView(i *types.Issue) map[string]any {
	labels := make([]string, len(i.Labels))
	for k, l := range i.Labels {
		labels[k] = clean(l)
	}
	return map[string]any{
		"number": i.Number, "title": clean(i.Title), "state": i.State, "labels": labels,
		"url": i.URL, "body": clean(i.Body),
	}
}

// searchQualifier is a GitHub search qualifier (repo:, org:, is:, in: …): a
// query keeps none, so a search can never reach past the repository it
// names.
var searchQualifier = regexp.MustCompile(`\S*:\S*`)

func (s *Toolset) searchIssues(ctx context.Context, raw json.RawMessage) (string, *Error) {
	var a struct {
		repoArgs
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	if err := s.checkRepo(a.repoArgs); err != nil {
		return "", err
	}
	query := strings.Join(strings.Fields(searchQualifier.ReplaceAllString(a.Query, " ")), " ")
	if query == "" || len(query) > 256 {
		return "", fail(CodeInvalidArguments, "query must be 1 to 256 characters of keywords, without search qualifiers")
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 30)
	f, ferr := s.forge()
	if ferr != nil {
		return "", ferr
	}
	ctx, cancel := context.WithTimeout(ctx, forgeTimeout)
	defer cancel()
	found, err := f.SearchIssues(ctx, a.Owner, a.Repo, query, limit)
	if err != nil {
		return "", forgeErr(err)
	}
	want := a.Owner + "/" + a.Repo
	var out []map[string]any
	for i := range found {
		// The query carries no qualifier, and a result from another
		// repository is dropped all the same.
		if found[i].Repo != "" && !strings.EqualFold(found[i].Repo, want) {
			continue
		}
		v := issueView(&found[i])
		delete(v, "body")
		out = append(out, v)
	}
	return untrusted("issue search in "+want, map[string]any{"query": query, "issues": out}), nil
}

func (s *Toolset) getPullRequest(ctx context.Context, raw json.RawMessage) (string, *Error) {
	var a struct {
		repoArgs
		Number int `json:"number"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	if err := s.checkRepo(a.repoArgs); err != nil {
		return "", err
	}
	if a.Number <= 0 {
		return "", fail(CodeInvalidArguments, "number must be a positive pull request number")
	}
	f, ferr := s.forge()
	if ferr != nil {
		return "", ferr
	}
	ctx, cancel := context.WithTimeout(ctx, forgeTimeout)
	defer cancel()
	pr, err := f.GetPullRequest(ctx, a.Owner, a.Repo, a.Number)
	if err != nil {
		return "", forgeErr(err)
	}
	return untrusted(fmt.Sprintf("pull request %s/%s#%d", a.Owner, a.Repo, a.Number), map[string]any{
		"number": pr.Number, "title": clean(pr.Title), "state": pr.State, "draft": pr.IsDraft,
		"head": clean(pr.HeadRef), "base": clean(pr.BaseRef), "url": pr.URL,
		"review_status": pr.ReviewStatus, "check_status": pr.CheckStatus,
		"additions": pr.Additions, "deletions": pr.Deletions, "body": clean(pr.Body),
	}), nil
}
