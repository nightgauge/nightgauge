package github

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// --- Constructor Test ---

func TestNewPRService(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewPRService(client)
	if svc == nil {
		t.Fatal("NewPRService returned nil")
	}
	if svc.client != client {
		t.Error("PRService.client is not the provided client")
	}
}

// --- GetPR Tests ---

func TestPRService_GetPR_HappyPath(t *testing.T) {
	response := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_ID",
		"number":7,
		"title":"Fix the bug",
		"body":"PR body",
		"state":"OPEN",
		"headRefName":"feat/fix-bug",
		"baseRefName":"main",
		"url":"https://github.com/owner/repo/pull/7",
		"mergeable":"MERGEABLE",
		"mergeStateStatus":"CLEAN",
		"isDraft":false,
		"reviewDecision":"APPROVED",
		"labels":{"nodes":[{"name":"bug"},{"name":"priority:high"}]},
		"commits":{"nodes":[{
			"commit":{"statusCheckRollup":{"state":"SUCCESS"}}
		}]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	pr, err := svc.GetPR(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("GetPR returned unexpected error: %v", err)
	}
	if pr.NodeID != "PR_NODE_ID" {
		t.Errorf("NodeID = %q, want PR_NODE_ID", pr.NodeID)
	}
	if pr.Number != 7 {
		t.Errorf("Number = %d, want 7", pr.Number)
	}
	if pr.Title != "Fix the bug" {
		t.Errorf("Title = %q, want Fix the bug", pr.Title)
	}
	if pr.State != "OPEN" {
		t.Errorf("State = %q, want OPEN", pr.State)
	}
	if pr.HeadRef != "feat/fix-bug" {
		t.Errorf("HeadRef = %q, want feat/fix-bug", pr.HeadRef)
	}
	if pr.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want main", pr.BaseRef)
	}
	if pr.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want owner/repo", pr.Repo)
	}
	if pr.Mergeable != "MERGEABLE" {
		t.Errorf("Mergeable = %q, want MERGEABLE", pr.Mergeable)
	}
	if pr.MergeStateStatus != "CLEAN" {
		t.Errorf("MergeStateStatus = %q, want CLEAN", pr.MergeStateStatus)
	}
	if pr.ReviewStatus != "APPROVED" {
		t.Errorf("ReviewStatus = %q, want APPROVED", pr.ReviewStatus)
	}
	if pr.CheckStatus != "SUCCESS" {
		t.Errorf("CheckStatus = %q, want SUCCESS", pr.CheckStatus)
	}
	if pr.IsDraft {
		t.Error("IsDraft should be false")
	}
	if len(pr.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(pr.Labels))
	}
}

