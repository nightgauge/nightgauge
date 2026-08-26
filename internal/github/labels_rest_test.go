package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// restProbeServer records every request path (query string included) and
// answers with a caller-supplied status and body.
func restProbeServer(t *testing.T, status int, body string, paths *[]string) (*Client, func()) {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*paths = append(*paths, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return NewClientWithURL("test-token", srv.URL), srv.Close
}

// TestListRepoLabels_UsesRESTWithFullPage pins BOTH halves of the #849
// migration that a decoded result alone would not: that the read is REST at
// all, and that it still asks for 100 labels. The GraphQL predecessor said
// `labels(first: 100)`; a REST GET with no per_page silently returns 30, which
// would drop 70 labels from a map that feeds label mutations — a truncation
// no assertion on the returned labels could see with a short fixture.
func TestListRepoLabels_UsesRESTWithFullPage(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusOK,
		`[{"node_id":"LA_1","name":"bug","description":"d","color":"c"}]`, &paths)
	defer cleanup()

	labels, err := listRepoLabels(context.Background(), c, "o", "r")
	if err != nil {
		t.Fatalf("listRepoLabels: %v", err)
	}
	if len(labels) != 1 || labels[0].NodeID != "LA_1" || labels[0].Name != "bug" {
		t.Fatalf("decoded %+v, want one label LA_1/bug", labels)
	}
	if len(paths) != 1 {
		t.Fatalf("made %d requests, want exactly 1", len(paths))
	}
	if paths[0] != "/repos/o/r/labels?per_page=100" {
		t.Errorf("requested %q, want /repos/o/r/labels?per_page=100", paths[0])
	}
}

// TestListRepoLabels_AbsentRepoIsAnError guards the fail-silent shape: a 404
// must NOT read as "this repository has no labels", because every caller then
// concludes the label it wants does not exist and skips a write.
//
// The assertion names the status on purpose. Without the status check the call
// still fails — decoding GitHub's error OBJECT into a []restLabel errors on its
// own — so a test that only asserted `err != nil` would pass with the guard
// deleted.
func TestListRepoLabels_AbsentRepoIsAnError(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusNotFound, `{"message":"Not Found"}`, &paths)
	defer cleanup()

	_, err := listRepoLabels(context.Background(), c, "o", "gone")
	if err == nil {
		t.Fatal("a 404 must be an error, not an empty label set")
	}
	if !strings.Contains(err.Error(), "REST 404") {
		t.Errorf("error = %q, want it to report the HTTP status", err)
	}
}

// TestListRepoLabels_MissingNodeIDIsAnError pins the second guard: the node IDs
// this read returns are fed straight to GraphQL label mutations, so a label
// with no ID must fail loudly rather than be dropped or passed on blank.
func TestListRepoLabels_MissingNodeIDIsAnError(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusOK,
		`[{"node_id":"LA_1","name":"bug"},{"node_id":"","name":"orphan"}]`, &paths)
	defer cleanup()

	_, err := listRepoLabels(context.Background(), c, "o", "r")
	if err == nil {
		t.Fatal("a label with no node_id must be an error")
	}
	if !strings.Contains(err.Error(), "orphan") {
		t.Errorf("error = %q, want it to name the offending label", err)
	}
}

func TestListRepoLabels_RequiresOwnerAndRepo(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusOK, `[]`, &paths)
	defer cleanup()

	if _, err := listRepoLabels(context.Background(), c, "", "r"); err == nil {
		t.Error("empty owner must be rejected")
	}
	if _, err := listRepoLabels(context.Background(), c, "o", ""); err == nil {
		t.Error("empty repo must be rejected")
	}
	if len(paths) != 0 {
		t.Errorf("dispatched %v, want no request for invalid input", paths)
	}
}

// TestGetRepositoryID_UsesREST pins the transport. GetRepositoryID returns the
// same node ID string either way, so nothing about its RESULT can tell a
// reader whether #849's migration is still in place — only the request path
// can. A regression to GraphQL POSTs to "/" and fails here.
func TestGetRepositoryID_UsesREST(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusOK, `{"node_id":"R_kgDOabc"}`, &paths)
	defer cleanup()

	id, err := c.GetRepositoryID(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("GetRepositoryID: %v", err)
	}
	if id != "R_kgDOabc" {
		t.Errorf("id = %q, want R_kgDOabc", id)
	}
	if len(paths) != 1 || paths[0] != "/repos/o/r" {
		t.Errorf("requested %v, want exactly [/repos/o/r]", paths)
	}
}

// TestGetRepositoryID_EmptyNodeIDIsAnError keeps a blank ID from reaching
// createIssue / createPullRequest / createLabel, where it would fail far from
// the read that produced it.
func TestGetRepositoryID_EmptyNodeIDIsAnError(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusOK, `{"node_id":""}`, &paths)
	defer cleanup()

	if _, err := c.GetRepositoryID(context.Background(), "o", "r"); err == nil {
		t.Fatal("a 200 with no node_id must be an error, not an empty ID")
	}
}

func TestGetRepositoryID_AbsentRepoIsAnError(t *testing.T) {
	var paths []string
	c, cleanup := restProbeServer(t, http.StatusNotFound, `{"message":"Not Found"}`, &paths)
	defer cleanup()

	_, err := c.GetRepositoryID(context.Background(), "o", "gone")
	if err == nil {
		t.Fatal("a 404 must be an error")
	}
	if !strings.Contains(err.Error(), "REST 404") {
		t.Errorf("error = %q, want it to report the HTTP status", err)
	}
}
