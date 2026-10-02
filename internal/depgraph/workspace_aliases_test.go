package depgraph

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// issue2349Workspace is the workspace #2349 describes: sibling repositories
// whose names the documentation's example alias map never heard of, one of
// them called "platform" so a short name the example map DID know is covered.
var issue2349Workspace = []string{"example-org/app", "example-org/widget-api", "example-org/platform"}

// issue2349Rows is #2349's table, row for row: a body line in an issue of
// example-org/app and the one dependency it declares. Before the fix the rows
// produced, in order: no edge; example-org/app#12 (the declaring repo's own,
// unrelated #12); acme/platform#12 (an off-board example repo); the right edge.
var issue2349Rows = []struct {
	body string
	want string
}{
	{"Blocked by widget-api#12", "example-org/widget-api#12"},
	{"Blocked by widget-api #12", "example-org/widget-api#12"},
	{"Blocked by platform #12", "example-org/platform#12"},
	{"Blocked by example-org/widget-api#12", "example-org/widget-api#12"},
}

func refKeys(refs []CrossRepoRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Repo+"#"+strconv.Itoa(r.Number))
	}
	sort.Strings(out)
	return out
}

func TestWorkspaceRepoAliases_MapsEachRepoByFullAndShortName(t *testing.T) {
	got := WorkspaceRepoAliases([]string{
		"example-org/app", "Example-Org/Widget-API", " example-org/platform ",
		"", "not-a-slug", "a/b/c", "/x", "y/",
	})
	want := map[string]string{
		"example-org/app":        "example-org/app",
		"app":                    "example-org/app",
		"Example-Org/Widget-API": "Example-Org/Widget-API",
		"widget-api":             "Example-Org/Widget-API",
		"example-org/platform":   "example-org/platform",
		"platform":               "example-org/platform",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorkspaceRepoAliases =\n  %v\nwant\n  %v", got, want)
	}
	if r := resolveAlias("WIDGET-API", got); r != "Example-Org/Widget-API" {
		t.Errorf("short names must resolve case-insensitively, got %q", r)
	}
}

// A short name two repositories share names neither: guessing one would gate
// on an issue in the wrong repository. It stays in the map, mapped to "", so a
// reference through it is recognised as naming a repository (and held as
// Unresolved) rather than read as prose. The full spellings still resolve, and
// a slug listed twice (the scheduler's set and the discovered workspace
// overlap) is not ambiguous with itself.
func TestWorkspaceRepoAliases_AmbiguousShortNameNamesNeither(t *testing.T) {
	aliases := WorkspaceRepoAliases([]string{"org-a/app", "org-b/app", "org-c/app", "org-a/app"})
	if v, ok := aliases["app"]; !ok || v != "" {
		t.Errorf("ambiguous short name = %q (present %v); want it present and mapped to \"\"", v, ok)
	}
	if r := resolveAlias("APP", aliases); r != "" {
		t.Errorf("an ambiguous short name resolved to %q", r)
	}
	for _, slug := range []string{"org-a/app", "org-b/app", "org-c/app"} {
		if aliases[slug] != slug {
			t.Errorf("full spelling %s = %q, want itself", slug, aliases[slug])
		}
	}
	if got := WorkspaceRepoAliases([]string{"org-a/app", "Org-A/App"})["app"]; !strings.EqualFold(got, "org-a/app") {
		t.Errorf("one repository listed twice must keep its short name, got %q", got)
	}
}

// TestParseDependencyRefs_SiblingShortNamesResolveThroughTheWorkspace is the
// parser half of #2349: with the workspace's alias map, the glued and the
// spaced short forms both reach the sibling, and nothing reaches the
// declaring repo's own #12 or an acme/* example repository.
func TestParseDependencyRefs_SiblingShortNamesResolveThroughTheWorkspace(t *testing.T) {
	aliases := WorkspaceRepoAliases(issue2349Workspace)
	for _, row := range issue2349Rows {
		got := refKeys(ParseDependencyRefs(row.body, "example-org/app", aliases))
		if want := []string{row.want}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q in example-org/app = %v, want %v", row.body, got, want)
		}
	}
}

