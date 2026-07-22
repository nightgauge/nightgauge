package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

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
	AddSubIssue(ctx context.Context, parentID, childID string) error
	RemoveSubIssue(ctx context.Context, parentID, childID string) error
	LinkSubIssue(ctx context.Context, owner, repo string, parentNumber, childNumber int) error

	// Blocking relationships.
	AddBlockedBy(ctx context.Context, blockedID, blockerID string) error
	RemoveBlockedBy(ctx context.Context, blockedID, blockerID string) error

	// Labels.
	AddLabels(ctx context.Context, issueID string, labelIDs []string) error
	RemoveLabels(ctx context.Context, issueID string, labelIDs []string) error
	SyncStatusLabel(ctx context.Context, owner, repo string, number int, newStatus string) error
	MarkRefined(ctx context.Context, owner, repo string, number int) error

	// Epic helpers.
	GetEpicProgress(ctx context.Context, epicNodeID string) (*forgetypes.EpicProgress, error)
	GetEpicProgressByNumber(ctx context.Context, owner, repo string, number int) (*forgetypes.EpicProgress, error)
}
