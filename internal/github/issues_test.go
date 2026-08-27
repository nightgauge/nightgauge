package github

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	return mockForgeServer(t, nil, responses...)
}

// mockForgeServer serves both transports the adapter now uses. GraphQL POSTs
// (path "/") are answered from `graphqlResponses` IN ORDER, exactly as before;
// REST calls are answered from `rest`, keyed by METHOD AND PATH without the
// query string (e.g. "GET /repos/o/r/labels", "POST /repos/o/r/issues/1/sub_issues").
//
// The two are kept in separate namespaces on purpose. #849 moved
// Client.GetRepositoryID and the repo-label read to REST, and a single
// positional list cannot express that: the REST call happens at a position
// that depends on cache state, so any test whose chain touches a cached read
// would silently consume the wrong entry and assert against a body meant for
// another call. Routing by path makes each fixture name the call it answers.
//
// **The method is part of the key, and it has to be.** #956 moved the four
// issue-link mutations to REST, and every one of them writes to a path the
// same test also READS — `POST /repos/o/r/issues/1/sub_issues` against
// `GET /repos/o/r/issues/1`. Keyed by path alone the fake cannot tell them
// apart, so a test could assert a write happened while the fixture it matched
// was the read's.
//
// An unregistered REST call is a test failure, not a 404 the code under test
// gets to interpret — a missing fixture must read as "this test did not say
// what that call returns", never as "the repository has no labels".
func mockForgeServer(t *testing.T, rest map[string]string, graphqlResponses ...string) (*Client, func()) {
	t.Helper()
	client, _, cleanup := mockForgeServerRecording(t, rest, graphqlResponses...)
	return client, cleanup
}

// mockForgeServerRecording is mockForgeServer plus the observed request log.
//
// The returned slice pointer records every call as "METHOD path", in order.
// Chained tests need it: mockForgeServer repeats its LAST GraphQL response
// once the positional list is exhausted, so deleting a read from a chain
// silently reindexes the remaining fixtures and the test passes against the
// wrong body. Asserting the observed order is what makes that visible —
// without it, "the test still passes" is not evidence the chain is intact.
func mockForgeServerRecording(t *testing.T, rest map[string]string, graphqlResponses ...string) (*Client, *[]string, func()) {
	t.Helper()
	var callIdx int32
	var mu sync.Mutex
	seen := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			key := r.Method + " " + r.URL.Path
			mu.Lock()
			*seen = append(*seen, key)
			mu.Unlock()
			body, ok := rest[key]
			if !ok {
				t.Errorf("mockForgeServer: no REST fixture registered for %s", key)
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"no fixture"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
			return
		}
		mu.Lock()
		*seen = append(*seen, "POST /graphql")
		mu.Unlock()
		idx := int(atomic.AddInt32(&callIdx, 1)) - 1
		if idx >= len(graphqlResponses) {
			idx = len(graphqlResponses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, graphqlResponses[idx])
	}))
	client := NewClientWithURL("test-token", srv.URL)
	return client, seen, srv.Close
}

// restRepoIDFixture is the REST body for GET /repos/{o}/{r} — the node ID read
// that used to be a GraphQL query (#849).
const restRepoIDFixture = `{"node_id":"REPO_NODE_ID"}`

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

// --- Sub-issue and dependency link tests (REST, #956) ---

// linkWriteServer serves the child/blocker resolution GET and records every
// request as "METHOD path <body>". The body is recorded because the database
// id travels in it for three of the four endpoints, and a test that asserts
// only the path cannot tell a call that sent the right id from one that sent
// the issue NUMBER instead -- the exact confusion these endpoints invite.
func linkWriteServer(t *testing.T, writeStatus int, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		entry := r.Method + " " + r.URL.Path
		if len(bytes.TrimSpace(body)) > 0 {
			entry += " " + string(bytes.TrimSpace(body))
		}
		*seen = append(*seen, entry)

		// A plain GET of an issue is a resolution; anything else is the write.
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(issueJSON(refNumberFromPath(r.URL.Path), "I_node", "")))
			return
		}
		w.WriteHeader(writeStatus)
		if writeStatus >= 300 {
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
}

