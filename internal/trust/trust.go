// Package trust decides whether the pipeline may work an issue (#2130).
//
// The rule is the one GitHub's Copilot coding agent and
// anthropics/claude-code-action use: an issue is eligible when someone with
// write access (or higher) to the repository vouches for it, either by
// authoring it or by applying the `pipeline:approved` label. Only comments by
// such users reach a model. Repository permission, not author_association,
// decides: author_association calls a read-only outside collaborator a
// COLLABORATOR and every org member a MEMBER, whatever their repository role.
//
// One function, Gate.Decide, makes the decision, and every entry point that
// can start work on an issue calls it: refinement, Backlog-to-Ready
// promotion, the scheduler's dispatch check, the queue, IPC pipeline.runItem
// and `nightgauge run <issue>`. It fails closed: a permission or issue read
// that errors leaves the actor untrusted.
package trust

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ApprovedLabel is the label a user with write access applies to approve an
// issue opened by someone without it.
const ApprovedLabel = "pipeline:approved"

// DefaultTrustedBots are the only bot accounts trusted by authorship when
// autonomous.trusted_bots is unset: the pipeline's own GitHub App. Named
// after claude-code-action's allowed_bots.
//
// github-actions[bot] is deliberately absent: any workflow can act as it,
// including one that files issues from a model's reading of text the
// repository does not control. Trust it by listing it.
var DefaultTrustedBots = []string{"nightgauge-pipeline[bot]"}

// Level is a repository permission level as GitHub names its roles.
type Level string

const (
	LevelAdmin    Level = "admin"
	LevelMaintain Level = "maintain"
	LevelWrite    Level = "write"
	LevelTriage   Level = "triage"
	LevelRead     Level = "read"
	LevelNone     Level = "none"
)

// CanVouch reports whether the level may vouch for an issue: write or higher.
// Triage can label issues but cannot push, so it cannot approve one.
func (l Level) CanVouch() bool {
	return l == LevelAdmin || l == LevelMaintain || l == LevelWrite
}

// ParseLevel maps GitHub's permission answer onto a Level. roleName is the
// precise role (admin, maintain, write, triage, read, or a custom role's
// name); permission is the legacy field (admin, write, read, none), which
// folds maintain into write and triage into read. A custom role falls back to
// the legacy field, which reports the base role it inherits from.
func ParseLevel(roleName, permission string) Level {
	switch l := Level(strings.ToLower(strings.TrimSpace(roleName))); l {
	case LevelAdmin, LevelMaintain, LevelWrite, LevelTriage, LevelRead, LevelNone:
		return l
	}
	switch l := Level(strings.ToLower(strings.TrimSpace(permission))); l {
	case LevelAdmin, LevelWrite, LevelRead:
		return l
	}
	return LevelNone
}

// EventKind names the timeline events the gate reads.
type EventKind string

const (
	EventLabeled   EventKind = "labeled"
	EventUnlabeled EventKind = "unlabeled"
	EventRenamed   EventKind = "renamed"
)

// Event is one issue timeline event.
type Event struct {
	Kind  EventKind
	Label string // labeled and unlabeled only
	Actor string // login; a bot's ends in "[bot]"
	At    time.Time
}

// Signals is what the gate reads from the forge about one issue.
type Signals struct {
	// Author is the issue author's login; a bot's ends in "[bot]". Empty for a
	// deleted account.
	Author string
	// Labels are the issue's current labels, complete.
	Labels []string
	// BodyEditedAt is when the issue body was last edited; zero if never.
	BodyEditedAt time.Time
	// Events are the issue's most recent labeled, unlabeled and renamed
	// events, oldest first. A window that does not reach back to an approval
	// makes the approval unreadable, which fails closed.
	Events []Event
}

// Comment is one issue comment.
type Comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// Forge is what the gate reads. Implementations follow the forge client's
// own retry and backoff policy; the gate never retries.
type Forge interface {
	RepoPermission(ctx context.Context, owner, repo, login string) (Level, error)
	IssueSignals(ctx context.Context, owner, repo string, number int) (Signals, error)
}

