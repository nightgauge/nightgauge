package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
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
		_, _ = w.Write([]byte(`{"number":0,"id":4242,"node_id":"I_would_be_valid","parent_issue_url":null}`))
	}))
}

// databaseIDFor is the fake database id this package's REST fixtures carry for
// a given issue number. Derived rather than hand-written so a test asserting a
// write body can name the same value the fixture served, without either side
// hard-coding a magic number the other might drift from.
//
// Deliberately NOT equal to the issue number: the sub-issue and dependency
// endpoints take the database id where the surrounding path takes the number,
// and a fixture where the two coincide would let a call that passes the wrong
// one of the pair pass its test.
func databaseIDFor(number int) int64 { return int64(number) + 5_000_000_000 }

func issueJSON(number int, nodeID, parentURL string) string {
	parent := "null"
	if parentURL != "" {
		parent = fmt.Sprintf("%q", parentURL)
	}
	return fmt.Sprintf(`{"number":%d,"id":%d,"node_id":%q,"parent_issue_url":%s}`,
		number, databaseIDFor(number), nodeID, parent)
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
	err := p.AddBlockedByNumber(context.Background(), issueAt("o/r", 849), issueAt("o/r", 842))
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
	err := p.AddBlockedByNumber(context.Background(), issueAt("o/r", 925), issueAt("o/r", 1))
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
			return p.AddBlockedByNumber(ctx, issueAt("o/r", 1), issueAt("o/r", 2))
		}},
		{"remove", func(p *ProjectService, ctx context.Context) error {
			return p.RemoveBlockedByNumber(ctx, issueAt("o/r", 1), issueAt("o/r", 2))
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
			if !strings.Contains(err.Error(), "blocker issue o/r#2") {
				t.Errorf("error = %q, want it to name the blocker", err)
			}
			// The ADD path resolves both: it needs the blocked issue's
			// parent number for the circular-dependency guard. The REMOVE
			// path has no guard, and the endpoint addresses the blocked issue
			// by number, so it resolves only the blocker -- a read made
			// solely to produce an error the write itself reports is a call
			// worth not making.
			want := []string{"GET /repos/o/r/issues/1", "GET /repos/o/r/issues/2"}
			if tc.name == "remove" {
				want = []string{"GET /repos/o/r/issues/2"}
			}
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
//
// Only the add path reads the blocked issue (for the guard), so this pins the
// add path. On the remove path an absent blocked issue surfaces from the write
// as a 404 naming both refs -- covered by
// TestLinkWrites_404NamesTheAbsentIssueNotTheRoute.
func TestBlockedByNumber_UnresolvableBlockedIssueIsAnError(t *testing.T) {
	var seen []string
	srv := blockedByRESTServer(t, map[int]string{}, &seen)
	defer srv.Close()

	p := &ProjectService{client: newClientForRESTTest(srv)}
	err := p.AddBlockedByNumber(context.Background(), issueAt("o/r", 7), issueAt("o/r", 8))
	if err == nil || !strings.Contains(err.Error(), "blocked issue o/r#7") {
		t.Fatalf("err = %v, want an error naming the blocked issue", err)
	}
}

func TestBlocksOwnParent(t *testing.T) {
	cases := []struct {
		name        string
		parentRepo  string
		parentNum   int
		blockerRepo string
		blockerNum  int
		want        bool
	}{
		{"blocker is the parent", "o/r", 842, "o/r", 842, true},
		{"repository names compare case-insensitively", "O/R", 842, "o/r", 842, true},
		{"blocker is a sibling", "o/r", 842, "o/r", 848, false},
		// The parent lives in another repository: a blocker that only shares
		// its number is a different issue and a legitimate edge (#2369).
		{"same number, parent in another repository", "o/platform", 842, "o/r", 842, false},
		// Zero means "no parent, or parent unknown", and so does a parent
		// whose repository could not be read. Neither may guard: a false
		// positive rejects a legitimate edge, which is the expensive
		// direction of this decision.
		{"no parent", "", 0, "o/r", 848, false},
		{"no parent, blocker numbered zero", "", 0, "o/r", 0, false},
		{"parent repository unknown", "", 842, "o/r", 842, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blocksOwnParent(tc.parentRepo, tc.parentNum, tc.blockerRepo, tc.blockerNum); got != tc.want {
				t.Errorf("blocksOwnParent(%q, %d, %q, %d) = %v, want %v",
					tc.parentRepo, tc.parentNum, tc.blockerRepo, tc.blockerNum, got, tc.want)
			}
		})
	}
}

// #2369: the blocked issue's parent is o/platform#842, and the blocker is
// o/r#842, an unrelated issue that shares the number. The edge is legitimate,
// so the guard must let it through to the write. The fixture serves no write
// endpoint, so the call fails there, after the guard.
func TestAddBlockedByNumber_SameNumberAsAParentInAnotherRepoIsNotCircular(t *testing.T) {
	var seen []string
	srv := blockedByRESTServer(t, map[int]string{
		849: issueJSON(849, "I_child", "https://api.github.com/repos/o/platform/issues/842"),
		842: issueJSON(842, "I_unrelated", ""),
	}, &seen)
	defer srv.Close()

	p := &ProjectService{client: newClientForRESTTest(srv)}
	err := p.AddBlockedByNumber(context.Background(), issueAt("o/r", 849), issueAt("o/r", 842))
	if err != nil && strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("refused a legitimate edge: %v", err)
	}
	wrote := false
	for _, req := range seen {
		if strings.HasPrefix(req, "POST /repos/o/r/issues/849/dependencies/blocked_by") {
			wrote = true
		}
	}
	if !wrote {
		t.Errorf("the edge never reached the write; requests = %v", seen)
	}
}

