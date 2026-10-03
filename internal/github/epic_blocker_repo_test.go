package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
	"golang.org/x/time/rate"
)

// #2369: `epic validate` reported any blocker carrying the epic's number as a
// circular blocker. For a sub-issue in another repository that is an
// unrelated issue, and issue-audit's CIRCULAR_BLOCKER repair removes the edge.
func TestBlockerIsEpic(t *testing.T) {
	const epicRepo, epicNumber = "o/platform", 20
	for _, tc := range []struct {
		name    string
		subRepo string
		blocker types.BlockingRef
		want    bool
	}{
		{"same-repo sub, blocker repo unset", "o/platform", types.BlockingRef{Number: 20}, true},
		{"cross-repo sub blocked by the epic", "o/app", types.BlockingRef{Number: 20, Repo: "o/platform"}, true},
		{"repository names compare case-insensitively", "o/app", types.BlockingRef{Number: 20, Repo: "O/Platform"}, true},
		{"cross-repo sub, same-numbered local blocker", "o/app", types.BlockingRef{Number: 20, Repo: "o/app"}, false},
		{"cross-repo sub, blocker repo unset", "o/app", types.BlockingRef{Number: 20}, false},
		{"different number", "o/platform", types.BlockingRef{Number: 21, Repo: "o/platform"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockerIsEpic(tc.blocker, tc.subRepo, epicRepo, epicNumber); got != tc.want {
				t.Errorf("blockerIsEpic = %v, want %v", got, tc.want)
			}
		})
	}
}

// multiRepoForge is a fake GitHub GraphQL endpoint whose issues live in
// several repositories, keyed "owner/name#N", so two of them can share a
// number. It serves the two reads EpicService.Validate makes: the epic with
// its sub-issue list, and one batched read per repository of the sub-issues
// with their blockers.
type multiRepoForge struct {
	t      *testing.T
	issues map[string]*multiRepoIssue
}

type multiRepoIssue struct {
	repo                 string
	number               int
	state                string
	subIssues, blockedBy []string // "owner/name#N"
}

func (f *multiRepoForge) add(repo string, number int, state string) *multiRepoIssue {
	iss := &multiRepoIssue{repo: repo, number: number, state: state}
	f.issues[fmt.Sprintf("%s#%d", repo, number)] = iss
	return iss
}

