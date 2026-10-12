package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchRecorder answers every GraphQL search with body and records the
// query string each request sent.
func searchRecorder(t *testing.T, body string) (*Client, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &req)
		q, _ := req.Variables["q"].(string)
		queries = append(queries, q)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewClientWithURL("test-token", srv.URL), &queries
}

// SearchIssues reaches the repository and state it pins and nothing else:
// GitHub ORs repeated scope qualifiers, so a repo:, org:, user:, is: or
// type: term in the caller's text widened the search to other repositories
// and closed issues. Those terms are removed, narrowing ones are kept, and a
// result outside the scope is dropped all the same.
func TestIssueService_SearchIssues_StaysInItsRepository(t *testing.T) {
	body := `{"data":{"search":{"issueCount":3,"nodes":[
		{"__typename":"Issue","id":"I_1","number":1,"title":"ours","state":"OPEN","url":"u","repository":{"nameWithOwner":"o/r"},"labels":{"nodes":[]}},
		{"__typename":"Issue","id":"I_2","number":2,"title":"theirs","state":"OPEN","url":"u","repository":{"nameWithOwner":"o/private"},"labels":{"nodes":[]}},
		{"__typename":"Issue","id":"I_3","number":3,"title":"closed","state":"CLOSED","url":"u","repository":{"nameWithOwner":"O/R"},"labels":{"nodes":[]}}
	]}}}`
	client, queries := searchRecorder(t, body)
	svc := NewIssueService(client)
	issues, err := svc.SearchIssues(context.Background(), "o", "r",
		`login repo:o/private ORG:other -user:x is:closed type:pr state:closed archived:true "repo:o/x" (org:y) label:bug in:title bug`, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(*queries) != 1 {
		t.Fatalf("queries = %v", *queries)
	}
	if got, want := (*queries)[0], "type:issue repo:o/r is:open login label:bug in:title bug"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
	if len(issues) != 1 || issues[0].Number != 1 {
		t.Errorf("issues = %+v, want only o/r's open #1", issues)
	}

	// The repository arguments are part of the query too.
	for _, bad := range [][2]string{{"o", "r org:other"}, {"o rg", "r"}, {"o", ""}, {"", "r"}, {"o", ".."}} {
		if _, err := svc.SearchIssues(context.Background(), bad[0], bad[1], "x", 5); err == nil {
			t.Errorf("SearchIssues(%q, %q) was sent", bad[0], bad[1])
		}
	}
	if len(*queries) != 1 {
		t.Errorf("a refused search reached GitHub: %v", *queries)
	}
}

func TestConfineSearchQuery(t *testing.T) {
	for in, want := range map[string]string{
		"migrate gh cli":                 "migrate gh cli",
		"repo:a/b fix":                   "fix",
		"fix,repo:a/b":                   "",
		"Repo : a/b":                     "Repo : a/b", // not a qualifier to GitHub
		"repo: a/b fix":                  "a/b fix",
		"label:\"good first issue\" bug": "label:\"good first issue\" bug",
		"no:assignee author:me is:open":  "no:assignee author:me",
	} {
		if got := confineSearchQuery(in); got != want {
			t.Errorf("confineSearchQuery(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(confineSearchQuery("x REPOSITORY:a/b"), "REPOSITORY") {
		t.Error("an upper-case qualifier survived")
	}
}