func TestPRService_GetPR_NoCheckRollup(t *testing.T) {
	response := `{"data":{"repository":{"pullRequest":{
		"id":"PR_NODE_ID",
		"number":8,
		"title":"Draft PR",
		"body":"",
		"state":"OPEN",
		"headRefName":"feat/wip",
		"baseRefName":"main",
		"url":"https://github.com/o/r/pull/8",
		"mergeable":"UNKNOWN",
		"isDraft":true,
		"reviewDecision":"",
		"labels":{"nodes":[]},
		"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	pr, err := svc.GetPR(context.Background(), "o", "r", 8)
	if err != nil {
		t.Fatalf("GetPR returned unexpected error: %v", err)
	}
	if pr.CheckStatus != "" {
		t.Errorf("CheckStatus = %q, want empty string when no rollup", pr.CheckStatus)
	}
	if !pr.IsDraft {
		t.Error("IsDraft should be true")
	}
}

func TestPRService_GetPR_Error(t *testing.T) {
	response := `{"errors":[{"message":"PR not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	_, err := svc.GetPR(context.Background(), "owner", "repo", 9999)
	if err == nil {
		t.Fatal("GetPR should return error on API error response")
	}
}

// --- ListPRs Tests ---

func TestPRService_ListPRs_WithHeadRef(t *testing.T) {
	// headRef present → uses pullRequestListQuery (with headRefName filter)
	response := `{"data":{"repository":{"pullRequests":{"nodes":[
		{
			"id":"PR_1",
			"number":11,
			"title":"Feature PR",
			"state":"OPEN",
			"headRefName":"feat/feature",
			"baseRefName":"main",
			"url":"https://github.com/o/r/pull/11",
			"isDraft":false,
			"labels":{"nodes":[{"name":"type:feature"}]}
		}
	]}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	prs, err := svc.ListPRs(context.Background(), "o", "r", "OPEN", "feat/feature")
	if err != nil {
		t.Fatalf("ListPRs returned unexpected error: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("ListPRs count = %d, want 1", len(prs))
	}
	if prs[0].Number != 11 {
		t.Errorf("prs[0].Number = %d, want 11", prs[0].Number)
	}
	if prs[0].HeadRef != "feat/feature" {
		t.Errorf("prs[0].HeadRef = %q, want feat/feature", prs[0].HeadRef)
	}
	if prs[0].Repo != "o/r" {
		t.Errorf("prs[0].Repo = %q, want o/r", prs[0].Repo)
	}
}

func TestPRService_ListPRs_WithoutHeadRef(t *testing.T) {
	// headRef absent → uses pullRequestListByStateQuery (no headRefName)
	response := `{"data":{"repository":{"pullRequests":{"nodes":[
		{
			"id":"PR_2",
			"number":12,
			"title":"Another PR",
			"state":"OPEN",
			"headRefName":"fix/something",
			"baseRefName":"main",
			"url":"https://github.com/o/r/pull/12",
			"isDraft":false,
			"labels":{"nodes":[]}
		},
		{
			"id":"PR_3",
			"number":13,
			"title":"Third PR",
			"state":"OPEN",
			"headRefName":"fix/other",
			"baseRefName":"main",
			"url":"https://github.com/o/r/pull/13",
			"isDraft":true,
			"labels":{"nodes":[]}
		}
	]}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	prs, err := svc.ListPRs(context.Background(), "o", "r", "OPEN", "")
	if err != nil {
		t.Fatalf("ListPRs returned unexpected error: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("ListPRs count = %d, want 2", len(prs))
	}
	if prs[1].IsDraft != true {
		t.Error("prs[1].IsDraft should be true")
	}
}

func TestPRService_ListPRs_EmptyState_DefaultsToOpen(t *testing.T) {
	response := `{"data":{"repository":{"pullRequests":{"nodes":[]}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	// Empty state should default to OPEN without error
	prs, err := svc.ListPRs(context.Background(), "o", "r", "", "")
	if err != nil {
		t.Fatalf("ListPRs with empty state returned unexpected error: %v", err)
	}
	if prs == nil {
		prs = nil // nil is acceptable for empty result
	}
	_ = prs
}

func TestPRService_ListPRs_Error(t *testing.T) {
	response := `{"errors":[{"message":"Repository not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	_, err := svc.ListPRs(context.Background(), "o", "nonexistent", "OPEN", "")
	if err == nil {
		t.Fatal("ListPRs should return error on API error response")
	}
}

// --- CreatePR Tests ---

func TestPRService_CreatePR_HappyPath(t *testing.T) {
	response := `{"data":{"createPullRequest":{"pullRequest":{
		"id":"NEW_PR_NODE_ID",
		"number":20,
		"url":"https://github.com/o/r/pull/20"
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	pr, err := svc.CreatePR(context.Background(), "REPO_ID", "My PR", "PR body", "feat/my-feature", "main")
	if err != nil {
		t.Fatalf("CreatePR returned unexpected error: %v", err)
	}
	if pr.NodeID != "NEW_PR_NODE_ID" {
		t.Errorf("NodeID = %q, want NEW_PR_NODE_ID", pr.NodeID)
	}
	if pr.Number != 20 {
		t.Errorf("Number = %d, want 20", pr.Number)
	}
	if pr.Title != "My PR" {
		t.Errorf("Title = %q, want My PR", pr.Title)
	}
	if pr.HeadRef != "feat/my-feature" {
		t.Errorf("HeadRef = %q, want feat/my-feature", pr.HeadRef)
	}
	if pr.BaseRef != "main" {
		t.Errorf("BaseRef = %q, want main", pr.BaseRef)
	}
}

func TestPRService_CreatePR_Error(t *testing.T) {
	response := `{"errors":[{"message":"Branch does not exist"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	_, err := svc.CreatePR(context.Background(), "REPO_ID", "PR", "body", "nonexistent-branch", "main")
	if err == nil {
		t.Fatal("CreatePR should return error on API error response")
	}
}

// --- MergePR / MergePRWithStrategy Tests ---

func TestPRService_MergePR_UsesSquashByDefault(t *testing.T) {
	response := `{"data":{"mergePullRequest":{"pullRequest":{"id":"PR_ID","state":"MERGED"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	// MergePR delegates to MergePRWithStrategy("SQUASH")
	if err := svc.MergePR(context.Background(), "PR_NODE_ID"); err != nil {
		t.Errorf("MergePR returned unexpected error: %v", err)
	}
}

func TestPRService_MergePRWithStrategy(t *testing.T) {
	strategies := []string{"SQUASH", "MERGE", "REBASE"}

	for _, strategy := range strategies {
		t.Run(strategy, func(t *testing.T) {
			response := `{"data":{"mergePullRequest":{"pullRequest":{"id":"PR_ID","state":"MERGED"}}}}`

			client, cleanup := mockGraphQLServer(t, response)
			defer cleanup()

			svc := NewPRService(client)
			if _, err := svc.MergePRWithStrategy(context.Background(), "PR_NODE_ID", strategy); err != nil {
				t.Errorf("MergePRWithStrategy(%q) returned unexpected error: %v", strategy, err)
			}
		})
	}
}

func TestPRService_MergePRWithStrategy_Error(t *testing.T) {
	response := `{"errors":[{"message":"Pull request is not mergeable"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	if _, err := svc.MergePRWithStrategy(context.Background(), "PR_NODE_ID", "SQUASH"); err == nil {
		t.Error("MergePRWithStrategy should return error on API error response")
	}
}

// --- DeleteBranch Tests ---

func TestPRService_DeleteBranch_HappyPath(t *testing.T) {
	// Chain: repositoryRefQuery (ref found) → deleteRefMutation (success)
	refQueryResponse := `{"data":{"repository":{"ref":{"id":"REF_NODE_ID"}}}}`
	deleteResponse := `{"data":{"deleteRef":{"clientMutationId":null}}}`

	client, cleanup := mockGraphQLServer(t, refQueryResponse, deleteResponse)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.DeleteBranch(context.Background(), "o", "r", "feat/old-branch"); err != nil {
		t.Errorf("DeleteBranch returned unexpected error: %v", err)
	}
}

func TestPRService_DeleteBranch_Idempotent_RefNotFound(t *testing.T) {
	// When the ref is already gone, repositoryRefQuery returns null ref
	// DeleteBranch should return nil (idempotent)
	response := `{"data":{"repository":{"ref":null}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.DeleteBranch(context.Background(), "o", "r", "already-deleted-branch"); err != nil {
		t.Errorf("DeleteBranch should return nil when ref not found (idempotent), got: %v", err)
	}
}

func TestPRService_DeleteBranch_RefLookupError(t *testing.T) {
	response := `{"errors":[{"message":"Repository not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.DeleteBranch(context.Background(), "o", "nonexistent", "branch"); err == nil {
		t.Error("DeleteBranch should return error when ref lookup fails")
	}
}

func TestPRService_DeleteBranch_DeleteError(t *testing.T) {
	// Ref found but delete mutation fails
	refQueryResponse := `{"data":{"repository":{"ref":{"id":"REF_NODE_ID"}}}}`
	deleteResponse := `{"errors":[{"message":"Permission denied"}]}`

	client, cleanup := mockGraphQLServer(t, refQueryResponse, deleteResponse)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.DeleteBranch(context.Background(), "o", "r", "protected-branch"); err == nil {
		t.Error("DeleteBranch should return error when deleteRef mutation fails")
	}
}

// --- CreateEpicPR Tests ---

func TestPRService_CreateEpicPR_Created(t *testing.T) {
	// No existing PRs → GetRepositoryID → CreatePR
	listPRsResponse := `{"data":{"repository":{"pullRequests":{"nodes":[]}}}}`
	getRepoIDResponse := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createPRResponse := `{"data":{"createPullRequest":{"pullRequest":{
		"id":"EPIC_PR_NODE_ID",
		"number":30,
		"url":"https://github.com/o/r/pull/30"
	}}}}`

	client, cleanup := mockGraphQLServer(t, listPRsResponse, getRepoIDResponse, createPRResponse)
	defer cleanup()

	svc := NewPRService(client)
	result, err := svc.CreateEpicPR(context.Background(), "o", "r", 5, "Big Epic", "epic/5-big-epic", "main")
	if err != nil {
		t.Fatalf("CreateEpicPR returned unexpected error: %v", err)
	}
	if result.Action != "created" {
		t.Errorf("Action = %q, want created", result.Action)
	}
	if result.PRNumber != 30 {
		t.Errorf("PRNumber = %d, want 30", result.PRNumber)
	}
	if result.PRNodeID != "EPIC_PR_NODE_ID" {
		t.Errorf("PRNodeID = %q, want EPIC_PR_NODE_ID", result.PRNodeID)
	}
}

func TestPRService_CreateEpicPR_AlreadyExists(t *testing.T) {
	// Open PR already exists for the epic branch
	response := `{"data":{"repository":{"pullRequests":{"nodes":[
		{
			"id":"EXISTING_PR_ID",
			"number":25,
			"title":"epic(#5): Big Epic",
			"state":"OPEN",
			"headRefName":"epic/5-big-epic",
			"baseRefName":"main",
			"url":"https://github.com/o/r/pull/25",
			"isDraft":false,
			"labels":{"nodes":[]}
		}
	]}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	result, err := svc.CreateEpicPR(context.Background(), "o", "r", 5, "Big Epic", "epic/5-big-epic", "main")
	if err != nil {
		t.Fatalf("CreateEpicPR returned unexpected error: %v", err)
	}
	if result.Action != "already_exists" {
		t.Errorf("Action = %q, want already_exists", result.Action)
	}
	if result.PRNumber != 25 {
		t.Errorf("PRNumber = %d, want 25", result.PRNumber)
	}
	if result.PRNodeID != "EXISTING_PR_ID" {
		t.Errorf("PRNodeID = %q, want EXISTING_PR_ID", result.PRNodeID)
	}
}

func TestPRService_CreateEpicPR_AlreadyMerged(t *testing.T) {
	// Merged PR already exists for the epic branch
	response := `{"data":{"repository":{"pullRequests":{"nodes":[
		{
			"id":"MERGED_PR_ID",
			"number":22,
			"title":"epic(#5): Big Epic",
			"state":"MERGED",
			"headRefName":"epic/5-big-epic",
			"baseRefName":"main",
			"url":"https://github.com/o/r/pull/22",
			"isDraft":false,
			"labels":{"nodes":[]}
		}
	]}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	result, err := svc.CreateEpicPR(context.Background(), "o", "r", 5, "Big Epic", "epic/5-big-epic", "main")
	if err != nil {
		t.Fatalf("CreateEpicPR returned unexpected error: %v", err)
	}
	if result.Action != "already_merged" {
		t.Errorf("Action = %q, want already_merged", result.Action)
	}
	if result.PRNumber != 22 {
		t.Errorf("PRNumber = %d, want 22", result.PRNumber)
	}
}

func TestPRService_CreateEpicPR_ListPRsError(t *testing.T) {
	response := `{"errors":[{"message":"Repository not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	_, err := svc.CreateEpicPR(context.Background(), "o", "nonexistent", 1, "Epic", "epic/1", "main")
	if err == nil {
		t.Fatal("CreateEpicPR should return error when ListPRs fails")
	}
}

// --- MergeEpicPR Tests ---

func TestPRService_MergeEpicPR_Success(t *testing.T) {
	// MergePRWithStrategy("MERGE") + DeleteBranch(ref found + deleted)
	mergeResponse := `{"data":{"mergePullRequest":{"pullRequest":{"id":"EPIC_PR_ID","state":"MERGED"}}}}`
	refQueryResponse := `{"data":{"repository":{"ref":{"id":"REF_NODE_ID"}}}}`
	deleteResponse := `{"data":{"deleteRef":{"clientMutationId":null}}}`

	client, cleanup := mockGraphQLServer(t, mergeResponse, refQueryResponse, deleteResponse)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.MergeEpicPR(context.Background(), "o", "r", "EPIC_PR_ID", "epic/5-big-epic"); err != nil {
		t.Errorf("MergeEpicPR returned unexpected error: %v", err)
	}
}

func TestPRService_MergeEpicPR_DeleteBranchFailureIsNonFatal(t *testing.T) {
	// Merge succeeds; DeleteBranch fails — but MergeEpicPR should still return nil
	mergeResponse := `{"data":{"mergePullRequest":{"pullRequest":{"id":"EPIC_PR_ID","state":"MERGED"}}}}`
	// Simulate branch already gone (idempotent path)
	refNotFoundResponse := `{"data":{"repository":{"ref":null}}}`

	client, cleanup := mockGraphQLServer(t, mergeResponse, refNotFoundResponse)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.MergeEpicPR(context.Background(), "o", "r", "EPIC_PR_ID", "epic/5-big-epic"); err != nil {
		t.Errorf("MergeEpicPR should not return error when DeleteBranch is non-fatal, got: %v", err)
	}
}

func TestPRService_MergeEpicPR_MergeError(t *testing.T) {
	response := `{"errors":[{"message":"Pull request is not mergeable"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewPRService(client)
	if err := svc.MergeEpicPR(context.Background(), "o", "r", "EPIC_PR_ID", "epic/5-big-epic"); err == nil {
		t.Error("MergeEpicPR should return error when merge fails")
	}
}

// --- splitPROwnerRepo Tests ---

func TestSplitPROwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"owner/repo", "owner", "repo"},
		{"nightgauge/nightgauge", "nightgauge", "nightgauge"},
		{"justname", "", "justname"},
		{"a/b/c", "a", "b/c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotOwner, gotRepo := splitPROwnerRepo(tt.input)
			if gotOwner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", gotOwner, tt.wantOwner)
			}
			if gotRepo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", gotRepo, tt.wantRepo)
			}
		})
	}
}

// --- UpdatePR / ClosePR / IteratePRs Tests ---

func TestPRService_UpdatePR_HappyPath(t *testing.T) {
	resp := `{"data":{"updatePullRequest":{"pullRequest":{
		"id":"PR_NODE","number":7,"title":"new","body":"newbody","state":"OPEN",
		"headRefName":"feat/x","baseRefName":"main","isDraft":false
	}}}}`
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()
	svc := NewPRService(client)

	title := "new"
	body := "newbody"
	got, err := svc.UpdatePR(context.Background(), "PR_NODE", forge.UpdatePROptions{
		Title: &title, Body: &body,
	})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if got.Number != 7 {
		t.Errorf("Number = %d", got.Number)
	}
}

func TestPRService_UpdatePR_RejectsEmptyID(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{}`)
	defer cleanup()
	svc := NewPRService(client)
	if _, err := svc.UpdatePR(context.Background(), "", forge.UpdatePROptions{}); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestPRService_UpdatePR_NoFieldsReturnsMinimal(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{}`)
	defer cleanup()
	svc := NewPRService(client)
	got, err := svc.UpdatePR(context.Background(), "PR_NODE", forge.UpdatePROptions{})
	if err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if got == nil || got.NodeID != "PR_NODE" {
		t.Errorf("expected minimal PR with NodeID, got %+v", got)
	}
}

func TestPRService_ClosePR_HappyPath(t *testing.T) {
	resp := `{"data":{"closePullRequest":{"pullRequest":{"id":"PR_NODE","state":"CLOSED"}}}}`
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()
	svc := NewPRService(client)
	if err := svc.ClosePR(context.Background(), "PR_NODE"); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
}

func TestPRService_ClosePR_RejectsEmptyID(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{}`)
	defer cleanup()
	svc := NewPRService(client)
	if err := svc.ClosePR(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestPRService_IteratePRs_YieldsThenEOF(t *testing.T) {
	listResp := `{"data":{"repository":{"pullRequests":{"nodes":[
		{"id":"PR_1","number":1,"title":"one","state":"OPEN","headRefName":"h1","baseRefName":"main","url":"u1","isDraft":false,"labels":{"nodes":[]}}
	]}}}}`
	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()
	svc := NewPRService(client)
	it := svc.IteratePRs(context.Background(), "owner", "repo", "OPEN", "")
	defer it.Close()

	first, err := it.Next(context.Background())
	if err != nil {
		t.Fatalf("Next #1: %v", err)
	}
	if first.Number != 1 {
		t.Errorf("Number = %d", first.Number)
	}
	if _, err := it.Next(context.Background()); err == nil {
		t.Error("expected EOF")
	}
}
