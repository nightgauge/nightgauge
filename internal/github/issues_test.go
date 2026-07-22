package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// mockGraphQLServer creates a test HTTP server that returns responses in sequence.
// Each call to the server consumes the next response from the list. After all
// responses are consumed, subsequent calls return the last response. Returns the
// Client pointed at the mock server and a cleanup function.
func mockGraphQLServer(t *testing.T, responses ...string) (*Client, func()) {
	t.Helper()
	var callIdx int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt32(&callIdx, 1)) - 1
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responses[idx])
	}))
	client := NewClientWithURL("test-token", srv.URL)
	return client, srv.Close
}

// --- Pure Function Tests ---

func TestIsDependabotIssue(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"dependency label", []string{"dependencies"}, true},
		{"security label", []string{"security"}, true},
		{"go label", []string{"go"}, true},
		{"javascript label", []string{"javascript"}, true},
		{"python label", []string{"python"}, true},
		{"rust label", []string{"rust"}, true},
		{"docker label", []string{"docker"}, true},
		{"github-actions label", []string{"github-actions"}, true},
		{"npm label", []string{"npm"}, true},
		{"non-dependabot single label", []string{"enhancement"}, false},
		{"non-dependabot labels", []string{"enhancement", "bug"}, false},
		{"empty labels", []string{}, false},
		{"nil labels", nil, false},
		{"mixed: dependabot + other", []string{"enhancement", "dependencies"}, true},
		{"type label only", []string{"type:feature"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDependabotIssue(tt.labels)
			if got != tt.want {
				t.Errorf("IsDependabotIssue(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

func TestDetectDependabotType(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"security label", []string{"security"}, "security"},
		{"dependencies only", []string{"dependencies"}, "dependency"},
		{"go label (dependency)", []string{"go"}, "dependency"},
		{"security + dependencies", []string{"security", "dependencies"}, "security"},
		{"non-dependabot", []string{"enhancement"}, ""},
		{"empty labels", []string{}, ""},
		{"nil labels", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectDependabotType(tt.labels)
			if got != tt.want {
				t.Errorf("DetectDependabotType(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

// --- Constructor Test ---

func TestNewIssueService(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewIssueService(client)
	if svc == nil {
		t.Fatal("NewIssueService returned nil")
	}
	if svc.client != client {
		t.Error("IssueService.client is not the provided client")
	}
}

// --- GetIssue Tests ---

func TestIssueService_GetIssue_HappyPath(t *testing.T) {
	response := `{"data":{"repository":{"issue":{
		"id":"ISSUE_NODE_ID",
		"number":42,
		"title":"Test Issue",
		"body":"Test body content",
		"state":"OPEN",
		"url":"https://github.com/owner/repo/issues/42",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"bug"},{"name":"priority:high"}]},
		"assignees":{"nodes":[{"login":"testuser"}]},
		"subIssues":{"nodes":[
			{"id":"SUB_NODE_1","number":43,"title":"Sub Issue","state":"OPEN",
			 "repository":{"nameWithOwner":"owner/repo"}}
		]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.GetIssue(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetIssue returned unexpected error: %v", err)
	}
	if issue.NodeID != "ISSUE_NODE_ID" {
		t.Errorf("NodeID = %q, want %q", issue.NodeID, "ISSUE_NODE_ID")
	}
	if issue.Number != 42 {
		t.Errorf("Number = %d, want 42", issue.Number)
	}
	if issue.Title != "Test Issue" {
		t.Errorf("Title = %q, want %q", issue.Title, "Test Issue")
	}
	if issue.State != "OPEN" {
		t.Errorf("State = %q, want OPEN", issue.State)
	}
	if issue.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want owner/repo", issue.Repo)
	}
	if len(issue.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(issue.Labels))
	}
	if len(issue.Assignees) != 1 || issue.Assignees[0] != "testuser" {
		t.Errorf("Assignees = %v, want [testuser]", issue.Assignees)
	}
	if len(issue.SubIssues) != 1 {
		t.Errorf("SubIssues count = %d, want 1", len(issue.SubIssues))
	}
	if !issue.IsEpic {
		t.Error("IsEpic should be true when sub-issues are present")
	}
}

func TestIssueService_GetIssue_WithBlockingRelationships(t *testing.T) {
	response := `{"data":{"repository":{"issue":{
		"id":"BLOCKED_NODE_ID",
		"number":10,
		"title":"Blocked Issue",
		"body":"",
		"state":"OPEN",
		"url":"https://github.com/owner/repo/issues/10",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[
			{"id":"BLOCKER_1","number":9,"title":"Blocker","state":"OPEN",
			 "repository":{"nameWithOwner":"owner/repo"}}
		]},
		"blocking":{"nodes":[
			{"id":"DEPENDENT_1","number":11,"title":"Dependent","state":"OPEN",
			 "repository":{"nameWithOwner":"owner/repo"}}
		]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.GetIssue(context.Background(), "owner", "repo", 10)
	if err != nil {
		t.Fatalf("GetIssue returned unexpected error: %v", err)
	}
	if len(issue.BlockedBy) != 1 {
		t.Errorf("BlockedBy count = %d, want 1", len(issue.BlockedBy))
	}
	if issue.BlockedBy[0].Number != 9 {
		t.Errorf("BlockedBy[0].Number = %d, want 9", issue.BlockedBy[0].Number)
	}
	if len(issue.Blocking) != 1 {
		t.Errorf("Blocking count = %d, want 1", len(issue.Blocking))
	}
	if issue.Blocking[0].Number != 11 {
		t.Errorf("Blocking[0].Number = %d, want 11", issue.Blocking[0].Number)
	}
	if issue.IsEpic {
		t.Error("IsEpic should be false when no sub-issues")
	}
}

func TestIssueService_GetIssue_Error(t *testing.T) {
	response := `{"errors":[{"message":"Could not resolve to a Repository with the name 'owner/nonexistent'."}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.GetIssue(context.Background(), "owner", "nonexistent", 1)
	if err == nil {
		t.Fatal("GetIssue should return error on API error response")
	}
}

// --- ListIssues Tests ---

func TestIssueService_ListIssues_NoFilter(t *testing.T) {
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"id":"I_1","number":1,"title":"First","state":"OPEN",
			 "url":"https://github.com/o/r/issues/1","labels":{"nodes":[{"name":"bug"}]}},
			{"id":"I_2","number":2,"title":"Second","state":"OPEN",
			 "url":"https://github.com/o/r/issues/2","labels":{"nodes":[]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssues(context.Background(), "o", "r", nil)
	if err != nil {
		t.Fatalf("ListIssues returned unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("ListIssues count = %d, want 2", len(issues))
	}
	if issues[0].Number != 1 {
		t.Errorf("issues[0].Number = %d, want 1", issues[0].Number)
	}
	if issues[0].Repo != "o/r" {
		t.Errorf("issues[0].Repo = %q, want o/r", issues[0].Repo)
	}
	if len(issues[0].Labels) != 1 || issues[0].Labels[0] != "bug" {
		t.Errorf("issues[0].Labels = %v, want [bug]", issues[0].Labels)
	}
}

func TestIssueService_ListIssues_WithLabels(t *testing.T) {
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"id":"I_3","number":3,"title":"Feature","state":"OPEN",
			 "url":"https://github.com/o/r/issues/3","labels":{"nodes":[{"name":"type:feature"}]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssues(context.Background(), "o", "r", []string{"type:feature"})
	if err != nil {
		t.Fatalf("ListIssues with labels returned unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("ListIssues count = %d, want 1", len(issues))
	}
}

func TestIssueService_ListIssues_Empty(t *testing.T) {
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssues(context.Background(), "o", "r", nil)
	if err != nil {
		t.Fatalf("ListIssues returned unexpected error: %v", err)
	}
	// Result may be nil slice — that's acceptable for empty result
	if len(issues) != 0 {
		t.Errorf("ListIssues empty result count = %d, want 0", len(issues))
	}
}

func TestIssueService_ListIssues_Error(t *testing.T) {
	response := `{"errors":[{"message":"rate limit exceeded"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.ListIssues(context.Background(), "o", "r", nil)
	if err == nil {
		t.Fatal("ListIssues should return error on API error response")
	}
}

// --- CreateIssue Tests ---

func TestIssueService_CreateIssue_HappyPath(t *testing.T) {
	response := `{"data":{"createIssue":{"issue":{
		"id":"NEW_ISSUE_NODE_ID",
		"number":99,
		"url":"https://github.com/owner/repo/issues/99"
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.CreateIssue(context.Background(), "REPO_ID", "New Issue", "Body text", []string{"LABEL_ID_1"})
	if err != nil {
		t.Fatalf("CreateIssue returned unexpected error: %v", err)
	}
	if issue.NodeID != "NEW_ISSUE_NODE_ID" {
		t.Errorf("NodeID = %q, want NEW_ISSUE_NODE_ID", issue.NodeID)
	}
	if issue.Number != 99 {
		t.Errorf("Number = %d, want 99", issue.Number)
	}
	if issue.Title != "New Issue" {
		t.Errorf("Title = %q, want New Issue", issue.Title)
	}
}

func TestIssueService_CreateIssue_Error(t *testing.T) {
	response := `{"errors":[{"message":"Repository not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.CreateIssue(context.Background(), "INVALID_REPO", "Title", "Body", nil)
	if err == nil {
		t.Fatal("CreateIssue should return error on API error response")
	}
}

// --- CloseIssue / ReopenIssue Tests ---

func TestIssueService_CloseIssue_HappyPath(t *testing.T) {
	response := `{"data":{"closeIssue":{"issue":{"id":"ISSUE_NODE_ID"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.CloseIssue(context.Background(), "ISSUE_NODE_ID"); err != nil {
		t.Errorf("CloseIssue returned unexpected error: %v", err)
	}
}

func TestIssueService_CloseIssue_Error(t *testing.T) {
	response := `{"errors":[{"message":"Issue not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.CloseIssue(context.Background(), "INVALID_ID"); err == nil {
		t.Error("CloseIssue should return error on API error response")
	}
}

func TestIssueService_ReopenIssue_HappyPath(t *testing.T) {
	response := `{"data":{"reopenIssue":{"issue":{"id":"ISSUE_NODE_ID"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.ReopenIssue(context.Background(), "ISSUE_NODE_ID"); err != nil {
		t.Errorf("ReopenIssue returned unexpected error: %v", err)
	}
}

func TestIssueService_ReopenIssue_Error(t *testing.T) {
	response := `{"errors":[{"message":"Issue is already open"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.ReopenIssue(context.Background(), "ISSUE_NODE_ID"); err == nil {
		t.Error("ReopenIssue should return error on API error response")
	}
}

// --- AddComment Tests ---

func TestIssueService_AddComment_HappyPath(t *testing.T) {
	response := `{"data":{"addComment":{"commentEdge":{"node":{"id":"COMMENT_ID"}}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddComment(context.Background(), "ISSUE_NODE_ID", "Test comment"); err != nil {
		t.Errorf("AddComment returned unexpected error: %v", err)
	}
}

func TestIssueService_AddComment_Error(t *testing.T) {
	response := `{"errors":[{"message":"Subject not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddComment(context.Background(), "INVALID_ID", "Test comment"); err == nil {
		t.Error("AddComment should return error on API error response")
	}
}

// --- AddLabels / RemoveLabels Tests ---

func TestIssueService_AddLabels_HappyPath(t *testing.T) {
	response := `{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddLabels(context.Background(), "ISSUE_NODE_ID", []string{"LABEL_ID_1", "LABEL_ID_2"}); err != nil {
		t.Errorf("AddLabels returned unexpected error: %v", err)
	}
}

func TestIssueService_AddLabels_Error(t *testing.T) {
	response := `{"errors":[{"message":"Label not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddLabels(context.Background(), "ISSUE_NODE_ID", []string{"INVALID_LABEL"}); err == nil {
		t.Error("AddLabels should return error on API error response")
	}
}

func TestIssueService_RemoveLabels_HappyPath(t *testing.T) {
	response := `{"data":{"removeLabelsFromLabelable":{"labelable":{"__typename":"Issue"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.RemoveLabels(context.Background(), "ISSUE_NODE_ID", []string{"LABEL_ID_1"}); err != nil {
		t.Errorf("RemoveLabels returned unexpected error: %v", err)
	}
}

func TestIssueService_RemoveLabels_Error(t *testing.T) {
	response := `{"errors":[{"message":"Permission denied"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.RemoveLabels(context.Background(), "ISSUE_NODE_ID", []string{"LABEL_ID"}); err == nil {
		t.Error("RemoveLabels should return error on API error response")
	}
}

// --- Sub-issue Mutation Tests ---

func TestIssueService_AddSubIssue_HappyPath(t *testing.T) {
	response := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_ID"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddSubIssue(context.Background(), "PARENT_ID", "CHILD_ID"); err != nil {
		t.Errorf("AddSubIssue returned unexpected error: %v", err)
	}
}

func TestIssueService_AddSubIssue_Error(t *testing.T) {
	response := `{"errors":[{"message":"Issue not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddSubIssue(context.Background(), "INVALID", "CHILD"); err == nil {
		t.Error("AddSubIssue should return error on API error response")
	}
}

func TestIssueService_RemoveSubIssue_HappyPath(t *testing.T) {
	response := `{"data":{"removeSubIssue":{"issue":{"id":"PARENT_ID"}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.RemoveSubIssue(context.Background(), "PARENT_ID", "CHILD_ID"); err != nil {
		t.Errorf("RemoveSubIssue returned unexpected error: %v", err)
	}
}

func TestIssueService_RemoveSubIssue_Error(t *testing.T) {
	response := `{"errors":[{"message":"Sub-issue not linked"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.RemoveSubIssue(context.Background(), "PARENT_ID", "CHILD_ID"); err == nil {
		t.Error("RemoveSubIssue should return error on API error response")
	}
}

// --- AddBlockedBy / RemoveBlockedBy Tests ---

func TestIssueService_AddBlockedBy_HappyPath(t *testing.T) {
	response := `{"data":{"addBlockedBy":{"clientMutationId":null}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddBlockedBy(context.Background(), "BLOCKED_ID", "BLOCKER_ID"); err != nil {
		t.Errorf("AddBlockedBy returned unexpected error: %v", err)
	}
}

func TestIssueService_AddBlockedBy_Error(t *testing.T) {
	response := `{"errors":[{"message":"Issue not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.AddBlockedBy(context.Background(), "INVALID", "BLOCKER_ID"); err == nil {
		t.Error("AddBlockedBy should return error on API error response")
	}
}

func TestIssueService_RemoveBlockedBy_HappyPath(t *testing.T) {
	response := `{"data":{"removeBlockedBy":{"clientMutationId":null}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.RemoveBlockedBy(context.Background(), "BLOCKED_ID", "BLOCKER_ID"); err != nil {
		t.Errorf("RemoveBlockedBy returned unexpected error: %v", err)
	}
}

func TestIssueService_RemoveBlockedBy_Error(t *testing.T) {
	response := `{"errors":[{"message":"Blocking relationship not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.RemoveBlockedBy(context.Background(), "BLOCKED_ID", "BLOCKER_ID"); err == nil {
		t.Error("RemoveBlockedBy should return error on API error response")
	}
}

// --- SyncStatusLabel Tests (chained operation) ---

// syncStatusLabelGetIssueResponse returns a mock GetIssue response for SyncStatusLabel tests.
func syncStatusLabelGetIssueResponse(statusLabel string) string {
	labelsJSON := `[]`
	if statusLabel != "" {
		labelsJSON = fmt.Sprintf(`[{"name":%q}]`, statusLabel)
	}
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"id":"ISSUE_NODE_ID",
		"number":5,
		"title":"Issue",
		"body":"",
		"state":"OPEN",
		"url":"https://github.com/o/r/issues/5",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":%s},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`, labelsJSON)
}

func syncStatusLabelGetRepoLabelsResponse() string {
	return `{"data":{"repository":{"labels":{"nodes":[
		{"id":"LABEL_STATUS_IN_PROGRESS","name":"status:In Progress"},
		{"id":"LABEL_STATUS_DONE","name":"status:Done"},
		{"id":"LABEL_STATUS_READY","name":"status:Ready"},
		{"id":"LABEL_BUG","name":"bug"}
	]}}}}`
}

func TestIssueService_SyncStatusLabel_ReplacesExistingStatus(t *testing.T) {
	// Chain: GetIssue → getRepoLabels → RemoveLabels → AddLabels
	client, cleanup := mockGraphQLServer(t,
		syncStatusLabelGetIssueResponse("status:In Progress"),
		syncStatusLabelGetRepoLabelsResponse(),
		`{"data":{"removeLabelsFromLabelable":{"labelable":{"__typename":"Issue"}}}}`,
		`{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`,
	)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.SyncStatusLabel(context.Background(), "o", "r", 5, "Done"); err != nil {
		t.Errorf("SyncStatusLabel returned unexpected error: %v", err)
	}
}

func TestIssueService_SyncStatusLabel_NoExistingStatus(t *testing.T) {
	// Chain: GetIssue (no status labels) → getRepoLabels → AddLabels only
	client, cleanup := mockGraphQLServer(t,
		syncStatusLabelGetIssueResponse(""),
		syncStatusLabelGetRepoLabelsResponse(),
		`{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`,
	)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.SyncStatusLabel(context.Background(), "o", "r", 5, "Ready"); err != nil {
		t.Errorf("SyncStatusLabel returned unexpected error: %v", err)
	}
}

func TestIssueService_SyncStatusLabel_LabelNotInRepo(t *testing.T) {
	// GetIssue → getRepoLabels → error: target label not found
	client, cleanup := mockGraphQLServer(t,
		syncStatusLabelGetIssueResponse(""),
		syncStatusLabelGetRepoLabelsResponse(),
	)
	defer cleanup()

	svc := NewIssueService(client)
	err := svc.SyncStatusLabel(context.Background(), "o", "r", 5, "nonexistent-status")
	if err == nil {
		t.Error("SyncStatusLabel should return error when target label not found in repo")
	}
}

func TestIssueService_SyncStatusLabel_GetIssueFails(t *testing.T) {
	response := `{"errors":[{"message":"Issue not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.SyncStatusLabel(context.Background(), "o", "r", 999, "Done"); err == nil {
		t.Error("SyncStatusLabel should propagate GetIssue error")
	}
}

// --- GetEpicProgress Tests ---

func TestIssueService_GetEpicProgress_HappyPath(t *testing.T) {
	response := `{"data":{"node":{
		"__typename":"Issue",
		"id":"EPIC_NODE_ID",
		"number":100,
		"title":"Epic Title",
		"state":"OPEN",
		"repository":{"nameWithOwner":"owner/repo"},
		"subIssues":{"nodes":[
			{"id":"SUB_1","number":101,"title":"Sub 1","state":"CLOSED",
			 "repository":{"nameWithOwner":"owner/repo"}},
			{"id":"SUB_2","number":102,"title":"Sub 2","state":"OPEN",
			 "repository":{"nameWithOwner":"owner/repo"}},
			{"id":"SUB_3","number":103,"title":"Sub 3","state":"CLOSED",
			 "repository":{"nameWithOwner":"owner/repo"}}
		]}
	}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	epic, err := svc.GetEpicProgress(context.Background(), "EPIC_NODE_ID")
	if err != nil {
		t.Fatalf("GetEpicProgress returned unexpected error: %v", err)
	}
	if epic.EpicNodeID != "EPIC_NODE_ID" {
		t.Errorf("EpicNodeID = %q, want EPIC_NODE_ID", epic.EpicNodeID)
	}
	if epic.Number != 100 {
		t.Errorf("Number = %d, want 100", epic.Number)
	}
	if epic.Total != 3 {
		t.Errorf("Total = %d, want 3", epic.Total)
	}
	if epic.Closed != 2 {
		t.Errorf("Closed = %d, want 2", epic.Closed)
	}
	if epic.Open != 1 {
		t.Errorf("Open = %d, want 1", epic.Open)
	}
	// 2/3 ≈ 66.67%
	wantPct := float64(2) / float64(3) * 100
	if epic.PercentComplete != wantPct {
		t.Errorf("PercentComplete = %v, want %v", epic.PercentComplete, wantPct)
	}
}

func TestIssueService_GetEpicProgress_EmptySubIssues(t *testing.T) {
	response := `{"data":{"node":{
		"__typename":"Issue",
		"id":"EPIC_NODE_ID",
		"number":100,
		"title":"Empty Epic",
		"state":"OPEN",
		"repository":{"nameWithOwner":"owner/repo"},
		"subIssues":{"nodes":[]}
	}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	epic, err := svc.GetEpicProgress(context.Background(), "EPIC_NODE_ID")
	if err != nil {
		t.Fatalf("GetEpicProgress returned unexpected error: %v", err)
	}
	if epic.Total != 0 {
		t.Errorf("Total = %d, want 0", epic.Total)
	}
	if epic.PercentComplete != 0 {
		t.Errorf("PercentComplete = %v, want 0", epic.PercentComplete)
	}
}

func TestIssueService_GetEpicProgress_NotIssueNode(t *testing.T) {
	// TypeName is not "Issue" — should return error
	response := `{"data":{"node":{
		"__typename":"PullRequest"
	}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.GetEpicProgress(context.Background(), "PR_NODE_ID")
	if err == nil {
		t.Error("GetEpicProgress should return error when node is not an Issue")
	}
}

func TestIssueService_GetEpicProgress_Error(t *testing.T) {
	response := `{"errors":[{"message":"Node not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.GetEpicProgress(context.Background(), "INVALID_ID")
	if err == nil {
		t.Error("GetEpicProgress should return error on API error response")
	}
}

// --- GetEpicProgressByNumber Tests ---

func TestIssueService_GetEpicProgressByNumber_HappyPath(t *testing.T) {
	// GetEpicProgressByNumber calls GetIssue internally, then aggregates
	getIssueResponse := `{"data":{"repository":{"issue":{
		"id":"EPIC_NODE_ID",
		"number":50,
		"title":"My Epic",
		"body":"",
		"state":"OPEN",
		"url":"https://github.com/o/r/issues/50",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[
			{"id":"S1","number":51,"title":"Sub A","state":"CLOSED",
			 "repository":{"nameWithOwner":"o/r"}},
			{"id":"S2","number":52,"title":"Sub B","state":"OPEN",
			 "repository":{"nameWithOwner":"o/r"}}
		]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, getIssueResponse)
	defer cleanup()

	svc := NewIssueService(client)
	epic, err := svc.GetEpicProgressByNumber(context.Background(), "o", "r", 50)
	if err != nil {
		t.Fatalf("GetEpicProgressByNumber returned unexpected error: %v", err)
	}
	if epic.Number != 50 {
		t.Errorf("Number = %d, want 50", epic.Number)
	}
	if epic.Title != "My Epic" {
		t.Errorf("Title = %q, want My Epic", epic.Title)
	}
	if epic.Total != 2 {
		t.Errorf("Total = %d, want 2", epic.Total)
	}
	if epic.Closed != 1 {
		t.Errorf("Closed = %d, want 1", epic.Closed)
	}
	if epic.PercentComplete != 50.0 {
		t.Errorf("PercentComplete = %v, want 50.0", epic.PercentComplete)
	}
}

func TestIssueService_GetEpicProgressByNumber_Error(t *testing.T) {
	response := `{"errors":[{"message":"Issue not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.GetEpicProgressByNumber(context.Background(), "o", "r", 9999)
	if err == nil {
		t.Error("GetEpicProgressByNumber should propagate GetIssue error")
	}
}

// --- ListIssuesExcludingLabels Tests ---

func TestIssueService_ListIssuesExcludingLabels_ExcludesRefined(t *testing.T) {
	// Response has 3 issues: one refined, one epic, one unrefined
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"number":1,"title":"Refined Issue","createdAt":"2026-01-01T00:00:00Z",
			 "labels":{"nodes":[{"name":"pipeline:refined"},{"name":"type:feature"}]}},
			{"number":2,"title":"Epic Issue","createdAt":"2026-01-02T00:00:00Z",
			 "labels":{"nodes":[{"name":"type:epic"}]}},
			{"number":3,"title":"Unrefined Feature","createdAt":"2026-01-03T00:00:00Z",
			 "labels":{"nodes":[{"name":"type:feature"},{"name":"priority:high"}]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssuesExcludingLabels(context.Background(), "o", "r",
		[]string{LabelRefined, "type:epic"}, 0)
	if err != nil {
		t.Fatalf("ListIssuesExcludingLabels returned unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("ListIssuesExcludingLabels count = %d, want 1", len(issues))
	}
	if issues[0].Number != 3 {
		t.Errorf("issues[0].Number = %d, want 3", issues[0].Number)
	}
	if issues[0].Title != "Unrefined Feature" {
		t.Errorf("issues[0].Title = %q, want %q", issues[0].Title, "Unrefined Feature")
	}
	if issues[0].CreatedAt != "2026-01-03T00:00:00Z" {
		t.Errorf("issues[0].CreatedAt = %q, want %q", issues[0].CreatedAt, "2026-01-03T00:00:00Z")
	}
	if len(issues[0].Labels) != 2 {
		t.Errorf("issues[0].Labels count = %d, want 2", len(issues[0].Labels))
	}
}

func TestIssueService_ListIssuesExcludingLabels_AllFiltered(t *testing.T) {
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"number":1,"title":"Already Refined","createdAt":"2026-01-01T00:00:00Z",
			 "labels":{"nodes":[{"name":"pipeline:refined"}]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssuesExcludingLabels(context.Background(), "o", "r",
		[]string{LabelRefined}, 0)
	if err != nil {
		t.Fatalf("ListIssuesExcludingLabels returned unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("ListIssuesExcludingLabels count = %d, want 0", len(issues))
	}
}

func TestIssueService_ListIssuesExcludingLabels_LimitApplied(t *testing.T) {
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"number":1,"title":"Issue A","createdAt":"2026-01-01T00:00:00Z","labels":{"nodes":[]}},
			{"number":2,"title":"Issue B","createdAt":"2026-01-02T00:00:00Z","labels":{"nodes":[]}},
			{"number":3,"title":"Issue C","createdAt":"2026-01-03T00:00:00Z","labels":{"nodes":[]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssuesExcludingLabels(context.Background(), "o", "r",
		[]string{LabelRefined}, 2)
	if err != nil {
		t.Fatalf("ListIssuesExcludingLabels returned unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("ListIssuesExcludingLabels with limit=2 count = %d, want 2", len(issues))
	}
}

func TestIssueService_ListIssuesExcludingLabels_Error(t *testing.T) {
	response := `{"errors":[{"message":"rate limit exceeded"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.ListIssuesExcludingLabels(context.Background(), "o", "r", []string{LabelRefined}, 0)
	if err == nil {
		t.Fatal("ListIssuesExcludingLabels should return error on API error")
	}
}

// --- HasLabel Tests ---

func TestIssueService_HasLabel_True(t *testing.T) {
	response := `{"data":{"repository":{"issue":{
		"id":"I_1","number":42,"title":"Test","body":"","state":"OPEN",
		"url":"https://github.com/o/r/issues/42",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"pipeline:refined"},{"name":"type:feature"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[]},"blocking":{"nodes":[]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	has, err := svc.HasLabel(context.Background(), "o", "r", 42, LabelRefined)
	if err != nil {
		t.Fatalf("HasLabel returned unexpected error: %v", err)
	}
	if !has {
		t.Error("HasLabel should return true when issue has the label")
	}
}

func TestIssueService_HasLabel_False(t *testing.T) {
	response := `{"data":{"repository":{"issue":{
		"id":"I_1","number":42,"title":"Test","body":"","state":"OPEN",
		"url":"https://github.com/o/r/issues/42",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"type:feature"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[]},"blocking":{"nodes":[]}
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	has, err := svc.HasLabel(context.Background(), "o", "r", 42, LabelRefined)
	if err != nil {
		t.Fatalf("HasLabel returned unexpected error: %v", err)
	}
	if has {
		t.Error("HasLabel should return false when issue lacks the label")
	}
}

func TestIssueService_HasLabel_Error(t *testing.T) {
	response := `{"errors":[{"message":"Issue not found"}]}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.HasLabel(context.Background(), "o", "r", 999, LabelRefined)
	if err == nil {
		t.Fatal("HasLabel should propagate GetIssue error")
	}
}

// --- MarkRefined Tests ---

func TestIssueService_MarkRefined_HappyPath(t *testing.T) {
	// Sequence: GetIssue → getRepoLabels → AddLabels
	getIssueResp := `{"data":{"repository":{"issue":{
		"id":"I_42","number":42,"title":"Feature","body":"","state":"OPEN",
		"url":"https://github.com/o/r/issues/42",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"type:feature"}]},
		"assignees":{"nodes":[]},"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[]},"blocking":{"nodes":[]}
	}}}}`
	getLabelsResp := `{"data":{"repository":{"labels":{"nodes":[
		{"id":"LABEL_REFINED_ID","name":"pipeline:refined"},
		{"id":"LABEL_EPIC_ID","name":"type:epic"}
	]}}}}`
	addLabelsResp := `{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`

	client, cleanup := mockGraphQLServer(t, getIssueResp, getLabelsResp, addLabelsResp)
	defer cleanup()

	svc := NewIssueService(client)
	err := svc.MarkRefined(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("MarkRefined returned unexpected error: %v", err)
	}
}

func TestIssueService_MarkRefined_LabelNotFound(t *testing.T) {
	// Repo has no pipeline:refined label — should return error
	getIssueResp := `{"data":{"repository":{"issue":{
		"id":"I_42","number":42,"title":"Feature","body":"","state":"OPEN",
		"url":"https://github.com/o/r/issues/42",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[]},"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},"blockedBy":{"nodes":[]},"blocking":{"nodes":[]}
	}}}}`
	getLabelsResp := `{"data":{"repository":{"labels":{"nodes":[
		{"id":"LABEL_OTHER_ID","name":"type:feature"}
	]}}}}`

	client, cleanup := mockGraphQLServer(t, getIssueResp, getLabelsResp)
	defer cleanup()

	svc := NewIssueService(client)
	err := svc.MarkRefined(context.Background(), "o", "r", 42)
	if err == nil {
		t.Fatal("MarkRefined should return error when pipeline:refined label not in repo")
	}
}

// --- SearchIssues Tests ---

func TestIssueService_SearchIssues_HappyPath(t *testing.T) {
	searchResp := `{"data":{"search":{"issueCount":2,"nodes":[
		{"__typename":"Issue","id":"I_10","number":10,"title":"Migrate CLI","state":"OPEN",
		 "url":"https://github.com/o/r/issues/10",
		 "repository":{"nameWithOwner":"o/r"},
		 "labels":{"nodes":[{"name":"type:refactor"}]}},
		{"__typename":"Issue","id":"I_20","number":20,"title":"CLI migration part 2","state":"OPEN",
		 "url":"https://github.com/o/r/issues/20",
		 "repository":{"nameWithOwner":"o/r"},
		 "labels":{"nodes":[]}}
	]}}}`

	client, cleanup := mockGraphQLServer(t, searchResp)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.SearchIssues(context.Background(), "o", "r", "migrate CLI", 5)
	if err != nil {
		t.Fatalf("SearchIssues returned error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Number != 10 {
		t.Errorf("expected issue #10, got #%d", issues[0].Number)
	}
	if issues[0].Title != "Migrate CLI" {
		t.Errorf("expected title 'Migrate CLI', got %q", issues[0].Title)
	}
	if len(issues[0].Labels) != 1 || issues[0].Labels[0] != "type:refactor" {
		t.Errorf("expected labels [type:refactor], got %v", issues[0].Labels)
	}
	if issues[1].Number != 20 {
		t.Errorf("expected issue #20, got #%d", issues[1].Number)
	}
}

func TestIssueService_SearchIssues_Empty(t *testing.T) {
	searchResp := `{"data":{"search":{"issueCount":0,"nodes":[]}}}`

	client, cleanup := mockGraphQLServer(t, searchResp)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.SearchIssues(context.Background(), "o", "r", "nonexistent", 5)
	if err != nil {
		t.Fatalf("SearchIssues returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %d", len(issues))
	}
}

func TestIssueService_SearchIssues_DefaultLimit(t *testing.T) {
	// When limit is 0 or negative, defaults to 10
	searchResp := `{"data":{"search":{"issueCount":0,"nodes":[]}}}`

	client, cleanup := mockGraphQLServer(t, searchResp)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.SearchIssues(context.Background(), "o", "r", "test", 0)
	if err != nil {
		t.Fatalf("SearchIssues with limit=0 returned error: %v", err)
	}
}

// --- EditIssue Tests ---

func TestIssueService_SearchIssues_Error(t *testing.T) {
	errorResp := `{"errors":[{"message":"search failed"}]}`

	client, cleanup := mockGraphQLServer(t, errorResp)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.SearchIssues(context.Background(), "o", "r", "test", 5)
	if err == nil {
		t.Fatal("SearchIssues should return error on GraphQL failure")
	}
}

func TestIssueService_EditIssue_HappyPath(t *testing.T) {
	editResp := `{"data":{"updateIssue":{"issue":{
		"id":"I_42","number":42,"title":"Feature","body":"updated body"
	}}}}`

	client, cleanup := mockGraphQLServer(t, editResp)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.EditIssue(context.Background(), "I_42", "updated body")
	if err != nil {
		t.Fatalf("EditIssue returned error: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("expected issue #42, got #%d", issue.Number)
	}
	if issue.Body != "updated body" {
		t.Errorf("expected body 'updated body', got %q", issue.Body)
	}
}

func TestIssueService_EditIssue_EmptyNodeID(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{}`)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.EditIssue(context.Background(), "", "body")
	if err == nil {
		t.Fatal("EditIssue should return error for empty nodeID")
	}
}

func TestIssueService_EditIssue_Error(t *testing.T) {
	errorResp := `{"errors":[{"message":"not found"}]}`

	client, cleanup := mockGraphQLServer(t, errorResp)
	defer cleanup()

	svc := NewIssueService(client)
	_, err := svc.EditIssue(context.Background(), "I_INVALID", "body")
	if err == nil {
		t.Fatal("EditIssue should return error for invalid node ID")
	}
}

// --- CreateSubIssue Tests ---

// getIssueResp builds a minimal GetIssue GraphQL response matching issueQuery struct fields.
func getIssueResp(nodeID string, number int) string {
	return `{"data":{"repository":{"issue":{` +
		`"id":"` + nodeID + `",` +
		`"number":` + fmt.Sprintf("%d", number) + `,` +
		`"title":"Test Issue",` +
		`"body":"",` +
		`"state":"OPEN",` +
		`"url":"https://github.com/o/r/issues/` + fmt.Sprintf("%d", number) + `",` +
		`"parent":{"id":"","number":0,"title":""},` +
		`"labels":{"nodes":[]},` +
		`"assignees":{"nodes":[]},` +
		`"subIssues":{"nodes":[]},` +
		`"blockedBy":{"nodes":[]},` +
		`"blocking":{"nodes":[]}` +
		`}}}}`
}

func TestIssueService_CreateSubIssue_NilProjectSvc(t *testing.T) {
	// Sequence: GetRepositoryID → CreateIssue → GetIssue(parent) → AddSubIssue
	repoIDResp := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":101,"url":"https://github.com/o/r/issues/101"}}}}`
	parentResp := getIssueResp("PARENT_NODE", 50)
	addSubResp := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_NODE"}}}}`

	client, cleanup := mockGraphQLServer(t, repoIDResp, createResp, parentResp, addSubResp)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, nil)
	if err != nil {
		t.Fatalf("CreateSubIssue returned unexpected error: %v", err)
	}
	if issue.Number != 101 {
		t.Errorf("issue.Number = %d, want 101", issue.Number)
	}
}

func TestIssueService_CreateSubIssue_WithProjectSvc(t *testing.T) {
	// Sequence: GetRepositoryID → CreateIssue → GetIssue(parent) → AddSubIssue →
	//           ensureFields(project) → GetIssue(new issue, for AddIssueByNumber) →
	//           addProjectV2ItemById → (syncLabelsToFields: no labels, no extra calls)
	repoIDResp := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":102,"url":"https://github.com/o/r/issues/102"}}}}`
	parentResp := getIssueResp("PARENT_NODE", 50)
	addSubResp := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_NODE"}}}}`
	// ensureFields: org project query
	fieldsResp := `{"data":{"organization":{"projectV2":{"id":"PROJ_ID","fields":{"nodes":[]}}}}}`
	// AddIssueByNumber calls GetIssue internally
	newIssueResp := getIssueResp("NEW_NODE", 102)
	addItemResp := `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM_ID"}}}}`

	// AddIssueByNumber order: GetIssue(new) → ensureFields → AddItem
	client, cleanup := mockGraphQLServer(t,
		repoIDResp, createResp, parentResp, addSubResp,
		newIssueResp, fieldsResp, addItemResp,
	)
	defer cleanup()

	svc := NewIssueService(client)
	projectSvc := NewProjectService(client, "o", 3)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, projectSvc)
	if err != nil {
		t.Fatalf("CreateSubIssue returned unexpected error: %v", err)
	}
	if issue.Number != 102 {
		t.Errorf("issue.Number = %d, want 102", issue.Number)
	}
}

func TestIssueService_CreateSubIssue_BoardSyncFailure(t *testing.T) {
	// Board sync fails: issue + link succeed, but AddItem returns error.
	// Verify: error wraps "board sync failed" and issue object is non-nil.
	repoIDResp := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":103,"url":"https://github.com/o/r/issues/103"}}}}`
	parentResp := getIssueResp("PARENT_NODE", 50)
	addSubResp := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_NODE"}}}}`
	// ensureFields succeeds
	fieldsResp := `{"data":{"organization":{"projectV2":{"id":"PROJ_ID","fields":{"nodes":[]}}}}}`
	// GetIssue inside AddIssueByNumber succeeds
	newIssueResp := getIssueResp("NEW_NODE", 103)
	// AddItem fails
	addItemErr := `{"errors":[{"message":"project not found"}]}`

	// AddIssueByNumber order: GetIssue(new) → ensureFields → AddItem(fails)
	client, cleanup := mockGraphQLServer(t,
		repoIDResp, createResp, parentResp, addSubResp,
		newIssueResp, fieldsResp, addItemErr,
	)
	defer cleanup()

	svc := NewIssueService(client)
	projectSvc := NewProjectService(client, "o", 99)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, projectSvc)
	if err == nil {
		t.Fatal("expected error for board sync failure, got nil")
	}
	if issue == nil {
		t.Fatal("expected non-nil issue on partial success (board sync failed)")
	}
	if issue.Number != 103 {
		t.Errorf("issue.Number = %d, want 103", issue.Number)
	}
	wantSubstr := "board sync failed"
	if !containsStr(err.Error(), wantSubstr) {
		t.Errorf("error %q does not contain %q", err.Error(), wantSubstr)
	}
	wantIssueNum := "issue #103"
	if !containsStr(err.Error(), wantIssueNum) {
		t.Errorf("error %q does not contain %q", err.Error(), wantIssueNum)
	}
}

// --- AddBlockedBy Tests (exercises the blocker workflow used by create-sub --blocked-by) ---

func TestIssueService_CreateSubIssue_WithBlockedBy(t *testing.T) {
	// Simulates the full CLI workflow for create-sub --blocked-by 1,2:
	// CreateSubIssue sequence → GetIssue(blocker1) → AddBlockedBy(1) → GetIssue(blocker2) → AddBlockedBy(2)
	repoIDResp := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":110,"url":"https://github.com/o/r/issues/110"}}}}`
	parentResp := getIssueResp("PARENT_NODE", 50)
	addSubResp := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_NODE"}}}}`
	blocker1Resp := getIssueResp("BLOCKER1_NODE", 1)
	addBlocked1Resp := `{"data":{"addBlockedBy":{"clientMutationId":null}}}`
	blocker2Resp := getIssueResp("BLOCKER2_NODE", 2)
	addBlocked2Resp := `{"data":{"addBlockedBy":{"clientMutationId":null}}}`

	client, cleanup := mockGraphQLServer(t,
		repoIDResp, createResp, parentResp, addSubResp,
		blocker1Resp, addBlocked1Resp,
		blocker2Resp, addBlocked2Resp,
	)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, nil)
	if err != nil {
		t.Fatalf("CreateSubIssue returned unexpected error: %v", err)
	}
	if issue.Number != 110 {
		t.Errorf("issue.Number = %d, want 110", issue.Number)
	}

	// Simulate the --blocked-by loop: GetIssue + AddBlockedBy for each blocker.
	for _, tc := range []struct {
		blockerNumber int
		blockerNodeID string
	}{
		{1, "BLOCKER1_NODE"},
		{2, "BLOCKER2_NODE"},
	} {
		blocker, fetchErr := svc.GetIssue(context.Background(), "o", "r", tc.blockerNumber)
		if fetchErr != nil {
			t.Fatalf("GetIssue(#%d) returned unexpected error: %v", tc.blockerNumber, fetchErr)
		}
		if blocker.NodeID != tc.blockerNodeID {
			t.Errorf("blocker.NodeID = %q, want %q", blocker.NodeID, tc.blockerNodeID)
		}
		if addErr := svc.AddBlockedBy(context.Background(), issue.NodeID, blocker.NodeID); addErr != nil {
			t.Fatalf("AddBlockedBy(#%d) returned unexpected error: %v", tc.blockerNumber, addErr)
		}
	}
}

func TestIssueService_CreateSubIssue_PartialBlockedByFailure(t *testing.T) {
	// Simulates: blocker 1 resolves successfully, blocker 2 fails at AddBlockedBy.
	repoIDResp := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":111,"url":"https://github.com/o/r/issues/111"}}}}`
	parentResp := getIssueResp("PARENT_NODE", 50)
	addSubResp := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_NODE"}}}}`
	blocker1Resp := getIssueResp("BLOCKER1_NODE", 1)
	addBlocked1Resp := `{"data":{"addBlockedBy":{"clientMutationId":null}}}`
	blocker2Resp := getIssueResp("BLOCKER2_NODE", 2)
	addBlocked2Err := `{"errors":[{"message":"permission denied"}]}`

	client, cleanup := mockGraphQLServer(t,
		repoIDResp, createResp, parentResp, addSubResp,
		blocker1Resp, addBlocked1Resp,
		blocker2Resp, addBlocked2Err,
	)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, nil)
	if err != nil {
		t.Fatalf("CreateSubIssue returned unexpected error: %v", err)
	}
	if issue.Number != 111 {
		t.Errorf("issue.Number = %d, want 111", issue.Number)
	}

	// Blocker 1: succeeds.
	blocker1, err := svc.GetIssue(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatalf("GetIssue(#1) unexpected error: %v", err)
	}
	if addErr := svc.AddBlockedBy(context.Background(), issue.NodeID, blocker1.NodeID); addErr != nil {
		t.Fatalf("AddBlockedBy(#1) unexpected error: %v", addErr)
	}

	// Blocker 2: GetIssue succeeds, AddBlockedBy fails.
	blocker2, err := svc.GetIssue(context.Background(), "o", "r", 2)
	if err != nil {
		t.Fatalf("GetIssue(#2) unexpected error: %v", err)
	}
	addErr := svc.AddBlockedBy(context.Background(), issue.NodeID, blocker2.NodeID)
	if addErr == nil {
		t.Fatal("expected AddBlockedBy(#2) to fail, got nil")
	}
	if !containsStr(addErr.Error(), "add blockedBy") {
		t.Errorf("error %q does not contain 'add blockedBy'", addErr.Error())
	}
}

func TestIssueService_CreateSubIssue_BlockedByIdempotency(t *testing.T) {
	// GitHub's addBlockedBy mutation is idempotent: calling it twice with the same
	// IDs succeeds on both calls (no error, no duplicate edge created).
	repoIDResp := `{"data":{"repository":{"id":"REPO_NODE_ID"}}}`
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":112,"url":"https://github.com/o/r/issues/112"}}}}`
	parentResp := getIssueResp("PARENT_NODE", 50)
	addSubResp := `{"data":{"addSubIssue":{"issue":{"id":"PARENT_NODE"}}}}`
	blockerResp := getIssueResp("BLOCKER_NODE", 5)
	addBlockedResp := `{"data":{"addBlockedBy":{"clientMutationId":null}}}`

	// First run: create + AddBlockedBy once.
	// Second run: GetIssue again + AddBlockedBy again (idempotent).
	client, cleanup := mockGraphQLServer(t,
		repoIDResp, createResp, parentResp, addSubResp,
		blockerResp, addBlockedResp,
		blockerResp, addBlockedResp,
	)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, nil)
	if err != nil {
		t.Fatalf("CreateSubIssue returned unexpected error: %v", err)
	}

	for i := 0; i < 2; i++ {
		blocker, fetchErr := svc.GetIssue(context.Background(), "o", "r", 5)
		if fetchErr != nil {
			t.Fatalf("run %d: GetIssue(#5) unexpected error: %v", i+1, fetchErr)
		}
		if addErr := svc.AddBlockedBy(context.Background(), issue.NodeID, blocker.NodeID); addErr != nil {
			t.Fatalf("run %d: AddBlockedBy unexpected error: %v", i+1, addErr)
		}
	}
}

func TestIssueService_GetIssue_VerifyClosedState(t *testing.T) {
	response := `{"data":{"repository":{"issue":{` +
		`"id":"ISSUE_NODE_ID",` +
		`"number":42,` +
		`"title":"Test Issue",` +
		`"body":"Test body",` +
		`"state":"CLOSED",` +
		`"url":"https://github.com/owner/repo/issues/42",` +
		`"parent":{"id":"","number":0,"title":""},` +
		`"labels":{"nodes":[]},` +
		`"assignees":{"nodes":[]},` +
		`"subIssues":{"nodes":[]},` +
		`"blockedBy":{"nodes":[]},` +
		`"blocking":{"nodes":[]}` +
		`}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.GetIssue(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetIssue returned unexpected error: %v", err)
	}

	if issue.State != "CLOSED" {
		t.Errorf("Expected state CLOSED, got %s", issue.State)
	}
}

// --- UpdateIssue (broadened) Tests ---

func TestIssueService_UpdateIssue_TitleAndBody(t *testing.T) {
	resp := `{"data":{"updateIssue":{"issue":{"id":"I_42","number":42,"title":"new title","body":"new body","state":"OPEN"}}}}`
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()

	svc := NewIssueService(client)
	title := "new title"
	body := "new body"
	got, err := svc.UpdateIssue(context.Background(), "I_42", forge.UpdateIssueOptions{
		Title: &title,
		Body:  &body,
	})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if got.Title != "new title" {
		t.Errorf("Title = %q", got.Title)
	}
}

func TestIssueService_UpdateIssue_ClosesViaCloseIssueMutation(t *testing.T) {
	closeResp := `{"data":{"closeIssue":{"issue":{"id":"I_42"}}}}`
	client, cleanup := mockGraphQLServer(t, closeResp)
	defer cleanup()

	svc := NewIssueService(client)
	closed := "closed"
	got, err := svc.UpdateIssue(context.Background(), "I_42", forge.UpdateIssueOptions{State: &closed})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if got.State != "CLOSED" {
		t.Errorf("State = %q, want CLOSED", got.State)
	}
}

func TestIssueService_UpdateIssue_ReopensViaReopenMutation(t *testing.T) {
	reopenResp := `{"data":{"reopenIssue":{"issue":{"id":"I_42"}}}}`
	client, cleanup := mockGraphQLServer(t, reopenResp)
	defer cleanup()

	svc := NewIssueService(client)
	open := "opened"
	got, err := svc.UpdateIssue(context.Background(), "I_42", forge.UpdateIssueOptions{State: &open})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if got.State != "OPEN" {
		t.Errorf("State = %q, want OPEN", got.State)
	}
}

func TestIssueService_UpdateIssue_RejectsEmptyNodeID(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{}`)
	defer cleanup()
	svc := NewIssueService(client)
	if _, err := svc.UpdateIssue(context.Background(), "", forge.UpdateIssueOptions{}); err == nil {
		t.Fatal("expected error for empty nodeID")
	}
}

func TestIssueService_UpdateIssue_RejectsUnknownState(t *testing.T) {
	client, cleanup := mockGraphQLServer(t, `{}`)
	defer cleanup()
	svc := NewIssueService(client)
	bogus := "frozen"
	if _, err := svc.UpdateIssue(context.Background(), "I_42",
		forge.UpdateIssueOptions{State: &bogus}); err == nil {
		t.Fatal("expected error for unknown state")
	}
}

// --- IterateIssues Tests ---

func TestIssueService_IterateIssues_YieldsThenEOF(t *testing.T) {
	listResp := `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"id":"I_1","number":1,"title":"One","state":"OPEN","url":"u1","labels":{"nodes":[]},"milestone":{"title":""}},
		{"id":"I_2","number":2,"title":"Two","state":"OPEN","url":"u2","labels":{"nodes":[]},"milestone":{"title":""}}
	]}}}}`
	client, cleanup := mockGraphQLServer(t, listResp)
	defer cleanup()

	svc := NewIssueService(client)
	it := svc.IterateIssues(context.Background(), "owner", "repo", nil)
	defer it.Close()

	first, err := it.Next(context.Background())
	if err != nil {
		t.Fatalf("Next #1: %v", err)
	}
	if first.Number != 1 {
		t.Errorf("first.Number = %d", first.Number)
	}
	second, err := it.Next(context.Background())
	if err != nil {
		t.Fatalf("Next #2: %v", err)
	}
	if second.Number != 2 {
		t.Errorf("second.Number = %d", second.Number)
	}
	if _, err := it.Next(context.Background()); err == nil {
		t.Error("expected io.EOF after exhaustion")
	}
}
