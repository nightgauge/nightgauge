package github

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/shurcooL/graphql"
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

// TestCommitParentsVars_DeclaresGitObjectID pins the one thing about this
// query that the Go type system cannot: shurcooL/graphql derives each
// variable's declared GraphQL type from its Go type NAME, and GitHub's
// `object(oid:)` takes `GitObjectID!`, not `String!`. Typing oid as
// graphql.String compiles, reads fine, and fails at runtime against the live
// API with a type error — the kind of break no fake-backed test can see.
func TestCommitParentsVars_DeclaresGitObjectID(t *testing.T) {
	vars := commitParentsVars("o", "r", "deadbeef")

	if got := reflect.TypeOf(vars["oid"]).Name(); got != "GitObjectID" {
		t.Errorf("oid declares GraphQL type %q!, want GitObjectID! — GitHub rejects the query otherwise", got)
	}
	for _, k := range []string{"owner", "name"} {
		if got := reflect.TypeOf(vars[k]).Name(); got != "String" {
			t.Errorf("%s declares %q!, want String!", k, got)
		}
	}
	if _, isString := vars["oid"].(graphql.String); isString {
		t.Error("oid is a graphql.String; it must be a GitObjectID")
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
		{"", "", "", false},
		{"not-a-remote", "", "", false},
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
