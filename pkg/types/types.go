// Package types defines shared domain types for the nightgauge CLI.
package types

import "time"

// Priority represents issue priority levels.
type Priority string

const (
	PriorityP0 Priority = "P0"
	PriorityP1 Priority = "P1"
	PriorityP2 Priority = "P2"
	PriorityP3 Priority = "P3"
)

// Size represents issue size labels.
type Size string

const (
	SizeXS Size = "XS"
	SizeS  Size = "S"
	SizeM  Size = "M"
	SizeL  Size = "L"
	SizeXL Size = "XL"
)

// BoardItem represents an item on the GitHub Project Board.
type BoardItem struct {
	ID            string    `json:"id"`
	NodeID        string    `json:"nodeId"`
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	State         string    `json:"state"`
	Status        string    `json:"status"`
	Priority      Priority  `json:"priority"`
	Size          Size      `json:"size"`
	PipelineStage string    `json:"pipelineStage,omitempty"`
	Labels        []string  `json:"labels"`
	Repo          string    `json:"repo"`
	URL           string    `json:"url"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	IsPR          bool      `json:"isPR"`

	// Sub-issue relationships (GitHub native)
	IsEpic       bool          `json:"isEpic"`
	SubIssues    []SubIssueRef `json:"subIssues,omitempty"`
	ParentNumber int           `json:"parentIssueNumber,omitempty"` // parent epic number (0 = no parent)
	ParentTitle  string        `json:"parentIssueTitle,omitempty"`  // parent epic title (for cross-status resolution)

	// Blocking relationships (GitHub native)
	BlockedBy []BlockingRef `json:"blockedBy,omitempty"`
	Blocking  []BlockingRef `json:"blocking,omitempty"`
}

// Issue represents a GitHub issue with sub-issue and blocking relationships.
type Issue struct {
	NodeID string `json:"nodeId"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	// StateReason is GitHub's issue close reason: "COMPLETED", "NOT_PLANNED",
	// or "REOPENED" (empty for OPEN issues). The post-merge reconciler uses it
	// to distinguish an epic closed as completed (its orphaned open sub-issues
	// are safe to auto-close) from one closed as not-planned/cancelled (subs
	// must be left untouched).
	StateReason string   `json:"stateReason,omitempty"`
	Labels      []string `json:"labels"`
	Repo        string   `json:"repo"`
	URL         string   `json:"url"`
	Assignees   []string `json:"assignees"`
	IsEpic      bool     `json:"isEpic"`
	Milestone   string   `json:"milestone,omitempty"`

	// Sub-issue relationships
	ParentIssueID     string        `json:"parentIssueId,omitempty"`
	ParentIssueNumber int           `json:"parentIssueNumber,omitempty"`
	SubIssues         []SubIssueRef `json:"subIssues,omitempty"`

	// Blocking relationships
	BlockedBy []BlockingRef `json:"blockedBy,omitempty"`
	Blocking  []BlockingRef `json:"blocking,omitempty"`
}

// SubIssueRef is a lightweight reference to a sub-issue.
type SubIssueRef struct {
	NodeID string   `json:"nodeId"`
	Number int      `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state"`
	Repo   string   `json:"repo"`
	Labels []string `json:"labels,omitempty"`
}

// BlockingRef is a lightweight reference to a blocking/blockedBy issue.
type BlockingRef struct {
	NodeID string `json:"nodeId"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Repo   string `json:"repo"`
}

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	NodeID    string `json:"nodeId"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	HeadRef   string `json:"headRef"`
	BaseRef   string `json:"baseRef"`
	Repo      string `json:"repo"`
	URL       string `json:"url"`
	Mergeable string `json:"mergeable"`
	// MergeStateStatus is GitHub's mergeStateStatus enum: CLEAN | DIRTY |
	// BLOCKED | BEHIND | UNSTABLE | HAS_HOOKS | UNKNOWN. It distinguishes a
	// clean-but-unflipped PR from one blocked by conflicts (DIRTY), a moved
	// base (BEHIND), branch protection (BLOCKED), or failing checks (UNSTABLE)
	// — the fail-closed merge verifier (#4070) uses it to name the blocker.
	MergeStateStatus string   `json:"mergeStateStatus"`
	ReviewStatus     string   `json:"reviewStatus"`
	CheckStatus      string   `json:"checkStatus"`
	Labels           []string `json:"labels"`
	IsDraft          bool     `json:"isDraft"`
	Additions        int      `json:"additions"`
	Deletions        int      `json:"deletions"`
	CreatedAt        string   `json:"createdAt"`
	// MergeCommitSHA is the OID of the squashed/merge commit on the base branch,
	// populated only once the PR is MERGED (empty otherwise). It is the
	// post-merge ground-truth breadcrumb the survival-feedback loop keys on
	// (#4133).
	MergeCommitSHA string `json:"mergeCommitSha"`
	// MergedAt is GitHub's ISO-8601 merge timestamp, populated only once the PR
	// is MERGED (empty otherwise) (#4133).
	MergedAt string `json:"mergedAt"`
}

// ReviewDecision represents PR review states.
type ReviewDecision string

const (
	ReviewApproved         ReviewDecision = "APPROVED"
	ReviewChangesRequested ReviewDecision = "CHANGES_REQUESTED"
	ReviewReviewRequired   ReviewDecision = "REVIEW_REQUIRED"
)

// StatusCounts holds per-status item counts from the project board.
type StatusCounts struct {
	Ready      int `json:"ready"`
	InProgress int `json:"inProgress"`
	InReview   int `json:"inReview"`
	Done       int `json:"done"`
	Backlog    int `json:"backlog"`
}

// EpicProgress represents aggregated progress for an epic across repos.
type EpicProgress struct {
	EpicNodeID      string        `json:"epicNodeId"`
	Number          int           `json:"number"`
	Title           string        `json:"title"`
	Repo            string        `json:"repo"`
	SubIssues       []SubIssueRef `json:"subIssues"`
	Total           int           `json:"total"`
	Closed          int           `json:"closed"`
	Open            int           `json:"open"`
	PercentComplete float64       `json:"percentComplete"` // 0 - 100
}
