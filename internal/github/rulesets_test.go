package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newRulesetServiceForTest builds a RulesetService pointing at a mock server with
// near-zero poll interval so tests don't slow down.
func newRulesetServiceForTest(client *Client) *RulesetService {
	svc := NewRulesetService(client)
	svc.pollInterval = time.Millisecond
	return svc
}

// rulesetRESTHandler returns an http.HandlerFunc that serves the provided JSON body
// with the given status code for any request.
func rulesetRESTHandler(statusCode int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}
}

// newRulesetClientForRESTTest creates a Client whose HTTP transport redirects to srv.
func newRulesetClientForRESTTest(srv *httptest.Server) *Client {
	return &Client{
		http: &http.Client{Transport: &mockRESTTransport{server: srv}},
	}
}

// --- Constructor ---

func TestNewRulesetService(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewRulesetService(client)
	if svc == nil {
		t.Fatal("NewRulesetService returned nil")
	}
	if svc.client != client {
		t.Error("RulesetService.client is not the provided client")
	}
	if svc.pollInterval != 10*time.Second {
		t.Errorf("pollInterval = %v, want 10s", svc.pollInterval)
	}
}

// --- CheckRulesets ---

func TestCheckRulesets_NoBlockers(t *testing.T) {
	// GraphQL: return PR with base ref "main"
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_1",
		"number":1,
		"title":"No rules PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/no-rules",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/1",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	// After the GraphQL call, mock server returns the REST ruleset response (empty array).
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// GraphQL PR fetch
			_, _ = w.Write([]byte(prResp))
		} else {
			// REST rules/branches response — empty array
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatalf("CheckRulesets returned unexpected error: %v", err)
	}
	if len(result.Blockers) != 0 {
		t.Errorf("Blockers = %v, want empty", result.Blockers)
	}
	if !result.AllowedToMerge {
		t.Error("AllowedToMerge should be true when no blockers")
	}
	if result.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want main", result.BaseRef)
	}
}

func TestCheckRulesets_CopilotCodeReview(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_2",
		"number":2,
		"title":"Copilot PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/copilot",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/2",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	rulesResp, _ := json.Marshal([]branchRule{
		{Type: "copilot_code_review"},
	})

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(prResp))
		} else {
			_, _ = w.Write(rulesResp)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 2)
	if err != nil {
		t.Fatalf("CheckRulesets returned unexpected error: %v", err)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != "copilot_code_review" {
		t.Errorf("Blockers = %v, want [copilot_code_review]", result.Blockers)
	}
	if len(result.DetectedRules) != 1 || result.DetectedRules[0] != "copilot_code_review" {
		t.Errorf("DetectedRules = %v, want [copilot_code_review]", result.DetectedRules)
	}
	if result.AllowedToMerge {
		t.Error("AllowedToMerge should be false when blockers present")
	}
}

func TestCheckRulesets_RequiredReviews(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_3",
		"number":3,
		"title":"Required reviews PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/reviews",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/3",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	rulesResp := []byte(`[{"type":"pull_request","parameters":{"required_approving_review_count":1}}]`)

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(prResp))
		} else {
			_, _ = w.Write(rulesResp)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 3)
	if err != nil {
		t.Fatalf("CheckRulesets returned unexpected error: %v", err)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != "required_pull_request_reviews" {
		t.Errorf("Blockers = %v, want [required_pull_request_reviews]", result.Blockers)
	}
}

func TestCheckRulesets_RequiredReviews_ZeroCount_NoBlocker(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_4",
		"number":4,
		"title":"Zero reviews PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/zero",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/4",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	// pull_request rule with count=0 should NOT be a blocker
	rulesResp := []byte(`[{"type":"pull_request","parameters":{"required_approving_review_count":0}}]`)

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(prResp))
		} else {
			_, _ = w.Write(rulesResp)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 4)
	if err != nil {
		t.Fatalf("CheckRulesets returned unexpected error: %v", err)
	}
	if len(result.Blockers) != 0 {
		t.Errorf("Blockers = %v, want empty for zero required reviews", result.Blockers)
	}
}

