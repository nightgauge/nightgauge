package github

import (
	"github.com/nightgauge/nightgauge/internal/forge"
)

// Compile-time interface satisfaction asserts. Each line will fail the
// build immediately if a github service drifts out of sync with its forge
// interface — surfacing the offending method one service at a time so the
// failure points to a concrete fix.
//
// ADR-006 §"Audit table" lists the full method-by-method mapping; these
// asserts are the load-bearing check that the audit table stays accurate.
var (
	_ forge.IssueService   = (*IssueService)(nil)
	_ forge.PRService      = (*PRService)(nil)
	_ forge.ProjectService = (*ProjectService)(nil)
	_ forge.BoardService   = (*BoardService)(nil)
	_ forge.CIService      = (*CIService)(nil)
	_ forge.LabelService   = (*LabelService)(nil)
	_ forge.RulesetService = (*RulesetService)(nil)
	_ forge.AuthService    = (*Client)(nil)
	_ forge.RepoService    = (*RepoService)(nil)
	_ forge.GraphQLService = (*Client)(nil)
	_ forge.ForgeClient    = (*ForgeAdapter)(nil)
)

// ForgeAdapter wraps a *Client (plus owner/projectNumber/ownerType) and
// exposes per-domain services as forge.* interfaces. The struct is the
// concrete value returned by forge.New({Kind: "github"}).
//
// Service instances are constructed lazily on first access and cached so
// callers can hold ForgeAdapter and re-use the inner services without
// paying the construction cost on every accessor call.
type ForgeAdapter struct {
	client        *Client
	owner         string
	projectNumber int
	ownerType     OwnerType

	// Lazy-cached service singletons. nil until first accessed.
	issues   *IssueService
	prs      *PRService
	project  *ProjectService
	board    *BoardService
	ci       *CIService
	labels   *LabelService
	rulesets *RulesetService
	repo     *RepoService
}

// NewForgeAdapter constructs a ForgeAdapter for the given client/project.
// The Forge() accessor on *Client is the typical entry point; this
// constructor is exported for tests and for forge.New's GitHub branch.
func NewForgeAdapter(client *Client, owner string, projectNumber int, ownerType OwnerType) *ForgeAdapter {
	return &ForgeAdapter{
		client:        client,
		owner:         owner,
		projectNumber: projectNumber,
		ownerType:     ownerType,
	}
}

// Issues returns the IssueService as a forge.IssueService.
func (a *ForgeAdapter) Issues() forge.IssueService {
	if a.issues == nil {
		a.issues = NewIssueService(a.client)
	}
	return a.issues
}

// PRs returns the PRService as a forge.PRService.
func (a *ForgeAdapter) PRs() forge.PRService {
	if a.prs == nil {
		a.prs = NewPRService(a.client)
	}
	return a.prs
}

// Project returns the ProjectService as a forge.ProjectService.
func (a *ForgeAdapter) Project() forge.ProjectService {
	if a.project == nil {
		a.project = NewProjectService(a.client, a.owner, a.projectNumber, a.ownerType)
	}
	return a.project
}

// Board returns a read-only BoardService as a forge.BoardService.
func (a *ForgeAdapter) Board() forge.BoardService {
	if a.board == nil {
		a.board = NewBoardService(a.client, a.owner, a.projectNumber, a.ownerType)
	}
	return a.board
}

// CI returns the CIService as a forge.CIService.
func (a *ForgeAdapter) CI() forge.CIService {
	if a.ci == nil {
		a.ci = NewCIService(a.client)
	}
	return a.ci
}

// Labels returns the LabelService as a forge.LabelService. The label
// service is repo-scoped — the adapter uses the configured owner and
// "nightgauge" as the default repo name when ProjectNumber is the
// only known scope. Callers needing a different repo construct
// NewLabelService directly.
func (a *ForgeAdapter) Labels() forge.LabelService {
	if a.labels == nil {
		// Default repo name comes from the owner namespace; the convention
		// is the repository hosting the pipeline. If callers need a
		// different repo, they should bypass the adapter and call
		// NewLabelService directly.
		a.labels = NewLabelService(a.client, a.owner, "nightgauge")
	}
	return a.labels
}

// Rulesets returns the RulesetService as a forge.RulesetService.
func (a *ForgeAdapter) Rulesets() forge.RulesetService {
	if a.rulesets == nil {
		a.rulesets = NewRulesetService(a.client)
	}
	return a.rulesets
}

// Auth returns the *Client itself as a forge.AuthService — the underlying
// CheckTokenScopes method hangs off Client to avoid a needless wrapper.
// Documented choice in ADR-006.
func (a *ForgeAdapter) Auth() forge.AuthService {
	return a.client
}

// Repo returns the RepoService as a forge.RepoService.
func (a *ForgeAdapter) Repo() forge.RepoService {
	if a.repo == nil {
		a.repo = NewRepoService(a.client)
	}
	return a.repo
}

// Forge returns this Client wrapped as a forge.ForgeClient. Convenience
// for callers that want to pass a *Client forward as the abstract type
// without explicitly constructing a ForgeAdapter. Owner/project info
// defaults to empty when not set on the client; callers needing
// non-default owner/project should use NewForgeAdapter directly.
func (c *Client) Forge(owner string, projectNumber int, ownerType OwnerType) forge.ForgeClient {
	return NewForgeAdapter(c, owner, projectNumber, ownerType)
}

// init registers the github adapter with forge.New so importing this
// package is the only wiring the caller needs.
func init() {
	forge.RegisterAdapter(forge.KindGitHub, func(cfg forge.Config) (forge.ForgeClient, error) {
		client := NewClientWithToken(cfg.Token)
		ot := OwnerTypeOrg
		if cfg.OwnerType == string(OwnerTypeUser) {
			ot = OwnerTypeUser
		}
		return NewForgeAdapter(client, cfg.Owner, cfg.ProjectNumber, ot), nil
	})
}
