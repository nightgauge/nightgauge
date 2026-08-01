package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// readyItemsServer returns an httptest server that responds to the board's
// "Ready" filtered project-items query with two Issue items: #1 (untrusted
// author, e.g. a stranger's issue) and #2 (trusted OWNER author).
func readyItemsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := fmt.Sprintf(`{
			"data": {
				"organization": {
					"projectV2": {
						"id": "PVT_1",
						"title": "Board",
						"items": {
							"pageInfo": {"hasNextPage": false, "endCursor": ""},
							"nodes": [
								{
									"id": "ITEM_1",
									"content": {
										"__typename": "Issue",
										"number": 1,
										"title": "Stranger's issue",
										"state": "OPEN",
										"url": "https://github.com/o/r/issues/1",
										"createdAt": "2026-01-01T00:00:00Z",
										"updatedAt": "2026-01-01T00:00:00Z",
										"authorAssociation": "NONE",
										"labels": {"nodes": []},
										"repository": {"nameWithOwner": "o/r"},
										"subIssues": {"nodes": []},
										"blockedBy": {"nodes": []},
										"blocking": {"nodes": []},
										"parent": {"number": 0, "title": ""}
									},
									"fieldValues": {"nodes": []}
								},
								{
									"id": "ITEM_2",
									"content": {
										"__typename": "Issue",
										"number": 2,
										"title": "Owner's issue",
										"state": "OPEN",
										"url": "https://github.com/o/r/issues/2",
										"createdAt": "2026-01-01T00:00:00Z",
										"updatedAt": "2026-01-01T00:00:00Z",
										"authorAssociation": "OWNER",
										"labels": {"nodes": []},
										"repository": {"nameWithOwner": "o/r"},
										"subIssues": {"nodes": []},
										"blockedBy": {"nodes": []},
										"blocking": {"nodes": []},
										"parent": {"number": 0, "title": ""}
									},
									"fieldValues": {"nodes": []}
								}
							]
						}
					}
				}
			}
		}`)
		_, _ = w.Write([]byte(resp))
	}))
}

// TestPickNext_SkipsUntrustedAuthor is the defense-in-depth assertion for
// #270: even when an untrusted-author issue reaches "Ready" status by some
// other route, PickNext must still refuse to dispatch it.
func TestPickNext_SkipsUntrustedAuthor(t *testing.T) {
	srv := readyItemsServer(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test-token", srv.URL)
	s := &Scheduler{
		client:      client,
		boardSvc:    gh.NewBoardService(client, "o", 1),
		repoRunning: map[string]int{},
	}

	item, err := s.PickNext(context.Background())
	if err != nil {
		t.Fatalf("PickNext returned error: %v", err)
	}
	if item == nil {
		t.Fatal("expected a dispatchable item (the trusted-author issue), got nil")
	}
	if item.Number != 2 {
		t.Errorf("expected issue #2 (OWNER) to be picked, got #%d — untrusted-author issue #1 should have been filtered", item.Number)
	}
}

// TestPickNext_SkipsUntrustedAuthor_ConfiguredOverrideExcludesEveryone verifies
// the configured override list fully replaces the default trusted set, so
// even an OWNER-authored issue is skipped when the configured list omits it.
func TestPickNext_SkipsUntrustedAuthor_ConfiguredOverrideExcludesEveryone(t *testing.T) {
	srv := readyItemsServer(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test-token", srv.URL)
	s := &Scheduler{
		client:                    client,
		boardSvc:                  gh.NewBoardService(client, "o", 1),
		repoRunning:               map[string]int{},
		trustedAuthorAssociations: []string{"CONTRIBUTOR"}, // neither NONE nor OWNER qualifies
	}

	item, err := s.PickNext(context.Background())
	if err != nil {
		t.Fatalf("PickNext returned error: %v", err)
	}
	if item != nil {
		t.Errorf("expected no dispatchable item — configured override excludes both authors, got #%d", item.Number)
	}
}
