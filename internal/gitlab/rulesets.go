package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// RulesetService implements forge.RulesetService against GitLab's
// protected-branches and approval-rules surfaces. The pre-merge gate flow
// matches the GitHub adapter's CheckRulesets / SatisfyRulesets contract so
// the pr-merge stage handles either backend transparently.
type RulesetService struct {
	client       *Client
	pollInterval time.Duration
}

// NewRulesetService constructs a RulesetService bound to client.
func NewRulesetService(client *Client) *RulesetService {
	return &RulesetService{client: client, pollInterval: 10 * time.Second}
}

// RulesetCheckResult is an alias for the forge-agnostic ruleset precheck
// outcome — same shape the GitHub adapter exposes.
type RulesetCheckResult = forgetypes.RulesetCheckResult

// AccessLevel mirrors the GitLab access-level entry returned by
// /protected_branches. The numeric AccessLevel is GitLab's enum
// (0=NoAccess, 30=Developer, 40=Maintainer, 60=Admin).
type AccessLevel struct {
	AccessLevel int    `json:"access_level"`
	Description string `json:"access_level_description"`
	UserID      int    `json:"user_id,omitempty"`
	GroupID     int    `json:"group_id,omitempty"`
}

// ProtectionInfo is the shape returned by GetProtection. It is concrete-only
// (not on the forge interface — see ADR-002).
type ProtectionInfo struct {
	Branch            string
	Pattern           string
	AllowForcePush    bool
	PushAccessLevels  []AccessLevel
	MergeAccessLevels []AccessLevel
	ApprovalsRequired int
}

// rawProtectedBranch decodes /api/v4/projects/:id/protected_branches/:name.
type rawProtectedBranch struct {
	Name              string        `json:"name"`
	AllowForcePush    bool          `json:"allow_force_push"`
	PushAccessLevels  []AccessLevel `json:"push_access_levels"`
	MergeAccessLevels []AccessLevel `json:"merge_access_levels"`
}

// rawApprovals decodes /api/v4/projects/:id/approvals.
type rawApprovals struct {
	ApprovalsBeforeMerge int `json:"approvals_before_merge"`
}

// rawMRApprovals decodes /api/v4/projects/:id/merge_requests/:iid/approvals.
type rawMRApprovals struct {
	ApprovalsRequired int `json:"approvals_required"`
	ApprovalsLeft     int `json:"approvals_left"`
}

// GetProtection reads the protected-branch + approvals surfaces and folds
// them into a single ProtectionInfo. 404/403 on either surface returns a
// zero-value ProtectionInfo (Branch set, everything else falsy/zero) so
// callers degrade cleanly on CE / under-scoped tokens — matches the GitHub
// adapter's graceful-fallback pattern (ADR-004).
func (s *RulesetService) GetProtection(ctx context.Context, owner, repo, branch string) (*ProtectionInfo, error) {
	out := &ProtectionInfo{Branch: branch}

	bpath := fmt.Sprintf("/projects/%s/protected_branches/%s", projectPath(owner, repo), url.PathEscape(branch))
	var pb rawProtectedBranch
	if _, err := s.client.do(ctx, "GET", s.client.buildURL(bpath, nil), nil, &pb, "get protected branch"); err != nil {
		if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrPermissionDenied) {
			return out, nil
		}
		return nil, err
	}
	out.Pattern = pb.Name
	out.AllowForcePush = pb.AllowForcePush
	out.PushAccessLevels = pb.PushAccessLevels
	out.MergeAccessLevels = pb.MergeAccessLevels

	apath := fmt.Sprintf("/projects/%s/approvals", projectPath(owner, repo))
	var approvals rawApprovals
	if _, err := s.client.do(ctx, "GET", s.client.buildURL(apath, nil), nil, &approvals, "get approvals"); err != nil {
		if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrPermissionDenied) {
			return out, nil
		}
		return nil, err
	}
	out.ApprovalsRequired = approvals.ApprovalsBeforeMerge
	return out, nil
}