func TestCheckRulesets_RequiredStatusChecks(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_7",
		"number":7,
		"title":"Ruleset required checks PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/required-checks",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/7",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	// The #184 incident shape: a required_status_checks rule that the old
	// parser silently dropped.
	rulesResp := []byte(`[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"Sentry Smoke (integration)"},{"context":"CI"}]}}]`)

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(prResp))
		} else {
			_, _ = w.Write(rulesResp)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("CheckRulesets returned unexpected error: %v", err)
	}
	if len(result.RequiredChecks) != 2 ||
		result.RequiredChecks[0] != "Sentry Smoke (integration)" ||
		result.RequiredChecks[1] != "CI" {
		t.Errorf("RequiredChecks = %v, want the two ruleset contexts", result.RequiredChecks)
	}
	if len(result.DetectedRules) != 1 || result.DetectedRules[0] != "required_status_checks" {
		t.Errorf("DetectedRules = %v, want [required_status_checks]", result.DetectedRules)
	}
	// Required checks are CI-satisfiable — not blockers.
	if len(result.Blockers) != 0 {
		t.Errorf("Blockers = %v, want empty (required checks are not blockers)", result.Blockers)
	}
	if !result.AllowedToMerge {
		t.Error("AllowedToMerge should stay true — required checks gate via CI, not merge prohibition")
	}
}

func TestCheckRulesets_PermissionError(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_5",
		"number":5,
		"title":"Forbidden PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/forbidden",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/5",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(prResp))
		} else {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Must have admin rights"}`))
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 5)
	if err != nil {
		t.Fatalf("CheckRulesets should not error on 403, got: %v", err)
	}
	if len(result.Blockers) != 0 {
		t.Errorf("Blockers = %v, want empty on permission error", result.Blockers)
	}
	if !result.AllowedToMerge {
		t.Error("AllowedToMerge should be true when permission denied (graceful skip)")
	}
}

func TestCheckRulesets_NotFoundError(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_6",
		"number":6,
		"title":"No rulesets PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/notfound",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/6",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			_, _ = w.Write([]byte(prResp))
		} else {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := newRulesetServiceForTest(client)

	result, err := svc.CheckRulesets(context.Background(), "o", "r", 6)
	if err != nil {
		t.Fatalf("CheckRulesets should not error on 404, got: %v", err)
	}
	if len(result.Blockers) != 0 {
		t.Errorf("Blockers = %v, want empty on 404", result.Blockers)
	}
}

