package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #916. WorktreeSweepOptions.MergedPRLookup existed from #593 with a doc
// comment, two unit tests and no production caller — the door was built and
// never opened. These tests cover the production implementation; the test that
// the WIRING supplies it lives with each call site, which is where that class
// of bug actually lives.

type fakeHeads struct {
	heads    []MergedPRHead
	headsErr error
	parents  map[string][]string
	parErr   error

	headCalls   int
	parentCalls int
}

func (f *fakeHeads) ListMergedPRHeads(_ context.Context, _, _ string, _ int) ([]MergedPRHead, error) {
	f.headCalls++
	return f.heads, f.headsErr
}

func (f *fakeHeads) CommitParents(_ context.Context, _, _, oid string) ([]string, error) {
	f.parentCalls++
	if f.parErr != nil {
		return nil, f.parErr
	}
	return f.parents[oid], nil
}

func TestNewMergedPRLookup_ResolvesABranchToItsMergedPRHead(t *testing.T) {
	f := &fakeHeads{
		heads:   []MergedPRHead{{Number: 913, HeadRefName: "docs/graduate", HeadRefOid: "84bbb0e4"}},
		parents: map[string][]string{"84bbb0e4": {"6af649af"}},
	}
	lookup := NewMergedPRLookup(context.Background(), f, "nightgauge", "nightgauge")

	head, parents, ok := lookup("docs/graduate")
	if !ok {
		t.Fatal("a branch with a merged PR was not found")
	}
	if head != "84bbb0e4" {
		t.Errorf("head = %q, want 84bbb0e4", head)
	}
	if len(parents) != 1 || parents[0] != "6af649af" {
		t.Errorf("parents = %v, want [6af649af]", parents)
	}
}

func TestNewMergedPRLookup_FetchesTheIndexAtMostOnce(t *testing.T) {
	// The door is consulted per branch. An index fetch per branch would turn a
	// one-query door into an N-query one on exactly the repos that need it
	// most — and #842 is an open epic about this API budget.
	f := &fakeHeads{heads: []MergedPRHead{{HeadRefName: "a", HeadRefOid: "1"}}}
	lookup := NewMergedPRLookup(context.Background(), f, "o", "r")

	for i := 0; i < 5; i++ {
		lookup("a")
		lookup("nonexistent")
	}
	if f.headCalls != 1 {
		t.Errorf("index fetched %d times, want exactly 1", f.headCalls)
	}
}

func TestNewMergedPRLookup_IssuesNothingUntilFirstCall(t *testing.T) {
	// Laziness is the whole reason the daemon's periodic sweep can carry this
	// door. Constructing it must cost nothing; a repo with no branch that
	// fails the content test never calls it and never pays.
	f := &fakeHeads{heads: []MergedPRHead{{HeadRefName: "a", HeadRefOid: "1"}}}
	_ = NewMergedPRLookup(context.Background(), f, "o", "r")

	if f.headCalls != 0 {
		t.Fatalf("constructing the door issued %d request(s); it must issue none", f.headCalls)
	}
}

func TestNewMergedPRLookup_IndexFailureReportsNotFoundAndDoesNotRetry(t *testing.T) {
	// Fail-open toward KEEPING: a door that cannot answer leaves the content
	// test's verdict standing, so the branch is kept. And one failure is a
	// failure for the sweep — retrying per branch is how a rate-limited API
	// gets hammered.
	f := &fakeHeads{headsErr: errors.New("401 Bad credentials")}
	lookup := NewMergedPRLookup(context.Background(), f, "o", "r")

	for i := 0; i < 3; i++ {
		if _, _, ok := lookup("anything"); ok {
			t.Fatal("a failed index reported a branch as merged")
		}
	}
	if f.headCalls != 1 {
		t.Errorf("index retried %d times after failure, want 1", f.headCalls)
	}
}

func TestNewMergedPRLookup_ParentFailureStillReportsTheHead(t *testing.T) {
	// The equality case needs no parents, so a parents failure must not throw
	// away a real hit — that would close the door on the common shape because
	// of a lookup only the rare shape needs.
	f := &fakeHeads{
		heads:  []MergedPRHead{{HeadRefName: "a", HeadRefOid: "deadbeef"}},
		parErr: errors.New("network"),
	}
	lookup := NewMergedPRLookup(context.Background(), f, "o", "r")

	head, parents, ok := lookup("a")
	if !ok || head != "deadbeef" {
		t.Fatalf("head lost to a parents failure: ok=%v head=%q", ok, head)
	}
	if parents != nil {
		t.Errorf("parents = %v, want nil", parents)
	}
}

