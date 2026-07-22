package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// UpdatePROptions describes a partial update to an existing pull / merge
// request. All fields are pointers so callers can distinguish "do not change"
// (nil) from "set to empty" (non-nil pointer to zero value). Forge-specific
// fields (Squash, AllowForcePush, ApprovalsBeforeMerge) are honoured by
// adapters that support them; adapters that don't surface them as a
// non-fatal ErrUnsupportedOnEdition warning where applicable.
type UpdatePROptions struct {
	Title                *string
	Body                 *string
	Draft                *bool
	TargetBranch         *string
	Squash               *bool
	AllowForcePush       *bool
	ApprovalsBeforeMerge *int
	Labels               *[]string
}

// PRService is the forge-agnostic surface for pull/merge-request operations.
type PRService interface {
	GetPR(ctx context.Context, owner, repo string, number int) (*forgetypes.PullRequest, error)
	ListPRs(ctx context.Context, owner, repo string, state string, headRef string) ([]forgetypes.PullRequest, error)
	IteratePRs(ctx context.Context, owner, repo, state, headRef string) Iterator[forgetypes.PullRequest]

	CreatePR(ctx context.Context, repoID, title, body, headRef, baseRef string) (*forgetypes.PullRequest, error)
	UpdatePR(ctx context.Context, prID string, opts UpdatePROptions) (*forgetypes.PullRequest, error)
	ClosePR(ctx context.Context, prID string) error
	MergePR(ctx context.Context, prID string) error
	MergePRWithStrategy(ctx context.Context, prID string, strategy string) (string, error)
	DeleteBranch(ctx context.Context, owner, repo, branch string) error

	// Epic PR helpers — composite operations that orchestrate a multi-PR
	// merge for an epic branch.
	CreateEpicPR(ctx context.Context, owner, repo string, epicNumber int, epicTitle, epicBranch, baseBranch string) (*forgetypes.EpicPRResult, error)
	MergeEpicPR(ctx context.Context, owner, repo string, prNodeID, epicBranch string) error
}