// markedKeys is refKeys with each Unresolved reference prefixed "?".
func markedKeys(refs []CrossRepoRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		k := r.Repo + "#" + strconv.Itoa(r.Number)
		if r.Unresolved {
			k = "?" + k
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// With no alias map no short name crosses a repository boundary, and in
// particular none reaches the example repositories the old default map named:
// those entries exist only in this package's tests now (testRepoAliases). Nor
// does one fall back to the declaring repository's own same-numbered issue:
// in the repo position, or glued to the `#`, an unknown name is Unresolved
// and holds the issue. Only a spaced word later in the sentence is prose.
func TestParseDependencyRefs_NilAliasesNeverReachTheExampleRepos(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"Blocked by platform #12", []string{"?platform#12"}},
		{"Blocked by platform#12", []string{"?platform#12"}},
		{"Depends on: flutter #127, angular #152", []string{"?flutter#127", "example-org/app#152"}},
		{"Blocked by core#7", []string{"?core#7"}},
		{"## Cross-Repo Dependencies\n\n- ❌ acme-mobile #127\n", []string{"?acme-mobile#127"}},
		{"Blocked by example-org/widget-api#12", []string{"example-org/widget-api#12"}},
	}
	for _, tc := range cases {
		got := markedKeys(ParseDependencyRefs(tc.body, "example-org/app", nil))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// issue2349ReviewWorkspace adds a repository whose name has a dot in it, the
// shape of a site repository such as nightgauge.dev, to #2349's workspace.
var issue2349ReviewWorkspace = append(append([]string{}, issue2349Workspace...), "example-org/site.dev")

// issue2349ReviewRows are the #2349 review's additions to the issue's table,
// each a body of example-org/app declaring one dependency:
//
//   - A dotted repository in every spelling. The repo token had no dot, so the
//     spaced short form gated on example-org/app#12 and the glued and full
//     spellings declared nothing; only the issue URL worked.
//   - Repo-qualified entries under "## Dependencies", "## Blocked by" and
//     "## Depends on". Only "## Cross-Repo Dependencies" read an entry's
//     repository, and #2349 masked the short name out of the same-repo pass,
//     so "- widget-api #12" there declared nothing at all.
var issue2349ReviewRows = []struct {
	body string
	want string
}{
	{"Blocked by site.dev #12", "example-org/site.dev#12"},
	{"Blocked by site.dev#12", "example-org/site.dev#12"},
	{"Blocked by example-org/site.dev#12.", "example-org/site.dev#12"},
	{"Depends on: example-org/site.dev #12", "example-org/site.dev#12"},
	{"Blocked by https://github.com/example-org/site.dev/issues/12", "example-org/site.dev#12"},
	{"## Cross-Repo Dependencies\n\n- ❌ site.dev #12 — the landing page\n", "example-org/site.dev#12"},
	{"## Dependencies\n\n- widget-api #12\n", "example-org/widget-api#12"},
	{"## Dependencies\n\n- example-org/widget-api#12\n", "example-org/widget-api#12"},
	{"## Blocked by\n\n- ❌ widget-api #12 — not started\n", "example-org/widget-api#12"},
	{"## Depends on\n\n- example-org/widget-api#12 — the API\n", "example-org/widget-api#12"},
	{"## Dependencies\n\nwidget-api #12 must land first\n", "example-org/widget-api#12"},
	{"## Dependencies\n\n* [ ] widget-api #12\n", "example-org/widget-api#12"},
}

func TestParseDependencyRefs_Issue2349ReviewRows(t *testing.T) {
	aliases := WorkspaceRepoAliases(issue2349ReviewWorkspace)
	for _, row := range issue2349ReviewRows {
		got := markedKeys(ParseDependencyRefs(row.body, "example-org/app", aliases))
		if want := []string{row.want}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q in example-org/app = %v, want %v", row.body, got, want)
		}
	}
}

// A ✅ entry is Verified under any dependency header, as it always was under
// "## Cross-Repo Dependencies", and it still gates.
func TestParseDependencyRefs_VerifiedEntryUnderAnyDependencyHeader(t *testing.T) {
	aliases := WorkspaceRepoAliases(issue2349Workspace)
	refs := ParseDependencyRefs("## Dependencies\n\n- ✅ widget-api #12 — verified\n", "example-org/app", aliases)
	if len(refs) != 1 || refs[0].Repo != "example-org/widget-api" || !refs[0].Verified ||
		refs[0].Source != "structured_section" {
		t.Fatalf("refs = %+v, want one Verified structured_section ref to example-org/widget-api#12", refs)
	}
}

// TestParseDependencyRefs_UnnamedRepositoriesFailClosed: a dependency on a
// repository the workspace cannot name holds the issue. Before the #2349
// review an unknown or ambiguous name, written with a space, gated on the
// declaring repository's own same-numbered issue (dispatching over the real
// blocker whenever that issue was closed), and written glued it was dropped.
// Prose in front of a `#N` (issue, PR, Epic, a count) still names the
// declaring repository's own issue.
func TestParseDependencyRefs_UnnamedRepositoriesFailClosed(t *testing.T) {
	aliases := WorkspaceRepoAliases([]string{"example-org/app", "other-org/app", "example-org/widget-api"})
	const self = "example-org/widget-api"
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"unknown name, spaced", "Blocked by core #12", []string{"?core#12"}},
		{"unknown name, glued", "Blocked by core#12", []string{"?core#12"}},
		{"retired example alias", "Depends on: platform #491", []string{"?platform#491"}},
		{"unknown name in a section entry", "## Cross-Repo Dependencies\n\n- ❌ flutter #127 — not started\n", []string{"?flutter#127"}},
		{"ambiguous name, spaced", "Blocked by app #12", []string{"?app#12"}},
		{"ambiguous name, glued", "Blocked by app#12", []string{"?app#12"}},
		{"ambiguous name later in the sentence", "Blocked by #5 and app #12", []string{"?app#12", self + "#5"}},
		{"unknown name glued later in the sentence", "Blocked by #5 and core#12", []string{"?core#12", self + "#5"}},
		{"full spelling outside the workspace", "Blocked by acme/platform#12", []string{"acme/platform#12"}},
		{"full spelling of an ambiguous name", "Blocked by other-org/app#12", []string{"other-org/app#12"}},
		{"prose qualifier", "Blocked by issue #12", []string{self + "#12"}},
		{"prose qualifier, glued", "Depends on PR#12", []string{self + "#12"}},
		{"capitalised prose", "Blocked by Epic #295. Reaches its full value with Epic #301", []string{self + "#295"}},
		{"a count is not a repository", "Depends on both #5 and 2 #6", []string{self + "#5", self + "#6"}},
		{"a spaced word later in the sentence is prose", "Blocked by #5 until release #6", []string{self + "#5", self + "#6"}},
		{"section prose", "## Dependencies\n\n- Needs #12\n", []string{self + "#12"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := ParseDependencyRefs(tc.body, self, aliases)
			if got := markedKeys(refs); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%q = %v, want %v", tc.body, got, tc.want)
			}
			for _, r := range refs {
				if r.Unresolved && r.SourceLine == "" {
					t.Errorf("unresolved %s#%d names no body line; the hold must say which line to fix", r.Repo, r.Number)
				}
			}
		})
	}
}