// CommentSource is a Forge that can also list an issue's comments.
type CommentSource interface {
	Forge
	IssueComments(ctx context.Context, owner, repo string, number int) ([]Comment, error)
}

// Subject names the issue to decide, with what the caller already knows from
// the board so a decision usually costs no issue read.
type Subject struct {
	Owner  string
	Repo   string
	Number int
	// Author is the author's login when known; empty makes the gate read it.
	Author string
	// Labels are the labels the caller holds. LabelsComplete says they are the
	// whole set; only then is a missing approval label taken as absent
	// without reading the issue.
	Labels         []string
	LabelsComplete bool
}

// How an eligible issue was vouched for.
const (
	ViaAuthor     = "author"
	ViaTrustedBot = "trusted-bot"
	ViaApproval   = "approval"
)

// Decision is the gate's answer.
type Decision struct {
	Eligible    bool
	Via         string // ViaAuthor, ViaTrustedBot or ViaApproval when eligible
	Author      string
	AuthorLevel Level  // empty when the author is a bot or the lookup failed
	Approver    string // the approving writer, when Via is ViaApproval
	// Reason says why an issue is not eligible, naming the author, their
	// permission level and how to approve it.
	Reason string
}

// cacheTTL bounds a cached answer even if no cycle boundary resets it.
const cacheTTL = 5 * time.Minute

type permEntry struct {
	level Level
	err   error
	at    time.Time
}

type sigEntry struct {
	sig Signals
	err error
	at  time.Time
}

// Gate makes the eligibility decision. It caches permission lookups per
// (repository, login), and issue signals per issue, until ResetCycle, so a
// board scan makes at most one permission call per distinct actor (#842).
// It is safe for concurrent use.
type Gate struct {
	forge Forge
	bots  map[string]bool
	// appOwners are the repository owners where the pipeline's stages act as
	// a GitHub App installation. Label approval exists only there: every bot,
	// the App included, is refused as an approver, so no label a stage
	// applies — by any path — can approve anything. Elsewhere the stages act
	// as a person's own login, a label they applied cannot be told from that
	// person's approval, and only authorship vouches (#2130).
	appOwners map[string]bool
	// stageIDs lists every login a stage on this machine can authenticate as
	// (the gh keyring, env tokens, configured users). A label applied by one
	// of them never approves: a stage could have applied it. Shared by Fresh
	// copies; read once.
	stageIDs *stageIdentities
	now      func() time.Time

	mu    sync.Mutex
	perms map[string]permEntry
	sigs  map[string]sigEntry
}

// NewGate returns a gate over forge. trustedBots is autonomous.trusted_bots;
// empty means DefaultTrustedBots. Entries may be written "name",
// "name[bot]" or "app/name".
func NewGate(forge Forge, trustedBots []string) *Gate {
	if len(trustedBots) == 0 {
		trustedBots = DefaultTrustedBots
	}
	bots := make(map[string]bool, len(trustedBots))
	for _, b := range trustedBots {
		if n := botName(b); n != "" {
			bots[n] = true
		}
	}
	return &Gate{forge: forge, bots: bots, now: time.Now,
		perms: map[string]permEntry{}, sigs: map[string]sigEntry{}}
}

// Fresh returns a gate over the same forge and bots with an empty cache, for
// a decision that must see the forge as it is now without discarding the
// shared cycle's cache.
func (g *Gate) Fresh() *Gate {
	return &Gate{forge: g.forge, bots: g.bots, appOwners: g.appOwners, stageIDs: g.stageIDs, now: g.now,
		perms: map[string]permEntry{}, sigs: map[string]sigEntry{}}
}