// refNumberFromPath pulls the trailing issue number out of a resolution path.
func refNumberFromPath(path string) int {
	seg := path[strings.LastIndex(path, "/")+1:]
	n, err := strconv.Atoi(seg)
	if err != nil {
		return 0
	}
	return n
}

func ref(owner, repo string, number int) forge.IssueRef {
	return forge.IssueRef{Owner: owner, Repo: repo, Number: number}
}

// Each of these asserts the TRANSPORT, not merely the outcome. A GraphQL
// implementation would satisfy an error-free return just as well, which is
// exactly how a migration regresses unnoticed -- see the convention set by
// TestGetRepositoryID_UsesREST (#849).

func TestAddSubIssue_UsesREST(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusCreated, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	if err := svc.AddSubIssue(context.Background(), ref("o", "r", 1), ref("o", "r", 2)); err != nil {
		t.Fatalf("AddSubIssue: %v", err)
	}
	want := []string{
		"GET /repos/o/r/issues/2",
		fmt.Sprintf(`POST /repos/o/r/issues/1/sub_issues {"sub_issue_id":%d}`, databaseIDFor(2)),
	}
	assertRequests(t, seen, want)
}

// The parent is addressed by NUMBER and the child by DATABASE ID. Their values
// differ in the fixtures on purpose, so a call that swapped them would fail
// here rather than pass by coincidence.
func TestAddSubIssue_SendsTheChildsDatabaseIDNotItsNumber(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusCreated, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	if err := svc.AddSubIssue(context.Background(), ref("o", "r", 1), ref("o", "r", 2)); err != nil {
		t.Fatalf("AddSubIssue: %v", err)
	}
	for _, entry := range seen {
		if strings.HasPrefix(entry, "POST") && strings.Contains(entry, `"sub_issue_id":2}`) {
			t.Errorf("request sent the child's NUMBER as sub_issue_id: %s", entry)
		}
	}
}

// `sub_issue` SINGULAR on the delete path where the add path is `sub_issues`
// plural. GitHub's asymmetry, not a typo -- pinned so nobody tidies it.
func TestRemoveSubIssue_UsesRESTWithTheSingularPath(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusOK, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	if err := svc.RemoveSubIssue(context.Background(), ref("o", "r", 1), ref("o", "r", 2)); err != nil {
		t.Fatalf("RemoveSubIssue: %v", err)
	}
	want := []string{
		"GET /repos/o/r/issues/2",
		fmt.Sprintf(`DELETE /repos/o/r/issues/1/sub_issue {"sub_issue_id":%d}`, databaseIDFor(2)),
	}
	assertRequests(t, seen, want)
}

func TestAddBlockedBy_UsesREST(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusCreated, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	if err := svc.AddBlockedBy(context.Background(), ref("o", "r", 1), ref("o", "r", 2)); err != nil {
		t.Fatalf("AddBlockedBy: %v", err)
	}
	want := []string{
		"GET /repos/o/r/issues/2",
		fmt.Sprintf(`POST /repos/o/r/issues/1/dependencies/blocked_by {"issue_id":%d}`, databaseIDFor(2)),
	}
	assertRequests(t, seen, want)
}

// The odd one out: the blocker's database id goes in the PATH and no body is
// sent at all.
func TestRemoveBlockedBy_UsesRESTWithTheIDInThePath(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusNoContent, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	if err := svc.RemoveBlockedBy(context.Background(), ref("o", "r", 1), ref("o", "r", 2)); err != nil {
		t.Fatalf("RemoveBlockedBy: %v", err)
	}
	want := []string{
		"GET /repos/o/r/issues/2",
		fmt.Sprintf("DELETE /repos/o/r/issues/1/dependencies/blocked_by/%d", databaseIDFor(2)),
	}
	assertRequests(t, seen, want)
}