func TestCheckRulesets_PRFetchError(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{"errors":[{"message":"PR not found"}]}`)
	defer cleanup()

	svc := newRulesetServiceForTest(client)
	_, err := svc.CheckRulesets(context.Background(), "o", "r", 9999)
	if err == nil {
		t.Fatal("CheckRulesets should return error when PR fetch fails")
	}
}

// --- SatisfyRulesets ---

func TestSatisfyRulesets_CopilotReview_ImmediateSuccess(t *testing.T) {
	// GetPR (for SatisfyRulesets), then requestReviewsMutation, then prReviewsQuery (Copilot already reviewed)
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_10",
		"number":10,
		"title":"Copilot PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/copilot",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/10",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	mutResp := `{"data":{"requestReviews":{"clientMutationId":null}}}`

	// Poll response: Copilot has already reviewed
	reviewResp := `{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[
		{"author":{"login":"Copilot"}}
	]}}}}}`

	client, cleanup := mockGraphQLServer(t, prResp, mutResp, reviewResp)
	defer cleanup()

	svc := newRulesetServiceForTest(client)
	resolved, err := svc.SatisfyRulesets(context.Background(), "o", "r", 10, []string{"copilot_code_review"})
	if err != nil {
		t.Errorf("SatisfyRulesets returned unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "copilot_code_review" {
		t.Errorf("resolved = %v, want [copilot_code_review]", resolved)
	}
}

func TestSatisfyRulesets_Timeout(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_11",
		"number":11,
		"title":"Timeout PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/timeout",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/11",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	mutResp := `{"data":{"requestReviews":{"clientMutationId":null}}}`

	// Copilot never reviews — return empty reviews every time
	pollResp := `{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[]}}}}}`

	client, cleanup := mockGraphQLServer(t, prResp, mutResp, pollResp)
	defer cleanup()

	svc := newRulesetServiceForTest(client)
	// Use a context that times out immediately after one poll cycle
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	resolved, err := svc.SatisfyRulesets(ctx, "o", "r", 11, []string{"copilot_code_review"})
	if err == nil {
		t.Fatal("SatisfyRulesets should return error on timeout")
	}
	if len(resolved) != 0 {
		t.Errorf("resolved = %v, want empty on timeout", resolved)
	}
}

func TestSatisfyRulesets_RequiredReviewsInfo(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_12",
		"number":12,
		"title":"Required PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/required",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/12",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, prResp)
	defer cleanup()

	svc := newRulesetServiceForTest(client)
	// required_pull_request_reviews should be informational only — no error,
	// and it must NOT appear in resolved (human reviewer action required).
	resolved, err := svc.SatisfyRulesets(context.Background(), "o", "r", 12, []string{"required_pull_request_reviews"})
	if err != nil {
		t.Errorf("SatisfyRulesets should not error for required_pull_request_reviews, got: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("required_pull_request_reviews must not be auto-resolved; got resolved = %v", resolved)
	}
}

func TestSatisfyRulesets_EmptyBlockers(t *testing.T) {
	prResp := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_13",
		"number":13,
		"title":"No blockers PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/none",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/13",
		"mergeable":"MERGEABLE",
		"isDraft":false,
		"reviewDecision":"",
		"additions":0,"deletions":0,
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, prResp)
	defer cleanup()

	svc := newRulesetServiceForTest(client)
	resolved, err := svc.SatisfyRulesets(context.Background(), "o", "r", 13, []string{})
	if err != nil {
		t.Errorf("SatisfyRulesets with empty blockers should not error, got: %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("resolved = %v, want empty for empty blockers input", resolved)
	}
}

// --- Bypass-aware blocker classification ---

// rulesetPRResponse is a minimal OPEN PR on main, reused by the bypass tests.
const rulesetPRResponse = `{"data":{"repository":{"pullRequest":{
	"id":"PR_NODE_BYPASS",
	"number":9,
	"title":"Bypass PR",
	"body":"",
	"state":"OPEN",
	"headRefName":"feat/bypass",
	"baseRefName":"main",
	"url":"https://github.com/o/r/pull/9",
	"mergeable":"MERGEABLE",
	"isDraft":false,
	"reviewDecision":"",
	"additions":0,"deletions":0,
	"labels":{"nodes":[]},
	"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
}}}}`

// bypassTestServer dispatches by path so the ruleset-detail lookup is
// distinguishable from the branch-rules lookup. rulesetStatus/rulesetBody
// control the /rulesets/{id} response.
func bypassTestServer(t *testing.T, rules []branchRule, rulesetStatus int, rulesetBody string) *httptest.Server {
	t.Helper()
	rulesResp, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("marshal rules: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/rulesets/"):
			w.WriteHeader(rulesetStatus)
			_, _ = w.Write([]byte(rulesetBody))
		case strings.Contains(r.URL.Path, "/rules/branches/"):
			_, _ = w.Write(rulesResp)
		default:
			_, _ = w.Write([]byte(rulesetPRResponse))
		}
	}))
}

// TestCheckRulesets_BypassableRuleIsNotABlocker is the regression for the
// defect that killed a real run: the rules endpoint reports what is configured
// on the branch, not what binds the caller. A required-review rule on a ruleset
// the merging identity can bypass was reported as a hard blocker, and the
// orchestrator promotes a hard blocker to a NON-RETRYABLE terminal failure —
// so the run died, and spent the issue's lifetime budget, over a merge that
// would have succeeded.
func TestCheckRulesets_BypassableRuleIsNotABlocker(t *testing.T) {
	srv := bypassTestServer(t,
		[]branchRule{{
			Type:      "pull_request",
			RulesetID: 4242,
			Parameters: &struct {
				RequiredApprovingReviewCount int `json:"required_approving_review_count"`
				RequiredStatusChecks         []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			}{RequiredApprovingReviewCount: 1},
		}},
		http.StatusOK,
		`{"name":"main-branch-protection","current_user_can_bypass":"pull_requests_only"}`,
	)
	defer srv.Close()

	svc := newRulesetServiceForTest(NewClientWithURL("test-token", srv.URL))
	result, err := svc.CheckRulesets(context.Background(), "o", "r", 9)
	if err != nil {
		t.Fatalf("CheckRulesets: %v", err)
	}

	if len(result.Blockers) != 0 {
		t.Errorf("Blockers = %v, want empty — the identity can bypass this ruleset", result.Blockers)
	}
	if !result.AllowedToMerge {
		t.Error("AllowedToMerge = false, want true — a bypassable rule must not read as a hard block")
	}
	if len(result.BypassedRules) != 1 || result.BypassedRules[0] != "required_pull_request_reviews" {
		t.Errorf("BypassedRules = %v, want [required_pull_request_reviews]", result.BypassedRules)
	}
	// Detection must still report it: "not binding on me" is not "not present".
	if len(result.DetectedRules) != 1 || result.DetectedRules[0] != "required_pull_request_reviews" {
		t.Errorf("DetectedRules = %v, want the rule still reported as detected", result.DetectedRules)
	}
}

// TestCheckRulesets_NonBypassableRuleStillBlocks pins the other direction: when
// GitHub says the caller can never bypass, the rule must keep blocking. A fix
// that made everything bypassable would trade a false block for a false green.
func TestCheckRulesets_NonBypassableRuleStillBlocks(t *testing.T) {
	srv := bypassTestServer(t,
		[]branchRule{{Type: "copilot_code_review", RulesetID: 77}},
		http.StatusOK,
		`{"name":"strict","current_user_can_bypass":"never"}`,
	)
	defer srv.Close()

	svc := newRulesetServiceForTest(NewClientWithURL("test-token", srv.URL))
	result, err := svc.CheckRulesets(context.Background(), "o", "r", 9)
	if err != nil {
		t.Fatalf("CheckRulesets: %v", err)
	}

	if len(result.Blockers) != 1 || result.Blockers[0] != "copilot_code_review" {
		t.Errorf("Blockers = %v, want [copilot_code_review]", result.Blockers)
	}
	if result.AllowedToMerge {
		t.Error("AllowedToMerge = true, want false — caller can never bypass this ruleset")
	}
	if len(result.BypassedRules) != 0 {
		t.Errorf("BypassedRules = %v, want empty", result.BypassedRules)
	}
}

// TestCheckRulesets_UnreadableRulesetKeepsBlocking asserts the conservative
// direction on error: an unreadable ruleset is not evidence of a bypass, so the
// rule keeps blocking — and the reason is surfaced in the message rather than
// swallowed, since the same credentials just read the rules endpoint.
func TestCheckRulesets_UnreadableRulesetKeepsBlocking(t *testing.T) {
	srv := bypassTestServer(t,
		[]branchRule{{Type: "copilot_code_review", RulesetID: 5}},
		http.StatusForbidden,
		`{"message":"Forbidden"}`,
	)
	defer srv.Close()

	svc := newRulesetServiceForTest(NewClientWithURL("test-token", srv.URL))
	result, err := svc.CheckRulesets(context.Background(), "o", "r", 9)
	if err != nil {
		t.Fatalf("CheckRulesets should not hard-fail when a ruleset is unreadable: %v", err)
	}

	if result.AllowedToMerge {
		t.Error("AllowedToMerge = true, want false — an unreadable ruleset must not be assumed bypassable")
	}
	if !strings.Contains(result.Message, "bypass undetermined") {
		t.Errorf("Message = %q, want it to surface that bypass could not be determined", result.Message)
	}
}

// TestCheckRulesets_BypassResolvedOncePerRuleset guards the N+1: two rules from
// one ruleset must cost one ruleset lookup, not two.
func TestCheckRulesets_BypassResolvedOncePerRuleset(t *testing.T) {
	rules := []branchRule{
		{Type: "copilot_code_review", RulesetID: 31},
		{Type: "pull_request", RulesetID: 31, Parameters: &struct {
			RequiredApprovingReviewCount int `json:"required_approving_review_count"`
			RequiredStatusChecks         []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		}{RequiredApprovingReviewCount: 2}},
	}
	rulesResp, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("marshal rules: %v", err)
	}

	var rulesetLookups int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/rulesets/"):
			rulesetLookups++
			_, _ = w.Write([]byte(`{"current_user_can_bypass":"always"}`))
		case strings.Contains(r.URL.Path, "/rules/branches/"):
			_, _ = w.Write(rulesResp)
		default:
			_, _ = w.Write([]byte(rulesetPRResponse))
		}
	}))
	defer srv.Close()

	svc := newRulesetServiceForTest(NewClientWithURL("test-token", srv.URL))
	result, err := svc.CheckRulesets(context.Background(), "o", "r", 9)
	if err != nil {
		t.Fatalf("CheckRulesets: %v", err)
	}

	if rulesetLookups != 1 {
		t.Errorf("ruleset lookups = %d, want 1 — bypass is per-ruleset and must be cached", rulesetLookups)
	}
	if !result.AllowedToMerge || len(result.Blockers) != 0 {
		t.Errorf("AllowedToMerge=%v Blockers=%v, want merge allowed with no blockers", result.AllowedToMerge, result.Blockers)
	}
	if len(result.BypassedRules) != 2 {
		t.Errorf("BypassedRules = %v, want both rules reported as bypassed", result.BypassedRules)
	}
}