// WithAppOwners names the repository owners where the pipeline runs as a
// GitHub App installation (github_auth.app with an installation for the
// owner) and returns the gate. Label approval is accepted only for issues in
// those owners' repositories.
func (g *Gate) WithAppOwners(owners ...string) *Gate {
	for _, o := range owners {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			if g.appOwners == nil {
				g.appOwners = map[string]bool{}
			}
			g.appOwners[o] = true
		}
	}
	return g
}

// stageIdentities resolves the stage logins at most once per cycle, shared
// by a gate and its Fresh copies. A successful read lasts until ResetCycle; a
// failed one is not kept, so the next decision reads again.
type stageIdentities struct {
	resolve func(context.Context) ([]string, error)
	mu      sync.Mutex
	set     map[string]bool
}

func (s *stageIdentities) get(ctx context.Context) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set != nil {
		return s.set, nil
	}
	logins, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	s.set = map[string]bool{}
	for _, l := range logins {
		s.set[strings.ToLower(strings.TrimSpace(l))] = true
	}
	return s.set, nil
}

func (s *stageIdentities) reset() {
	s.mu.Lock()
	s.set = nil
	s.mu.Unlock()
}

// WithStageIdentities sets how the gate learns every login a stage on this
// machine can authenticate as, and returns the gate. Without it, or when it
// errors, no label approves anything: fail closed.
func (g *Gate) WithStageIdentities(resolve func(context.Context) ([]string, error)) *Gate {
	if resolve != nil {
		g.stageIDs = &stageIdentities{resolve: resolve}
	}
	return g
}

// LabelApprovalAvailable reports whether `pipeline:approved` can approve an
// issue in owner's repositories: only where the pipeline acts as an App.
func (g *Gate) LabelApprovalAvailable(owner string) bool {
	return g.appOwners[strings.ToLower(strings.TrimSpace(owner))]
}

// ResetCycle forgets every cached answer. Call it at the start of each
// scheduler cycle and before a manual dispatch, so a permission or label
// change takes effect on the next decision.
func (g *Gate) ResetCycle() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.perms = map[string]permEntry{}
	g.sigs = map[string]sigEntry{}
	if g.stageIDs != nil {
		// Read again next cycle: an account added to the machine since the
		// last read must be refused from now on.
		g.stageIDs.reset()
	}
}

// botName normalizes a configured bot to its bare app name.
func botName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "app/")
	return strings.TrimSuffix(s, "[bot]")
}

// isBot reports whether login is a bot account. A user login cannot contain
// "[", so the suffix is a reliable discriminator.
func isBot(login string) bool {
	return strings.HasSuffix(strings.ToLower(login), "[bot]")
}

// IsTrustedBot reports whether login is a bot trusted by authorship. A user
// account that shares a trusted bot's name is not the bot.
func (g *Gate) IsTrustedBot(login string) bool {
	return isBot(login) && g.bots[botName(login)]
}

func (g *Gate) permission(ctx context.Context, owner, repo, login string) (Level, error) {
	if isBot(login) {
		// A bot is trusted by name or not at all; its permission is not asked.
		return LevelNone, nil
	}
	key := strings.ToLower(owner + "/" + repo + "@" + login)
	g.mu.Lock()
	if e, ok := g.perms[key]; ok && g.now().Sub(e.at) < cacheTTL {
		g.mu.Unlock()
		return e.level, e.err
	}
	g.mu.Unlock()
	level, err := g.forge.RepoPermission(ctx, owner, repo, login)
	if err != nil {
		level = ""
	}
	g.mu.Lock()
	g.perms[key] = permEntry{level: level, err: err, at: g.now()}
	g.mu.Unlock()
	return level, err
}

func (g *Gate) signals(ctx context.Context, owner, repo string, number int) (Signals, error) {
	key := strings.ToLower(fmt.Sprintf("%s/%s#%d", owner, repo, number))
	g.mu.Lock()
	if e, ok := g.sigs[key]; ok && g.now().Sub(e.at) < cacheTTL {
		g.mu.Unlock()
		return e.sig, e.err
	}
	g.mu.Unlock()
	sig, err := g.forge.IssueSignals(ctx, owner, repo, number)
	g.mu.Lock()
	g.sigs[key] = sigEntry{sig: sig, err: err, at: g.now()}
	g.mu.Unlock()
	return sig, err
}

