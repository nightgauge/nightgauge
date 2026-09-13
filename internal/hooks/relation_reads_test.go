package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// followUpFailForge is a fake GitHub GraphQL endpoint for the hooks' real
// issue read. It serves an issue with the first page of every relationship
// connection the query selects, as GitHub does, and answers every later page
// read (the node(id:) follow-ups of internal/github/connection_paging.go) with
// a 502. A hook that reads a list longer than its first page therefore fails,
// and one that reads only what it uses does not.
type followUpFailForge struct {
	t      *testing.T
	issues map[int]forgeIssue

	mu        sync.Mutex
	followUps int
}

// forgeIssue is one issue of the fake. The list sizes are how many issues
// each relationship connection holds; the members are open issues numbered
// from 1000.
type forgeIssue struct {
	state, body                    string
	subIssues, blockedBy, blocking int
}

var reFirstPage = regexp.MustCompile(`(subIssues|blockedBy|blocking)\(first: (\d+)\)`)

// issueService starts the fake and returns an issue service aimed at it.
func (f *followUpFailForge) issueService() *gh.IssueService {
	f.t.Helper()
	// The real client records every request in the API ledger, by default
	// under the working directory — the package source tree. Keep it out.
	f.t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(f.t.TempDir(), "github-api.jsonl"))
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(srv.Close)
	return gh.NewIssueService(gh.NewClientWithURL("test-token", srv.URL))
}

// followUpCount is the number of later-page reads the fake has refused.
func (f *followUpFailForge) followUpCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.followUps
}

func (f *followUpFailForge) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !strings.Contains(req.Query, "issue(number: $number)") {
		f.mu.Lock()
		f.followUps++
		f.mu.Unlock()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	number := int(req.Variables["number"].(float64))
	iss, ok := f.issues[number]
	if !ok {
		f.t.Errorf("followUpFailForge: no issue #%d", number)
		http.Error(w, "no such issue", http.StatusNotFound)
		return
	}
	fields := map[string]interface{}{
		"id":     fmt.Sprintf("I_%d", number),
		"number": number,
		"title":  fmt.Sprintf("Issue %d", number),
		"body":   iss.body,
		"state":  iss.state,
	}
	sizes := map[string]int{"subIssues": iss.subIssues, "blockedBy": iss.blockedBy, "blocking": iss.blocking}
	for _, m := range reFirstPage.FindAllStringSubmatch(req.Query, -1) {
		conn := m[1]
		first, _ := strconv.Atoi(m[2])
		nodes := make([]interface{}, 0, first)
		for i := 0; i < min(first, sizes[conn]); i++ {
			nodes = append(nodes, map[string]interface{}{
				"id":         fmt.Sprintf("I_%d", 1000+i),
				"number":     1000 + i,
				"title":      fmt.Sprintf("Issue %d", 1000+i),
				"state":      "OPEN",
				"repository": map[string]interface{}{"nameWithOwner": "acme/widgets"},
			})
		}
		fields[conn] = map[string]interface{}{
			"pageInfo": map[string]interface{}{"hasNextPage": sizes[conn] > first, "endCursor": "page-1"},
			"nodes":    nodes,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": map[string]interface{}{"repository": map[string]interface{}{"issue": fields}},
	})
}

// TestEvaluateIssueDeps_LongListsItDoesNotUseCannotFailIt drives the gate
// through the real issue read. #10 has 30 sub-issues and blocks 7 issues, and
// its body depends on OPEN #20, which blocks 7 more; every page after the
// first fails. None of those lists is a dependency of #10, so the gate must
// read none of them: when it read them it failed, the skills' fallback turned
// the failure into "no open dependencies", and #10 was picked up while #20 was
// still open.
func TestEvaluateIssueDeps_LongListsItDoesNotUseCannotFailIt(t *testing.T) {
	forge := &followUpFailForge{t: t, issues: map[int]forgeIssue{
		10: {state: "OPEN", body: "Depends on: #20", subIssues: 30, blocking: 7},
		20: {state: "OPEN", blocking: 7},
	}}

	result, err := EvaluateIssueDeps(context.Background(), forge.issueService(), "acme", "widgets", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.ShouldBlock || result.OpenCount != 1 || result.OpenDependencies[0].Number != 20 {
		t.Fatalf("result = %+v, want #10 held by its open dependency #20", result)
	}
	if n := forge.followUpCount(); n != 0 {
		t.Errorf("the gate read %d later page(s) of lists it does not use", n)
	}
}

// TestEvaluateIssueDeps_LongBlockedByIsStillReadWhole: #10 has 11 blockers,
// more than the first page holds. The gate evaluates that list, so it must
// read past the first page, and a later page it cannot read fails the gate
// rather than leaving it to judge #10 from 5 of its 11 blockers.
func TestEvaluateIssueDeps_LongBlockedByIsStillReadWhole(t *testing.T) {
	forge := &followUpFailForge{t: t, issues: map[int]forgeIssue{
		10: {state: "OPEN", blockedBy: 11},
	}}

	result, err := EvaluateIssueDeps(context.Background(), forge.issueService(), "acme", "widgets", 10)
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("err = %v (result %+v), want ErrConnectionTruncated", err, result)
	}
	if n := forge.followUpCount(); n != 1 {
		t.Errorf("later-page reads = %d, want 1: the blockedBy list must be read past its first page", n)
	}
}

// TestEvaluatePostMerge_LongListsCannotFailTheMerge drives the post-merge hook
// through the real issue read. The merged issue blocks 7 issues, and in one
// case is also an epic with 30 sub-issues; every page after the first fails.
// The hook uses neither list, only whether sub-issues exist, so it must read
// neither: when it did, the merge ended at issue_fetch_error with the issue
// left open, the board not moved to Done and the parent epic not rolled up.
func TestEvaluatePostMerge_LongListsCannotFailTheMerge(t *testing.T) {
	cases := []struct {
		name   string
		issue  forgeIssue
		isEpic bool
	}{
		{"blocks 7 issues", forgeIssue{state: "OPEN", blocking: 7}, false},
		{"epic with 30 sub-issues that blocks 7", forgeIssue{state: "OPEN", subIssues: 30, blocking: 7}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forge := &followUpFailForge{t: t, issues: map[int]forgeIssue{10: tc.issue}}
			closer := &mockIssueCloser{}
			epics := &mockEpicAutoCloser{}

			result := EvaluatePostMerge(context.Background(), forge.issueService(), closer, epics, nil, nil, PostMergeInput{
				IssueNumber:     10,
				RepositoryOwner: "acme",
				RepositoryName:  "widgets",
			})

			if result.Failed || result.Reason != "no_parent" {
				t.Fatalf("result = %+v, want the merge reconciled (reason no_parent, not failed)", result)
			}
			if !closer.called || closer.nodeID != "I_10" {
				t.Errorf("CloseIssue called=%v node=%q, want the merged issue I_10 closed", closer.called, closer.nodeID)
			}
			if !result.IssueClosed {
				t.Error("IssueClosed = false, want true")
			}
			if epics.orphanCalled != tc.isEpic {
				t.Errorf("orphan-sub close ran = %v, want %v: whether the issue is an epic comes from its first page",
					epics.orphanCalled, tc.isEpic)
			}
			if n := forge.followUpCount(); n != 0 {
				t.Errorf("the hook read %d later page(s) of lists it does not use", n)
			}
		})
	}
}