// CheckRulesets evaluates the merge-time gate for an MR and surfaces any
// blocking conditions. Mirrors internal/github/rulesets.go:CheckRulesets:
//   - 404/403 path → AllowedToMerge=true with an informational Message.
//   - approvals_required > current approvals → "required_pull_request_reviews"
//     blocker (matches GitHub's vocabulary so cross-forge consumers see one
//     name).
func (s *RulesetService) CheckRulesets(ctx context.Context, owner, repo string, prNumber int) (*RulesetCheckResult, error) {
	prSvc := NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch MR for ruleset check: %w", err)
	}
	baseRef := pr.BaseRef

	prot, err := s.GetProtection(ctx, owner, repo, baseRef)
	if err != nil {
		// Treat any error from protection read as "unable to read" — the
		// gate degrades to allow rather than blocking on read failure.
		return &RulesetCheckResult{
			Blockers:       []string{},
			BaseRef:        baseRef,
			AllowedToMerge: true,
			Message:        fmt.Sprintf("NOTE: Unable to read branch protection for %s: %v. Skipping pre-check.", baseRef, err),
		}, nil
	}

	if prot.Pattern == "" && prot.ApprovalsRequired == 0 {
		// No protection configured at all.
		return &RulesetCheckResult{
			Blockers:       []string{},
			BaseRef:        baseRef,
			AllowedToMerge: true,
			Message:        "No blocking rulesets detected — safe to merge.",
		}, nil
	}

	var blockers []string
	var detected []string

	if prot.ApprovalsRequired > 0 {
		detected = append(detected, "required_pull_request_reviews")
		left, lookupErr := s.fetchMRApprovalsLeft(ctx, owner, repo, prNumber)
		if lookupErr != nil {
			// On lookup failure, surface the rule in detected but treat as
			// a blocker so the caller can flag it.
			blockers = append(blockers, "required_pull_request_reviews")
		} else if left > 0 {
			blockers = append(blockers, "required_pull_request_reviews")
		}
	}

	allowed := len(blockers) == 0
	msg := "No blocking rulesets detected — safe to merge."
	if !allowed {
		msg = fmt.Sprintf("Branch protection blocks merge on %q: required approvals not met.", baseRef)
	}
	return &RulesetCheckResult{
		Blockers:         append([]string{}, blockers...),
		DetectedRules:    detected,
		ResolvedBlockers: nil,
		BaseRef:          baseRef,
		AllowedToMerge:   allowed,
		Message:          msg,
	}, nil
}

// fetchMRApprovalsLeft reads /merge_requests/:iid/approvals and returns the
// approvals_left count.
func (s *RulesetService) fetchMRApprovalsLeft(ctx context.Context, owner, repo string, prNumber int) (int, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/approvals", projectPath(owner, repo), prNumber)
	var ap rawMRApprovals
	if _, err := s.client.do(ctx, "GET", s.client.buildURL(path, nil), nil, &ap, "get MR approvals"); err != nil {
		return 0, err
	}
	return ap.ApprovalsLeft, nil
}

// SatisfyRulesets emits informational notices for blockers it cannot
// auto-resolve. GitLab has no Copilot-equivalent auto-reviewer so the
// required-review path mirrors the GitHub adapter's
// `required_pull_request_reviews` branch (internal/github/rulesets.go:141)
// and returns an empty resolved list.
func (s *RulesetService) SatisfyRulesets(ctx context.Context, owner, repo string, prNumber int, blockers []string) ([]string, error) {
	if len(blockers) == 0 {
		return nil, nil
	}
	for _, b := range blockers {
		switch b {
		case "required_pull_request_reviews":
			fmt.Printf("Required reviewers enforced by GitLab approval rules. Add approvals before merge.\n")
		}
	}
	return nil, nil
}