// howToApprove ends every refusal.
const howToApprove = "a user with write access or higher must apply `" + ApprovedLabel + "` to approve it"

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), name) {
			return true
		}
	}
	return false
}

// Decide answers whether the pipeline may work the issue.
func (g *Gate) Decide(ctx context.Context, s Subject) Decision {
	repo := s.Owner + "/" + s.Repo
	refuse := func(d Decision, format string, args ...any) Decision {
		d.Eligible = false
		d.Reason = fmt.Sprintf("%s#%d is not eligible: ", repo, s.Number) +
			fmt.Sprintf(format, args...) + "; " + howToApprove
		return d
	}

	author := strings.TrimSpace(s.Author)
	labels, labelsComplete := s.Labels, s.LabelsComplete
	var sig *Signals
	if author == "" {
		got, err := g.signals(ctx, s.Owner, s.Repo, s.Number)
		if err != nil {
			return refuse(Decision{}, "its author could not be read (%v)", err)
		}
		sig = &got
		author, labels, labelsComplete = got.Author, got.Labels, true
	}
	d := Decision{Author: author}
	if author == "" {
		return refuse(d, "its author's account no longer exists")
	}
	if g.IsTrustedBot(author) {
		d.Eligible, d.Via = true, ViaTrustedBot
		return d
	}

	level, err := g.permission(ctx, s.Owner, s.Repo, author)
	var who string
	switch {
	case err != nil:
		who = fmt.Sprintf("the permission of @%s on %s could not be read (%v), so it counts as none", author, repo, err)
	case isBot(author):
		who = fmt.Sprintf("@%s is a bot that autonomous.trusted_bots does not list", author)
	default:
		d.AuthorLevel = level
		if level.CanVouch() {
			d.Eligible, d.Via = true, ViaAuthor
			return d
		}
		who = fmt.Sprintf("@%s has %s access to %s", author, level, repo)
	}

	// Not vouched for by authorship. Label approval exists only where the
	// pipeline acts as an App; elsewhere authorship is the only voucher.
	if !g.LabelApprovalAvailable(s.Owner) {
		d.Eligible = false
		d.Reason = fmt.Sprintf("%s#%d is not eligible: %s. Label approval is unavailable for %s: the pipeline "+
			"acts as a personal token there, so a `%s` its stages applied could not be told apart from a "+
			"maintainer's. A user with write access must open the issue, or configure a GitHub App installation "+
			"for %s (github_auth.app) to approve issues by label", repo, s.Number, who, s.Owner, ApprovedLabel, s.Owner)
		return d
	}

	// Look for a writer's approval.
	if labelsComplete && !hasLabel(labels, ApprovedLabel) {
		return refuse(d, "%s, and the issue does not carry `%s`", who, ApprovedLabel)
	}
	if sig == nil {
		got, err := g.signals(ctx, s.Owner, s.Repo, s.Number)
		if err != nil {
			return refuse(d, "%s, and its approval could not be read (%v)", who, err)
		}
		sig = &got
	}
	if !hasLabel(sig.Labels, ApprovedLabel) {
		return refuse(d, "%s, and the issue does not carry `%s`", who, ApprovedLabel)
	}
	events := append([]Event(nil), sig.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	var last *Event
	for i := range events {
		e := &events[i]
		if e.Kind == EventLabeled && strings.EqualFold(e.Label, ApprovedLabel) {
			last = e
		}
	}
	if last == nil {
		return refuse(d, "%s, and there is no record of who applied `%s` in the timeline read", who, ApprovedLabel)
	}
	approver := strings.TrimSpace(last.Actor)
	if approver == "" {
		return refuse(d, "%s, and `%s` was applied by an account that no longer exists", who, ApprovedLabel)
	}
	alevel, aerr := g.permission(ctx, s.Owner, s.Repo, approver)
	switch {
	case aerr != nil:
		return refuse(d, "%s, and the permission of @%s, who applied `%s`, could not be read (%v)",
			who, approver, ApprovedLabel, aerr)
	case isBot(approver):
		// The pipeline's stages act as its App, a bot: refusing every bot is
		// what makes a stage-applied label, by any path, never an approval.
		return refuse(d, "%s, and `%s` was applied by @%s, a bot, which cannot approve", who, ApprovedLabel, approver)
	case !alevel.CanVouch():
		return refuse(d, "%s, and `%s` was last applied by @%s, who has %s access", who, ApprovedLabel, approver, alevel)
	}
	// The approver must not be an identity a stage here can act as: a label
	// such an identity applied may be a stage's, not a person's. For a
	// maintainer whose own login is on the machine that runs the stages, that
	// leaves authorship as the way to vouch.
	if g.stageIDs == nil {
		return refuse(d, "%s, and the identities this machine's stages can act as are unknown, so no label can approve", who)
	}
	ids, idErr := g.stageIDs.get(ctx)
	switch {
	case idErr != nil:
		return refuse(d, "%s, and the identities this machine's stages can act as could not be read (%v), so no label can approve", who, idErr)
	case ids[strings.ToLower(approver)]:
		return refuse(d, "%s, and `%s` was applied by @%s, an identity this machine's stages can act as, so it may "+
			"be a stage's label rather than a person's", who, ApprovedLabel, approver)
	}
	// An approval covers the text it was given. Any edit to the body or the
	// title at or after the approval voids it, whoever made it, the
	// pipeline's own App included: its stages are model sessions, and a
	// model's rewrite of an approved issue is not what the maintainer
	// approved. The maintainer re-applies the label to approve the new text.
	approvedAt := last.At.UTC().Format(time.RFC3339)
	if !sig.BodyEditedAt.IsZero() && !last.At.After(sig.BodyEditedAt) {
		return refuse(d, "%s, and its body was edited after @%s approved it at %s", who, approver, approvedAt)
	}
	for _, e := range events {
		if e.Kind == EventRenamed && !e.At.Before(last.At) {
			return refuse(d, "%s, and its title was edited after @%s approved it at %s", who, approver, approvedAt)
		}
	}
	d.Eligible, d.Via, d.Approver = true, ViaApproval, approver
	return d
}

// TrustedComments lists an issue's comments and keeps those FilterComments
// keeps. It is the one read for a comment whose text releases or gates work,
// such as the post-merge validation marker (#2574): a comment by anyone else
// is dropped before a caller can see it. A forge that cannot list comments is
// an error, never an empty list — an empty list would read as "no marker".
func (g *Gate) TrustedComments(ctx context.Context, owner, repo string, number int) ([]Comment, error) {
	src, ok := g.forge.(CommentSource)
	if !ok {
		return nil, fmt.Errorf("comments of %s/%s#%d: this forge cannot list comments", owner, repo, number)
	}
	all, err := src.IssueComments(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	kept, _ := g.FilterComments(ctx, owner, repo, all)
	return kept, nil
}

// FilterComments keeps the comments whose author may vouch for an issue: a
// user with write access or higher, or a trusted bot. It returns the kept
// comments in order and how many it removed. A lookup that fails removes the
// comment. Callers log the count, never the removed text.
func (g *Gate) FilterComments(ctx context.Context, owner, repo string, comments []Comment) (kept []Comment, removed int) {
	kept = make([]Comment, 0, len(comments))
	for _, c := range comments {
		author := strings.TrimSpace(c.Author)
		ok := false
		switch {
		case author == "":
		case g.IsTrustedBot(author):
			ok = true
		case isBot(author):
		default:
			level, err := g.permission(ctx, owner, repo, author)
			ok = err == nil && level.CanVouch()
		}
		if ok {
			kept = append(kept, c)
		} else {
			removed++
		}
	}
	return kept, removed
}
