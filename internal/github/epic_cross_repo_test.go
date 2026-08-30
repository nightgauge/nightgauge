package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #1181 — the epic number used to travel without its repository.
//
// The workspace that surfaced this has three repos on one board. The epics live
// in `platform`; the sub-issues live in `mobile` and
// `api`. Because issue numbers are per-repository, resolving an
// epic number against the SUB-ISSUE's repo does not fail cleanly — it has two
// faces, and both are reproduced below against a fake GitHub that answers
// per-(owner, repo, number) exactly as the real one does:
//
//	Face 1 (loud):   platform#207 is a merged PULL REQUEST, so GraphQL
//	                 `issue(number: 207)` refuses to resolve it.
//	Face 2 (silent): flutter#205 is a real, closed, sub-issue-less issue, so
//	                 CheckCompletion returns Total == 0 and the old code
//	                 answered ("skipped", "no_subs", nil) — success.
//
// The fixture below is the shape of that workspace.

type fakeIssueNode struct {
	// kind is "issue" or "pull_request". A pull request is not resolvable by
	// GraphQL's issue(number:) selector, which is Face 1.
	kind     string
	title    string
	state    string
	subs     []fakeSubIssue
	parent   string // "owner/repo#N" of the parent epic, empty for none
	parentID string
}

type fakeSubIssue struct {
	number int
	repo   string
	state  string
}

// fakeForge is a GraphQL endpoint that routes on (owner, name, number) — the
// only fixture shape that can express this bug at all, because the defect is
// precisely that two repos answer the same number differently.
type fakeForge struct {
	mu sync.Mutex
	// issues is keyed "owner/repo#number".
	issues map[string]fakeIssueNode
	// epicsByRepo drives ListIssues (the type:epic sweep), keyed "owner/repo".
	epicsByRepo map[string][]int
	// getIssueCalls records every (owner/repo#number) GetIssue actually asked
	// for, so a test can assert WHICH repository answered rather than only
	// what the answer was.
	getIssueCalls []string
	// closed records every node ID passed to CloseIssue.
	closed []string
}

func (f *fakeForge) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.getIssueCalls...)
}

func (f *fakeForge) closedNodes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

func (f *fakeForge) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("fakeForge: undecodable request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		owner, _ := req.Variables["owner"].(string)
		name, _ := req.Variables["name"].(string)

		switch {
		case strings.Contains(req.Query, "issue(number:"):
			num := 0
			if n, ok := req.Variables["number"].(float64); ok {
				num = int(n)
			}
			key := fmt.Sprintf("%s/%s#%d", owner, name, num)
			f.mu.Lock()
			f.getIssueCalls = append(f.getIssueCalls, key)
			node, ok := f.issues[key]
			f.mu.Unlock()
			if !ok || node.kind != "issue" {
				// Exactly what GitHub says when the number is a PR or absent.
				fmt.Fprintf(w, `{"errors":[{"message":"Could not resolve to an Issue with the number of %d."}]}`, num)
				return
			}
			fmt.Fprint(w, node.render(key))
		case strings.Contains(req.Query, "issues("):
			f.mu.Lock()
			nums := f.epicsByRepo[owner+"/"+name]
			f.mu.Unlock()
			var nodes []string
			for _, n := range nums {
				key := fmt.Sprintf("%s/%s#%d", owner+"", name, n)
				title := f.issues[key].title
				nodes = append(nodes, fmt.Sprintf(
					`{"id":"I_%d","number":%d,"title":%q,"state":"OPEN","url":"","labels":{"nodes":[{"name":"type:epic"}]},"milestone":{"title":""}}`,
					n, n, title))
			}
			fmt.Fprintf(w, `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[%s]}}}}`,
				strings.Join(nodes, ","))
		case strings.Contains(req.Query, "closeIssue"):
			id, _ := req.Variables["input"].(map[string]interface{})
			nodeID := ""
			if id != nil {
				nodeID, _ = id["issueId"].(string)
			}
			f.mu.Lock()
			f.closed = append(f.closed, nodeID)
			f.mu.Unlock()
			fmt.Fprint(w, `{"data":{"closeIssue":{"issue":{"id":"x"}}}}`)
		default:
			fmt.Fprint(w, `{"data":{}}`)
		}
	}
}

