package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

func TestAddIssueLink_PostsLinkBody(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("POST", "/api/v4/projects/o%2Fr/issues/42/links", 201, `{"link_type":"relates_to"}`)
	c := NewClient(srv.srv.URL, "tok")

	if err := addIssueLink(context.Background(), c, "o", "r", 42, "o", "r", 43, linkTypeRelatesTo); err != nil {
		t.Fatalf("addIssueLink: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(srv.lastBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["target_project_id"] != "o/r" {
		t.Errorf("target_project_id = %v, want o/r", body["target_project_id"])
	}
	if body["target_issue_iid"].(float64) != 43 {
		t.Errorf("target_issue_iid = %v, want 43", body["target_issue_iid"])
	}
	if body["link_type"] != "relates_to" {
		t.Errorf("link_type = %v, want relates_to", body["link_type"])
	}
}

func TestAddIssueLink_409IsIdempotentSuccess(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("POST", "/api/v4/projects/o%2Fr/issues/42/links", 409, `{"message":"already related"}`)
	c := NewClient(srv.srv.URL, "tok")

	if err := addIssueLink(context.Background(), c, "o", "r", 42, "o", "r", 43, linkTypeRelatesTo); err != nil {
		t.Fatalf("expected nil on 409, got %v", err)
	}
}

func TestAddIssueLink_404IsError(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("POST", "/api/v4/projects/o%2Fr/issues/42/links", 404, `{"message":"not found"}`)
	c := NewClient(srv.srv.URL, "tok")

	err := addIssueLink(context.Background(), c, "o", "r", 42, "o", "r", 43, linkTypeRelatesTo)
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound chain", err)
	}
}

