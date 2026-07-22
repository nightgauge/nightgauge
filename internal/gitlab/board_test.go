package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

func TestBoardService_ListItems_HappyPath(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues", 200,
		"["+MarshalRawIssue(1, "First", []string{"Status::Ready", "Priority::P1"})+
			","+MarshalRawIssue(2, "Second", []string{"Status::Done", "Size::M"})+"]")
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	items, err := b.ListItems(context.Background(), "")
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}
	if items[0].Status != "Ready" {
		t.Errorf("items[0].Status = %q", items[0].Status)
	}
	if items[0].Priority != pkgtypes.PriorityP1 {
		t.Errorf("items[0].Priority = %q", items[0].Priority)
	}
	if items[1].Size != pkgtypes.SizeM {
		t.Errorf("items[1].Size = %q", items[1].Size)
	}
}

func TestBoardService_ListItems_ServerSideStatusFilter(t *testing.T) {
	srv := newStubServer(t)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "labels=Status%3A%3AReady") {
			t.Errorf("missing Status::Ready label filter in %q", r.URL.RawQuery)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[" + MarshalRawIssue(1, "Ready item", []string{"Status::Ready"}) + "]"))
	})
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	items, err := b.ListItems(context.Background(), "Ready")
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d", len(items))
	}
}

func TestBoardService_ListItems_PaginatesLinkHeader(t *testing.T) {
	srv := newStubServer(t)
	var calls int
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Link", `<`+srv.srv.URL+`/api/v4/projects/o%2Fr/issues?page=2>; rel="next"`)
			w.WriteHeader(200)
			_, _ = w.Write([]byte("[" + MarshalRawIssue(1, "page1", []string{"Status::Ready"}) + "]"))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[" + MarshalRawIssue(2, "page2", []string{"Status::Backlog"}) + "]"))
	})
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	items, err := b.ListItems(context.Background(), "")
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("len = %d, want 2", len(items))
	}
	if calls != 2 {
		t.Errorf("HTTP calls = %d, want 2 (paginated)", calls)
	}
}

func TestBoardService_ListOpenItems_FiltersToOpenedState(t *testing.T) {
	srv := newStubServer(t)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "state=opened") {
			t.Errorf("expected state=opened in %q", r.URL.RawQuery)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[" + MarshalRawIssue(1, "x", nil) + "]"))
	})
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	items, raw, err := b.ListOpenItems(context.Background())
	if err != nil {
		t.Fatalf("ListOpenItems: %v", err)
	}
	if len(items) != 1 || raw != 1 {
		t.Errorf("items=%d raw=%d", len(items), raw)
	}
}

func TestBoardService_GetItem_HappyPath(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200,
		MarshalRawIssue(42, "answer", []string{"Status::In progress", "type:epic"}))
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	got, err := b.GetItem(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.Number != 42 || got.Title != "answer" {
		t.Errorf("got %+v", got)
	}
	if got.Status != "In progress" {
		t.Errorf("Status = %q", got.Status)
	}
	if !got.IsEpic {
		t.Error("expected IsEpic=true from type:epic label")
	}
	if got.ID != "gitlab:o/r#42" {
		t.Errorf("ID = %q, want synthetic gitlab:o/r#42", got.ID)
	}
}

func TestBoardService_GetItem_404ReturnsErrNotFound(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/99", 404, `{"message":"404"}`)
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	_, err := b.GetItem(context.Background(), "o", "r", 99)
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestBoardService_CountsByStatus_AggregatesAllBuckets(t *testing.T) {
	srv := newStubServer(t)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		labels := r.URL.Query().Get("labels")
		var count int
		switch labels {
		case "Status::Ready":
			count = 3
		case "Status::In progress":
			count = 2
		case "Status::In review":
			count = 1
		case "Status::Done":
			count = 7
		case "Status::Backlog":
			count = 5
		}
		w.Header().Set("X-Total", fmt.Sprintf("%d", count))
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[]"))
	})
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	got, err := b.CountsByStatus(context.Background())
	if err != nil {
		t.Fatalf("CountsByStatus: %v", err)
	}
	if got.Ready != 3 || got.InProgress != 2 || got.InReview != 1 || got.Done != 7 || got.Backlog != 5 {
		t.Errorf("got %+v", got)
	}
}

