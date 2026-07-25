package github

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// The attention sweep's default-branch producer asks the forge which branch is
// the default rather than assuming "main", because a repo that renamed its
// trunk would otherwise be checked against a branch that does not exist —
// producing a 404 that reads as a permanent producer failure.

func TestRepoMetadata_ReportsDefaultBranch(t *testing.T) {
	response := `{"data":{"repository":{
		"nameWithOwner":"octocat/acme",
		"owner":{"login":"octocat"},
		"name":"acme",
		"defaultBranchRef":{"name":"trunk"}
	}}}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	got, err := NewRepoService(client).RepoMetadata(context.Background(), "octocat", "acme")
	if err != nil {
		t.Fatalf("RepoMetadata: %v", err)
	}
	if got.DefaultBranch != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk", got.DefaultBranch)
	}
}

func TestRepoMetadata_EmptyRepoHasNoDefaultBranch(t *testing.T) {
	// defaultBranchRef is null on a repository with no commits. Callers must
	// see "" and decline to observe, not fall back to a guess.
	response := `{"data":{"repository":{
		"nameWithOwner":"octocat/fresh",
		"owner":{"login":"octocat"},
		"name":"fresh",
		"defaultBranchRef":null
	}}}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	got, err := NewRepoService(client).RepoMetadata(context.Background(), "octocat", "fresh")
	if err != nil {
		t.Fatalf("RepoMetadata: %v", err)
	}
	if got.DefaultBranch != "" {
		t.Errorf("DefaultBranch = %q, want empty", got.DefaultBranch)
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