func (n fakeIssueNode) render(key string) string {
	var subs []string
	for _, s := range n.subs {
		subs = append(subs, fmt.Sprintf(
			`{"id":"SUB_%d","number":%d,"title":"sub","state":%q,"repository":{"nameWithOwner":%q},"labels":{"nodes":[]}}`,
			s.number, s.number, s.state, s.repo))
	}
	parent := `{"id":"","number":0,"title":"","repository":{"nameWithOwner":""}}`
	if n.parent != "" {
		repo, numStr, _ := strings.Cut(n.parent, "#")
		parent = fmt.Sprintf(`{"id":%q,"number":%s,"title":"epic","repository":{"nameWithOwner":%q}}`,
			n.parentID, numStr, repo)
	}
	num := key[strings.Index(key, "#")+1:]
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"id":"I_%s","number":%s,"title":%q,"body":"","state":%q,"stateReason":"","url":"",
		"parent":%s,
		"labels":{"nodes":[{"name":"type:epic"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[%s]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`, strings.ReplaceAll(key, "/", "_"), num, n.title, n.state, parent, strings.Join(subs, ","))
}

// acmeForge is the observed workspace, reduced to the two collisions.
func acmeForge() *fakeForge {
	return &fakeForge{
		issues: map[string]fakeIssueNode{
			// --- the real epics, in their OWN repo ---
			"acme/platform#205": {
				kind: "issue", title: "E49", state: "OPEN",
				subs: []fakeSubIssue{
					{number: 3001, repo: "acme/mobile", state: "CLOSED"},
					{number: 3002, repo: "acme/mobile", state: "CLOSED"},
				},
			},
			"acme/platform#207": {
				kind: "issue", title: "E51", state: "OPEN",
				subs: []fakeSubIssue{
					{number: 4001, repo: "acme/api", state: "CLOSED"},
				},
			},

			// --- Face 2: flutter#205 is a REAL closed issue with no subs ---
			"acme/mobile#205": {
				kind: "issue", title: "Redesign the settings sheet", state: "CLOSED",
			},
			// --- Face 1: platform#207 is a merged PULL REQUEST ---
			"acme/api#207": {kind: "pull_request", title: "chore: bump deps"},

			// --- the merged sub-issues, each pointing at its cross-repo parent ---
			"acme/mobile#3001": {
				kind: "issue", title: "sub", state: "CLOSED",
				parent: "acme/platform#205", parentID: "I_epic205",
			},
			"acme/mobile#3002": {
				kind: "issue", title: "sub", state: "CLOSED",
				parent: "acme/platform#205", parentID: "I_epic205",
			},
			"acme/api#4001": {
				kind: "issue", title: "sub", state: "CLOSED",
				parent: "acme/platform#207", parentID: "I_epic207",
			},
		},
		epicsByRepo: map[string][]int{
			"acme/platform": {205, 207},
		},
	}
}

func newForgeClient(t *testing.T, f *fakeForge) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return NewClientWithURL("test-token", srv.URL)
}

// TestGetIssue_ParentCarriesItsOwnRepository pins the authority the fix relies
// on: GitHub's native sub-issue link reports the parent's repository, and
// GetIssue surfaces it. Without this, everything downstream is guessing.
func TestGetIssue_ParentCarriesItsOwnRepository(t *testing.T) {
	f := acmeForge()
	svc := NewIssueService(newForgeClient(t, f))

	issue, err := svc.GetIssue(context.Background(), "acme", "mobile", 3001)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.ParentIssueNumber != 205 {
		t.Fatalf("ParentIssueNumber = %d, want 205", issue.ParentIssueNumber)
	}
	if issue.ParentIssueRepo != "acme/platform" {
		t.Errorf("ParentIssueRepo = %q, want %q — the parent's repo is the whole coordinate; without it #205 is ambiguous",
			issue.ParentIssueRepo, "acme/platform")
	}
}

// TestAutoCloseSingle_CrossRepoEpicClosesInItsOwnRepo is the headline
// acceptance criterion: a cross-repo epic whose sub-issues are all closed
// actually auto-closes, and the lookup goes to the EPIC's repository.
func TestAutoCloseSingle_CrossRepoEpicClosesInItsOwnRepo(t *testing.T) {
	f := acmeForge()
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{
		Owner:                "acme",
		Repo:                 "platform",
		Number:               205,
		ExpectSubIssueNumber: 3001,
		ExpectSubIssueRepo:   "acme/mobile",
	}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle: %v", err)
	}
	if res.Status != "closed" || res.Reason != "all_closed" {
		t.Fatalf("status/reason = %q/%q (error=%q), want closed/all_closed", res.Status, res.Reason, res.Error)
	}
	if res.EpicRepo != "acme/platform" {
		t.Errorf("EpicRepo = %q, want acme/platform", res.EpicRepo)
	}
	if len(f.closedNodes()) == 0 {
		t.Error("expected the epic to actually be closed on the forge")
	}
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "acme/mobile#205") {
			t.Errorf("epic #205 was looked up in the SUB-ISSUE's repo (%s) — that is the #1181 defect", c)
		}
	}
}

// TestAutoCloseSingle_Face2_RealIssueCollisionIsNotSilentSuccess is the
// dangerous face. Point the epic check at flutter#205 — a real, closed,
// sub-issue-less issue that merely occupies the number — and the answer must
// NOT be a cheerful "skipped/no_subs". Before the fix this returned
// ("skipped", "no_subs", nil) and the hook reported failed:false while the
// real epic was never evaluated.
func TestAutoCloseSingle_Face2_RealIssueCollisionIsNotSilentSuccess(t *testing.T) {
	f := acmeForge()
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{
		Owner:                "acme",
		Repo:                 "mobile", // the SUB-ISSUE's repo: the old default
		Number:               205,
		ExpectSubIssueNumber: 3001,
		ExpectSubIssueRepo:   "acme/mobile",
	}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle returned a hard error: %v", err)
	}
	if res.Reason == "no_subs" {
		t.Fatal("flutter#205 (a closed redesign issue) reported reason=no_subs — the silent face of #1181 is back")
	}
	if res.Status != "error" || res.Reason != "wrong_epic" {
		t.Fatalf("status/reason = %q/%q, want error/wrong_epic", res.Status, res.Reason)
	}
	if !strings.Contains(res.Error, "3001") {
		t.Errorf("error should name the sub-issue that is missing; got %q", res.Error)
	}
	if len(f.closedNodes()) != 0 {
		t.Errorf("nothing may be closed when the epic coordinate is wrong; closed %v", f.closedNodes())
	}
}

// TestAutoCloseSingle_Face1_PullRequestCollisionSurfacesLoudly documents the
// loud face and pins that it is a FAILURE, not a skip: platform#207 is a merged
// PR, and GraphQL will not resolve a PR through issue(number:).
func TestAutoCloseSingle_Face1_PullRequestCollisionSurfacesLoudly(t *testing.T) {
	f := acmeForge()
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{
		Owner:                "acme",
		Repo:                 "api", // the SUB-ISSUE's repo
		Number:               207,
		ExpectSubIssueNumber: 4001,
		ExpectSubIssueRepo:   "acme/api",
	}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle returned a hard error: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("status = %q, want error (a PR number is not an epic)", res.Status)
	}
	if !strings.Contains(res.Error, "Could not resolve to an Issue with the number of 207") {
		t.Errorf("error = %q, want the GraphQL resolve failure", res.Error)
	}

	// ...and with the epic's OWN repo the same number resolves and closes.
	res2, err := svc.AutoCloseSingle(context.Background(), EpicRef{
		Owner:                "acme",
		Repo:                 "platform",
		Number:               207,
		ExpectSubIssueNumber: 4001,
		ExpectSubIssueRepo:   "acme/api",
	}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle (epic repo): %v", err)
	}
	if res2.Status != "closed" {
		t.Fatalf("status = %q reason=%q error=%q, want closed", res2.Status, res2.Reason, res2.Error)
	}
}

// TestAutoCloseSingle_WrongEpicWithItsOwnSubIssues covers the collision that a
// number-only guard would miss: the wrong repo's #205 has sub-issues of its
// own. Total > 0, so "no_subs" never fires — but it is still not our epic.
func TestAutoCloseSingle_WrongEpicWithItsOwnSubIssues(t *testing.T) {
	f := acmeForge()
	f.issues["acme/mobile#205"] = fakeIssueNode{
		kind: "issue", title: "An unrelated flutter epic", state: "OPEN",
		subs: []fakeSubIssue{{number: 9001, repo: "acme/mobile", state: "CLOSED"}},
	}
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{
		Owner:                "acme",
		Repo:                 "mobile",
		Number:               205,
		ExpectSubIssueNumber: 3001,
		ExpectSubIssueRepo:   "acme/mobile",
	}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle: %v", err)
	}
	if res.Status != "error" || res.Reason != "wrong_epic" {
		t.Fatalf("status/reason = %q/%q, want error/wrong_epic — an unrelated epic that happens to be complete must not be closed",
			res.Status, res.Reason)
	}
	if len(f.closedNodes()) != 0 {
		t.Errorf("closed an unrelated epic: %v", f.closedNodes())
	}
}

// TestAutoCloseSingle_SameRepoEpicUnchanged pins that the same-repo path is
// untouched: the epic in the caller's own repo still closes exactly as before.
func TestAutoCloseSingle_SameRepoEpicUnchanged(t *testing.T) {
	f := &fakeForge{issues: map[string]fakeIssueNode{
		"acme/solo#10": {
			kind: "issue", title: "Epic", state: "OPEN",
			subs: []fakeSubIssue{{number: 11, repo: "acme/solo", state: "CLOSED"}},
		},
	}}
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{
		Owner:                "acme",
		Repo:                 "solo",
		Number:               10,
		ExpectSubIssueNumber: 11,
		ExpectSubIssueRepo:   "acme/solo",
	}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle: %v", err)
	}
	if res.Status != "closed" || res.Reason != "all_closed" {
		t.Fatalf("status/reason = %q/%q error=%q, want closed/all_closed", res.Status, res.Reason, res.Error)
	}
}

// TestAutoCloseSingle_NoSubsStaysReachableWithoutAnExpectedSub keeps "no_subs"
// meaningful on the sweep path, which has no triggering sub-issue: an epic that
// genuinely has no sub-issues is skipped, not failed.
func TestAutoCloseSingle_NoSubsStaysReachableWithoutAnExpectedSub(t *testing.T) {
	f := &fakeForge{issues: map[string]fakeIssueNode{
		"acme/solo#10": {kind: "issue", title: "Empty epic", state: "OPEN"},
	}}
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{Owner: "acme", Repo: "solo", Number: 10}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle: %v", err)
	}
	if res.Status != "skipped" || res.Reason != "no_subs" {
		t.Fatalf("status/reason = %q/%q, want skipped/no_subs", res.Status, res.Reason)
	}
}

// TestAutoCloseSingle_MissingEpicRepoIsRefused: a number with no repository is
// not a coordinate, and guessing one is what caused #1181.
func TestAutoCloseSingle_MissingEpicRepoIsRefused(t *testing.T) {
	f := acmeForge()
	svc := NewEpicService(newForgeClient(t, f))

	res, err := svc.AutoCloseSingle(context.Background(), EpicRef{Number: 205}, 0)
	if err != nil {
		t.Fatalf("AutoCloseSingle: %v", err)
	}
	if res.Status != "error" || res.Reason != "epic_repo_missing" {
		t.Fatalf("status/reason = %q/%q, want error/epic_repo_missing", res.Status, res.Reason)
	}
	if len(f.calls()) != 0 {
		t.Errorf("must not query the forge at all without a repo; queried %v", f.calls())
	}
}

// TestEpicSweepAndPostMergeHookAgreeOnSubIssues is the reconciliation the issue
// asks for. The nightly sweep runs in the EPIC's repo and reported
// "#205 (9/11)"; the post-merge hook, resolving in the SUB-ISSUE's repo,
// simultaneously reported "no sub-issues". Two paths, one question, opposite
// answers — and the wrong one decided whether the epic closed.
//
// Both paths now read the same membership record (EpicCompletionResult.SubIssues),
// so this asserts the answers are identical for every epic in the sweep.
func TestEpicSweepAndPostMergeHookAgreeOnSubIssues(t *testing.T) {
	f := acmeForge()
	// Leave one sub-issue open so the epics are not closed out from under the
	// sweep mid-test, and both paths have a non-trivial answer to agree on.
	n := f.issues["acme/platform#205"]
	n.subs[1].state = "OPEN"
	f.issues["acme/platform#205"] = n

	svc := NewEpicService(newForgeClient(t, f))

	sweep, err := svc.SweepEpics(context.Background(), "acme", "platform")
	if err != nil {
		t.Fatalf("SweepEpics: %v", err)
	}
	if len(sweep) == 0 {
		t.Fatal("sweep returned no epics")
	}

	for _, s := range sweep {
		// The post-merge path's view of the same epic: resolved through the
		// merged sub-issue's parent link, exactly as EvaluatePostMerge does.
		var subRepo string
		var subNum int
		for _, si := range s.SubIssues {
			subRepo, subNum = si.Repo, si.Number
			break
		}
		if subNum == 0 {
			t.Fatalf("epic #%d: sweep reports no sub-issues; fixture is wrong", s.EpicNumber)
		}
		subOwner, subName, _ := strings.Cut(subRepo, "/")
		sub, err := NewIssueService(svc.client).GetIssue(context.Background(), subOwner, subName, subNum)
		if err != nil {
			t.Fatalf("GetIssue(%s#%d): %v", subRepo, subNum, err)
		}
		epicOwner, epicName, _ := strings.Cut(sub.ParentIssueRepo, "/")
		hookView, err := svc.CheckCompletion(context.Background(), epicOwner, epicName, sub.ParentIssueNumber)
		if err != nil {
			t.Fatalf("hook-path CheckCompletion: %v", err)
		}

		if hookView.Total != s.Total {
			t.Errorf("epic #%d: sweep sees %d sub-issue(s), post-merge path sees %d — the two paths disagree (#1181)",
				s.EpicNumber, s.Total, hookView.Total)
		}
		if hookView.EpicNumber != s.EpicNumber || hookView.Repo != s.Repo {
			t.Errorf("epic #%d: post-merge path landed on %s#%d", s.EpicNumber, hookView.Repo, hookView.EpicNumber)
		}
		if !hookView.HasSubIssue(subRepo, subNum) {
			t.Errorf("epic #%d: post-merge path does not list its own sub-issue %s#%d", s.EpicNumber, subRepo, subNum)
		}
	}
}
