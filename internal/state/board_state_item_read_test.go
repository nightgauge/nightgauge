package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// truncatingBoard is a GraphQL server for a board that holds item1, the item
// under test, and an unrelated closed issue whose blocking list is longer than
// the board scan's first page and whose later page cannot be read. A
// board-wide read of it therefore fails with ErrConnectionTruncated. It
// answers the board scan, the single-item field read, field metadata and
// mutations, and counts the mutations.
type truncatingBoard struct {
	status, stage string
	// failItemRead makes the single-item field read fail with HTTP 502.
	failItemRead bool

	mu        sync.Mutex
	mutations int
}

// reRelationFollowUp matches the request that reads later pages of
// relationship lists; its cursors are $after0, $after1 and so on, where the
// board scan's own cursor is $after.
var reRelationFollowUp = regexp.MustCompile(`after: \$after\d`)

func (b *truncatingBoard) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var resp map[string]interface{}
		switch q := req.Query; {
		case strings.Contains(q, "mutation"):
			b.mu.Lock()
			b.mutations++
			b.mu.Unlock()
			resp = mutationResp()
		case strings.Contains(q, "fields("):
			resp = fieldsResp(&mockConfig{hasStatus: true, hasPipelineStage: true})
		case reRelationFollowUp.MatchString(q):
			// A later page of a relationship list.
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		case strings.Contains(q, "node(id: $id)"):
			if b.failItemRead {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			resp = map[string]interface{}{"data": map[string]interface{}{"node": map[string]interface{}{
				"__typename":  "ProjectV2Item",
				"fieldValues": b.fieldValues(),
			}}}
		case strings.Contains(q, "items("):
			resp = b.itemsResp()
		default:
			http.Error(w, "unrecognized query: "+q, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (b *truncatingBoard) mutationCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mutations
}

func (b *truncatingBoard) fieldValues() map[string]interface{} {
	return map[string]interface{}{"nodes": []interface{}{statusValue(b.status), stageValue(b.stage)}}
}

// itemsResp is the board scan: item1, then closed issue #7, which blocks six
// issues. The scan's first page of a blocking list holds five, so #7's list
// reports a next page.
func (b *truncatingBoard) itemsResp() map[string]interface{} {
	whole := map[string]interface{}{
		"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
		"nodes":    []interface{}{},
	}
	blocked := make([]interface{}, 0, 5)
	for n := 100; n < 105; n++ {
		blocked = append(blocked, map[string]interface{}{
			"id": fmt.Sprintf("I_%d", n), "number": n, "title": "blocked", "state": "OPEN",
			"repository": map[string]interface{}{"nameWithOwner": "test/test"},
		})
	}
	issue := func(id string, number int, state string, blocking map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"__typename": "Issue",
			"id":         id,
			"number":     number,
			"title":      "Issue",
			"state":      state,
			"labels":     map[string]interface{}{"totalCount": 0, "nodes": []interface{}{}},
			"repository": map[string]interface{}{"nameWithOwner": "test/test"},
			"subIssues":  whole,
			"blockedBy":  whole,
			"blocking":   blocking,
			"parent":     map[string]interface{}{"number": 0, "title": ""},
		}
	}
	return map[string]interface{}{"data": map[string]interface{}{"organization": map[string]interface{}{
		"projectV2": map[string]interface{}{
			"id":    "PVT_test123",
			"title": "Test Project",
			"items": map[string]interface{}{
				"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				"nodes": []interface{}{
					map[string]interface{}{
						"id":          "item1",
						"content":     issue("I_42", 42, "OPEN", whole),
						"fieldValues": b.fieldValues(),
					},
					map[string]interface{}{
						"id": "item2",
						"content": issue("I_7", 7, "CLOSED", map[string]interface{}{
							"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": "c5"},
							"nodes":    blocked,
						}),
						"fieldValues": map[string]interface{}{"nodes": []interface{}{statusValue("Done")}},
					},
				},
			},
		},
	}}}
}