// A keyword never reaches across a line break. Bodies are hard-wrapped, and a
// lead that crossed the break read the keyword ending one line onto the
// reference opening the next, so prose that denied a dependency declared it,
// and through the epic cascade held every sub-issue of the epic whose body it
// was (#2349 review).
func TestParseDependencyRefs_AKeywordDoesNotReachAcrossALineBreak(t *testing.T) {
	aliases := WorkspaceRepoAliases(issue2349Workspace)
	for _, body := range []string{
		"This change does **not** add a service, or depend on\n`example-org/widget-api#11` (that is separate).",
		"It cascades: `example-org/platform#31` is in turn blocked by\n`example-org/widget-api#32`.",
		"Depends on:\nwidget-api #11",
		"blocked\nby widget-api#11",
	} {
		if refs := ParseDependencyRefs(body, "example-org/app", aliases); len(refs) != 0 {
			t.Errorf("%q = %v, want none", body, markedKeys(refs))
		}
	}
	got := markedKeys(ParseDependencyRefs("or depend on `example-org/widget-api#11` (that is separate).", "example-org/app", aliases))
	if want := []string{"example-org/widget-api#11"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the same declaration on one line = %v, want %v", got, want)
	}
}

// A repo-qualified parent link in a dependency section is membership, not a
// dependency, exactly as the bare `Part of #308` is (#1497): once a section
// line's qualified references gate, an unmasked one deadlocks the issue on its
// own epic.
func TestParseDependencyRefs_QualifiedParentLinkInASectionIsNotADep(t *testing.T) {
	aliases := WorkspaceRepoAliases(issue2349Workspace)
	for _, line := range []string{
		"Part of example-org/platform#308",
		"Part of example-org/platform#308 (Wave 4)",
		"Part of platform #308",
		"Epic: widget-api#308",
		"- Closes example-org/widget-api#308",
	} {
		body := "## Dependencies\n\nBlocked by #300.\n\n" + line + "\n"
		got := markedKeys(ParseDependencyRefs(body, "example-org/app", aliases))
		if want := []string{"example-org/app#300"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v, want %v", line, got, want)
		}
	}
}