// Cross-repository linking is a live path (internal/audit's issue creator
// resolves the epic's repo and the sub-issue's repo independently), so the two
// refs must be able to name different repositories.
func TestAddSubIssue_LinksAcrossRepositories(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusCreated, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	if err := svc.AddSubIssue(context.Background(), ref("o", "parent-repo", 1), ref("o", "child-repo", 2)); err != nil {
		t.Fatalf("AddSubIssue: %v", err)
	}
	want := []string{
		"GET /repos/o/child-repo/issues/2",
		fmt.Sprintf(`POST /repos/o/parent-repo/issues/1/sub_issues {"sub_issue_id":%d}`, databaseIDFor(2)),
	}
	assertRequests(t, seen, want)
}

// A 404 from a link route means the REFERENCED ISSUE is absent, not that the
// route is -- GitHub matched the route or the request would not have reached
// the handler. The distinction matters: read as "endpoint missing" it would
// invite a fallback to the GraphQL path this change deleted.
func TestLinkWrites_404NamesTheAbsentIssueNotTheRoute(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*IssueService) error
	}{
		{"AddSubIssue", func(s *IssueService) error {
			return s.AddSubIssue(context.Background(), ref("o", "r", 1), ref("o", "r", 2))
		}},
		{"RemoveSubIssue", func(s *IssueService) error {
			return s.RemoveSubIssue(context.Background(), ref("o", "r", 1), ref("o", "r", 2))
		}},
		{"AddBlockedBy", func(s *IssueService) error {
			return s.AddBlockedBy(context.Background(), ref("o", "r", 1), ref("o", "r", 2))
		}},
		{"RemoveBlockedBy", func(s *IssueService) error {
			return s.RemoveBlockedBy(context.Background(), ref("o", "r", 1), ref("o", "r", 2))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			srv := linkWriteServer(t, http.StatusNotFound, &seen)
			defer srv.Close()

			err := tc.call(NewIssueService(newClientForRESTTest(srv)))
			if err == nil {
				t.Fatal("a 404 from the link endpoint must be an error, not a silent no-op")
			}
			if !strings.Contains(err.Error(), "absent") {
				t.Errorf("error = %q, want it to report the referenced issue as absent", err)
			}
			if !strings.Contains(err.Error(), "o/r#1") || !strings.Contains(err.Error(), "o/r#2") {
				t.Errorf("error = %q, want it to name both refs so the caller knows which is missing", err)
			}
		})
	}
}

// A non-404 failure must not be reported as an absent issue.
func TestLinkWrites_ServerErrorIsNotReportedAsAbsence(t *testing.T) {
	var seen []string
	srv := linkWriteServer(t, http.StatusInternalServerError, &seen)
	defer srv.Close()

	err := NewIssueService(newClientForRESTTest(srv)).
		AddSubIssue(context.Background(), ref("o", "r", 1), ref("o", "r", 2))
	if err == nil {
		t.Fatal("a 500 must be an error")
	}
	if strings.Contains(err.Error(), "absent") {
		t.Errorf("error = %q, want it NOT to claim the issue is absent", err)
	}
}

func assertRequests(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("requests =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d =\n  %s\nwant\n  %s", i, got[i], want[i])
		}
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

// syncStatusLabelRepoLabelsFixture is the REST body for
// GET /repos/o/r/labels — the repo-label read moved off GraphQL by #849. The
// node IDs are unchanged, because REST reports `node_id` itself and the label
// mutations that consume them still run on GraphQL.
func syncStatusLabelRepoLabelsFixture() string {
	return `[
		{"node_id":"LABEL_STATUS_IN_PROGRESS","name":"status:In Progress"},
		{"node_id":"LABEL_STATUS_DONE","name":"status:Done"},
		{"node_id":"LABEL_STATUS_READY","name":"status:Ready"},
		{"node_id":"LABEL_BUG","name":"bug"}
	]`
}