func TestNewMergedPRLookup_NewestMergeWinsForAReusedBranchName(t *testing.T) {
	// Branch names get reused. The index is newest-first, and only the most
	// recent merge's head can still be the local tip.
	f := &fakeHeads{heads: []MergedPRHead{
		{Number: 900, HeadRefName: "fix/thing", HeadRefOid: "newest"},
		{Number: 100, HeadRefName: "fix/thing", HeadRefOid: "oldest"},
	}}
	lookup := NewMergedPRLookup(context.Background(), f, "o", "r")

	head, _, _ := lookup("fix/thing")
	if head != "newest" {
		t.Errorf("head = %q, want the most recent merge (newest)", head)
	}
}

func TestNewMergedPRLookup_NilServiceOrMissingRepoClosesTheDoor(t *testing.T) {
	if NewMergedPRLookup(context.Background(), nil, "o", "r") != nil {
		t.Error("a nil service must produce the closed door (nil), not a panicking one")
	}
	if NewMergedPRLookup(context.Background(), &fakeHeads{}, "", "r") != nil {
		t.Error("an empty owner must produce the closed door")
	}
	if NewMergedPRLookup(context.Background(), &fakeHeads{}, "o", "") != nil {
		t.Error("an empty repo must produce the closed door")
	}
}

// newPRServiceForRESTTest wires a PRService onto an httptest server through
// the same mock transport the other REST-backed services use.
func newPRServiceForRESTTest(server *httptest.Server) *PRService {
	return &PRService{client: &Client{http: &http.Client{Transport: &mockRESTTransport{server: server}}}}
}

// TestCommitParents_ReadsParentSHAs pins the happy path and the request shape.
func TestCommitParents_ReadsParentSHAs(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sha": "child",
			"parents": []map[string]string{
				{"sha": "first-parent"},
				{"sha": "second-parent"},
			},
		})
	}))
	defer srv.Close()

	got, err := newPRServiceForRESTTest(srv).CommitParents(context.Background(), "o", "r", "child")
	if err != nil {
		t.Fatalf("CommitParents: %v", err)
	}
	want := []string{"first-parent", "second-parent"}
	if len(got) != len(want) {
		t.Fatalf("parents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parents[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if gotPath != "/repos/o/r/commits/child" {
		t.Errorf("path = %q, want /repos/o/r/commits/child", gotPath)
	}
}