func (f *multiRepoForge) client() *Client {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.t.Cleanup(srv.Close)
	c := NewClientWithURL("test-token", srv.URL)
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

func (f *multiRepoForge) node(key string) map[string]interface{} {
	iss := f.issues[key]
	if iss == nil {
		f.t.Fatalf("multiRepoForge: no issue %s", key)
	}
	return map[string]interface{}{
		"id": "I_" + key, "number": iss.number, "title": "Issue " + key, "state": iss.state,
		"repository": map[string]interface{}{"nameWithOwner": iss.repo},
	}
}

func (f *multiRepoForge) fields(q string, iss *multiRepoIssue) map[string]interface{} {
	out := map[string]interface{}{
		"id": fmt.Sprintf("I_%s#%d", iss.repo, iss.number), "number": iss.number,
		"title": fmt.Sprintf("Issue %s#%d", iss.repo, iss.number), "state": iss.state,
	}
	for conn, keys := range map[string][]string{"subIssues": iss.subIssues, "blockedBy": iss.blockedBy} {
		if !regexp.MustCompile(conn + `\(first: \d+\)`).MatchString(q) {
			continue
		}
		nodes := make([]interface{}, 0, len(keys))
		for _, k := range keys {
			nodes = append(nodes, f.node(k))
		}
		page := map[string]interface{}{"nodes": nodes}
		if regexp.MustCompile(conn + `\([^)]*\)\s*\{\s*pageInfo`).MatchString(q) {
			page["pageInfo"] = map[string]interface{}{"hasNextPage": false, "endCursor": ""}
		}
		out[conn] = page
	}
	return out
}

func (f *multiRepoForge) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	q, vars := req.Query, req.Variables
	repo := fmt.Sprintf("%v/%v", vars["owner"], vars["name"])
	var data map[string]interface{}
	switch {
	case strings.Contains(q, "fragment IssueFields"):
		out := map[string]interface{}{}
		for _, m := range reBatchItem.FindAllStringSubmatch(q, -1) {
			if iss := f.issues[repo+"#"+m[2]]; iss != nil {
				out["i"+m[1]] = f.fields(q, iss)
			} else {
				out["i"+m[1]] = nil
			}
		}
		data = map[string]interface{}{"repository": out}
	case strings.Contains(q, "issue(number: $number)"):
		iss := f.issues[fmt.Sprintf("%s#%v", repo, vars["number"])]
		if iss == nil {
			data = map[string]interface{}{"repository": map[string]interface{}{"issue": nil}}
			break
		}
		data = map[string]interface{}{"repository": map[string]interface{}{"issue": f.fields(q, iss)}}
	default:
		f.t.Errorf("multiRepoForge: unrecognised query %q", q)
		http.Error(w, "unrecognised query", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
}

// TestValidate_MatchesTheEpicAndItsSubIssuesByRepository drives `epic
// validate` itself, not only blockerIsEpic, over an epic in o/platform with
// sub-issues in o/platform and o/app (#2369 review):
//
//   - o/app#21 is blocked by o/app#20, an open issue that only shares the
//     epic's number: a legitimate blocker, so no circular_blocker.
//   - o/app#22 is blocked by the epic itself: circular, named in full.
//   - o/platform#21 shares o/app#21's number and is blocked by a closed
//     o/platform#5. Keyed by number alone, one #21 was judged by the other's
//     blockers; each must report its own.
//
// Every gap carries the repository of its sub-issue and of its blocker, which
// is what issue-audit needs to repair the right relationship.
func TestValidate_MatchesTheEpicAndItsSubIssuesByRepository(t *testing.T) {
	f := &multiRepoForge{t: t, issues: map[string]*multiRepoIssue{}}
	epic := f.add("o/platform", 20, "OPEN")
	epic.subIssues = []string{"o/app#21", "o/app#22", "o/platform#21"}
	f.add("o/app", 20, "OPEN")
	f.add("o/platform", 5, "CLOSED")
	f.add("o/app", 21, "OPEN").blockedBy = []string{"o/app#20"}
	f.add("o/app", 22, "OPEN").blockedBy = []string{"o/platform#20"}
	f.add("o/platform", 21, "OPEN").blockedBy = []string{"o/platform#5"}

	res, err := NewEpicService(f.client()).Validate(context.Background(), "o", "platform", 20)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := map[string]EpicValidationGap{}
	for _, g := range res.Gaps {
		got[fmt.Sprintf("%s %s#%d", g.GapType, g.SubIssueRepo, g.SubIssueNumber)] = g
	}
	want := map[string]EpicValidationGap{
		"circular_blocker o/app#22": {
			SubIssueNumber: 22, SubIssueRepo: "o/app", GapType: "circular_blocker",
			BlockerNumber: 20, BlockerRepo: "o/platform",
			Detail: "sub-issue o/app#22 is blocked by its own epic #20",
		},
		"stale_blocker o/platform#21": {
			SubIssueNumber: 21, SubIssueRepo: "o/platform", GapType: "stale_blocker",
			BlockerNumber: 5, BlockerRepo: "o/platform",
			Detail: "sub-issue #21 blocked by closed issue #5",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("gaps = %+v, want exactly %v", res.Gaps, want)
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("missing gap %s (gaps %+v)", k, res.Gaps)
			continue
		}
		g.SubIssueTitle = ""
		if g != w {
			t.Errorf("gap %s = %+v, want %+v", k, g, w)
		}
	}
	if res.Valid {
		t.Error("Valid = true over a circular and a stale blocker")
	}
}