func TestIssueService_SyncStatusLabel_ReplacesExistingStatus(t *testing.T) {
	// Chain: GetIssue (GraphQL) → getRepoLabels (REST) → RemoveLabels → AddLabels
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/o/r/labels": syncStatusLabelRepoLabelsFixture()},
		syncStatusLabelGetIssueResponse("status:In Progress"),
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
	// Chain: GetIssue (no status labels) → getRepoLabels (REST) → AddLabels only
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/o/r/labels": syncStatusLabelRepoLabelsFixture()},
		syncStatusLabelGetIssueResponse(""),
		`{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`,
	)
	defer cleanup()

	svc := NewIssueService(client)
	if err := svc.SyncStatusLabel(context.Background(), "o", "r", 5, "Ready"); err != nil {
		t.Errorf("SyncStatusLabel returned unexpected error: %v", err)
	}
}

func TestIssueService_SyncStatusLabel_LabelNotInRepo(t *testing.T) {
	// GetIssue → getRepoLabels (REST) → error: target label not found
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/o/r/labels": syncStatusLabelRepoLabelsFixture()},
		syncStatusLabelGetIssueResponse(""),
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
	getLabelsResp := `[
		{"node_id":"LABEL_REFINED_ID","name":"pipeline:refined"},
		{"node_id":"LABEL_EPIC_ID","name":"type:epic"}
	]`
	addLabelsResp := `{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`

	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/o/r/labels": getLabelsResp},
		getIssueResp, addLabelsResp)
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
	getLabelsResp := `[
		{"node_id":"LABEL_OTHER_ID","name":"type:feature"}
	]`

	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/o/r/labels": getLabelsResp},
		getIssueResp)
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
	// Sequence: GetRepositoryID (REST) -> CreateIssue (GraphQL) ->
	//           resolve child (REST) -> link (REST).
	//
	// The GetIssue(parent) that used to sit between CreateIssue and the link
	// is GONE: the sub-issue endpoint addresses the parent by number. Its
	// absence from the observed order below is the assertion.
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":101,"url":"https://github.com/o/r/issues/101"}}}}`

	client, seen, cleanup := mockForgeServerRecording(t,
		map[string]string{
			"GET /repos/o/r":                       restRepoIDFixture,
			"GET /repos/o/r/issues/101":            issueJSON(101, "NEW_NODE", ""),
			"POST /repos/o/r/issues/50/sub_issues": `{}`,
		},
		createResp)
	defer cleanup()

	svc := NewIssueService(client)
	issue, err := svc.CreateSubIssue(context.Background(), "o", "r", 50, "Sub", "", nil, nil)
	if err != nil {
		t.Fatalf("CreateSubIssue returned unexpected error: %v", err)
	}
	if issue.Number != 101 {
		t.Errorf("issue.Number = %d, want 101", issue.Number)
	}
	assertRequests(t, *seen, []string{
		"GET /repos/o/r",
		"POST /graphql",
		"GET /repos/o/r/issues/101",
		"POST /repos/o/r/issues/50/sub_issues",
	})
}

