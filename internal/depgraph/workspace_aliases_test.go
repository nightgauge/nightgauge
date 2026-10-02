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
// on an issue in the wrong repository. The full spellings still resolve, and a
// slug listed twice (the scheduler's set and the discovered workspace overlap)
// is not ambiguous with itself.
func TestWorkspaceRepoAliases_AmbiguousShortNameNamesNeither(t *testing.T) {
	aliases := WorkspaceRepoAliases([]string{"org-a/app", "org-b/app", "org-c/app", "org-a/app"})
	if v, ok := aliases["app"]; ok {
		t.Errorf("ambiguous short name resolved to %q; it must be absent", v)
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

// With no alias map no short name crosses a repository boundary, and in
// particular none reaches the example repositories the old default map named:
// those entries exist only in this package's tests now (testRepoAliases).
func TestParseDependencyRefs_NilAliasesNeverReachTheExampleRepos(t *testing.T) {
	bodies := []string{
		"Blocked by platform #12",
		"Blocked by platform#12",
		"Depends on: flutter #127, angular #152",
		"Blocked by core#7",
		"## Cross-Repo Dependencies\n\n- ❌ acme-mobile #127\n",
	}
	for _, body := range bodies {
		for _, ref := range ParseDependencyRefs(body, "example-org/app", nil) {
			if strings.HasPrefix(ref.Repo, "acme/") || ref.Repo == "nightgauge/nightgauge" {
				t.Errorf("%q resolved through the retired example map: %s#%d", body, ref.Repo, ref.Number)
			}
		}
	}
	got := refKeys(ParseDependencyRefs("Blocked by example-org/widget-api#12", "example-org/app", nil))
	if want := []string{"example-org/widget-api#12"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the full spelling must resolve without aliases, got %v", got)
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
