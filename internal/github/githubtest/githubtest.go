// Package githubtest serves a fake GitHub endpoint to tests that drive the
// real internal/github readers.
//
// The fake models the behaviour those tests are about. GitHub returns an
// issue's relationship connections (subIssues, blockedBy, blocking) as a first
// page, and internal/github reads any later page through a node(id:) follow-up
// request. This fake answers every follow-up request with a 502. A reader that
// follows a list longer than its first page therefore fails, and a reader that
// follows only the lists its caller uses does not, which is how a test tells
// the two apart.
//
// Beyond issue reads it answers what the readers under test go on to do:
// mutations succeed and are recorded, the project-fields read and the
// repository label list are served, and nothing else is. A request the fake
// does not model fails the test.
package githubtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// The repository the fake serves. Every issue, and every member of a
// relationship connection, belongs to it.
const (
	Owner = "acme"
	Repo  = "widgets"
)

// Issue is one issue of the fake.
type Issue struct {
	// State is OPEN or CLOSED; empty reads as OPEN.
	State       string
	StateReason string
	Body        string
	Labels      []string
	// Parent is the number of the issue's parent epic, 0 for none.
	Parent int
	// SubIssues, BlockedBy and Blocking are the members of each relationship
	// connection, by issue number. A member is served with the state the fake
	// holds for it, and as OPEN when the fake does not hold it.
	SubIssues, BlockedBy, Blocking []int
}

// Numbers returns n consecutive issue numbers starting at from, for building a
// long relationship list.
func Numbers(from, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = from + i
	}
	return out
}

// Forge is the fake endpoint. Build it with New, add any repository labels to
// RepoLabels, then call Client.
type Forge struct {
	t      testing.TB
	issues map[int]Issue
	// RepoLabels are repository labels served beyond those the issues carry.
	RepoLabels []string

	mu        sync.Mutex
	followUps int
	mutations []string
}

// New returns a fake serving issues, keyed by number.
func New(t testing.TB, issues map[int]Issue) *Forge {
	return &Forge{t: t, issues: issues}
}

// Client starts the fake and returns a client aimed at it. The client keeps
// the real rate limiter, which allows a burst of five requests and then about
// one a second, so a test should stay within five requests per client.
func (f *Forge) Client() *gh.Client {
	f.t.Helper()
	// The real client records every request in the API ledger, by default
	// under the working directory: for a test, its package's source tree.
	f.t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(f.t.TempDir(), "github-api.jsonl"))
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(srv.Close)
	return gh.NewClientWithURL("test-token", srv.URL)
}

// FollowUps returns how many later-page reads the fake has refused.
func (f *Forge) FollowUps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.followUps
}

// Mutations returns the root field of every mutation served, in order, such
// as closeIssue or addLabelsToLabelable.
func (f *Forge) Mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mutations...)
}

var (
	reFirstPage = regexp.MustCompile(`(subIssues|blockedBy|blocking)\(first: (\d+)\)`)
	reBatchItem = regexp.MustCompile(`i(\d+): issue\(number: (\d+)\)`)
	reMutation  = regexp.MustCompile(`^mutation\b[^{]*\{\s*(\w+)\(`)
)

