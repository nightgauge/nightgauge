// Package forge_test — RunContract is the cross-forge contract harness. It
// runs an adapter-agnostic suite of assertions against a forge.ForgeClient
// implementation and produces per-method t.Run subtests so failures pin the
// adapter + method by name.
//
// The harness deliberately accepts a pre-configured ForgeClient (Option B in
// ADR-002) instead of building one itself: the caller is responsible for
// standing up the adapter-specific stub server and seeding equivalent state.
// This keeps RunContract free of adapter-specific knowledge — the same
// assertions can run against GitHub, GitLab, or any future adapter just by
// wiring it through a TestForgeContract_* entry point.
//
// The contract is split into per-domain sub-suites (Issues, Board, CI) so
// callers can opt into the subset their stub server supports. Issues is the
// minimum every adapter must support; Board and CI are opt-in via the
// IncludeBoard / IncludeCI flags on ContractFixtures because their stubs are
// significantly more involved (e.g. GitHub's Board.GetItem traverses a deep
// projectV2 GraphQL surface).
//
// Failure attribution: every t.Run name embeds both the adapter label and the
// method under test, so a failure surface like
//
//	--- FAIL: TestForgeContract_GitLab/Issues/GetIssue
//
// pinpoints the broken adapter+method without requiring the reader to grep
// line numbers.
package forge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gitlab"
)

// ContractFixtures captures the seed data each adapter caller has staged into
// its stub server. RunContract reads from the fixtures (not the live network)
// to know what to assert.
type ContractFixtures struct {
	Owner       string
	Repo        string
	IssueNumber int
	IssueTitle  string
	IssueLabels []string
	IssueState  string // canonical e.g. "OPEN" (GitHub) / "opened" (GitLab)

	// IncludeBoard signals the caller's stub server can answer the Board
	// service queries this contract makes. Skip on adapters whose Board
	// stub is too involved for a focused contract (currently: GitHub —
	// projectV2 traversal is exercised by parity_test.go instead).
	IncludeBoard bool

	// IncludeCI signals the caller's stub server can answer CI service
	// queries. Adapter-specific reasons for skipping are documented in the
	// caller (e.g. parity_ci_test.go covers richer GitHub CI parity).
	IncludeCI bool
	PRNumber  int
}

// RunContract runs the forge-agnostic assertion suite. The adapter argument is
// the human-readable label embedded in subtest names ("GitHub", "GitLab"). The
// client must already be wired to a stub server seeded with state matching f.
func RunContract(t *testing.T, adapter string, client forge.ForgeClient, f ContractFixtures) {
	t.Helper()

	t.Run(adapter+"/Issues/GetIssue", func(t *testing.T) {
		got, err := client.Issues().GetIssue(context.Background(), f.Owner, f.Repo, f.IssueNumber)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Number != f.IssueNumber {
			t.Errorf("Number = %d, want %d", got.Number, f.IssueNumber)
		}
		if got.Title != f.IssueTitle {
			t.Errorf("Title = %q, want %q", got.Title, f.IssueTitle)
		}
		if got.State != f.IssueState {
			t.Errorf("State = %q, want %q", got.State, f.IssueState)
		}
		if !labelsEqual(got.Labels, f.IssueLabels) {
			t.Errorf("Labels = %v, want %v (order-insensitive)", got.Labels, f.IssueLabels)
		}
	})

	t.Run(adapter+"/Issues/ListIssues", func(t *testing.T) {
		issues, err := client.Issues().ListIssues(context.Background(), f.Owner, f.Repo, nil)
		if err != nil {
			t.Fatalf("ListIssues: %v", err)
		}
		if len(issues) == 0 {
			t.Fatal("ListIssues returned 0 issues; expected at least one")
		}
		found := false
		for _, iss := range issues {
			if iss.Number == f.IssueNumber {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ListIssues did not include seeded issue #%d", f.IssueNumber)
		}
	})

	if f.IncludeBoard {
		t.Run(adapter+"/Board/GetItem", func(t *testing.T) {
			got, err := client.Board().GetItem(context.Background(), f.Owner, f.Repo, f.IssueNumber)
			if err != nil {
				t.Fatalf("Board.GetItem: %v", err)
			}
			if got.Number != f.IssueNumber {
				t.Errorf("Number = %d, want %d", got.Number, f.IssueNumber)
			}
			if got.Repo != f.Owner+"/"+f.Repo {
				t.Errorf("Repo = %q, want %q", got.Repo, f.Owner+"/"+f.Repo)
			}
		})
	}

	if f.IncludeCI {
		t.Run(adapter+"/CI/GetCheckStatus", func(t *testing.T) {
			got, err := client.CI().GetCheckStatus(context.Background(), f.Owner, f.Repo, f.PRNumber)
			if err != nil {
				t.Fatalf("CI.GetCheckStatus: %v", err)
			}
			if got.PRNumber != f.PRNumber {
				t.Errorf("PRNumber = %d, want %d", got.PRNumber, f.PRNumber)
			}
			switch got.State {
			case "PENDING", "SUCCESS", "FAILURE", "ERROR":
				// canonical
			default:
				t.Errorf("State = %q, want canonical SUCCESS/FAILURE/PENDING/ERROR", got.State)
			}
		})
	}
}

// labelsEqual returns true when a and b contain the same labels regardless of
// order.
func labelsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, y := range b {
		if seen[y] == 0 {
			return false
		}
		seen[y]--
	}
	return true
}

// --- GitLab contract entry point ---

