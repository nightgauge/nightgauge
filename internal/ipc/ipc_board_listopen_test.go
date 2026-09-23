package ipc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The Repositories tree needs Ready, In progress and Backlog for every
// repository row. It used to send board.list once per status: three
// `items(query:"status:X is:open")` reads per board, 17 GraphQL points a page,
// none shared with board.counts or the sweeps. board.listOpen returns the
// daemon's open snapshot, and board.list for an open status is answered from
// that snapshot while it is fresh, so the whole row costs one board read.
//
// Red before the fix: board.listOpen was not registered, and the three status
// reads issued three more board queries.
func TestBoardListOpen_SharesOneBoardReadWithCountsAndStatusReads(t *testing.T) {
	var boardQueries atomic.Int32
	page := map[string]interface{}{
		"data": boardItemsGraphQLResponse("Ready", "Ready", "In progress", "Backlog", "In review"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		json.Unmarshal(body, &req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "projectV2") && strings.Contains(req.Query, "items") {
			boardQueries.Add(1)
			json.NewEncoder(w).Encode(page) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"data": nil}) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	h := newIpcTestHarnessWithGitHub(t, srv.URL)
	h.awaitReady()
	board := map[string]interface{}{"owner": "nightgauge", "projectNumber": 7}

	id := h.sendRequest("board.listOpen", board)
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("board.listOpen error: %+v", resp.Error)
	}
	var items []map[string]interface{}
	data, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("board.listOpen result is not a BoardItem array: %s", data)
	}
	if len(items) != 5 {
		t.Fatalf("board.listOpen returned %d items, want all 5 open items", len(items))
	}

	id = h.sendRequest("board.counts", board)
	if resp := h.readResponseFor(id, nil); resp.Error != nil {
		t.Fatalf("board.counts error: %+v", resp.Error)
	}
	wantPerStatus := map[string]int{"Ready": 2, "In progress": 1, "Backlog": 1}
	for status, want := range wantPerStatus {
		params := map[string]interface{}{"owner": "nightgauge", "projectNumber": 7, "status": status}
		id = h.sendRequest("board.list", params)
		resp := h.readResponseFor(id, nil)
		if resp.Error != nil {
			t.Fatalf("board.list %q error: %+v", status, resp.Error)
		}
		data, _ := json.Marshal(resp.Result)
		var got []map[string]interface{}
		json.Unmarshal(data, &got) //nolint:errcheck
		if len(got) != want {
			t.Fatalf("board.list %q returned %d items, want %d", status, len(got), want)
		}
	}

	if n := boardQueries.Load(); n != 1 {
		t.Fatalf("listOpen + counts + three status lists issued %d board queries, want 1", n)
	}
}