func TestParentIssueRepo(t *testing.T) {
	for raw, want := range map[string]string{
		"https://api.github.com/repos/o/platform/issues/842": "o/platform",
		"https://ghe.example/api/v3/repos/o/r/issues/7":      "o/r",
		"":                                 "",
		"https://api.github.com/repos/o/r": "",
		"https://api.github.com/repos//r/issues/7": "",
		"https://api.github.com/issues/7":          "",
		"not a url":                                "",
	} {
		if got := parentIssueRepo(raw); got != want {
			t.Errorf("parentIssueRepo(%q) = %q, want %q", raw, got, want)
		}
	}
}

// issueAt builds a forge.IssueRef from an "owner/repo" slug.
func issueAt(slug string, number int) forge.IssueRef {
	owner, repo, _ := strings.Cut(slug, "/")
	return forge.IssueRef{Owner: owner, Repo: repo, Number: number}
}

// blockedByPathServer serves REST issue objects keyed by their full path, so a
// fixture can hold two issues that share a number in different repositories.
// Every request is recorded with its body; any write succeeds with 201.
func blockedByPathServer(t *testing.T, byPath map[string]string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		entry := r.Method + " " + r.URL.Path
		if len(body) > 0 {
			entry += " " + strings.TrimSpace(string(body))
		}
		*seen = append(*seen, entry)
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if b, ok := byPath[r.URL.Path]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
}

// #1508: a blocker in another repository is resolved from ITS repository, and
// the edge is written on the blocked issue's repository with the blocker's
// database id. Both repositories hold an issue numbered 1253, with different
// ids, so resolving the blocker in the blocked issue's repository would send
// the wrong id.
func TestBlockedByNumber_CrossRepoBlocker(t *testing.T) {
	const otherID = 7_777_777
	issues := map[string]string{
		"/repos/o/dash/issues/692":      issueJSON(692, "I_dash", ""),
		"/repos/o/platform/issues/1253": issueJSON(1253, "I_platform", ""),
		"/repos/o/dash/issues/1253": fmt.Sprintf(
			`{"number":1253,"id":%d,"node_id":"I_wrong","parent_issue_url":null}`, otherID),
	}
	blocked, blocker := issueAt("o/dash", 692), issueAt("o/platform", 1253)
	id := databaseIDFor(1253)

	t.Run("add", func(t *testing.T) {
		var seen []string
		srv := blockedByPathServer(t, issues, &seen)
		defer srv.Close()
		p := &ProjectService{client: newClientForRESTTest(srv)}
		if err := p.AddBlockedByNumber(context.Background(), blocked, blocker); err != nil {
			t.Fatalf("AddBlockedByNumber: %v", err)
		}
		want := []string{
			"GET /repos/o/dash/issues/692",
			"GET /repos/o/platform/issues/1253",
			fmt.Sprintf(`POST /repos/o/dash/issues/692/dependencies/blocked_by {"issue_id":%d}`, id),
		}
		if strings.Join(seen, "\n") != strings.Join(want, "\n") {
			t.Errorf("requests =\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
		}
	})
	t.Run("remove", func(t *testing.T) {
		var seen []string
		srv := blockedByPathServer(t, issues, &seen)
		defer srv.Close()
		p := &ProjectService{client: newClientForRESTTest(srv)}
		if err := p.RemoveBlockedByNumber(context.Background(), blocked, blocker); err != nil {
			t.Fatalf("RemoveBlockedByNumber: %v", err)
		}
		want := []string{
			"GET /repos/o/platform/issues/1253",
			fmt.Sprintf("DELETE /repos/o/dash/issues/692/dependencies/blocked_by/%d", id),
		}
		if strings.Join(seen, "\n") != strings.Join(want, "\n") {
			t.Errorf("requests =\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
		}
	})
}

// #1508: the parent-epic guard compares against the BLOCKER's own repository.
// The blocked issue o/dash#849 has its parent epic in o/platform#842. Naming
// that epic as a cross-repo blocker is the cycle and is refused; o/other#842,
// which only shares the number, is a legitimate edge and reaches the write.
func TestAddBlockedByNumber_CrossRepoParentEpicGuard(t *testing.T) {
	issues := map[string]string{
		"/repos/o/dash/issues/849":     issueJSON(849, "I_child", "https://api.github.com/repos/o/platform/issues/842"),
		"/repos/o/platform/issues/842": issueJSON(842, "I_epic", ""),
		"/repos/O/Platform/issues/842": issueJSON(842, "I_epic", ""),
		"/repos/o/other/issues/842":    issueJSON(842, "I_unrelated", ""),
	}
	for _, tc := range []struct {
		blocker  string
		circular bool
	}{
		{"o/platform", true},
		{"O/Platform", true},
		{"o/other", false},
	} {
		t.Run(tc.blocker, func(t *testing.T) {
			var seen []string
			srv := blockedByPathServer(t, issues, &seen)
			defer srv.Close()
			p := &ProjectService{client: newClientForRESTTest(srv)}
			err := p.AddBlockedByNumber(context.Background(), issueAt("o/dash", 849), issueAt(tc.blocker, 842))
			gotCircular := err != nil && strings.Contains(err.Error(), "circular dependency")
			if gotCircular != tc.circular {
				t.Fatalf("circular = %v (err %v), want %v", gotCircular, err, tc.circular)
			}
			wrote := false
			for _, req := range seen {
				wrote = wrote || strings.HasPrefix(req, "POST ")
			}
			if wrote == tc.circular {
				t.Errorf("write issued = %v, want %v; requests = %v", wrote, !tc.circular, seen)
			}
		})
	}
}