func TestIssueService_CreateSubIssue_WithProjectSvc(t *testing.T) {
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":102,"url":"https://github.com/o/r/issues/102"}}}}`
	// ensureFields: org project query
	fieldsResp := `{"data":{"organization":{"projectV2":{"id":"PROJ_ID","fields":{"nodes":[]}}}}}`
	// AddIssueByNumber calls GetIssue internally
	newIssueResp := getIssueResp("NEW_NODE", 102)
	addItemResp := `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM_ID"}}}}`

	// AddIssueByNumber order: GetIssue(new) -> ensureFields -> AddItem
	client, cleanup := mockForgeServer(t,
		map[string]string{
			"GET /repos/o/r":                       restRepoIDFixture,
			"GET /repos/o/r/issues/102":            issueJSON(102, "NEW_NODE", ""),
			"POST /repos/o/r/issues/50/sub_issues": `{}`,
		},
		createResp,
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
	createResp := `{"data":{"createIssue":{"issue":{"id":"NEW_NODE","number":103,"url":"https://github.com/o/r/issues/103"}}}}`
	fieldsResp := `{"data":{"organization":{"projectV2":{"id":"PROJ_ID","fields":{"nodes":[]}}}}}`
	newIssueResp := getIssueResp("NEW_NODE", 103)
	addItemErr := `{"errors":[{"message":"project not found"}]}`

	client, cleanup := mockForgeServer(t,
		map[string]string{
			"GET /repos/o/r":                       restRepoIDFixture,
			"GET /repos/o/r/issues/103":            issueJSON(103, "NEW_NODE", ""),
			"POST /repos/o/r/issues/50/sub_issues": `{}`,
		},
		createResp,
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

// --- The `create-sub --blocked-by` loop ---
//
// These used to drive a GraphQL response sequence, because the loop was
// GetIssue(blocker) + addBlockedBy(mutation). After #956 there is no GraphQL in
// this loop at all -- the blocker is resolved and linked over REST -- so a
// GraphQL-sequence fake would be asserting against a transport the code no
// longer speaks. They drive the REST server instead.
//
// The GetIssue(blocker) call is gone too: AddBlockedBy resolves the blocker's
// database id itself, so the CLI loop no longer reads the blocker first.

// linkWriteScriptServer answers writes with `writeStatuses` IN ORDER, so a test
// can say "the first link succeeds and the second fails" -- which a per-path
// status cannot express, since both writes go to the same path.
func linkWriteScriptServer(t *testing.T, writeStatuses []int, seen *[]string) *httptest.Server {
	t.Helper()
	var writeIdx int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		entry := r.Method + " " + r.URL.Path
		if len(bytes.TrimSpace(body)) > 0 {
			entry += " " + string(bytes.TrimSpace(body))
		}
		*seen = append(*seen, entry)

		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(issueJSON(refNumberFromPath(r.URL.Path), "I_node", "")))
			return
		}
		status := http.StatusCreated
		if writeIdx < len(writeStatuses) {
			status = writeStatuses[writeIdx]
		}
		writeIdx++
		w.WriteHeader(status)
		if status >= 300 {
			_, _ = w.Write([]byte(`{"message":"permission denied"}`))
		}
	}))
}

func TestAddBlockedBy_LoopLinksEachBlocker(t *testing.T) {
	var seen []string
	srv := linkWriteScriptServer(t, []int{http.StatusCreated, http.StatusCreated}, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	for _, blockerNumber := range []int{1, 2} {
		if err := svc.AddBlockedBy(context.Background(), ref("o", "r", 110), ref("o", "r", blockerNumber)); err != nil {
			t.Fatalf("AddBlockedBy(#%d): %v", blockerNumber, err)
		}
	}
	assertRequests(t, seen, []string{
		"GET /repos/o/r/issues/1",
		fmt.Sprintf(`POST /repos/o/r/issues/110/dependencies/blocked_by {"issue_id":%d}`, databaseIDFor(1)),
		"GET /repos/o/r/issues/2",
		fmt.Sprintf(`POST /repos/o/r/issues/110/dependencies/blocked_by {"issue_id":%d}`, databaseIDFor(2)),
	})
}

// One blocker failing must not stop the others, and must be attributable: the
// CLI accumulates per-blocker errors rather than aborting the loop.
func TestAddBlockedBy_PartialFailureIsPerBlocker(t *testing.T) {
	var seen []string
	srv := linkWriteScriptServer(t, []int{http.StatusCreated, http.StatusForbidden}, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))

	if err := svc.AddBlockedBy(context.Background(), ref("o", "r", 111), ref("o", "r", 1)); err != nil {
		t.Fatalf("AddBlockedBy(#1) unexpected error: %v", err)
	}
	err := svc.AddBlockedBy(context.Background(), ref("o", "r", 111), ref("o", "r", 2))
	if err == nil {
		t.Fatal("expected AddBlockedBy(#2) to fail, got nil")
	}
	if !containsStr(err.Error(), "add blockedBy") {
		t.Errorf("error %q does not contain 'add blockedBy'", err.Error())
	}
	// A 403 is not a 404, and must not be reported as an absent issue.
	if containsStr(err.Error(), "absent") {
		t.Errorf("error %q claims the issue is absent on a 403", err.Error())
	}
}

