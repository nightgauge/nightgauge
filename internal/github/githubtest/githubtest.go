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
// An issue listed in Forge.Unreadable cannot be read at all, which is how a
// test fails a first page rather than a later one.
//
// Beyond issue reads it answers what the readers under test go on to do:
// mutations succeed and are recorded, the project-fields read, the project
// item read and the repository label list are served, and nothing else is. A
// request the fake does not model fails the test.
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
	// Status is the issue's Status on the organization's project board. An
	// issue with no Status is not on the board.
	Status string
	// ShippedBy is a merged pull request that names the issue as closing, 0
	// for none. It is served by the ship-evidence read (#2602).
	ShippedBy int
	// ClosedByHead, when set, is the head branch of the merged pull request
	// (numbered ClosedByPR) that most recently closed the issue.
	ClosedByHead string
	ClosedByPR   int
	// Comments are the bodies of the issue's comments, oldest first.
	Comments []string
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
// RepoLabels and any unreadable issues to Unreadable, then call Client.
type Forge struct {
	t      testing.TB
	issues map[int]Issue
	// RepoLabels are repository labels served beyond those the issues carry.
	RepoLabels []string
	// Unreadable are issues every read of which fails, first page and all,
	// as GitHub fails an issue it cannot serve: a read of the issue alone is
	// a GraphQL error, and a batch read returns it null beside an error while
	// the rest of the batch reads.
	Unreadable []int

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
	reItemQuery = regexp.MustCompile(`#(\d+)$`)
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
	var gqlErrors []interface{}
	switch {
	case strings.Contains(q, "projectV2(number: $projectNumber)") && strings.Contains(q, "items("):
		// The project item read, before the follow-up case: its items
		// connection pages with an $after variable of its own.
		data = map[string]interface{}{"organization": map[string]interface{}{
			"projectV2": map[string]interface{}{
				"id":    "PVT_1",
				"title": "Board",
				"items": map[string]interface{}{
					"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					"nodes":    f.boardItems(q, fmt.Sprint(req.Variables["query"])),
				},
			},
		}}
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
			if f.unreadable(n) {
				repo["i"+m[1]] = nil
				gqlErrors = append(gqlErrors, unreadableError(n, "i"+m[1]))
				continue
			}
			repo["i"+m[1]] = f.issueFields(q, n)
		}
		data = map[string]interface{}{"repository": repo}
	case strings.Contains(q, "timelineItems("):
		n, _ := req.Variables["number"].(float64)
		data = map[string]interface{}{"repository": map[string]interface{}{"issue": f.shipEvidence(int(n))}}
	case strings.Contains(q, "issue(number: $number)") && !strings.Contains(q, "projectItems("):
		n, _ := req.Variables["number"].(float64)
		if f.unreadable(int(n)) {
			data = map[string]interface{}{"repository": map[string]interface{}{"issue": nil}}
			gqlErrors = append(gqlErrors, unreadableError(int(n), "issue"))
			break
		}
		data = map[string]interface{}{"repository": map[string]interface{}{"issue": f.issueFields(q, int(n))}}
	default:
		f.t.Errorf("githubtest: unmodelled request %q", q)
		http.Error(w, "unmodelled request", http.StatusBadRequest)
		return
	}
	body := map[string]interface{}{"data": data}
	if len(gqlErrors) > 0 {
		body["errors"] = gqlErrors
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// shipEvidence renders issue n's ship-evidence timeline: its comments, then
// the pull request close ClosedByHead describes, and the closing reference
// ShippedBy names.
func (f *Forge) shipEvidence(n int) interface{} {
	iss, ok := f.issues[n]
	if !ok {
		f.t.Errorf("githubtest: no issue #%d", n)
		return nil
	}
	nodes := []interface{}{}
	for _, c := range iss.Comments {
		nodes = append(nodes, map[string]interface{}{"__typename": "IssueComment", "body": c})
	}
	if iss.ClosedByHead != "" {
		nodes = append(nodes, map[string]interface{}{
			"__typename": "ClosedEvent",
			"closer":     pullRequest(iss.ClosedByPR, iss.ClosedByHead),
		})
	}
	closing := []interface{}{}
	if iss.ShippedBy != 0 {
		pr := pullRequest(iss.ShippedBy, fmt.Sprintf("feat/%d", n))
		delete(pr, "__typename") // the connection's type is fixed; it is not selected
		closing = append(closing, pr)
	}
	return map[string]interface{}{
		"timelineItems":                  map[string]interface{}{"nodes": nodes},
		"closedByPullRequestsReferences": map[string]interface{}{"nodes": closing},
	}
}

// pullRequest renders merged pull request number from branch head.
func pullRequest(number int, head string) map[string]interface{} {
	return map[string]interface{}{"__typename": "PullRequest", "number": number, "merged": true, "headRefName": head}
}

// unreadable reports whether n is one of f.Unreadable.
func (f *Forge) unreadable(n int) bool {
	for _, u := range f.Unreadable {
		if u == n {
			return true
		}
	}
	return false
}

// unreadableError is the GraphQL error GitHub returns beside a null issue n
// at field: a server-side failure, not a rate limit, so it is not retried.
func unreadableError(n int, field string) map[string]interface{} {
	return map[string]interface{}{
		"path":    []interface{}{"repository", field},
		"message": fmt.Sprintf("Something went wrong while executing your query (issue #%d).", n),
	}
}

// boardItems renders the board's items that the items(query:) filter selects:
// the issue its "#<n>" names, or every issue with a Status when it names none.
// Each carries the first page of each relationship connection q selects, and
// its Status as a single-select field value.
func (f *Forge) boardItems(q, filter string) []interface{} {
	var numbers []int
	if m := reItemQuery.FindStringSubmatch(filter); m != nil {
		n, _ := strconv.Atoi(m[1])
		numbers = []int{n}
	} else {
		for n := range f.issues {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
	}
	nodes := []interface{}{}
	for _, n := range numbers {
		iss, ok := f.issues[n]
		if !ok || iss.Status == "" {
			continue
		}
		content, _ := f.issueFields(q, n).(map[string]interface{})
		// The item's content selects neither the body nor the parent's id,
		// and the client refuses a field it did not select.
		delete(content, "body")
		if parent, ok := content["parent"].(map[string]interface{}); ok {
			delete(parent, "id")
		}
		content["__typename"] = "Issue"
		content["url"] = fmt.Sprintf("https://github.com/%s/%s/issues/%d", Owner, Repo, n)
		content["repository"] = map[string]interface{}{"nameWithOwner": Owner + "/" + Repo}
		nodes = append(nodes, map[string]interface{}{
			"id":      fmt.Sprintf("PVTI_%d", n),
			"content": content,
			"fieldValues": map[string]interface{}{"nodes": []interface{}{map[string]interface{}{
				"__typename": "ProjectV2ItemFieldSingleSelectValue",
				"name":       iss.Status,
				"field":      map[string]interface{}{"name": "Status"},
			}}},
		})
	}
	return nodes
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
	if strings.Contains(q, "subIssuesSummary{total}") {
		out["subIssuesSummary"] = map[string]interface{}{"total": len(iss.SubIssues)}
	}
	members := map[string][]int{"subIssues": iss.SubIssues, "blockedBy": iss.BlockedBy, "blocking": iss.Blocking}
	for _, m := range reFirstPage.FindAllStringSubmatch(q, -1) {
		conn := m[1]
		first, ok := f.pageSize(conn, m[2])
		if !ok {
			continue
		}
		all := members[conn]
		page := all[:min(first, len(all))]
		nodes := make([]interface{}, 0, len(page))
		for _, member := range page {
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

// maxPageSize is the largest page GitHub serves a connection; it rejects a
// query that asks for more.
const maxPageSize = 100

// pageSize parses the page size a query asks of connection conn. GitHub
// accepts 1 to maxPageSize, so the fake fails the test on anything else
// rather than render a page of that size.
func (f *Forge) pageSize(conn, s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxPageSize {
		f.t.Errorf("githubtest: %s(first: %s) is outside GitHub's page size range 1..%d", conn, s, maxPageSize)
		return 0, false
	}
	return n, true
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
