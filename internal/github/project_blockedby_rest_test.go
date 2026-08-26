package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// blockedByRESTServer serves the REST issue objects AddBlockedByNumber
// resolves, keyed by number, and records every path it was asked for. A number
// absent from the map 404s, which is how a test forces the call to fail before
// it reaches the GraphQL mutation.
func blockedByRESTServer(t *testing.T, byNumber map[int]string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		for n, body := range byNumber {
			if strings.HasSuffix(r.URL.Path, fmt.Sprintf("/issues/%d", n)) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		// A well-formed body under a 404 status, so that a test asserting
		// "this failed" is pinning the STATUS check and not the empty-node_id
		// fallback. See TestResolveIssueRef_AbsentIssueIsAnError.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"number":0,"node_id":"I_would_be_valid","parent_issue_url":null}`))
	}))
}

func issueJSON(number int, nodeID, parentURL string) string {
	parent := "null"
	if parentURL != "" {
		parent = fmt.Sprintf("%q", parentURL)
	}
	return fmt.Sprintf(`{"number":%d,"node_id":%q,"parent_issue_url":%s}`, number, nodeID, parent)
}

// These tests deliberately stop before the GraphQL mutation. Reaching it needs
// a fully constructed Client (limiter + gql transport) that no test in this
// package builds, so each case below is shaped to return at the guard or at a
// failed resolution. The guard's own logic is covered exhaustively by
// TestBlocksOwnParent, which needs no client at all.

// The circular-dependency guard is the behaviour most at risk from the ID
// migration: it used to read ParentIssueNumber off a full GraphQL issue and
// now reads it out of REST's parent_issue_url. It must still fire, and the two
// resolutions must go over REST.
func TestAddBlockedByNumber_RejectsBlockingByParentEpic(t *testing.T) {
	var seen []string
	srv := blockedByRESTServer(t, map[int]string{
		849: issueJSON(849, "I_child", "https://api.github.com/repos/o/r/issues/842"),
		842: issueJSON(842, "I_epic", ""),
	}, &seen)
	defer srv.Close()

	p := &ProjectService{client: newClientForRESTTest(srv)}
	err := p.AddBlockedByNumber(context.Background(), "o", "r", 849, 842)
	if err == nil {
		t.Fatal("AddBlockedByNumber succeeded, want a circular-dependency error")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("error = %q, want the circular-dependency message", err)
	}
	// Assert the TRANSPORT, not just the value: a GraphQL implementation would
	// POST to /graphql and still produce the right error.
	want := []string{"GET /repos/o/r/issues/849", "GET /repos/o/r/issues/842"}
	if len(seen) != len(want) {
		t.Fatalf("requests = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

// A pull request number used to fail at the GraphQL read (NOT_FOUND). REST
// returns the PR instead, so the rejection has to be explicit or the migration
// silently widens what these commands accept.
func TestAddBlockedByNumber_RejectsPullRequestNumber(t *testing.T) {
	var seen []string
	srv := blockedByRESTServer(t, map[int]string{
		925: `{"number":925,"node_id":"PR_x","pull_request":{"url":"u"}}`,
	}, &seen)
	defer srv.Close()

	p := &ProjectService{client: newClientForRESTTest(srv)}
	err := p.AddBlockedByNumber(context.Background(), "o", "r", 925, 1)
	if err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Fatalf("err = %v, want a pull-request rejection", err)
	}
}

// Both entry points must resolve the BLOCKER too, not just the blocked issue.
// A migration that resolved only the first would pass every test above.
func TestBlockedByNumber_ResolvesBlockerOverREST(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*ProjectService, context.Context) error
	}{
		{"add", func(p *ProjectService, ctx context.Context) error {
			return p.AddBlockedByNumber(ctx, "o", "r", 1, 2)
		}},
		{"remove", func(p *ProjectService, ctx context.Context) error {
			return p.RemoveBlockedByNumber(ctx, "o", "r", 1, 2)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			// #1 resolves; #2 is absent and 404s, so the call fails at the
			// blocker's resolution — before the mutation, and only if the
			// blocker is actually resolved over REST at all.
			srv := blockedByRESTServer(t, map[int]string{
				1: issueJSON(1, "I_a", ""),
			}, &seen)
			defer srv.Close()

			p := &ProjectService{client: newClientForRESTTest(srv)}
			err := tc.call(p, context.Background())
			if err == nil {
				t.Fatal("call succeeded despite an unresolvable blocker")
			}
			if !strings.Contains(err.Error(), "blocker issue #2") {
				t.Errorf("error = %q, want it to name the blocker", err)
			}
			want := []string{"GET /repos/o/r/issues/1", "GET /repos/o/r/issues/2"}
			if len(seen) != len(want) {
				t.Fatalf("requests = %v, want %v", seen, want)
			}
			for i := range want {
				if seen[i] != want[i] {
					t.Errorf("request[%d] = %q, want %q", i, seen[i], want[i])
				}
			}
		})
	}
}

// An unresolvable BLOCKED issue must fail too, and say which one it was.
func TestBlockedByNumber_UnresolvableBlockedIssueIsAnError(t *testing.T) {
	var seen []string
	srv := blockedByRESTServer(t, map[int]string{}, &seen)
	defer srv.Close()

	p := &ProjectService{client: newClientForRESTTest(srv)}
	err := p.RemoveBlockedByNumber(context.Background(), "o", "r", 7, 8)
	if err == nil || !strings.Contains(err.Error(), "blocked issue #7") {
		t.Fatalf("err = %v, want an error naming the blocked issue", err)
	}
}

func TestBlocksOwnParent(t *testing.T) {
	cases := []struct {
		name                      string
		blockedParent, blockerNum int
		want                      bool
	}{
		{"blocker is the parent", 842, 842, true},
		{"blocker is a sibling", 842, 848, false},
		// Zero means "no parent, or parent unknown". It must never guard: a
		// false positive rejects a legitimate edge, which is the expensive
		// direction of this decision.
		{"no parent", 0, 848, false},
		{"no parent, blocker numbered zero", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blocksOwnParent(tc.blockedParent, tc.blockerNum); got != tc.want {
				t.Errorf("blocksOwnParent(%d, %d) = %v, want %v",
					tc.blockedParent, tc.blockerNum, got, tc.want)
			}
		})
	}
}
