// Package forge defines the forge-agnostic interface surface that
// Nightgauge consumes. A "forge" is any source-hosting system that
// provides issues, pull/merge requests, project boards, CI checks, branch
// rulesets, and labels — GitHub today, GitLab and others in the future.
//
// The package is structured as a set of per-domain service interfaces
// (IssueService, PRService, ProjectService, BoardService, CIService,
// LabelService, RulesetService, AuthService) plus a ForgeClient aggregate
// that exposes accessors for each. Concrete adapters live in sibling
// packages (internal/github today) and provide compile-time satisfaction
// of these interfaces.
//
// New(Config) is the factory entry point. It dispatches on Config.Kind to
// build the appropriate adapter and returns it as a ForgeClient. Unknown
// kinds return ErrUnsupported wrapped with %w so callers can use
// errors.Is(err, forge.ErrUnsupported).
//
// See docs/decisions/006-forge-abstraction.md for the design rationale,
// the audit table mapping every internal/github method to its forge
// counterpart, and the cross-forge interaction diagram.
package forge

// Kind discriminates between supported forges. New string values are added
// when an adapter is implemented; until then they return ErrUnsupported.
type Kind string

// Supported (and recognised-but-unsupported) Kind values.
const (
	KindGitHub Kind = "github"
	KindGitLab Kind = "gitlab"
)

// Config is the minimal data needed to construct a ForgeClient. Adapter-
// specific options (rate-limit floor, GraphQL URL override for tests,
// shared rate-limit tracker) remain on the underlying client constructors
// rather than leaking into this cross-forge config.
type Config struct {
	// Kind selects the adapter (e.g. "github").
	Kind Kind

	// Token is the auth token for the forge. Resolution from env / gh CLI
	// happens in the caller; Config carries only the resolved value.
	Token string

	// Owner is the org or user namespace the client targets (e.g.
	// "nightgauge" or "octocat").
	Owner string

	// ProjectNumber is the project board number (1-based) for forges that
	// expose projects (GitHub Projects V2). Zero means "no project bound".
	ProjectNumber int

	// OwnerType distinguishes organisations from user accounts. Forges
	// that don't separate the two should leave this empty.
	OwnerType string

	// Host is the server hostname for self-hosted forge instances (e.g. a
	// self-hosted GitLab at "gitlab.mycompany.com"). Empty for github.com
	// and gitlab.com SaaS. Consumed by Router.ResolveLink to construct
	// cross-forge full URLs.
	Host string

	// BoardStatusStrategy selects how Status field writes are mapped onto
	// the underlying forge. Currently consumed only by the GitLab adapter:
	//
	//   - "" (empty) or "label-status": scoped Status::<value> label,
	//     mutually-exclusive enforcement via GitLab scoped labels.
	//   - "state-only": map Done → close, anything else → reopen + clear
	//     Status::* labels. In-between states (In progress / In review)
	//     return a wrapped error because state-only mode cannot represent
	//     them.
	//
	// The GitHub adapter ignores this field — Status is a project-V2
	// single-select option there.
	BoardStatusStrategy string
}

// ForgeClient is the aggregate interface that exposes per-domain services.
// Callers depend on this interface (or a subset like BoardService) rather
// than on a concrete adapter.
type ForgeClient interface {
	Issues() IssueService
	PRs() PRService
	Project() ProjectService
	Board() BoardService
	CI() CIService
	Labels() LabelService
	Rulesets() RulesetService
	Auth() AuthService
	Repo() RepoService
}

// adapterFactory is the signature an adapter package registers via
// RegisterAdapter to participate in forge.New dispatch. Adapters live in
// sibling packages and call RegisterAdapter from an init() function so
// that importing the adapter package is the only wiring needed.
type adapterFactory func(Config) (ForgeClient, error)

// adapters is the per-Kind dispatch table populated by RegisterAdapter.
// Reads happen on a single goroutine in New (after any init() registers
// have run) so no synchronisation is required.
var adapters = map[Kind]adapterFactory{}

// RegisterAdapter associates a Kind with its constructor. Intended to be
// called from an init() in an adapter package. Re-registering an existing
// Kind silently overrides the prior factory — useful in tests.
func RegisterAdapter(kind Kind, factory func(Config) (ForgeClient, error)) {
	adapters[kind] = factory
}

// New constructs a ForgeClient for the given Config. Returns
// ErrUnsupported (wrapped) when no adapter is registered for the kind.
func New(cfg Config) (ForgeClient, error) {
	factory, ok := adapters[cfg.Kind]
	if !ok {
		return nil, wrapUnsupported(cfg.Kind)
	}
	return factory(cfg)
}

// wrapUnsupported produces an error chain such that errors.Is(err,
// ErrUnsupported) is true and the message identifies the offending kind.
func wrapUnsupported(kind Kind) error {
	return &unsupportedKindError{kind: kind}
}

type unsupportedKindError struct {
	kind Kind
}

func (e *unsupportedKindError) Error() string {
	if e.kind == "" {
		return ErrUnsupported.Error() + ": (empty kind)"
	}
	return ErrUnsupported.Error() + ": " + string(e.kind)
}

func (e *unsupportedKindError) Unwrap() error {
	return ErrUnsupported
}