func TestBoardService_RequiresProjectBinding(t *testing.T) {
	c := NewClient("", "tok")
	b := NewBoardService(c) // unbound — no owner/repo

	if _, err := b.ListItems(context.Background(), ""); err == nil {
		t.Error("expected error when project not configured")
	}
}

func TestRawIssueToBoardItem_DerivesFieldsFromLabels(t *testing.T) {
	raw := rawIssueList{
		IID:    1,
		Title:  "x",
		Labels: []string{"Status::Ready", "Priority::P0", "Size::L"},
	}
	item := rawIssueToBoardItem(raw, "o", "r")
	if item.Status != "Ready" {
		t.Errorf("Status = %q", item.Status)
	}
	if item.Priority != pkgtypes.PriorityP0 {
		t.Errorf("Priority = %q", item.Priority)
	}
	if item.Size != pkgtypes.SizeL {
		t.Errorf("Size = %q", item.Size)
	}
}

func TestRawIssueToBoardItem_FallsBackToLegacyLabels(t *testing.T) {
	raw := rawIssueList{
		IID:    1,
		Labels: []string{"priority:high", "size:M"},
	}
	item := rawIssueToBoardItem(raw, "o", "r")
	if item.Priority != pkgtypes.PriorityP1 {
		t.Errorf("Priority = %q (expected P1 from priority:high)", item.Priority)
	}
	if item.Size != pkgtypes.SizeM {
		t.Errorf("Size = %q", item.Size)
	}
}

// #3358: GetItem must enrich BoardItem with sub-issue + blocking link arrays
// from the /issues/:iid/links endpoint.
func TestBoardService_GetItem_PopulatesLinkArrays(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200,
		MarshalRawIssue(42, "Parent epic", []string{"Status::In progress"}))
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42/links", 200, `[
	  {"id":100,"iid":50,"state":"opened","link_type":"relates_to","title":"Child A","issue_link_id":100},
	  {"id":101,"iid":60,"state":"opened","link_type":"is_blocked_by","title":"Open blocker","issue_link_id":101},
	  {"id":102,"iid":70,"state":"closed","link_type":"is_blocked_by","title":"Closed blocker","issue_link_id":102}
	]`)
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	item, err := b.GetItem(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}

	if len(item.SubIssues) != 1 || item.SubIssues[0].Number != 50 {
		t.Errorf("SubIssues = %+v", item.SubIssues)
	}
	if !item.IsEpic {
		t.Errorf("IsEpic = false, want true (sub-issues present)")
	}
	if len(item.BlockedBy) != 1 || item.BlockedBy[0].Number != 60 {
		t.Errorf("BlockedBy = %+v (closed blocker should be filtered)", item.BlockedBy)
	}
	if len(item.Blocking) != 0 {
		t.Errorf("Blocking = %+v, want empty", item.Blocking)
	}
}

// GetItem must remain functional when the links endpoint returns an error
// — link arrays are simply empty.
func TestBoardService_GetItem_LinkFetchFailureIsNonFatal(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200,
		MarshalRawIssue(42, "Issue 42", nil))
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42/links", 500, `{"message":"internal error"}`)
	c := NewClient(srv.srv.URL, "tok")
	b := NewBoardServiceFor(c, "o", "r")

	item, err := b.GetItem(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v (link fetch failure should be non-fatal)", err)
	}
	if item == nil || item.Number != 42 {
		t.Fatalf("item = %+v", item)
	}
	if len(item.SubIssues) != 0 || len(item.BlockedBy) != 0 {
		t.Errorf("expected empty link arrays on link fetch failure, got SubIssues=%v BlockedBy=%v",
			item.SubIssues, item.BlockedBy)
	}
}
