package forgetypes

import pkgtypes "github.com/nightgauge/nightgauge/pkg/types"

// PullRequest is a forge-agnostic pull/merge-request representation.
// Aliased from pkg/types.PullRequest.
type PullRequest = pkgtypes.PullRequest

// ReviewDecision represents PR review states.
type ReviewDecision = pkgtypes.ReviewDecision

// EpicPRResult holds the result of creating or merging an epic PR. Returned
// by PRService.CreateEpicPR; Action is one of "created", "merged",
// "already_exists", "already_merged".
type EpicPRResult struct {
	Action   string `json:"action"`
	PRNumber int    `json:"prNumber"`
	PRURL    string `json:"prUrl"`
	PRNodeID string `json:"prNodeId"`
}