// TestForgeContract_GitLab stands up a GitLab stub server backed by handcrafted
// fixtures, constructs a gitlab.ForgeAdapter pointed at it, then runs the
// adapter-agnostic RunContract suite (Issues + Board + CI). Failures surface as
// `TestForgeContract_GitLab/<service>/<method>` so the broken method is
// immediately visible in the test output.
func TestForgeContract_GitLab(t *testing.T) {
	srv := newGitlabContractStub(t)
	c := gitlab.NewClient(srv.URL, "tok")
	adapter := gitlab.NewForgeAdapter(c, gitlab.WithProject("o", "r"))
	RunContract(t, "GitLab", adapter, ContractFixtures{
		Owner:        "o",
		Repo:         "r",
		IssueNumber:  42,
		IssueTitle:   "Sample issue",
		IssueLabels:  []string{"bug", "priority:high"},
		IssueState:   "opened",
		IncludeBoard: true,
		IncludeCI:    true,
		PRNumber:     7,
	})
}

// newGitlabContractStub returns an httptest.Server emulating the minimal
// subset of the GitLab REST surface RunContract exercises. The fixtures it
// returns intentionally match the seeded ContractFixtures values in
// TestForgeContract_GitLab.
func newGitlabContractStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Edition probe: 404 → CE so the iteration / weight / health paths take
	// the milestone fallback. The contract suite does not exercise those
	// paths but the stub must answer to keep edition detection deterministic.
	mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})

	// Single-issue GET (also reused by Board.GetItem).
	mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 1042,
			"iid": 42,
			"project_id": 5,
			"title": "Sample issue",
			"description": "Body",
			"state": "opened",
			"labels": ["bug","priority:high"],
			"web_url": "https://gitlab.example.com/o/r/-/issues/42",
			"assignees": []
		}`))
	})

	// /issues/:iid/links — empty list keeps the link-enrichment step happy.
	mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42/links", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})

	// Issues list endpoint.
	mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id": 1042,
			"iid": 42,
			"project_id": 5,
			"title": "Sample issue",
			"description": "Body",
			"state": "opened",
			"labels": ["bug","priority:high"],
			"web_url": "https://gitlab.example.com/o/r/-/issues/42",
			"assignees": []
		}]`))
	})

	// MR pipeline list — drives CI.GetCheckStatus.
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/7/pipelines", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":99,"status":"success","sha":"abcdef0123456789abcdef0123456789abcdef01"}]`))
	})

	// Pipelines list (by SHA / ref) and per-pipeline jobs.
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":99,"status":"success","sha":"abcdef0123456789abcdef0123456789abcdef01"}]`))
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/99/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":501,"name":"lint","stage":"test","status":"success"}]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// --- GitHub contract entry point ---

// TestForgeContract_GitHub runs the Issues sub-suite of RunContract against a
// GitHub-adapter stub. Board and CI are intentionally excluded — those rely on
// the projectV2 GraphQL traversal and the PR commits-checkSuite chain, both of
// which are exercised in dedicated tests (parity_test.go, parity_ci_test.go,
// internal/github/board_test.go, internal/github/ci_test.go). The contract
// suite here pins the cross-forge Issues parity that the GitLab adapter must
// match.
func TestForgeContract_GitHub(t *testing.T) {
	srv := newGithubContractStub(t)
	c := github.NewClientWithURL("test-token", srv.URL)
	adapter := github.NewForgeAdapter(c, "o", 1, github.OwnerTypeOrg)
	RunContract(t, "GitHub", adapter, ContractFixtures{
		Owner:       "o",
		Repo:        "r",
		IssueNumber: 42,
		IssueTitle:  "Sample issue",
		IssueLabels: []string{"bug", "priority:high"},
		IssueState:  "OPEN",
	})
}

// newGithubContractStub serves GraphQL responses by inspecting the operation
// body. GitHub queries are all POSTed to /graphql; the body's "query" field
// names the operation. The dispatcher matches keywords to choose a response.
func newGithubContractStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		switch {
		// GetIssue uses the `issue(number:` selector (singular) inside the
		// repository block.
		case strings.Contains(body.Query, "issue(number:"):
			fmt.Fprint(w, githubGetIssueResponse())
		// ListIssues uses the `issues(` plural selector with pagination args.
		case strings.Contains(body.Query, "issues("):
			fmt.Fprint(w, githubListIssuesResponse())
		default:
			fmt.Fprint(w, `{"data":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// githubGetIssueResponse returns a fixed GraphQL response for the GetIssue
// query. Numeric ID, title, and labels match the ContractFixtures values
// passed in by TestForgeContract_GitHub.
func githubGetIssueResponse() string {
	return `{"data":{"repository":{"issue":{
		"id":"I_NODE_42",
		"number":42,
		"title":"Sample issue",
		"body":"Body",
		"state":"OPEN",
		"url":"https://github.com/o/r/issues/42",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"bug"},{"name":"priority:high"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`
}

// githubListIssuesResponse returns a single-page list containing the seeded
// issue. The pageInfo signals no more pages so the iterator terminates.
func githubListIssuesResponse() string {
	return `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[{
			"id":"I_NODE_42","number":42,"title":"Sample issue","state":"OPEN",
			"url":"https://github.com/o/r/issues/42",
			"labels":{"nodes":[{"name":"bug"},{"name":"priority:high"}]}
		}]
	}}}}`
}

// Compile-time guard: pin the forgetypes import in case future contract
// expansions add fixture types from the package.
var _ = forgetypes.Issue{}
