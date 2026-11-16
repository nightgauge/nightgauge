package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func closingMergeJSON(nodes ...string) string {
	return fmt.Sprintf(`{"data":{"repository":{"issue":{"closedByPullRequestsReferences":{"nodes":[%s]}}}}}`,
		strings.Join(nodes, ","))
}

func mergedNode(number int, sha, at string) string {
	return mergedNodeIn("o/r", number, sha, at)
}

// mergedNodeIn is a merged closing PR that lives in repository nwo.
func mergedNodeIn(nwo string, number int, sha, at string) string {
	return fmt.Sprintf(`{"number":%d,"repository":{"nameWithOwner":%q},"merged":true,"mergedAt":%q,"mergeCommit":{"oid":%q}}`,
		number, nwo, at, sha)
}

func readLatestClosingMerge(t *testing.T, resp string) (*ClosingMerge, error) {
	t.Helper()
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()
	return NewIssueService(client).LatestClosingMerge(context.Background(), "o", "r", 2611)
}

func TestLatestClosingMerge_NewestMergedWins(t *testing.T) {
	got, err := readLatestClosingMerge(t, closingMergeJSON(
		mergedNode(2628, "2346803d", "2026-10-27T23:52:45Z"),
		mergedNode(2619, "dadc5798", "2026-10-27T10:00:00Z"),
		`{"number":2700,"merged":false,"mergedAt":null,"mergeCommit":null}`,
		`{"number":2701,"merged":true,"mergedAt":"2026-10-28T01:00:00Z","mergeCommit":null}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Number != 2628 || got.SHA != "2346803d" {
		t.Fatalf("LatestClosingMerge = %+v, want #2628 at 2346803d", got)
	}
}

func TestLatestClosingMerge_NoneMerged(t *testing.T) {
	got, err := readLatestClosingMerge(t, closingMergeJSON(`{"number":1,"merged":false,"mergedAt":null,"mergeCommit":null}`))
	if err != nil || got != nil {
		t.Fatalf("LatestClosingMerge = %+v, %v; want nil, nil", got, err)
	}
}

func TestLatestClosingMerge_IssueNotFound(t *testing.T) {
	if _, err := readLatestClosingMerge(t, `{"data":{"repository":{"issue":null}}}`); err == nil {
		t.Fatal("want an error for a missing issue")
	}
}

// #3352: a PR in another repository that closes the issue is never its
// closing merge, even when it is newer and shares the PR number.
func TestLatestClosingMerge_IgnoresCrossRepoMerge(t *testing.T) {
	got, err := readLatestClosingMerge(t, closingMergeJSON(
		mergedNodeIn("other/r", 2628, "f0re1gn0", "2026-10-28T09:00:00Z"),
		mergedNode(2628, "2346803d", "2026-10-27T23:52:45Z"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SHA != "2346803d" {
		t.Fatalf("LatestClosingMerge = %+v, want o/r #2628 at 2346803d", got)
	}

	got, err = readLatestClosingMerge(t, closingMergeJSON(
		mergedNodeIn("other/r", 9, "f0re1gn0", "2026-10-28T09:00:00Z"),
		mergedNodeIn("", 10, "n0repo00", "2026-10-28T10:00:00Z"),
	))
	if err != nil || got != nil {
		t.Fatalf("only foreign or unattributable merges: got %+v, %v; want nil, nil", got, err)
	}

	got, err = readLatestClosingMerge(t, closingMergeJSON(mergedNodeIn("O/R", 11, "upper000", "2026-10-28T10:00:00Z")))
	if err != nil || got == nil || got.SHA != "upper000" {
		t.Fatalf("case-insensitive repository: got %+v, %v; want #11", got, err)
	}
}
