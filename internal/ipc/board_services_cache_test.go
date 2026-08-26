package ipc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// boardMock is a GraphQL stub that counts the ONE thing these tests are about:
// how many times the board's item list was actually fetched from GitHub.
//
// Counting item queries rather than total requests matters. A status write
// legitimately issues a `fields(` query and a mutation of its own, and folding
// those into the count would make the invalidation assertion below pass for the
// wrong reason — it would rise whether or not the snapshot was dropped.
type boardMock struct {
	mu         sync.Mutex
	itemQuery  int
	itemStatus string
}

func (m *boardMock) itemQueries() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.itemQuery
}

func newBoardMock(t *testing.T) (*boardMock, *httptest.Server) {
	t.Helper()
	m := &boardMock{itemStatus: "Ready"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var resp map[string]interface{}
		switch {
		case strings.Contains(req.Query, "mutation"):
			resp = map[string]interface{}{"data": map[string]interface{}{
				"updateProjectV2ItemFieldValue": map[string]interface{}{"clientMutationId": nil},
			}}
		case strings.Contains(req.Query, "fields("):
			resp = map[string]interface{}{"data": map[string]interface{}{
				"organization": map[string]interface{}{"projectV2": map[string]interface{}{
					"id": "PVT_test",
					"fields": map[string]interface{}{
						"nodes": []interface{}{map[string]interface{}{
							"__typename": "ProjectV2SingleSelectField",
							"id":         "PVTSSF_status",
							"name":       "Status",
							// Spelled as gh.DefaultFieldSchema provisions the
							// column, so the write takes the exact-match path
							// rather than the case-insensitive fallback (#413).
							"options": []interface{}{
								map[string]interface{}{"id": "opt_ready", "name": "Ready"},
								map[string]interface{}{"id": "opt_done", "name": "Done"},
							},
						}},
					},
				}},
			}}
		case strings.Contains(req.Query, "items("):
			m.mu.Lock()
			m.itemQuery++
			status := m.itemStatus
			m.mu.Unlock()
			resp = itemsPayload(status)
		default:
			http.Error(w, "unrecognized query: "+req.Query, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return m, srv
}

func itemsPayload(status string) map[string]interface{} {
	return map[string]interface{}{"data": map[string]interface{}{
		"organization": map[string]interface{}{"projectV2": map[string]interface{}{
			"id": "PVT_test", "title": "T",
			"items": map[string]interface{}{
				"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				"nodes": []interface{}{map[string]interface{}{
					"id": "item1",
					"content": map[string]interface{}{
						"__typename": "Issue",
						"number":     42,
						"title":      "Test Issue",
						"state":      "OPEN",
						"url":        "https://github.com/test/test/issues/42",
						"createdAt":  "2026-01-01T00:00:00Z",
						"updatedAt":  "2026-01-01T00:00:00Z",
						"labels":     map[string]interface{}{"nodes": []interface{}{}},
						"repository": map[string]interface{}{"nameWithOwner": "test/test"},
						"subIssues":  map[string]interface{}{"nodes": []interface{}{}},
						"blockedBy":  map[string]interface{}{"nodes": []interface{}{}},
						"blocking":   map[string]interface{}{"nodes": []interface{}{}},
						"parent":     map[string]interface{}{"number": 0, "title": ""},
					},
					"fieldValues": map[string]interface{}{"nodes": []interface{}{
						map[string]interface{}{
							"__typename": "ProjectV2ItemFieldSingleSelectValue",
							"name":       status,
							"field":      map[string]interface{}{"name": "Status"},
						},
					}},
				}},
			},
		}},
	}}
}

func testServer(t *testing.T, url string) (*Server, *gh.Client) {
	t.Helper()
	c := gh.NewClientWithURL("test", url)
	return &Server{boards: boardcache.New(0)}, c
}

// AC 2 of #848, stated as the property it actually claims: a second read of the
// same board inside the TTL must issue NO GitHub call.
//
// Before this change the daemon built a fresh, unwrapped BoardService per verb,
// so N tree refreshes cost N board reads however warm the snapshot was. The
// assertion is on the request count rather than on the returned items, because
// the items were always correct — it was the spend that was not.
func TestSecondBoardReadInsideTTLIssuesNoRequest(t *testing.T) {
	m, srv := newBoardMock(t)
	s, c := testServer(t, srv.URL)
	ctx := context.Background()

	svcs := s.boardServicesFor(c, "testorg", 1, gh.OwnerTypeOrg)
	if _, err := svcs.Board.ListItems(ctx, ""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := m.itemQueries(); got != 1 {
		t.Fatalf("first read issued %d item queries, want 1", got)
	}

	// A SEPARATE accessor call, as a second IPC verb would do. The cache lives
	// on the Server, not on the service, so rebuilding the service must not
	// lose the snapshot — that is the whole difference from the old code.
	again := s.boardServicesFor(c, "testorg", 1, gh.OwnerTypeOrg)
	if _, err := again.Board.ListItems(ctx, ""); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if got := m.itemQueries(); got != 1 {
		t.Fatalf("second read inside the TTL issued a request: %d item queries, want 1", got)
	}
}

// The other half, and the reason wrapping only the reads would have been worse
// than doing nothing: a write this process issued must drop the snapshot it
// invalidated. Otherwise an operator moving an item to Done keeps seeing it in
// Ready for up to the TTL — not stale data, data we know to be wrong.
func TestBoardWriteInvalidatesTheSnapshot(t *testing.T) {
	m, srv := newBoardMock(t)
	s, c := testServer(t, srv.URL)
	ctx := context.Background()

	svcs := s.boardServicesFor(c, "testorg", 1, gh.OwnerTypeOrg)
	items, err := svcs.Board.ListItems(ctx, "")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if len(items) != 1 || items[0].Status != "Ready" {
		t.Fatalf("fixture: got %d items, first status %q", len(items), items[0].Status)
	}

	// Write through the state service, which is how board.updateStatus does it.
	m.mu.Lock()
	m.itemStatus = "Done"
	m.mu.Unlock()
	if err := s.boardStateFor(c, "testorg", 1, gh.OwnerTypeOrg).UpdateStatus(ctx, "item1", "Done"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	after, err := s.boardServicesFor(c, "testorg", 1, gh.OwnerTypeOrg).Board.ListItems(ctx, "")
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if got := m.itemQueries(); got != 2 {
		t.Fatalf("read after write issued %d item queries, want 2 — the snapshot survived a write to its own board", got)
	}
	if after[0].Status != "Done" {
		t.Fatalf("read after write served %q, want %q — the operator would still see the pre-write board", after[0].Status, "Done")
	}
}

// A project number of 0 is a service that was never bound to a board. It is not
// a board-scoped write, but BoardItem carries BlockedBy, so it still changes
// what a board read returns. Skipping invalidation there would be a hole that
// no board-scoped test could see.
func TestUnboundProjectServiceStillInvalidates(t *testing.T) {
	m, srv := newBoardMock(t)
	s, c := testServer(t, srv.URL)
	ctx := context.Background()

	if _, err := s.boardServicesFor(c, "testorg", 1, gh.OwnerTypeOrg).Board.ListItems(ctx, ""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := m.itemQueries(); got != 1 {
		t.Fatalf("setup: %d item queries, want 1", got)
	}

	// Mirrors the blocked-by verbs: built with project 0 against the same owner.
	// The call itself fails against this stub — it is the invalidation, which
	// runs on failure too, that is under test.
	unbound := s.boardServicesFor(c, "testorg", 0, gh.OwnerTypeUser).Project
	_ = unbound.RemoveBlockedByNumber(ctx, "testorg", "test", 42, 43)

	if _, err := s.boardServicesFor(c, "testorg", 1, gh.OwnerTypeOrg).Board.ListItems(ctx, ""); err != nil {
		t.Fatalf("read after unbound write: %v", err)
	}
	if got := m.itemQueries(); got != 2 {
		t.Fatalf("an unbound write left the board's snapshot in place: %d item queries, want 2", got)
	}
}
