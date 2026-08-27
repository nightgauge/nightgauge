package forge

import (
	"context"
	"strconv"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// IssueRef identifies one issue by number, carrying its own owner and repo.
//
// Both sides of a link carry their own repository because cross-repository
// linking is a live path in this tree, not a hypothetical: internal/audit's
// issue creator resolves the epic's repository and the sub-issue's repository
// from different sources and they routinely differ. A single owner/repo pair
// shared by both ends of the call would be silently wrong there — it would
// link within whichever repository the caller happened to name.
type IssueRef struct {
	Owner  string
	Repo   string
	Number int
}

// String renders the canonical "owner/repo#number" form used in errors.
func (r IssueRef) String() string {
	return r.Owner + "/" + r.Repo + "#" + strconv.Itoa(r.Number)
}

// UpdateIssueOptions describes a partial update to an existing issue. All
// fields are pointers so callers can distinguish "do not change" (nil) from
// "set to empty" (non-nil pointer to zero value). State accepts forge-agnostic
// "opened" / "closed".
type UpdateIssueOptions struct {
	Title     *string
	Body      *string
	Labels    *[]string
	Assignees *[]string
	State     *string // "opened" or "closed"
	Milestone *string
}

// IssueService is the forge-agnostic surface for issue operations. The
// signature set mirrors GitHub's issue operations; future GitLab support
// maps GitLab's Issue API to the same interface.
type IssueService interface {
	// Read.
	GetIssue(ctx context.Context, owner, repo string, number int) (*forgetypes.Issue, error)
	GetIssuesByNumbers(ctx context.Context, owner, repo string, numbers []int) (map[int]*forgetypes.Issue, error)
	ListIssues(ctx context.Context, owner, repo string, labels []string) ([]forgetypes.Issue, error)
	IterateIssues(ctx context.Context, owner, repo string, labels []string) Iterator[forgetypes.Issue]
	SearchIssues(ctx context.Context, owner, repo, query string, limit int) ([]forgetypes.Issue, error)
	HasLabel(ctx context.Context, owner, repo string, number int, label string) (bool, error)
	GetRepoLabels(ctx context.Context, owner, repo string) (map[string]string, error)

	// Mutate (CRUD).
	CreateIssue(ctx context.Context, repoID, title, body string, labelIDs []string) (*forgetypes.Issue, error)
	CloseIssue(ctx context.Context, issueID string) error
	ReopenIssue(ctx context.Context, issueID string) error
	EditIssue(ctx context.Context, nodeID, body string) (*forgetypes.Issue, error)
	UpdateIssue(ctx context.Context, nodeID string, opts UpdateIssueOptions) (*forgetypes.Issue, error)

	// Comments.
	AddComment(ctx context.Context, subjectID, body string) error

	// Sub-issue linking (GitHub-native; GitLab adapters may emulate via
	// related issues with a documented mapping).
	//
	// These take references rather than node IDs. The REST endpoints behind
	// them address issues by number and database id, so a caller that already
	// knows the number no longer pays a read to turn it into a node ID.
	AddSubIssue(ctx context.Context, parent, child IssueRef) error
	RemoveSubIssue(ctx context.Context, parent, child IssueRef) error
	LinkSubIssue(ctx context.Context, owner, repo string, parentNumber, childNumber int) error

	// Blocking relationships.
	AddBlockedBy(ctx context.Context, blocked, blocker IssueRef) error
	RemoveBlockedBy(ctx context.Context, blocked, blocker IssueRef) error

	// Labels.
	AddLabels(ctx context.Context, issueID string, labelIDs []string) error
	RemoveLabels(ctx context.Context, issueID string, labelIDs []string) error
	SyncStatusLabel(ctx context.Context, owner, repo string, number int, newStatus string) error
	MarkRefined(ctx context.Context, owner, repo string, number int) error

	// Epic helpers.
	GetEpicProgress(ctx context.Context, epicNodeID string) (*forgetypes.EpicProgress, error)
	GetEpicProgressByNumber(ctx context.Context, owner, repo string, number int) (*forgetypes.EpicProgress, error)
}