func (f *Forge) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/%s/%s/labels", Owner, Repo) {
		f.serveLabels(w)
		return
	}
	var req struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("githubtest: %s %s is not a GraphQL request: %v", r.Method, r.URL.Path, err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	q := req.Query
	var data map[string]interface{}
	switch {
	case strings.Contains(q, "after: $after"):
		// A follow-up page of a relationship connection.
		f.mu.Lock()
		f.followUps++
		f.mu.Unlock()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	case reMutation.MatchString(q):
		field := reMutation.FindStringSubmatch(q)[1]
		f.mu.Lock()
		f.mutations = append(f.mutations, field)
		f.mu.Unlock()
		data = map[string]interface{}{}
		if field == "addProjectV2ItemById" {
			data[field] = map[string]interface{}{"item": map[string]interface{}{"id": "PVTI_1"}}
		}
	case strings.Contains(q, "projectV2(number: $projectNumber)") && strings.Contains(q, "fields("):
		data = map[string]interface{}{"organization": map[string]interface{}{
			"projectV2": map[string]interface{}{
				"id":     "PVT_1",
				"fields": map[string]interface{}{"nodes": []interface{}{}},
			},
		}}
	case strings.Contains(q, "fragment IssueFields"):
		repo := map[string]interface{}{}
		for _, m := range reBatchItem.FindAllStringSubmatch(q, -1) {
			n, _ := strconv.Atoi(m[2])
			repo["i"+m[1]] = f.issueFields(q, n)
		}
		data = map[string]interface{}{"repository": repo}
	case strings.Contains(q, "issue(number: $number)") && !strings.Contains(q, "projectItems("):
		n, _ := req.Variables["number"].(float64)
		data = map[string]interface{}{"repository": map[string]interface{}{"issue": f.issueFields(q, int(n))}}
	default:
		f.t.Errorf("githubtest: unmodelled request %q", q)
		http.Error(w, "unmodelled request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

// issueFields renders issue n with the first page of each relationship
// connection q selects, at the size q asks for, or nil when the fake does not
// hold n, as GitHub renders a missing issue.
func (f *Forge) issueFields(q string, n int) interface{} {
	iss, ok := f.issues[n]
	if !ok {
		f.t.Errorf("githubtest: no issue #%d", n)
		return nil
	}
	out := map[string]interface{}{
		"id":     nodeID(n),
		"number": n,
		"title":  fmt.Sprintf("Issue %d", n),
		"body":   iss.Body,
		"state":  f.state(n),
	}
	if iss.StateReason != "" && strings.Contains(q, "stateReason") {
		out["stateReason"] = iss.StateReason
	}
	if len(iss.Labels) > 0 && strings.Contains(q, "labels(first: 10)") {
		nodes := make([]interface{}, 0, len(iss.Labels))
		for _, l := range iss.Labels {
			nodes = append(nodes, map[string]interface{}{"name": l})
		}
		out["labels"] = map[string]interface{}{"nodes": nodes}
	}
	if iss.Parent != 0 && strings.Contains(q, "parent") {
		out["parent"] = map[string]interface{}{
			"id":         nodeID(iss.Parent),
			"number":     iss.Parent,
			"title":      fmt.Sprintf("Issue %d", iss.Parent),
			"repository": map[string]interface{}{"nameWithOwner": Owner + "/" + Repo},
		}
	}
	members := map[string][]int{"subIssues": iss.SubIssues, "blockedBy": iss.BlockedBy, "blocking": iss.Blocking}
	for _, m := range reFirstPage.FindAllStringSubmatch(q, -1) {
		conn := m[1]
		first, _ := strconv.Atoi(m[2])
		all := members[conn]
		nodes := make([]interface{}, 0, first)
		for _, member := range all[:min(first, len(all))] {
			nodes = append(nodes, map[string]interface{}{
				"id":         nodeID(member),
				"number":     member,
				"title":      fmt.Sprintf("Issue %d", member),
				"state":      f.state(member),
				"repository": map[string]interface{}{"nameWithOwner": Owner + "/" + Repo},
			})
		}
		out[conn] = map[string]interface{}{
			"pageInfo": map[string]interface{}{"hasNextPage": len(all) > first, "endCursor": "page-1"},
			"nodes":    nodes,
		}
	}
	return out
}

// serveLabels answers the REST repository label list with RepoLabels and
// every label an issue carries.
func (f *Forge) serveLabels(w http.ResponseWriter) {
	names := map[string]bool{}
	for _, l := range f.RepoLabels {
		names[l] = true
	}
	for _, iss := range f.issues {
		for _, l := range iss.Labels {
			names[l] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for l := range names {
		sorted = append(sorted, l)
	}
	sort.Strings(sorted)
	labels := make([]interface{}, 0, len(sorted))
	for _, l := range sorted {
		labels = append(labels, map[string]interface{}{"name": l, "node_id": LabelID(l), "color": "ededed"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(labels)
}

// LabelID is the node id the fake gives the repository label name.
func LabelID(name string) string {
	return "LA_" + name
}

func (f *Forge) state(n int) string {
	if s := f.issues[n].State; s != "" {
		return s
	}
	return "OPEN"
}

func nodeID(n int) string {
	return fmt.Sprintf("I_%d", n)
}
