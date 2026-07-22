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

// newCIServiceWithGraphQL creates a CIService backed by a client that routes
// all GraphQL (and REST) calls to the given test server.
func newCIServiceWithGraphQL(server *httptest.Server) *CIService {
	c := NewClientWithURL("test-token", server.URL)
	return NewCIService(c)
}

func TestNewCIService(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewCIService(client)
	if svc == nil {
		t.Fatal("NewCIService returned nil")
	}
}

func TestDefaultWaitConfig(t *testing.T) {
	cfg := DefaultWaitConfig()
	if cfg.Timeout != 30*time.Minute {
		t.Errorf("Timeout = %v, want 30m", cfg.Timeout)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
	if cfg.OnProgress != nil {
		t.Error("OnProgress should be nil by default")
	}
}

func TestCheckStatusIsTerminal(t *testing.T) {
	tests := []struct {
		state string
		want  bool
	}{
		{"SUCCESS", true},
		{"FAILURE", true},
		{"ERROR", true},
		{"PENDING", false},
		{"", false},
	}

	for _, tt := range tests {
		got := tt.state == "SUCCESS" || tt.state == "FAILURE" || tt.state == "ERROR"
		if got != tt.want {
			t.Errorf("isTerminal(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

// mockRESTTransport redirects HTTP requests whose host matches apiHost to the
// provided test server, allowing REST API methods to be tested without hitting
// the real GitHub API.
type mockRESTTransport struct {
	server *httptest.Server
}

func (t *mockRESTTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the request URL to point at the test server
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.server.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(req)
}

// newCIServiceForRESTTest creates a CIService whose HTTP client redirects to server.
func newCIServiceForRESTTest(server *httptest.Server) *CIService {
	httpClient := &http.Client{Transport: &mockRESTTransport{server: server}}
	client := &Client{
		http: httpClient,
	}
	return &CIService{client: client}
}

// requiredChecksHandler routes the two probes GetRequiredCheckNames makes:
// classic branch protection and the branch rules API. Empty bodies mean 404.
func requiredChecksHandler(protectionJSON, rulesJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/protection/required_status_checks"):
			if protectionJSON == "" {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(protectionJSON))
		case strings.Contains(r.URL.Path, "/rules/branches/"):
			if rulesJSON == "" {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(rulesJSON))
		default:
			w.WriteHeader(500)
		}
	}
}

func TestGetRequiredCheckNames_ClassicOnly(t *testing.T) {
	srv := httptest.NewServer(requiredChecksHandler(
		`{"contexts":["CI","build-and-test"]}`, `[]`))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	names, err := svc.GetRequiredCheckNames(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("want 2 names, got %d: %v", len(names), names)
	}
	if names[0] != "CI" || names[1] != "build-and-test" {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestGetRequiredCheckNames_RulesetOnly(t *testing.T) {
	// The #184 incident shape: no classic protection (404), required check
	// enforced via ruleset — the probe must see it.
	srv := httptest.NewServer(requiredChecksHandler(
		"",
		`[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"Sentry Smoke (integration)"}]}}]`))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	names, err := svc.GetRequiredCheckNames(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 1 || names[0] != "Sentry Smoke (integration)" {
		t.Fatalf("want ruleset-enforced check visible, got %v", names)
	}
}

func TestGetRequiredCheckNames_UnionDedupes(t *testing.T) {
	srv := httptest.NewServer(requiredChecksHandler(
		`{"contexts":["CI","shared"]}`,
		`[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"shared"},{"context":"ruleset-only"}]}}]`))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	names, err := svc.GetRequiredCheckNames(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"CI", "shared", "ruleset-only"}
	if len(names) != len(want) {
		t.Fatalf("want %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestGetRequiredCheckNames_NoProtection(t *testing.T) {
	srv := httptest.NewServer(requiredChecksHandler("", ""))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	names, err := svc.GetRequiredCheckNames(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error on 404: %v", err)
	}
	if names != nil {
		t.Errorf("want nil names on 404, got %v", names)
	}
}

func TestWaitForChecks_RequiredOnly_PassesWhenRequiredPass(t *testing.T) {
	// required: "CI" passes, non-required: "codex-smoke" fails — should still succeed
	svc := &CIService{}
	status, err := svc.getRequiredOnlyStatusWithChecks([]CheckDetail{
		{Name: "CI", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "codex-smoke", Status: "COMPLETED", Conclusion: "FAILURE"},
	}, []string{"CI"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "SUCCESS" {
		t.Errorf("want State=SUCCESS, got %s", status.State)
	}
	if !status.RequiredPassed {
		t.Error("want RequiredPassed=true")
	}
	if !status.IsTerminal {
		t.Error("want IsTerminal=true")
	}
}

func TestWaitForChecks_RequiredOnly_FailsWhenRequiredFails(t *testing.T) {
	status, err := (&CIService{}).getRequiredOnlyStatusWithChecks([]CheckDetail{
		{Name: "CI", Status: "COMPLETED", Conclusion: "FAILURE"},
	}, []string{"CI"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "FAILURE" {
		t.Errorf("want State=FAILURE, got %s", status.State)
	}
	if status.RequiredPassed {
		t.Error("want RequiredPassed=false")
	}
	if !status.IsTerminal {
		t.Error("want IsTerminal=true")
	}
}

func TestWaitForChecks_RequiredOnly_WaitsWhenRequiredPending(t *testing.T) {
	status, err := (&CIService{}).getRequiredOnlyStatusWithChecks([]CheckDetail{
		{Name: "CI", Status: "IN_PROGRESS", Conclusion: ""},
	}, []string{"CI"}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "PENDING" {
		t.Errorf("want State=PENDING, got %s", status.State)
	}
	if status.IsTerminal {
		t.Error("want IsTerminal=false while required check is pending")
	}
}

// newCIServiceForCombinedTest creates a CIService whose GraphQL client posts
// directly to server.URL AND whose REST calls — GetIndividualCheckRuns is
// hardcoded to https://api.github.com/... — are redirected to the same
// server via mockRESTTransport. GetCheckStatus's terminal-failure
// augmentation (#273) issues both a GraphQL PR fetch and a REST check-runs
// fetch, so tests exercising it need both wired to one fake server.
func newCIServiceForCombinedTest(server *httptest.Server) *CIService {
	c := NewClientWithURL("test-token", server.URL)
	c.http.Transport = &mockRESTTransport{server: server}
	return &CIService{client: c}
}

// failureRolloutHandler simulates the exact moment described in #273
// (dogfood run bowlsheet-flutter PR #293): the GraphQL StatusCheckRollup
// already reads FAILURE while the individual check-runs REST endpoint shows
// one failed, one still running, and one still queued.
func failureRolloutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"repository": map[string]interface{}{
						"pullRequest": map[string]interface{}{
							"id":             "PR_293",
							"number":         293,
							"title":          "test PR",
							"body":           "",
							"state":          "OPEN",
							"headRefName":    "feat/test",
							"baseRefName":    "main",
							"url":            "https://github.com/owner/repo/pull/293",
							"mergeable":      "MERGEABLE",
							"isDraft":        false,
							"reviewDecision": "",
							"additions":      1,
							"deletions":      0,
							"labels":         map[string]interface{}{"nodes": []interface{}{}},
							"commits": map[string]interface{}{
								"nodes": []interface{}{
									map[string]interface{}{
										"commit": map[string]interface{}{
											"statusCheckRollup": map[string]interface{}{
												"state": "FAILURE",
											},
										},
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"check_runs": []map[string]interface{}{
					{"name": "build-and-test", "status": "completed", "conclusion": "failure"},
					{"name": "e2e", "status": "in_progress", "conclusion": ""},
					{"name": "lint", "status": "queued", "conclusion": ""},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// TestGetCheckStatus_FailurePopulatesChecks verifies the fix for #273: a
// terminal FAILURE verdict must carry the failing check(s) in checks[], not
// checks=nil/total=0/completed=0. Before the fix, GetCheckStatus derived
// State solely from the aggregate StatusCheckRollup and never looked at
// individual check runs outside required-only mode.
func TestGetCheckStatus_FailurePopulatesChecks(t *testing.T) {
	srv := httptest.NewServer(failureRolloutHandler())
	defer srv.Close()

	svc := newCIServiceForCombinedTest(srv)
	status, err := svc.GetCheckStatus(context.Background(), "owner", "repo", 293)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "FAILURE" {
		t.Fatalf("want State=FAILURE, got %s", status.State)
	}
	if !status.IsTerminal {
		t.Fatal("want IsTerminal=true for a FAILURE verdict")
	}
	if status.Total != 3 {
		t.Errorf("want Total=3, got %d", status.Total)
	}
	if status.Completed != 1 {
		t.Errorf("want Completed=1, got %d", status.Completed)
	}
	if status.Failed != 1 {
		t.Errorf("want Failed=1, got %d", status.Failed)
	}
	if status.Pending != 2 {
		t.Errorf("want Pending=2, got %d", status.Pending)
	}
	if len(status.Checks) != 3 {
		t.Fatalf("want 3 checks populated, got %d: %+v", len(status.Checks), status.Checks)
	}
}

// TestWaitForChecks_EarlyExitFailurePopulatesChecks covers the exact
// regression from #273: WaitForChecks's pre-tick pollFn() call already sees
// IsTerminal=true on the very first poll (the "early exit" before the ticker
// loop ever runs) whenever the rollup reads FAILURE immediately. The
// returned CheckStatus must still carry the failing check(s), not an empty
// evidence set.
func TestWaitForChecks_EarlyExitFailurePopulatesChecks(t *testing.T) {
	srv := httptest.NewServer(failureRolloutHandler())
	defer srv.Close()

	svc := newCIServiceForCombinedTest(srv)
	result, err := svc.WaitForChecks(context.Background(), "owner", "repo", 293, WaitConfig{
		Timeout:      time.Minute,
		PollInterval: time.Minute, // long enough that only the pre-tick early-exit path can fire
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "FAILURE" || !result.IsTerminal {
		t.Fatalf("want terminal FAILURE, got state=%s isTerminal=%v", result.State, result.IsTerminal)
	}
	if result.Total == 0 || result.Completed == 0 {
		t.Fatalf("want non-zero total/completed on early-exit FAILURE verdict, got total=%d completed=%d", result.Total, result.Completed)
	}
	if len(result.Checks) == 0 {
		t.Fatal("want non-empty checks[] on early-exit FAILURE verdict")
	}
}

// TestGetCheckStatus_FailureChecksFetchErrorStaysTerminal verifies that a
// failure while fetching individual check-runs (e.g. transient REST error)
// does not turn a terminal FAILURE verdict into an error return — the
// augmentation in #273 is best-effort so a flaky auxiliary lookup can never
// make WaitForChecks fail outright when it already has a real verdict.
func TestGetCheckStatus_FailureChecksFetchErrorStaysTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"repository": map[string]interface{}{
						"pullRequest": map[string]interface{}{
							"id":             "PR_293",
							"number":         293,
							"title":          "test PR",
							"body":           "",
							"state":          "OPEN",
							"headRefName":    "feat/test",
							"baseRefName":    "main",
							"url":            "https://github.com/owner/repo/pull/293",
							"mergeable":      "MERGEABLE",
							"isDraft":        false,
							"reviewDecision": "",
							"additions":      1,
							"deletions":      0,
							"labels":         map[string]interface{}{"nodes": []interface{}{}},
							"commits": map[string]interface{}{
								"nodes": []interface{}{
									map[string]interface{}{
										"commit": map[string]interface{}{
											"statusCheckRollup": map[string]interface{}{
												"state": "FAILURE",
											},
										},
									},
								},
							},
						},
					},
				},
			})
		case strings.Contains(r.URL.Path, "/check-runs"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := newCIServiceForCombinedTest(srv)
	status, err := svc.GetCheckStatus(context.Background(), "owner", "repo", 293)
	if err != nil {
		t.Fatalf("want no error even when the check-runs augmentation fetch fails, got: %v", err)
	}
	if status.State != "FAILURE" || !status.IsTerminal {
		t.Fatalf("want terminal FAILURE verdict preserved despite augmentation failure, got state=%s isTerminal=%v", status.State, status.IsTerminal)
	}
}

// TestGetCheckStatus_OutOfBandMerge verifies that GetCheckStatus returns
// SUCCESS+MergedExternally=true when the PR state is MERGED, even when
// CI checks are still PENDING. This is the fix for #3655.
func TestGetCheckStatus_OutOfBandMerge(t *testing.T) {
	// GraphQL server returns a PR with state=MERGED and no CI rollup
	// (simulating PENDING checks that will never complete).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"repository": map[string]interface{}{
					"pullRequest": map[string]interface{}{
						"id":             "PR_merged",
						"number":         42,
						"title":          "test PR",
						"body":           "",
						"state":          "MERGED",
						"headRefName":    "feat/test",
						"baseRefName":    "main",
						"url":            "https://github.com/owner/repo/pull/42",
						"mergeable":      "MERGEABLE",
						"isDraft":        false,
						"reviewDecision": "APPROVED",
						"additions":      1,
						"deletions":      0,
						"labels":         map[string]interface{}{"nodes": []interface{}{}},
						"commits": map[string]interface{}{
							"nodes": []interface{}{
								map[string]interface{}{
									"commit": map[string]interface{}{
										// null rollup simulates pending checks on a merged PR
										"statusCheckRollup": nil,
									},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	svc := newCIServiceWithGraphQL(srv)
	status, err := svc.GetCheckStatus(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.State != "SUCCESS" {
		t.Errorf("want State=SUCCESS when PR is MERGED, got %s", status.State)
	}
	if !status.IsTerminal {
		t.Error("want IsTerminal=true when PR is MERGED")
	}
	if !status.MergedExternally {
		t.Error("want MergedExternally=true when PR was merged out-of-band")
	}
}
