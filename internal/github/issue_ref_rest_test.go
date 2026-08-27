package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newClientForRESTTest returns a *Client whose REST calls land on server.
func newClientForRESTTest(server *httptest.Server) *Client {
	return &Client{http: &http.Client{Transport: &mockRESTTransport{server: server}}}
}

// issueRefHandler serves one REST issue object, and records the paths asked
// for so a test can assert the call actually went to REST rather than
// silently falling back to something else.
func issueRefHandler(t *testing.T, body string, status int, seen *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestResolveIssueRef_ReadsNodeIDAndParentOverREST(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(issueRefHandler(t, `{
		"number": 849,
		"id": 5262249483,
		"node_id": "I_kwDOTfoR3s8AAAABN_sThw",
		"parent_issue_url": "https://api.github.com/repos/o/r/issues/842"
	}`, http.StatusOK, &seen))
	defer srv.Close()

	ref, err := resolveIssueRef(context.Background(), newClientForRESTTest(srv), "o", "r", 849)
	if err != nil {
		t.Fatalf("resolveIssueRef: %v", err)
	}
	if ref.NodeID != "I_kwDOTfoR3s8AAAABN_sThw" {
		t.Errorf("NodeID = %q, want the node_id REST reported", ref.NodeID)
	}
	if ref.ParentNumber != 842 {
		t.Errorf("ParentNumber = %d, want 842 parsed from parent_issue_url", ref.ParentNumber)
	}
	// The database id is what the sub-issue and dependency endpoints take. It
	// is a distinct value from the node ID and arrives in the same response,
	// which is the property that let the link mutations move transport.
	if ref.DatabaseID != 5262249483 {
		t.Errorf("DatabaseID = %d, want the top-level id REST reported", ref.DatabaseID)
	}
	// The point of the migration is that this read leaves the graphql bucket.
	// Assert the transport, not just the value: a GraphQL implementation would
	// POST to /graphql and still return the right node ID.
	if len(seen) != 1 || seen[0] != "GET /repos/o/r/issues/849" {
		t.Errorf("requests = %v, want exactly one GET /repos/o/r/issues/849", seen)
	}
}

// A pull request number is the one place the two APIs disagree about what
// exists: GraphQL's repository.issue(number:) errors NOT_FOUND, REST returns
// the pull request. Verified live 2026-08-26. Without the discriminator the
// migration silently widens the contract.
func TestResolveIssueRef_RejectsAPullRequestNumber(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(issueRefHandler(t, `{
		"number": 925,
		"node_id": "PR_kwDOabc",
		"pull_request": {"url": "https://api.github.com/repos/o/r/pulls/925"}
	}`, http.StatusOK, &seen))
	defer srv.Close()

	ref, err := resolveIssueRef(context.Background(), newClientForRESTTest(srv), "o", "r", 925)
	if err == nil {
		t.Fatalf("resolveIssueRef returned %+v, want an error for a PR number", ref)
	}
	if !strings.Contains(err.Error(), "pull request") {
		t.Errorf("error = %q, want it to name the pull-request cause", err)
	}
}

// The GraphQL predecessor failed on an unknown number. A 404 must stay an
// error rather than becoming a ref a mutation would then use.
//
// The body is a COMPLETE, valid issue object even though the status is 404.
// That is deliberate: with GitHub's real `{"message":"Not Found"}` body this
// test passes even when the status check is deleted, because the empty
// node_id check catches it instead — the test would be pinning a different
// line than the one it names. Only a well-formed body isolates the status
// check as the thing under test.
func TestResolveIssueRef_AbsentIssueIsAnError(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(issueRefHandler(t,
		`{"number":9999,"id":4242,"node_id":"I_would_be_valid","parent_issue_url":null}`,
		http.StatusNotFound, &seen))
	defer srv.Close()

	if _, err := resolveIssueRef(context.Background(), newClientForRESTTest(srv), "o", "r", 9999); err == nil {
		t.Fatal("resolveIssueRef succeeded on a 404, want an error")
	}
}

// A 200 carrying no node_id would hand an empty ID to the mutation, which
// GitHub answers with a confusing error far from the cause.
func TestResolveIssueRef_MissingNodeIDIsAnError(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(issueRefHandler(t, `{"number": 1, "id": 4242}`, http.StatusOK, &seen))
	defer srv.Close()

	if _, err := resolveIssueRef(context.Background(), newClientForRESTTest(srv), "o", "r", 1); err == nil {
		t.Fatal("resolveIssueRef succeeded with no node_id, want an error")
	}
}

// The mirror of the node_id guard, and it needs its own test for the same
// reason that one does: a zero database id reaches a link endpoint as
// `{"sub_issue_id": 0}`, which GitHub answers 404 — indistinguishable at the
// call site from "the referenced issue is gone".
//
// The fixture carries a VALID node_id so the missing `id` is the only thing
// wrong with it. Sharing the previous test's body would leave both guards
// satisfied by whichever check runs first, and neither actually pinned.
func TestResolveIssueRef_MissingDatabaseIDIsAnError(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(issueRefHandler(t,
		`{"number": 1, "node_id": "I_kwDOTfoR3s8AAAABN_sThw"}`, http.StatusOK, &seen))
	defer srv.Close()

	if _, err := resolveIssueRef(context.Background(), newClientForRESTTest(srv), "o", "r", 1); err == nil {
		t.Fatal("resolveIssueRef succeeded with no database id, want an error")
	}
}

func TestParentIssueNumber(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"no parent", "", 0},
		{"api url", "https://api.github.com/repos/o/r/issues/842", 842},
		{"trailing slash", "https://api.github.com/repos/o/r/issues/842/", 842},
		// An unparseable URL must read as "no parent known". The caller uses
		// this value to REJECT a link, so inventing a number would reject a
		// legitimate edge; zero merely declines to guard.
		{"non-numeric tail", "https://api.github.com/repos/o/r/issues/abc", 0},
		{"no slash", "842", 0},
		{"zero", "https://api.github.com/repos/o/r/issues/0", 0},
		{"negative", "https://api.github.com/repos/o/r/issues/-3", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parentIssueNumber(tc.in); got != tc.want {
				t.Errorf("parentIssueNumber(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