// TestBuildGraph_SiblingShortNamesResolveThroughTheWorkspace is the
// dispatcher's half of #2349: the graph the autonomous scheduler gates on,
// built with the workspace's alias map, holds each row's issue on the sibling's
// #12 and never on the declaring repository's own open #12.
func TestBuildGraph_SiblingShortNamesResolveThroughTheWorkspace(t *testing.T) {
	repos := []RepoConfig{
		{Owner: "example-org", Name: "app", Project: 1},
		{Owner: "example-org", Name: "widget-api", Project: 2},
		{Owner: "example-org", Name: "platform", Project: 3},
	}
	appItems := []types.BoardItem{
		// The trap: app's own #12, open and on the board.
		{Number: 12, Title: "unrelated", State: "OPEN", Repo: "example-org/app", Size: "S"},
	}
	bodies := map[string]string{}
	for i, row := range issue2349Rows {
		n := i + 1
		appItems = append(appItems, types.BoardItem{
			Number: n, Title: "row " + strconv.Itoa(n), State: "OPEN", Repo: "example-org/app", Size: "S",
		})
		bodies["example-org/app#"+strconv.Itoa(n)] = row.body
	}
	fetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		switch repo.FullName() {
		case "example-org/app":
			return appItems, len(appItems), nil
		case "example-org/widget-api":
			return []types.BoardItem{{Number: 12, Title: "w12", State: "OPEN", Repo: "example-org/widget-api", Size: "S"}}, 1, nil
		case "example-org/platform":
			return []types.BoardItem{{Number: 12, Title: "p12", State: "OPEN", Repo: "example-org/platform", Size: "S"}}, 1, nil
		}
		return nil, 0, nil
	}
	bodyFetcher := func(_ context.Context, owner, name string, number int) (string, error) {
		return bodies[owner+"/"+name+"#"+strconv.Itoa(number)], nil
	}

	g, err := buildGraphFromFetcher(context.Background(), fetcher, bodyFetcher, repos,
		WorkspaceRepoAliases(issue2349Workspace))
	if err != nil {
		t.Fatal(err)
	}

	edgesFrom := map[string][]Edge{}
	for _, e := range g.Edges {
		edgesFrom[e.From.String()] = append(edgesFrom[e.From.String()], e)
	}
	for i, row := range issue2349Rows {
		from := "example-org/app#" + strconv.Itoa(i+1)
		edges := edgesFrom[from]
		if len(edges) != 1 {
			t.Errorf("%s (%q): edges = %+v, want exactly one, to %s", from, row.body, edges, row.want)
			continue
		}
		e := edges[0]
		if e.To.String() != row.want || !e.Resolvable || e.Type != "crossRepo" {
			t.Errorf("%s (%q): edge = %+v, want a resolvable crossRepo edge to %s", from, row.body, e, row.want)
		}
	}
}

