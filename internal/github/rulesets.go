package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/shurcooL/graphql"
)

// RulesetService detects and auto-satisfies GitHub branch rulesets.
type RulesetService struct {
	client       *Client
	pollInterval time.Duration
}

// NewRulesetService creates a RulesetService with a 10-second poll interval.
func NewRulesetService(client *Client) *RulesetService {
	return &RulesetService{client: client, pollInterval: 10 * time.Second}
}

// RulesetCheckResult is an alias for the forge-agnostic ruleset precheck
// outcome. The semantics documented in internal/forge/types/ruleset.go
// apply.
type RulesetCheckResult = forgetypes.RulesetCheckResult

// branchRule is the shape of one rule object from /repos/{owner}/{repo}/rules/branches/{ref}.
type branchRule struct {
	Type string `json:"type"`
	// RulesetID identifies the ruleset that contributed this rule. The rules
	// endpoint reports what is configured on the branch, NOT what binds the
	// caller — bypass is a property of the ruleset, so resolving "does this
	// actually block me?" requires going back to the ruleset by id.
	RulesetID  int64 `json:"ruleset_id"`
	Parameters *struct {
		RequiredApprovingReviewCount int `json:"required_approving_review_count"`
		RequiredStatusChecks         []struct {
			Context string `json:"context"`
		} `json:"required_status_checks"`
	} `json:"parameters,omitempty"`
}

// GitHub's `current_user_can_bypass` values on a ruleset. Both "always" and
// "pull_requests_only" clear a merge performed through a pull request, which
// is the only way this pipeline merges.
const (
	bypassNever            = "never"
	bypassAlways           = "always"
	bypassPullRequestsOnly = "pull_requests_only"
)

// candidateBlocker is a detected rule paired with the ruleset it came from, so
// bypass can be resolved per-ruleset after detection.
type candidateBlocker struct {
	name      string
	rulesetID int64
}

