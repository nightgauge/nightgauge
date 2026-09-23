package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// The attention sweep's default-branch producer asks the forge which branch is
// the default rather than assuming "main", because a repo that renamed its
// trunk would otherwise be checked against a branch that does not exist —
// producing a 404 that reads as a permanent producer failure.

// repoRESTServer answers GET /repos/{o}/{r} with default_branch=branch and
// GET /repos/{o}/{r}/branches/{branch} with 200 when branchExists, else 404 —
// the two answers GitHub gives for a repository with and without commits.
// Every request is recorded as "METHOD path" with its If-None-Match.
func repoRESTServer(t *testing.T, owner, name, branch string, branchExists bool) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.Method+" "+r.URL.Path+" inm="+r.Header.Get("If-None-Match"))
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/" + owner + "/" + name:
			if r.Header.Get("If-None-Match") == `"repo-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"repo-v1"`)
			fmt.Fprintf(w, `{"full_name":%q,"name":%q,"owner":{"login":%q},"default_branch":%q,"size":0}`, owner+"/"+name, name, owner, branch)
		case "/repos/" + owner + "/" + name + "/branches/" + branch:
			if !branchExists {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Branch not found"}`)
				return
			}
			if r.Header.Get("If-None-Match") == `"br-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"br-v1"`)
			fmt.Fprintf(w, `{"name":%q}`, branch)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClientWithURL("test-token", srv.URL), seen
}

func TestRepoMetadata_ReportsDefaultBranch(t *testing.T) {
	client, seen := repoRESTServer(t, "octocat", "acme", "trunk", true)
	got, err := NewRepoService(client).RepoMetadata(context.Background(), "octocat", "acme")
	if err != nil {
		t.Fatalf("RepoMetadata: %v", err)
	}
	if got.DefaultBranch != "trunk" || got.NameWithOwner != "octocat/acme" || got.Owner != "octocat" || got.Name != "acme" {
		t.Errorf("RepoMetadata = %+v", got)
	}
	if len(*seen) != 2 {
		t.Fatalf("requests = %v, want the repo and the branch", *seen)
	}
}

func TestRepoMetadata_EmptyRepoHasNoDefaultBranch(t *testing.T) {
	// REST names a default_branch for a repository with no commits; the
	// branch itself 404s. Callers must see "" and decline to observe, not
	// fall back to the name REST reported.
	client, _ := repoRESTServer(t, "octocat", "fresh", "main", false)
	got, err := NewRepoService(client).RepoMetadata(context.Background(), "octocat", "fresh")
	if err != nil {
		t.Fatalf("RepoMetadata: %v", err)
	}
	if got.DefaultBranch != "" {
		t.Errorf("DefaultBranch = %q, want empty", got.DefaultBranch)
	}
	if got.NameWithOwner != "octocat/fresh" {
		t.Errorf("NameWithOwner = %q", got.NameWithOwner)
	}
}

func TestRepoMetadata_RepeatIsConditional(t *testing.T) {
	// The sweep asks every pass. The second ask must revalidate both reads
	// with their stored ETags, so an unchanged repository answers 304 twice.
	client, seen := repoRESTServer(t, "octocat", "acme", "main", true)
	svc := NewRepoService(client)
	for i := 0; i < 2; i++ {
		got, err := svc.RepoMetadata(context.Background(), "octocat", "acme")
		if err != nil || got.DefaultBranch != "main" {
			t.Fatalf("RepoMetadata #%d = %+v, %v", i+1, got, err)
		}
	}
	want := []string{
		"GET /repos/octocat/acme inm=",
		"GET /repos/octocat/acme/branches/main inm=",
		`GET /repos/octocat/acme inm="repo-v1"`,
		`GET /repos/octocat/acme/branches/main inm="br-v1"`,
	}
	if strings.Join(*seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests =\n%s\nwant\n%s", strings.Join(*seen, "\n"), strings.Join(want, "\n"))
	}
}

