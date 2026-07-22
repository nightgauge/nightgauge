package gitlab

import (
	"github.com/nightgauge/nightgauge/internal/forge"
)

// Compile-time interface satisfaction asserts. Drift between gitlab and
// forge surfaces breaks the build immediately, surfacing the offending
// method one service at a time. Mirrors the GitHub adapter's pattern.
var (
	_ forge.IssueService   = (*IssueService)(nil)
	_ forge.PRService      = (*PRService)(nil)
	_ forge.ProjectService = (*ProjectService)(nil)
	_ forge.BoardService   = (*BoardService)(nil)
	_ forge.CIService      = (*CIService)(nil)
	_ forge.LabelService   = (*LabelService)(nil)
	_ forge.RulesetService = (*RulesetService)(nil)
	_ forge.AuthService    = (*AuthAdapter)(nil)
	_ forge.RepoService    = (*RepoAdapter)(nil)
	_ forge.ForgeClient    = (*ForgeAdapter)(nil)
)

// ForgeAdapter wraps a *Client and exposes per-domain services as
// forge.* interfaces. Service instances are constructed lazily on first
// access and cached.
type ForgeAdapter struct {
	client *Client

	// owner / repo bind project + board services to a specific GitLab
	// project. The forge.New factory derives owner from cfg.Owner; repo
	// is currently empty unless the caller supplied a "owner/repo" string
	// in cfg.Owner. The full multi-repo workspace resolver lands in
	// #3361 and will replace the inline string-split here.
	owner    string
	repo     string
	strategy BoardStatusStrategy
	boardID  int

	// Lazy-cached service singletons. nil until first accessed.
	issues   *IssueService
	prs      *PRService
	project  *ProjectService
	board    *BoardService
	ci       *CIService
	labels   *LabelService
	rulesets *RulesetService
	auth     *AuthAdapter
	repoSvc  *RepoAdapter
}

// NewForgeAdapter constructs a ForgeAdapter for the given client. owner /
// repo / strategy / boardID are project-scoped configuration consumed by
// the project + board services.
func NewForgeAdapter(client *Client, opts ...ForgeAdapterOption) *ForgeAdapter {
	a := &ForgeAdapter{client: client, strategy: StrategyLabelStatus}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// ForgeAdapterOption tunes a ForgeAdapter at construction.
type ForgeAdapterOption func(*ForgeAdapter)

// WithProject binds the adapter to a specific GitLab project ("owner/repo")
// for project + board services that need a project context.
func WithProject(owner, repo string) ForgeAdapterOption {
	return func(a *ForgeAdapter) {
		a.owner = owner
		a.repo = repo
	}
}

// WithStatusStrategy selects the Status mapping strategy used by
// ProjectService. Empty (or unrecognised) strategies default to
// StrategyLabelStatus.
func WithStatusStrategy(s BoardStatusStrategy) ForgeAdapterOption {
	return func(a *ForgeAdapter) {
		if s != "" {
			a.strategy = s
		}
	}
}

// WithBoardID binds the adapter to a specific GitLab board number used as
// the SnapshotFields.ProjectID surface.
func WithBoardID(id int) ForgeAdapterOption {
	return func(a *ForgeAdapter) {
		a.boardID = id
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
		a.project = NewProjectServiceFor(a.client, a.owner, a.repo, a.strategy, a.boardID)
	}
	return a.project
}

// Board returns the BoardService as a forge.BoardService.
func (a *ForgeAdapter) Board() forge.BoardService {
	if a.board == nil {
		a.board = NewBoardServiceFor(a.client, a.owner, a.repo)
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

// Labels returns the LabelService as a forge.LabelService.
func (a *ForgeAdapter) Labels() forge.LabelService {
	if a.labels == nil {
		a.labels = NewLabelService(a.client)
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

// Auth returns the AuthAdapter as a forge.AuthService. The implementation
// dispatches CheckTokenScopes and Whoami based on the auth method resolved
// at client construction time (PAT, OAuth2, CI job token, or deploy token).
func (a *ForgeAdapter) Auth() forge.AuthService {
	if a.auth == nil {
		a.auth = NewAuthAdapter(a.client)
	}
	return a.auth
}

// Repo returns the RepoAdapter as a forge.RepoService. The placeholder
// implementation returns ErrUnsupported until #3361 lands.
func (a *ForgeAdapter) Repo() forge.RepoService {
	if a.repoSvc == nil {
		a.repoSvc = NewRepoAdapter(a.client)
	}
	return a.repoSvc
}

// init registers the gitlab adapter with forge.New so importing this
// package is the only wiring the caller needs.
func init() {
	forge.RegisterAdapter(forge.KindGitLab, func(cfg forge.Config) (forge.ForgeClient, error) {
		// Base URL discovery is intentionally minimal here — full env-var /
		// config-tier wiring is part of the auth-chain work (#3354). When
		// the caller has not supplied a URL via Owner (which doubles as a
		// host hint for self-hosted instances), default to gitlab.com.
		baseURL := DefaultBaseURL
		client := NewClient(baseURL, cfg.Token)

		// Owner is interpreted as a "owner/repo" string when it contains a
		// slash, otherwise as the bare group/owner name (repo unset). The
		// proper multi-repo resolver is W3-5 / #3361.
		owner := cfg.Owner
		var repo string
		if i := indexOfSlash(owner); i >= 0 {
			repo = owner[i+1:]
			owner = owner[:i]
		}

		return NewForgeAdapter(client,
			WithProject(owner, repo),
			WithStatusStrategy(BoardStatusStrategy(cfg.BoardStatusStrategy)),
			WithBoardID(cfg.ProjectNumber),
		), nil
	})
}

// indexOfSlash returns the first '/' index in s, or -1 when absent. Lifted
// out of init() so the registration body stays linear.
func indexOfSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