// TestCommitParents_UnknownSHAIsNotAnError pins the trap that the GraphQL →
// REST migration (#849) introduced.
//
// GraphQL expressed "no such object" as a null `object` field, which mapped to
// (nil, nil) — an index miss the door reads as "no containment". REST splits
// that same answer across TWO status codes, and the one it actually returns
// for a commit that is not in the repository is **422**, not 404 (verified
// live: `{"message":"No commit found for SHA: …","status":"422"}`). A migration
// that mapped only 404 would turn every index miss into a sweep failure.
//
// The 500 case is the other half and must stay: an unrecognised status may NOT
// read as "no parents", or a permission or outage answer would be reported as
// a clean not-contained verdict.
func TestCommitParents_UnknownSHAIsNotAnError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"422 no commit found for sha", http.StatusUnprocessableEntity, `{"message":"No commit found for SHA: deadbeef"}`, false},
		{"404 repository or ref unreachable", http.StatusNotFound, `{"message":"Not Found"}`, false},
		{"403 is not an absence", http.StatusForbidden, `{"message":"Forbidden"}`, true},
		{"500 is not an absence", http.StatusInternalServerError, `{"message":"boom"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := newPRServiceForRESTTest(srv).CommitParents(context.Background(), "o", "r", "deadbeef")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("status %d returned no error; an unrecognised answer must not read as \"no parents\"", tc.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("status %d: unexpected error: %v", tc.status, err)
			}
			if len(got) != 0 {
				t.Errorf("status %d: parents = %v, want none", tc.status, got)
			}
		})
	}
}

func TestParseOriginSlug(t *testing.T) {
	cases := []struct {
		remote, owner, name string
		ok                  bool
	}{
		{"git@github.com:nightgauge/nightgauge.git", "nightgauge", "nightgauge", true},
		{"git@github.com:nightgauge/nightgauge", "nightgauge", "nightgauge", true},
		{"https://github.com/nightgauge/nightgauge.git", "nightgauge", "nightgauge", true},
		{"https://user@github.com/nightgauge/nightgauge", "nightgauge", "nightgauge", true},
		{"ssh://git@github.com/nightgauge/nightgauge.git", "nightgauge", "nightgauge", true},
		{"git://github.com/nightgauge/nightgauge.git", "nightgauge", "nightgauge", true},
		{"https://gitlab.example.com/group/proj.git", "group", "proj", true},
		{"", "", "", false},
		{"not-a-remote", "", "", false},

		// A LOCAL CLONE IS NOT A FORGE. Every git fixture in this repo clones
		// from a bare repo under t.TempDir(), so before #920 these parsed as
		// owner="…" name="origin" and the merged-PR door was built for a
		// repository that does not exist — an API call per sweep to be told
		// so, a WARN per sweep, and the network dragged into unit tests.
		{"/private/var/folders/xy/T/TestFoo/001/origin.git", "", "", false},
		{"/tmp/base/origin.git", "", "", false},
		{"file:///tmp/base/origin.git", "", "", false},
		{"./relative/origin.git", "", "", false},
		{"../sibling/origin.git", "", "", false},

		// Malformed shapes that must not produce a plausible slug either.
		{"https://github.com", "", "", false},
		{"git@github.com:nightgauge", "", "", false},
		{"https:///owner/name", "", "", false},
	}
	for _, c := range cases {
		owner, name, ok := parseOriginSlug(c.remote)
		if ok != c.ok || owner != c.owner || name != c.name {
			t.Errorf("parseOriginSlug(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.remote, owner, name, ok, c.owner, c.name, c.ok)
		}
	}
}

// TestMergedPRIndexSize_FitsOneGitHubPage pins the bound that cost this
// feature its first run.
//
// `mergedPRIndexSize` was 250, chosen by eye against the shell script's
// `--limit 500`. GitHub caps a connection page at 100 and rejects the query
// above it — and the door swallows that error by design, so the symptom was
// not an error anywhere. It was `lookup()` answering not-found for every
// branch, i.e. the entire feature quietly doing nothing, which is
// indistinguishable from a repo that genuinely has no merged PRs.
//
// No fake-backed test can catch this: the fake has no page limit. So the
// number gets a test of its own.
func TestMergedPRIndexSize_FitsOneGitHubPage(t *testing.T) {
	if mergedPRIndexSize > maxGraphQLPageSize {
		t.Fatalf("mergedPRIndexSize = %d exceeds GitHub's per-connection maximum of %d; "+
			"the query is rejected and every lookup silently answers not-found",
			mergedPRIndexSize, maxGraphQLPageSize)
	}
	if mergedPRIndexSize <= 0 {
		t.Fatalf("mergedPRIndexSize = %d leaves the index empty", mergedPRIndexSize)
	}
}

func TestListMergedPRHeads_ClampsAnOversizedLimit(t *testing.T) {
	// A caller passing its own limit must not be able to reintroduce the bug.
	// The clamp is silent because the alternative — erroring — would take out
	// a supplementary check over a caller's arithmetic.
	if got := clampPageSize(250); got != maxGraphQLPageSize {
		t.Errorf("clampPageSize(250) = %d, want %d", got, maxGraphQLPageSize)
	}
	if got := clampPageSize(0); got != mergedPRIndexSize {
		t.Errorf("clampPageSize(0) = %d, want the default %d", got, mergedPRIndexSize)
	}
	if got := clampPageSize(25); got != 25 {
		t.Errorf("clampPageSize(25) = %d, want 25 unchanged", got)
	}
}

func TestNewMergedPRLookupForRoot_DoesNotBuildAClientForANonGitHubRemote(t *testing.T) {
	// Laziness in the OTHER direction from the index query (#920). Building the
	// client can shell out to `gh auth token`, which costs seconds; a repo
	// whose origin is a local clone or another forge never needs it.
	//
	// Before this, every cmd-level sweep test over a temp-dir fixture paid that
	// cost — 6.3s down to 2.0s on one test — and, worse, a local-path remote
	// parsed as a plausible GitHub slug, so the door was built and would have
	// queried a repository that does not exist.
	calls := 0
	factory := func() (*Client, error) {
		calls++
		return nil, nil
	}

	// t.TempDir() is not a git repo at all, so `git remote get-url` fails and
	// the slug never resolves — the same closed path a local clone takes.
	if got := NewMergedPRLookupForRoot(context.Background(), factory, t.TempDir()); got != nil {
		t.Error("a root with no GitHub origin produced an open door")
	}
	if calls != 0 {
		t.Errorf("client factory called %d time(s) for a root with no GitHub origin; want 0", calls)
	}

	if got := NewMergedPRLookupForRoot(context.Background(), nil, "/anywhere"); got != nil {
		t.Error("a nil factory must produce the closed door, not a panicking one")
	}
}
