package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// stubGitLabServer wires up a programmable httptest.Server. The handler
// dispatches by method+path so individual tests can register their own
// behaviour.
type stubGitLabServer struct {
	t        *testing.T
	mux      *http.ServeMux
	srv      *httptest.Server
	mu       sync.Mutex
	lastReq  *http.Request
	lastBody []byte
}

func newStubServer(t *testing.T) *stubGitLabServer {
	t.Helper()
	s := &stubGitLabServer{t: t, mux: http.NewServeMux()}
	wrap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			s.mu.Lock()
			s.lastBody = b
			s.lastReq = r
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.lastReq = r
			s.mu.Unlock()
		}
		s.mux.ServeHTTP(w, r)
	})
	s.srv = httptest.NewServer(wrap)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubGitLabServer) handle(method, path string, status int, body string) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			s.t.Errorf("path %s: method = %s, want %s", path, r.Method, method)
		}
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	})
}

const sampleIssueJSON = `{
  "id": 1001,
  "iid": 42,
  "project_id": 5,
  "title": "Sample issue",
  "description": "Body text",
  "state": "opened",
  "labels": ["bug","priority:high"],
  "web_url": "https://gitlab.example.com/o/r/-/issues/42",
  "assignees": [{"id":7,"username":"alice"}],
  "milestone": {"title":"v1.0"}
}`