// ListPRs carries the mergeability triple so a caller that needs to know which
// open PRs are blocked pays for one list query, not one list query plus a GetPR
// per PR on every sweep.
func TestListPRs_CarriesMergeabilityTriple(t *testing.T) {
	response := `{"data":{"repository":{"pullRequests":{"nodes":[{
		"id":"PR_1",
		"number":42,
		"title":"feat: thing",
		"state":"OPEN",
		"headRefName":"feat/thing",
		"baseRefName":"main",
		"url":"https://github.com/octocat/acme/pull/42",
		"isDraft":false,
		"createdAt":"2026-07-24T10:00:00Z",
		"mergeable":"MERGEABLE",
		"mergeStateStatus":"BLOCKED",
		"reviewDecision":"REVIEW_REQUIRED",
		"labels":{"nodes":[{"name":"bug"}]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}
	}]}}}}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	prs, err := NewPRService(client).ListPRs(context.Background(), "octocat", "acme", "OPEN", "")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("prs = %d, want 1", len(prs))
	}
	pr := prs[0]
	if pr.MergeStateStatus != "BLOCKED" {
		t.Errorf("MergeStateStatus = %q, want BLOCKED", pr.MergeStateStatus)
	}
	if pr.ReviewStatus != string(types.ReviewReviewRequired) {
		t.Errorf("ReviewStatus = %q, want %q", pr.ReviewStatus, types.ReviewReviewRequired)
	}
	if pr.CheckStatus != "SUCCESS" {
		t.Errorf("CheckStatus = %q, want SUCCESS", pr.CheckStatus)
	}
	if pr.Mergeable != "MERGEABLE" {
		t.Errorf("Mergeable = %q, want MERGEABLE", pr.Mergeable)
	}
	// The identity fields both list paths already carried must survive the
	// refactor onto the shared node type.
	if pr.Number != 42 || pr.Title != "feat: thing" || pr.URL == "" || pr.CreatedAt == "" {
		t.Errorf("identity fields lost: %+v", pr)
	}
	if len(pr.Labels) != 1 || pr.Labels[0] != "bug" {
		t.Errorf("Labels = %v, want [bug]", pr.Labels)
	}
	if pr.Repo != "octocat/acme" {
		t.Errorf("Repo = %q, want octocat/acme", pr.Repo)
	}
}

func TestListPRs_ByHeadRefCarriesTheSameFields(t *testing.T) {
	response := `{"data":{"repository":{"pullRequests":{"nodes":[{
		"id":"PR_1",
		"number":7,
		"state":"OPEN",
		"headRefName":"feat/thing",
		"mergeStateStatus":"DIRTY",
		"reviewDecision":"APPROVED",
		"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}
	}]}}}}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	prs, err := NewPRService(client).ListPRs(context.Background(), "octocat", "acme", "OPEN", "feat/thing")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("prs = %d, want 1", len(prs))
	}
	// Both list paths share one mapper precisely so this cannot drift.
	if prs[0].MergeStateStatus != "DIRTY" || prs[0].CheckStatus != "SUCCESS" {
		t.Errorf("head-ref path dropped merge state: %+v", prs[0])
	}
}

func TestListPRs_NoRollupLeavesCheckStatusEmpty(t *testing.T) {
	// A repo with no CI has no rollup. Empty must not be mistaken for green:
	// the human-gate producer keys off SUCCESS exactly, so an empty rollup
	// leaves every PR uncarded rather than carding all of them.
	response := `{"data":{"repository":{"pullRequests":{"nodes":[{
		"id":"PR_1","number":3,"state":"OPEN","mergeStateStatus":"BLOCKED",
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}]}}}}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	prs, err := NewPRService(client).ListPRs(context.Background(), "octocat", "acme", "OPEN", "")
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if prs[0].CheckStatus != "" {
		t.Errorf("CheckStatus = %q, want empty", prs[0].CheckStatus)
	}
}