func TestListIssueLinks_DecodesPayload(t *testing.T) {
	body := `[
	  {"id":1001,"iid":43,"project_id":5,"title":"Child","state":"opened","web_url":"u","link_type":"relates_to","issue_link_id":77},
	  {"id":1002,"iid":44,"project_id":5,"title":"Blocker","state":"opened","web_url":"u","link_type":"is_blocked_by","issue_link_id":78}
	]`
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42/links", 200, body)
	c := NewClient(srv.srv.URL, "tok")

	links, err := listIssueLinks(context.Background(), c, "o", "r", 42)
	if err != nil {
		t.Fatalf("listIssueLinks: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("len = %d, want 2", len(links))
	}
	if links[0].LinkType != "relates_to" || links[0].IID != 43 {
		t.Errorf("links[0] = %+v", links[0])
	}
	if links[0].linkID() != 77 {
		t.Errorf("linkID() = %d, want 77", links[0].linkID())
	}
}

func TestListIssueLinks_FallsBackToIDWhenIssueLinkIDMissing(t *testing.T) {
	body := `[{"id":1001,"iid":43,"state":"opened","link_type":"relates_to"}]`
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42/links", 200, body)
	c := NewClient(srv.srv.URL, "tok")

	links, err := listIssueLinks(context.Background(), c, "o", "r", 42)
	if err != nil {
		t.Fatalf("listIssueLinks: %v", err)
	}
	if links[0].linkID() != 1001 {
		t.Errorf("fallback linkID() = %d, want 1001", links[0].linkID())
	}
}

func TestDeleteIssueLink_HitsCorrectPath(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("DELETE", "/api/v4/projects/o%2Fr/issues/42/links/77", 200, "{}")
	c := NewClient(srv.srv.URL, "tok")

	if err := deleteIssueLink(context.Background(), c, "o", "r", 42, 77); err != nil {
		t.Fatalf("deleteIssueLink: %v", err)
	}
}

func TestFindLinkID_ReturnsZeroWhenAbsent(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42/links", 200, `[]`)
	c := NewClient(srv.srv.URL, "tok")

	id, err := findLinkID(context.Background(), c, "o", "r", 42, 99, linkTypeRelatesTo)
	if err != nil {
		t.Fatalf("findLinkID: %v", err)
	}
	if id != 0 {
		t.Errorf("id = %d, want 0", id)
	}
}

func TestFindLinkID_MatchesByIIDAndType(t *testing.T) {
	body := `[
	  {"id":1,"iid":43,"state":"opened","link_type":"relates_to","issue_link_id":77},
	  {"id":2,"iid":43,"state":"opened","link_type":"is_blocked_by","issue_link_id":78}
	]`
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42/links", 200, body)
	c := NewClient(srv.srv.URL, "tok")

	id, err := findLinkID(context.Background(), c, "o", "r", 42, 43, linkTypeIsBlockedBy)
	if err != nil {
		t.Fatalf("findLinkID: %v", err)
	}
	if id != 78 {
		t.Errorf("id = %d, want 78", id)
	}
}

func TestClassifyLinks_PartitionsByType(t *testing.T) {
	links := []rawIssueLink{
		{ID: 100, IID: 1, Title: "Child A", State: "opened", LinkType: "relates_to"},
		{ID: 101, IID: 2, Title: "Child B (closed)", State: "closed", LinkType: "relates_to"},
		{ID: 102, IID: 3, Title: "Open blocker", State: "opened", LinkType: "is_blocked_by"},
		{ID: 103, IID: 4, Title: "Closed blocker", State: "closed", LinkType: "is_blocked_by"},
		{ID: 104, IID: 5, Title: "Downstream", State: "opened", LinkType: "blocks"},
	}
	subIssues, blockedBy, blocking := classifyLinks("o", "r", links)

	// SubIssues includes both opened and closed relates_to (closed sub-issues are still tracked)
	if len(subIssues) != 2 {
		t.Errorf("subIssues = %d, want 2", len(subIssues))
	}
	if subIssues[0].Number != 1 || subIssues[0].Repo != "o/r" {
		t.Errorf("subIssues[0] = %+v", subIssues[0])
	}

	// BlockedBy filters to opened only — matches GitHub's isBlocked semantics
	if len(blockedBy) != 1 {
		t.Fatalf("blockedBy = %d, want 1 (open-state filter)", len(blockedBy))
	}
	if blockedBy[0].Number != 3 {
		t.Errorf("blockedBy[0].Number = %d, want 3", blockedBy[0].Number)
	}

	// Blocking is reported regardless of state — caller decides what to do
	if len(blocking) != 1 || blocking[0].Number != 5 {
		t.Errorf("blocking = %+v", blocking)
	}
}

func TestTryWriteParentID_PUTsField(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/issues/43", 200, `{"iid":43}`)
	c := NewClient(srv.srv.URL, "tok")

	if err := tryWriteParentID(context.Background(), c, "o", "r", 43, 42); err != nil {
		t.Fatalf("tryWriteParentID: %v", err)
	}
	if !strings.Contains(string(srv.lastBody), `"parent_id":42`) {
		t.Errorf("body did not contain parent_id=42: %s", srv.lastBody)
	}
}

// Validate that listIssueLinks paginates correctly via Link header.
func TestListIssueLinks_Paginates(t *testing.T) {
	srv := newStubServer(t)
	calls := 0
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42/links", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "2" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`[{"id":2,"iid":44,"state":"opened","link_type":"relates_to"}]`))
			return
		}
		w.Header().Set("Link", "<"+srv.srv.URL+"/api/v4/projects/o%2Fr/issues/42/links?page=2>; rel=\"next\"")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[{"id":1,"iid":43,"state":"opened","link_type":"relates_to"}]`))
	})
	c := NewClient(srv.srv.URL, "tok")

	links, err := listIssueLinks(context.Background(), c, "o", "r", 42)
	if err != nil {
		t.Fatalf("listIssueLinks: %v", err)
	}
	if len(links) != 2 {
		t.Errorf("len = %d, want 2", len(links))
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}