func TestGetIssue_HappyPath(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.GetIssue(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Number != 42 {
		t.Errorf("Number = %d, want 42", got.Number)
	}
	if got.NodeID != "1001" {
		t.Errorf("NodeID = %q, want 1001", got.NodeID)
	}
	if got.Title != "Sample issue" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.State != "opened" {
		t.Errorf("State = %q", got.State)
	}
	if got.Repo != "o/r" {
		t.Errorf("Repo = %q", got.Repo)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "bug" {
		t.Errorf("Labels = %v", got.Labels)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != "alice" {
		t.Errorf("Assignees = %v", got.Assignees)
	}
	if got.Milestone != "v1.0" {
		t.Errorf("Milestone = %q", got.Milestone)
	}
}

func TestGetIssue_404ReturnsErrNotFound(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/99", 404, `{"message":"404"}`)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	_, err := svc.GetIssue(context.Background(), "o", "r", 99)
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetIssue_403ReturnsErrPermissionDenied(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/1", 403, `{"message":"forbidden"}`)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	_, err := svc.GetIssue(context.Background(), "o", "r", 1)
	if !errors.Is(err, forge.ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestCreateIssue_PostsTitleAndLabels(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("POST", "/api/v4/projects/o%2Fr/issues", 201, sampleIssueJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.CreateIssue(context.Background(), "o/r", "Sample issue", "Body text", []string{"bug", "priority:high"})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if got.Number != 42 {
		t.Errorf("Number = %d", got.Number)
	}

	var body map[string]any
	if err := json.Unmarshal(srv.lastBody, &body); err != nil {
		t.Fatalf("decode posted body: %v", err)
	}
	if body["title"] != "Sample issue" {
		t.Errorf("posted title = %v", body["title"])
	}
	if body["description"] != "Body text" {
		t.Errorf("posted description = %v", body["description"])
	}
	if body["labels"] != "bug,priority:high" {
		t.Errorf("posted labels = %v", body["labels"])
	}
}

func TestCreateIssue_RejectsBadRepoID(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	_, err := svc.CreateIssue(context.Background(), "no-slash", "t", "b", nil)
	if err == nil || !strings.Contains(err.Error(), "owner/repo") {
		t.Errorf("expected error about owner/repo, got %v", err)
	}
}

func TestUpdateIssue_PatchesAndCloses(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	title := "Updated title"
	closed := "closed"
	got, err := svc.UpdateIssue(context.Background(), "o/r#42", forge.UpdateIssueOptions{
		Title: &title,
		State: &closed,
	})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}

	var body map[string]any
	if err := json.Unmarshal(srv.lastBody, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["title"] != "Updated title" {
		t.Errorf("title = %v", body["title"])
	}
	if body["state_event"] != "close" {
		t.Errorf("state_event = %v, want close", body["state_event"])
	}
}

func TestUpdateIssue_RejectsBadRef(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	_, err := svc.UpdateIssue(context.Background(), "no-hash", forge.UpdateIssueOptions{})
	if err == nil {
		t.Fatal("expected error for malformed ref")
	}
}

func TestUpdateIssue_RejectsUnknownState(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	bogus := "frozen"
	_, err := svc.UpdateIssue(context.Background(), "o/r#1", forge.UpdateIssueOptions{State: &bogus})
	if err == nil {
		t.Fatal("expected error for unknown state")
	}
}

func TestCloseIssue_SendsCloseEvent(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.CloseIssue(context.Background(), "o/r#42"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastBody, &body)
	if body["state_event"] != "close" {
		t.Errorf("state_event = %v", body["state_event"])
	}
}

func TestReopenIssue_SendsReopenEvent(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.ReopenIssue(context.Background(), "o/r#42"); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastBody, &body)
	if body["state_event"] != "reopen" {
		t.Errorf("state_event = %v", body["state_event"])
	}
}

func TestListIssues_FiltersByState(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues", 200, "["+sampleIssueJSON+"]")
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.ListIssues(context.Background(), "o", "r", []string{"bug"})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if !strings.Contains(srv.lastReq.URL.RawQuery, "state=opened") {
		t.Errorf("missing state=opened in %q", srv.lastReq.URL.RawQuery)
	}
	if !strings.Contains(srv.lastReq.URL.RawQuery, "labels=bug") {
		t.Errorf("missing labels=bug in %q", srv.lastReq.URL.RawQuery)
	}
}

func TestIterateIssues_YieldsThenEOF(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues", 200, "["+sampleIssueJSON+"]")
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	it := svc.IterateIssues(context.Background(), "o", "r", nil)
	defer it.Close()

	first, err := it.Next(context.Background())
	if err != nil {
		t.Fatalf("Next #1: %v", err)
	}
	if first.Number != 42 {
		t.Errorf("first.Number = %d", first.Number)
	}

	if _, err := it.Next(context.Background()); err != io.EOF {
		t.Errorf("Next #2: want io.EOF, got %v", err)
	}

	// Close is idempotent.
	if err := it.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := it.Close(); err != nil {
		t.Errorf("Close (2nd): %v", err)
	}

	// Post-close Next returns io.EOF, not the original yielded value.
	if _, err := it.Next(context.Background()); err != io.EOF {
		t.Errorf("post-close Next: want EOF, got %v", err)
	}
}

func TestGetIssuesByNumbers_DedupesAndSkips404(t *testing.T) {
	srv := newStubServer(t)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/", func(w http.ResponseWriter, r *http.Request) {
		// Number 99 → 404, others → ok with sample
		if strings.HasSuffix(r.URL.Path, "/99") {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sampleIssueJSON))
	})
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.GetIssuesByNumbers(context.Background(), "o", "r", []int{42, 42, -1, 99, 7})
	if err != nil {
		t.Fatalf("GetIssuesByNumbers: %v", err)
	}
	if len(got) != 2 { // 42 and 7 (deduped, 99 skipped)
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestParseIssueRef(t *testing.T) {
	owner, repo, iid, err := parseIssueRef("nightgauge/nightgauge#42")
	if err != nil {
		t.Fatalf("parseIssueRef: %v", err)
	}
	if owner != "nightgauge" || repo != "nightgauge" || iid != 42 {
		t.Errorf("got (%q,%q,%d)", owner, repo, iid)
	}
}

func TestParseIssueRef_Errors(t *testing.T) {
	cases := []string{"", "no-hash", "owner#42", "owner/repo#abc"}
	for _, c := range cases {
		if _, _, _, err := parseIssueRef(c); err == nil {
			t.Errorf("parseIssueRef(%q): expected error", c)
		}
	}
}

func TestStubMethods_ReturnUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	ctx := context.Background()

	// AddSubIssue / RemoveSubIssue / LinkSubIssue / AddBlockedBy /
	// RemoveBlockedBy were stubs in W2.4; #3358 implemented them via the
	// REST /issues/:iid/links endpoint, so they are excluded from this
	// stub-only assertion.
	checks := []struct {
		name string
		err  error
	}{
		{"AddComment", svc.AddComment(ctx, "x", "y")},
		{"AddLabels", svc.AddLabels(ctx, "id", []string{"x"})},
		{"RemoveLabels", svc.RemoveLabels(ctx, "id", []string{"x"})},
		{"SyncStatusLabel", svc.SyncStatusLabel(ctx, "o", "r", 1, "ready")},
		{"MarkRefined", svc.MarkRefined(ctx, "o", "r", 1)},
	}
	for _, c := range checks {
		if !errors.Is(c.err, forge.ErrUnsupported) {
			t.Errorf("%s: expected ErrUnsupported, got %v", c.name, c.err)
		}
	}
}

// --- #3358 sub-issue + blocking link tests ---

// linkBackend is a minimal in-process state machine for the
// /issues/:iid/links endpoint. It supports list (GET), create (POST), and
// delete (DELETE) so the IssueService mutation tests can round-trip writes
// without re-asserting the wire shape per test.
type linkBackend struct {
	t      *testing.T
	srv    *stubGitLabServer
	links  map[int][]rawIssueLink // iid → links
	nextID int64
}

func newLinkBackend(t *testing.T) *linkBackend {
	t.Helper()
	srv := newStubServer(t)
	b := &linkBackend{t: t, srv: srv, links: map[int][]rawIssueLink{}, nextID: 1000}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/", func(w http.ResponseWriter, r *http.Request) {
		// net/http URL-decodes %2F → '/' in r.URL.Path so the prefix
		// matched by ServeMux is the encoded form, but inside the handler
		// we must trim using the decoded form.
		const prefix = "/api/v4/projects/o/r/issues/"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		rest := path[len(prefix):]
		// Only "links" routes are relevant here. Match by suffix.
		if strings.HasSuffix(rest, "/links") {
			iid := parseIIDPrefix(rest)
			if r.Method == "GET" {
				w.WriteHeader(200)
				_ = json.NewEncoder(w).Encode(b.links[iid])
				return
			}
			if r.Method == "POST" {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				targetIID := int(body["target_issue_iid"].(float64))
				linkType, _ := body["link_type"].(string)
				b.nextID++
				link := rawIssueLink{
					ID:          b.nextID,
					IID:         targetIID,
					IssueLinkID: b.nextID,
					Title:       "Issue " + intToStr(targetIID),
					State:       "opened",
					LinkType:    linkType,
				}
				b.links[iid] = append(b.links[iid], link)
				// Also write the inverse for blocks/is_blocked_by so the
				// stub mirrors GitLab's bidirectional auto-inverse behaviour.
				if linkType == linkTypeIsBlockedBy {
					b.links[targetIID] = append(b.links[targetIID], rawIssueLink{
						ID: b.nextID + 100, IID: iid, IssueLinkID: b.nextID + 100,
						Title: "Issue " + intToStr(iid), State: "opened", LinkType: linkTypeBlocks,
					})
				}
				if linkType == linkTypeBlocks {
					b.links[targetIID] = append(b.links[targetIID], rawIssueLink{
						ID: b.nextID + 100, IID: iid, IssueLinkID: b.nextID + 100,
						Title: "Issue " + intToStr(iid), State: "opened", LinkType: linkTypeIsBlockedBy,
					})
				}
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(link)
				return
			}
		}
		// /links/<linkID>
		if strings.Contains(rest, "/links/") && r.Method == "DELETE" {
			parts := strings.Split(rest, "/links/")
			if len(parts) == 2 {
				iid := parseIntStrict(parts[0])
				linkID := parseInt64Strict(parts[1])
				current := b.links[iid]
				out := current[:0]
				for _, l := range current {
					if l.linkID() != linkID {
						out = append(out, l)
					}
				}
				b.links[iid] = out
				w.WriteHeader(200)
				_, _ = w.Write([]byte("{}"))
				return
			}
		}
		// /<iid>  (PUT — parent_id write or generic PATCH; ignore body)
		if r.Method == "PUT" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"iid":1}`))
			return
		}
		// /<iid>  GET — sample issue payload for GetIssue tests
		if r.Method == "GET" {
			iid := parseIIDPrefix(rest)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(iid, "Issue "+intToStr(iid), nil)))
			return
		}
		http.NotFound(w, r)
	})
	srv.mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})
	return b
}

func parseIIDPrefix(rest string) int {
	// rest = "<iid>/links" or "<iid>/links/<linkID>" or "<iid>"
	i := strings.Index(rest, "/")
	if i < 0 {
		return parseIntStrict(rest)
	}
	return parseIntStrict(rest[:i])
}

func parseIntStrict(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func parseInt64Strict(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestAddSubIssue_PostsRelatesToOnParent(t *testing.T) {
	b := newLinkBackend(t)
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.AddSubIssue(context.Background(), "o/r#42", "o/r#43"); err != nil {
		t.Fatalf("AddSubIssue: %v", err)
	}
	if len(b.links[42]) != 1 || b.links[42][0].LinkType != linkTypeRelatesTo || b.links[42][0].IID != 43 {
		t.Errorf("parent links = %+v", b.links[42])
	}
}

func TestAddSubIssue_IsIdempotentOn409(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("POST", "/api/v4/projects/o%2Fr/issues/42/links", 409, `{"message":"already linked"}`)
	srv.mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	})
	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.AddSubIssue(context.Background(), "o/r#42", "o/r#43"); err != nil {
		t.Errorf("expected nil on 409, got %v", err)
	}
}

func TestAddSubIssue_RejectsMalformedRef(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)

	if err := svc.AddSubIssue(context.Background(), "garbage", "o/r#43"); err == nil {
		t.Error("expected error for malformed parent ref")
	}
	if err := svc.AddSubIssue(context.Background(), "o/r#42", "garbage"); err == nil {
		t.Error("expected error for malformed child ref")
	}
}

func TestRemoveSubIssue_DeletesByLinkID(t *testing.T) {
	b := newLinkBackend(t)
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)
	ctx := context.Background()

	if err := svc.AddSubIssue(ctx, "o/r#42", "o/r#43"); err != nil {
		t.Fatalf("AddSubIssue: %v", err)
	}
	if err := svc.RemoveSubIssue(ctx, "o/r#42", "o/r#43"); err != nil {
		t.Fatalf("RemoveSubIssue: %v", err)
	}
	if len(b.links[42]) != 0 {
		t.Errorf("after remove, links = %+v", b.links[42])
	}
}

func TestRemoveSubIssue_NoSuchLinkIsNoOp(t *testing.T) {
	b := newLinkBackend(t)
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.RemoveSubIssue(context.Background(), "o/r#42", "o/r#99"); err != nil {
		t.Errorf("expected nil for non-existent link, got %v", err)
	}
}

func TestLinkSubIssue_DelegatesToAddSubIssue(t *testing.T) {
	b := newLinkBackend(t)
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.LinkSubIssue(context.Background(), "o", "r", 42, 43); err != nil {
		t.Fatalf("LinkSubIssue: %v", err)
	}
	if len(b.links[42]) != 1 || b.links[42][0].IID != 43 {
		t.Errorf("links = %+v", b.links[42])
	}
}

func TestAddBlockedBy_PostsIsBlockedByOnBlocked(t *testing.T) {
	b := newLinkBackend(t)
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)

	if err := svc.AddBlockedBy(context.Background(), "o/r#42", "o/r#100"); err != nil {
		t.Fatalf("AddBlockedBy: %v", err)
	}
	// Posted link is on the blocked side (#42), pointing at the blocker (#100)
	if len(b.links[42]) != 1 || b.links[42][0].LinkType != linkTypeIsBlockedBy || b.links[42][0].IID != 100 {
		t.Errorf("blocked.links = %+v", b.links[42])
	}
	// The stub mirrors GitLab's auto-inverse: the blocker side gets a `blocks` link
	if len(b.links[100]) != 1 || b.links[100][0].LinkType != linkTypeBlocks {
		t.Errorf("blocker.links = %+v (auto-inverse not materialised)", b.links[100])
	}
}

func TestRemoveBlockedBy_DeletesFromBlockedSide(t *testing.T) {
	b := newLinkBackend(t)
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)
	ctx := context.Background()

	if err := svc.AddBlockedBy(ctx, "o/r#42", "o/r#100"); err != nil {
		t.Fatalf("AddBlockedBy: %v", err)
	}
	if err := svc.RemoveBlockedBy(ctx, "o/r#42", "o/r#100"); err != nil {
		t.Fatalf("RemoveBlockedBy: %v", err)
	}
	if len(b.links[42]) != 0 {
		t.Errorf("blocked.links after remove = %+v", b.links[42])
	}
}

func TestGetIssue_EnrichesWithLinkArrays(t *testing.T) {
	b := newLinkBackend(t)
	// Pre-seed links onto issue #42
	b.links[42] = []rawIssueLink{
		{ID: 1, IID: 50, IssueLinkID: 1, Title: "Child", State: "opened", LinkType: linkTypeRelatesTo},
		{ID: 2, IID: 60, IssueLinkID: 2, Title: "Open blocker", State: "opened", LinkType: linkTypeIsBlockedBy},
		{ID: 3, IID: 70, IssueLinkID: 3, Title: "Closed blocker", State: "closed", LinkType: linkTypeIsBlockedBy},
	}
	c := NewClient(b.srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.GetIssue(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(got.SubIssues) != 1 || got.SubIssues[0].Number != 50 {
		t.Errorf("SubIssues = %+v", got.SubIssues)
	}
	if !got.IsEpic {
		t.Errorf("IsEpic = false, want true (sub-issues present)")
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].Number != 60 {
		t.Errorf("BlockedBy = %+v (closed blocker should be filtered)", got.BlockedBy)
	}
}