// TestBuildGraph_Issue2349ReviewRows is the dispatcher's half of the review
// rows: each holds the issue on the right repository's #12, never on the
// declaring repository's own open #12. A dependency on a repository the
// workspace cannot name becomes an unresolvable edge to the name as written,
// which the dispatcher fails closed on, instead of an edge to that #12.
func TestBuildGraph_Issue2349ReviewRows(t *testing.T) {
	repos := []RepoConfig{
		{Owner: "example-org", Name: "app", Project: 1},
		{Owner: "example-org", Name: "widget-api", Project: 2},
		{Owner: "example-org", Name: "site.dev", Project: 3},
	}
	type row struct{ body, want string }
	rows := make([]row, 0, len(issue2349ReviewRows)+1)
	for _, r := range issue2349ReviewRows {
		rows = append(rows, row{r.body, r.want})
	}
	rows = append(rows, row{"Blocked by core #12", "core#12"})

	appItems := []types.BoardItem{
		{Number: 12, Title: "unrelated", State: "OPEN", Repo: "example-org/app", Size: "S"},
	}
	bodies := map[string]string{}
	for i, r := range rows {
		n := 100 + i
		appItems = append(appItems, types.BoardItem{
			Number: n, Title: "row", State: "OPEN", Repo: "example-org/app", Size: "S",
		})
		bodies["example-org/app#"+strconv.Itoa(n)] = r.body
	}
	fetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		switch repo.FullName() {
		case "example-org/app":
			return appItems, len(appItems), nil
		case "example-org/widget-api", "example-org/site.dev":
			return []types.BoardItem{{Number: 12, Title: "12", State: "OPEN", Repo: repo.FullName(), Size: "S"}}, 1, nil
		}
		return nil, 0, nil
	}
	bodyFetcher := func(_ context.Context, owner, name string, number int) (string, error) {
		return bodies[owner+"/"+name+"#"+strconv.Itoa(number)], nil
	}

	g, err := buildGraphFromFetcher(context.Background(), fetcher, bodyFetcher, repos,
		WorkspaceRepoAliases(issue2349ReviewWorkspace))
	if err != nil {
		t.Fatal(err)
	}
	edgesFrom := map[string][]Edge{}
	for _, e := range g.Edges {
		edgesFrom[e.From.String()] = append(edgesFrom[e.From.String()], e)
	}
	for i, r := range rows {
		from := "example-org/app#" + strconv.Itoa(100+i)
		edges := edgesFrom[from]
		if len(edges) != 1 {
			t.Errorf("%s (%q): edges = %+v, want exactly one, to %s", from, r.body, edges, r.want)
			continue
		}
		e := edges[0]
		wantResolvable := strings.HasPrefix(r.want, "example-org/")
		if e.To.String() != r.want || e.Resolvable != wantResolvable || e.Type != "crossRepo" {
			t.Errorf("%s (%q): edge = %+v, want a crossRepo edge to %s (resolvable %v)",
				from, r.body, e, r.want, wantResolvable)
		}
	}
}
