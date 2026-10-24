package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// readyItemsServer returns an httptest server that responds to the board's
// "Ready" filtered project-items query with two Issue items: #1 (untrusted
// author, e.g. a stranger's issue) and #2 (trusted OWNER author).
func readyItemsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return readyItemsServerWithLabels(t, nil)
}

// readyItemsServerWithLabels is readyItemsServer with labels on chosen issues.
func readyItemsServerWithLabels(t *testing.T, labels map[int][]string) *httptest.Server {
	t.Helper()
	labelJSON := func(n int) string {
		var parts []string
		for _, l := range labels[n] {
			parts = append(parts, fmt.Sprintf(`{"name": %q}`, l))
		}
		return `{"nodes": [` + strings.Join(parts, ",") + `]}`
	}
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
										"author": {"login": "stranger", "__typename": "User"},
										"labels": %s,
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
										"author": {"login": "maintainer", "__typename": "User"},
										"labels": %s,
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
		}`, labelJSON(1), labelJSON(2))
		_, _ = w.Write([]byte(resp))
	}))
}

// TestPickNext_SkipsUntrustedAuthor is the defense-in-depth assertion
// (#270, #2130): even when an issue no writer vouched for reaches "Ready" by
// some other route, PickNext must still refuse to dispatch it.
func TestPickNext_SkipsUntrustedAuthor(t *testing.T) {
	srv := readyItemsServer(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test-token", srv.URL)
	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead, "maintainer": trust.LevelAdmin})
	s := &Scheduler{
		client:      client,
		boardSvc:    gh.NewBoardService(client, "o", 1),
		repoRunning: map[string]int{},
		trustGate:   gate,
	}

	item, err := s.PickNext(context.Background())
	if err != nil {
		t.Fatalf("PickNext returned error: %v", err)
	}
	if item == nil {
		t.Fatal("expected a dispatchable item (the writer's issue), got nil")
	}
	if item.Number != 2 {
		t.Errorf("expected issue #2 (a writer's) to be picked, got #%d — the read-access author's #1 should have been filtered", item.Number)
	}
}

// A Ready item whose author has read access and no approval is not
// dispatched; once a writer approves it, it is (#2130).
func TestPickNext_DispatchesAReadAuthorsIssueOnceAWriterApprovesIt(t *testing.T) {
	srv := readyItemsServer(t)
	defer srv.Close()
	client := gh.NewClientWithURL("test-token", srv.URL)

	// Only the stranger's issue is a candidate: the maintainer cannot vouch.
	gate, forge := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead, "maintainer": trust.LevelTriage,
		"approver": trust.LevelWrite})
	gate.WithAppOwners("o") // label approval exists only where the pipeline runs as an App
	gate.WithStageIdentities(func(context.Context) ([]string, error) { return nil, nil })
	s := &Scheduler{client: client, boardSvc: gh.NewBoardService(client, "o", 1),
		repoRunning: map[string]int{}, trustGate: gate}
	if item, err := s.PickNext(context.Background()); err != nil || item != nil {
		t.Fatalf("PickNext = %v, %v; want nothing dispatchable without an approval", item, err)
	}

	// The board's label page here is the fixture's (empty) set, so the gate
	// reads the issue: a writer applied pipeline:approved after the last edit.
	forge.signals["o/r#1"] = trust.Signals{Author: "stranger", Labels: []string{trust.ApprovedLabel},
		Events: []trust.Event{{Kind: trust.EventLabeled, Label: trust.ApprovedLabel, Actor: "approver", At: time.Now()}}}
	srvApproved := readyItemsServerWithLabels(t, map[int][]string{1: {trust.ApprovedLabel}})
	defer srvApproved.Close()
	client2 := gh.NewClientWithURL("test-token", srvApproved.URL)
	s.client, s.boardSvc = client2, gh.NewBoardService(client2, "o", 1)
	item, err := s.PickNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if item == nil || item.Number != 1 {
		t.Fatalf("PickNext = %+v, want #1 once a writer approved it", item)
	}
}

// A scheduler without a gate refuses everything: fail closed.
func TestPickNext_NoGateRefusesEverything(t *testing.T) {
	srv := readyItemsServer(t)
	defer srv.Close()
	client := gh.NewClientWithURL("test-token", srv.URL)
	s := &Scheduler{client: client, boardSvc: gh.NewBoardService(client, "o", 1), repoRunning: map[string]int{}}
	if item, err := s.PickNext(context.Background()); err != nil || item != nil {
		t.Fatalf("PickNext = %v, %v; want nothing without a gate", item, err)
	}
}