// canBypassRuleset asks GitHub whether the authenticated identity may bypass
// the given ruleset when merging via a pull request.
//
// GitHub answers this directly via `current_user_can_bypass`, so this does NOT
// reimplement bypass evaluation — mapping bypass_actors (OrganizationAdmin,
// RepositoryRole, Team, Integration) onto the caller by hand would be a second,
// silently-drifting copy of GitHub's own logic.
func (s *RulesetService) canBypassRuleset(ctx context.Context, owner, repo string, rulesetID int64) (bool, error) {
	path := fmt.Sprintf("/repos/%s/%s/rulesets/%d", owner, repo, rulesetID)
	baseURL := strings.TrimSuffix(s.client.graphqlURL, "/graphql")

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	if err != nil {
		return false, fmt.Errorf("create ruleset bypass request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("ruleset bypass request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("ruleset GET %s: status %d", path, resp.StatusCode)
	}

	var detail struct {
		CurrentUserCanBypass string `json:"current_user_can_bypass"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return false, fmt.Errorf("decode ruleset %d: %w", rulesetID, err)
	}

	switch detail.CurrentUserCanBypass {
	case bypassAlways, bypassPullRequestsOnly:
		return true, nil
	case bypassNever, "":
		return false, nil
	default:
		// An unrecognized value is not evidence of a bypass.
		return false, nil
	}
}

// requiredCheckContexts extracts the status-check contexts demanded by
// required_status_checks rules.
func requiredCheckContexts(rules []branchRule) []string {
	var contexts []string
	for _, rule := range rules {
		if rule.Type != "required_status_checks" || rule.Parameters == nil {
			continue
		}
		for _, check := range rule.Parameters.RequiredStatusChecks {
			if check.Context != "" {
				contexts = append(contexts, check.Context)
			}
		}
	}
	return contexts
}

// CheckRulesets fetches branch rulesets for the PR's base branch and identifies blockers.
// Handles 403/404 gracefully: returns empty blockers with an informational message.
func (s *RulesetService) CheckRulesets(ctx context.Context, owner, repo string, prNumber int) (*RulesetCheckResult, error) {
	prSvc := NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR for ruleset check: %w", err)
	}

	baseRef := pr.BaseRef
	path := fmt.Sprintf("/repos/%s/%s/rules/branches/%s", owner, repo, baseRef)
	baseURL := strings.TrimSuffix(s.client.graphqlURL, "/graphql")
	url := baseURL + path

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create ruleset request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")

	resp, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ruleset request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		msg := fmt.Sprintf("NOTE: Unable to read branch rulesets for %s (status %d; token may lack 'administration:read'). Skipping pre-check.", baseRef, resp.StatusCode)
		return &RulesetCheckResult{
			Blockers:       []string{},
			BaseRef:        baseRef,
			AllowedToMerge: true,
			Message:        msg,
		}, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ruleset GET %s: status %d", path, resp.StatusCode)
	}

	var rules []branchRule
	if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil {
		return nil, fmt.Errorf("decode rulesets: %w", err)
	}

	var candidates []candidateBlocker
	detected := []string{}
	for _, rule := range rules {
		switch rule.Type {
		case "copilot_code_review":
			candidates = append(candidates, candidateBlocker{"copilot_code_review", rule.RulesetID})
			detected = append(detected, "copilot_code_review")
		case "pull_request":
			if rule.Parameters != nil && rule.Parameters.RequiredApprovingReviewCount > 0 {
				candidates = append(candidates, candidateBlocker{"required_pull_request_reviews", rule.RulesetID})
				detected = append(detected, "required_pull_request_reviews")
			}
		case "required_status_checks":
			detected = append(detected, "required_status_checks")
		}
	}

	// A rule the merging identity can bypass is DETECTED but not BINDING. The
	// rules endpoint reports branch configuration, not the caller's authority
	// over it, so treating its output as the blocker list asserts something it
	// never claimed. Callers promote a blocker to a non-retryable terminal
	// failure, so a rule that reads as blocking when the merge would actually
	// succeed does not merely mis-report — it kills the run and spends the
	// issue's lifetime budget on a merge nobody was preventing.
	//
	// Bypass is per-ruleset, so resolve once per id rather than per rule.
	var blockers, bypassed []string
	var bypassErrs []string
	bypassCache := make(map[int64]bool, len(candidates))
	for _, c := range candidates {
		canBypass, seen := bypassCache[c.rulesetID]
		if !seen {
			var err error
			canBypass, err = s.canBypassRuleset(ctx, owner, repo, c.rulesetID)
			if err != nil {
				// Conservative: an unreadable ruleset is not proof of a bypass,
				// so the rule keeps blocking. Surfaced in the message rather
				// than swallowed — the same credentials just read the rules
				// endpoint, so failing here is anomalous and worth seeing.
				canBypass = false
				bypassErrs = append(bypassErrs, fmt.Sprintf("ruleset %d: %v", c.rulesetID, err))
			}
			bypassCache[c.rulesetID] = canBypass
		}
		if canBypass {
			bypassed = append(bypassed, c.name)
			continue
		}
		blockers = append(blockers, c.name)
	}

	// Ruleset-enforced required checks are not blockers (a green CI run
	// satisfies them) but must be visible: callers wait on them and the
	// merge precheck cross-references them against workflow config (#184).
	requiredChecks := requiredCheckContexts(rules)

	allowed := len(blockers) == 0
	msg := "No blocking rulesets detected — safe to merge."
	if !allowed {
		msg = fmt.Sprintf("Branch ruleset blocks merge on %q: %s", baseRef, strings.Join(blockers, ", "))
	} else if len(requiredChecks) > 0 {
		msg = fmt.Sprintf("No blocking rulesets detected — merge requires status checks: %s", strings.Join(requiredChecks, ", "))
	}
	if len(bypassed) > 0 {
		msg += fmt.Sprintf(" Bypassed by the merging identity: %s.", strings.Join(bypassed, ", "))
	}
	if len(bypassErrs) > 0 {
		msg += fmt.Sprintf(" NOTE: bypass undetermined for %s — treated as blocking.", strings.Join(bypassErrs, "; "))
	}

	return &RulesetCheckResult{
		Blockers:       append([]string{}, blockers...),
		DetectedRules:  detected,
		BypassedRules:  bypassed,
		RequiredChecks: requiredChecks,
		BaseRef:        baseRef,
		AllowedToMerge: allowed,
		Message:        msg,
	}, nil
}

// SatisfyRulesets attempts to resolve detected blockers and returns the subset
// it successfully resolved. Caller is responsible for subtracting the resolved
// list from any "remaining blockers" view it presents.
//
// For copilot_code_review: requests Copilot review and polls up to ctx
// deadline; resolution = Copilot has reviewed.
// For required_pull_request_reviews: emits an informational note and does NOT
// add to the resolved list — human reviewer action is required.
func (s *RulesetService) SatisfyRulesets(ctx context.Context, owner, repo string, prNumber int, blockers []string) ([]string, error) {
	if len(blockers) == 0 {
		return nil, nil
	}
	prSvc := NewPRService(s.client)
	pr, err := prSvc.GetPR(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR for auto-satisfy: %w", err)
	}

	var resolved []string
	for _, blocker := range blockers {
		switch blocker {
		case "copilot_code_review":
			if err := s.requestCopilotReview(ctx, pr.NodeID); err != nil {
				return resolved, fmt.Errorf("request Copilot review: %w", err)
			}
			if err := s.pollForCopilotReview(ctx, owner, repo, prNumber); err != nil {
				return resolved, err
			}
			resolved = append(resolved, "copilot_code_review")
		case "required_pull_request_reviews":
			fmt.Printf("Required reviewers enforced by branch ruleset. Ensure required reviewers have approved before merge proceeds.\n")
		}
	}
	return resolved, nil
}

// requestCopilotReview adds "Copilot" as a review requester on the PR (idempotent).
func (s *RulesetService) requestCopilotReview(ctx context.Context, prNodeID string) error {
	var m requestReviewsMutation
	input := map[string]interface{}{
		"input": requestReviewsInput{
			PullRequestID: graphql.ID(prNodeID),
			UserLogins:    []graphql.String{"Copilot"},
		},
	}
	if err := s.client.mutate(ctx, &m, input); err != nil {
		return err
	}
	return nil
}

// pollForCopilotReview polls until Copilot leaves a review or ctx expires.
func (s *RulesetService) pollForCopilotReview(ctx context.Context, owner, repo string, prNumber int) error {
	for {
		reviewed, err := s.hasCopilotReviewed(ctx, owner, repo, prNumber)
		if err != nil {
			return fmt.Errorf("poll for Copilot review: %w", err)
		}
		if reviewed {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Copilot review on PR #%d", prNumber)
		case <-time.After(s.pollInterval):
		}
	}
}

// hasCopilotReviewed queries whether Copilot has submitted a review on the PR.
func (s *RulesetService) hasCopilotReviewed(ctx context.Context, owner, repo string, prNumber int) (bool, error) {
	graphQLNumber, err := checkedGraphQLInt("pull request number", prNumber)
	if err != nil {
		return false, err
	}
	var q prReviewsQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphQLNumber,
	}
	if err := s.client.query(ctx, &q, vars); err != nil {
		return false, err
	}
	for _, review := range q.Repository.PullRequest.Reviews.Nodes {
		if strings.EqualFold(string(review.Author.Login), "copilot") {
			return true, nil
		}
	}
	return false, nil
}

// --- GraphQL types for RulesetService ---

type requestReviewsInput struct {
	PullRequestID graphql.ID       `json:"pullRequestId"`
	UserLogins    []graphql.String `json:"userLogins"`
}

type requestReviewsMutation struct {
	RequestReviews struct {
		ClientMutationID *graphql.String
	} `graphql:"requestReviews(input: $input)"`
}

type prReviewsQuery struct {
	Repository struct {
		PullRequest struct {
			Reviews struct {
				Nodes []struct {
					Author struct {
						Login graphql.String
					}
				}
			} `graphql:"reviews(first: 10)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}