// Linking the same pair twice succeeds both times -- the endpoint is
// idempotent, so the CLI need not check first.
func TestAddBlockedBy_IsIdempotent(t *testing.T) {
	var seen []string
	srv := linkWriteScriptServer(t, []int{http.StatusCreated, http.StatusCreated}, &seen)
	defer srv.Close()

	svc := NewIssueService(newClientForRESTTest(srv))
	for i := 0; i < 2; i++ {
		if err := svc.AddBlockedBy(context.Background(), ref("o", "r", 112), ref("o", "r", 5)); err != nil {
			t.Fatalf("run %d: AddBlockedBy unexpected error: %v", i+1, err)
		}
	}
	if len(seen) != 4 {
		t.Errorf("requests = %v, want two resolve+write pairs", seen)
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

// TestIssueService_ListIssuesExcludingLabels_TruncatedLabelsExcluded proves the
// fail-closed guard on a partial label page (#993).
//
// The exclusion is computed from the labels the query RETURNS. When the labels
// connection is truncated, an issue that genuinely carries pipeline:refined can
// come back without it and read as a refinement candidate — and refinement then
// rewrites a human-reviewed body again, every cycle. The safe reading of "I
// could not see all the labels" is "assume it is excluded".
func TestIssueService_ListIssuesExcludingLabels_TruncatedLabelsExcluded(t *testing.T) {
	// Issue 1: totalCount exceeds the returned nodes — truncated, must be
	// excluded even though none of the VISIBLE labels is in the exclude set.
	// Issue 2: complete label list, none excluded — must survive.
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"number":1,"title":"Heavily Labelled","createdAt":"2026-01-01T00:00:00Z",
			 "labels":{"totalCount":21,"nodes":[{"name":"type:feature"},{"name":"priority:high"}]}},
			{"number":2,"title":"Ordinary","createdAt":"2026-01-02T00:00:00Z",
			 "labels":{"totalCount":1,"nodes":[{"name":"type:feature"}]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssuesExcludingLabels(context.Background(), "o", "r",
		[]string{LabelRefined, LabelEpic}, 0)
	if err != nil {
		t.Fatalf("ListIssuesExcludingLabels returned unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("count = %d, want 1 — the truncated issue must be excluded", len(issues))
	}
	if issues[0].Number != 2 {
		t.Errorf("surviving issue = #%d, want #2; #1's label list was truncated and "+
			"cannot be proven free of the excluded labels", issues[0].Number)
	}
}

// TestIssueService_ListIssuesExcludingLabels_CompleteListNotExcluded guards the
// other direction: totalCount equal to the node count is NOT truncation, and
// must not cause a wholesale exclusion. Without this, a guard written as
// `totalCount >= len(nodes)` would silently exclude every issue and refinement
// would stop entirely — a fix that trades a runaway loop for a dead loop.
func TestIssueService_ListIssuesExcludingLabels_CompleteListNotExcluded(t *testing.T) {
	response := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"number":7,"title":"Exactly Complete","createdAt":"2026-01-01T00:00:00Z",
			 "labels":{"totalCount":2,"nodes":[{"name":"type:feature"},{"name":"priority:high"}]}}
		]
	}}}}`

	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewIssueService(client)
	issues, err := svc.ListIssuesExcludingLabels(context.Background(), "o", "r",
		[]string{LabelRefined, LabelEpic}, 0)
	if err != nil {
		t.Fatalf("ListIssuesExcludingLabels returned unexpected error: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != 7 {
		t.Fatalf("got %d issue(s) %v, want exactly #7 — a complete label list is not truncation",
			len(issues), issues)
	}
}