// statusValue is a Status single-select field value as GitHub returns it.
func statusValue(status string) map[string]interface{} {
	return map[string]interface{}{
		"__typename": "ProjectV2ItemFieldSingleSelectValue",
		"name":       status,
		"field":      map[string]interface{}{"name": "Status"},
	}
}

// stageValue is a Pipeline Stage text field value as GitHub returns it.
func stageValue(stage string) map[string]interface{} {
	return map[string]interface{}{
		"__typename": "ProjectV2ItemFieldTextValue",
		"text":       stage,
		"field":      map[string]interface{}{"name": "Pipeline Stage"},
	}
}

// TestBoardStateReadsTheItemAlone pins the two state reads to the item they
// are about. The board also holds an issue whose relationship list cannot be
// read whole, which fails any board-wide read. A status read that went
// through one left the revert guard blind, and FailPipeline then moved an
// In-review issue back to Ready, to be re-dispatched on top of its own open
// PR; a pipeline stage read failed on a list it never uses.
func TestBoardStateReadsTheItemAlone(t *testing.T) {
	ctx := context.Background()

	// The fixture's premise, so the reads below cannot pass on a board that
	// no longer fails.
	t.Run("a board-wide read of the board fails", func(t *testing.T) {
		b := &truncatingBoard{status: "In review"}
		board := gh.NewBoardService(gh.NewClientWithURL("test", b.server(t).URL), "testorg", 1)
		if _, err := board.ListItems(ctx, ""); !errors.Is(err, gh.ErrConnectionTruncated) {
			t.Fatalf("ListItems err = %v, want ErrConnectionTruncated", err)
		}
	})

	t.Run("FailPipeline keeps an In-review issue In review", func(t *testing.T) {
		b := &truncatingBoard{status: "In review", stage: string(StagePRMerge)}
		svc := NewBoardStateServiceForClient(gh.NewClientWithURL("test", b.server(t).URL), "testorg", 1)

		changed, err := svc.FailPipeline(ctx, "item1", StatusReady)
		if err != nil {
			t.Fatalf("FailPipeline: %v", err)
		}
		if changed || b.mutationCount() != 0 {
			t.Fatalf("FailPipeline changed=%v with %d writes; an In-review issue must stay In review",
				changed, b.mutationCount())
		}
	})

	t.Run("GetPipelineStage", func(t *testing.T) {
		b := &truncatingBoard{status: "In progress", stage: string(StageFeatureDev)}
		svc := NewBoardStateServiceForClient(gh.NewClientWithURL("test", b.server(t).URL), "testorg", 1)

		stage, err := svc.GetPipelineStage(ctx, "item1")
		if err != nil {
			t.Fatalf("GetPipelineStage: %v", err)
		}
		if stage != StageFeatureDev {
			t.Fatalf("stage = %q, want %q", stage, StageFeatureDev)
		}
	})
}

// TestFailPipeline_UnreadableStatusLeavesItUnchanged pins the revert guard's
// failure mode. When the item's current status cannot be read, FailPipeline
// does not revert: the issue may be In review, and reverting it would make it
// dispatchable on top of its own open PR. It reports why instead.
func TestFailPipeline_UnreadableStatusLeavesItUnchanged(t *testing.T) {
	b := &truncatingBoard{status: "In review", failItemRead: true}
	svc := NewBoardStateServiceForClient(gh.NewClientWithURL("test", b.server(t).URL), "testorg", 1)

	changed, err := svc.FailPipeline(context.Background(), "item1", StatusReady)
	if err == nil {
		t.Fatal("FailPipeline returned no error for a status it could not read")
	}
	if changed || b.mutationCount() != 0 {
		t.Fatalf("FailPipeline changed=%v with %d writes; a status it could not read must be left as it is",
			changed, b.mutationCount())
	}
}
