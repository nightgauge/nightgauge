// Package github — Phase 4 GitHub API schema validation tests.
//
// These tests validate that our GraphQL query structs reference real GitHub API
// fields with correct names and pagination arguments. They catch field
// renames/deprecations at test time rather than at runtime when a live query
// would fail with an obscure error.
//
// Three categories of tests:
//  1. Dependency manifest — asserts our critical subIssues/blockedBy/blocking
//     field dependencies exist in the expected query types.
//  2. Field name validation — inspects graphql struct tags to confirm field
//     names match GitHub API naming conventions (camelCase).
//  3. Deprecation detection — asserts no field tag uses a known deprecated
//     field name. Update [deprecatedFields] when GitHub announces deprecations.
//
// See: docs/GITHUB_API_DEPENDENCIES.md
// See: internal/github/types.go — all query/mutation struct definitions
package github

import (
	"reflect"
	"strings"
	"testing"
)

// --- Helpers -----------------------------------------------------------------

// graphqlTagsOf returns all graphql struct tag values from a struct type,
// recursively visiting nested anonymous/named structs. It skips pointers,
// slices, and non-struct fields.
func graphqlTagsOf(t reflect.Type) []string {
	if t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var tags []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if tag := f.Tag.Get("graphql"); tag != "" {
			tags = append(tags, tag)
		}
		tags = append(tags, graphqlTagsOf(f.Type)...)
	}
	return tags
}

// hasGraphQLField returns true if any graphql struct tag in v contains the
// given field name as a complete word (handling argument forms like
// "subIssues(first: 50)").
func hasGraphQLField(t reflect.Type, field string) bool {
	for _, tag := range graphqlTagsOf(t) {
		// Strip arguments: "subIssues(first: 50)" → "subIssues"
		name := strings.SplitN(tag, "(", 2)[0]
		// Strip inline fragment prefix: "... on Issue" → skip
		if strings.HasPrefix(name, "...") {
			continue
		}
		if name == field {
			return true
		}
	}
	return false
}

// graphqlTagFor returns the graphql tag value for the named struct field in t.
// Returns ("", false) if not found.
func graphqlTagFor(t reflect.Type, fieldName string) (string, bool) {
	if t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return "", false
	}
	f, ok := t.FieldByName(fieldName)
	if !ok {
		return "", false
	}
	tag := f.Tag.Get("graphql")
	return tag, tag != ""
}

// --- 1. Dependency Manifest --------------------------------------------------

// TestCriticalAPIDependencies_IssueQuery asserts that issueQuery contains our
// three critical sub-relationships: subIssues, blockedBy, and blocking.
//
// These fields are GitHub-specific extensions (not in the core GraphQL spec).
// If GitHub deprecates or renames any of them, this test will fail — giving us
// early warning before runtime breakage.
func TestCriticalAPIDependencies_IssueQuery(t *testing.T) {
	t.Helper()
	typ := reflect.TypeOf(issueQuery{})

	deps := []struct {
		field       string
		description string
	}{
		{"subIssues", "hierarchical parent/child issue relationships"},
		{"blockedBy", "issue blocking relationships (blocked by another issue)"},
		{"blocking", "issue blocking relationships (this issue blocks others)"},
	}

	for _, dep := range deps {
		t.Run(dep.field, func(t *testing.T) {
			if !hasGraphQLField(typ, dep.field) {
				t.Errorf(
					"issueQuery does not query field %q (%s)\n"+
						"If GitHub renamed this field, update types.go and this test.\n"+
						"See: docs/GITHUB_API_DEPENDENCIES.md",
					dep.field, dep.description,
				)
			}
		})
	}
}

// TestCriticalAPIDependencies_ProjectItemContent asserts that
// projectItemContent (used in board queries) also fetches subIssues, blockedBy,
// and blocking for each board item so the Ready Items tree view works.
func TestCriticalAPIDependencies_ProjectItemContent(t *testing.T) {
	typ := reflect.TypeOf(projectItemContent{})

	deps := []string{"subIssues", "blockedBy", "blocking"}
	for _, dep := range deps {
		t.Run(dep, func(t *testing.T) {
			if !hasGraphQLField(typ, dep) {
				t.Errorf(
					"projectItemContent does not query field %q\n"+
						"Board views require this field to show lock icons and group sub-issues.\n"+
						"See: docs/GITHUB_API_DEPENDENCIES.md",
					dep,
				)
			}
		})
	}
}

// TestCriticalAPIDependencies_NodeQuery asserts that nodeQuery (used for
// cross-repo epic progress) includes subIssues so epic completion tracking
// works across repositories.
func TestCriticalAPIDependencies_NodeQuery(t *testing.T) {
	typ := reflect.TypeOf(nodeQuery{})

	if !hasGraphQLField(typ, "subIssues") {
		t.Errorf(
			"nodeQuery does not query field \"subIssues\"\n" +
				"Cross-repo epic progress tracking requires subIssues on node lookups.\n" +
				"See: docs/GITHUB_API_DEPENDENCIES.md",
		)
	}
}

// --- 2. Mutation Dependency Manifest -----------------------------------------

// The four sub-issue and blocking mutations used to be pinned here as GraphQL
// mutation structs. They moved to REST in #956 and the structs were deleted, so
// there is nothing left for a GraphQL schema test to assert about them.
//
// This is deliberately recorded rather than silently removed: a schema-manifest
// test disappearing looks like coverage loss. The replacement pins are stronger,
// because they assert the transport rather than the document shape --
// TestAddSubIssue_UsesREST, TestRemoveSubIssue_UsesREST,
// TestAddBlockedBy_UsesREST and TestRemoveBlockedBy_UsesREST in issues_test.go,
// each asserting the exact method, path and request body.

// --- 3. Field Name Convention Validation -------------------------------------

// TestSubIssueNodeFieldNames validates that subIssueNode fields use camelCase
// names matching GitHub API conventions. A field renamed on the Go side but not
// in the graphql tag (or vice versa) causes silent query failures.
func TestSubIssueNodeFieldNames(t *testing.T) {
	// subIssueNode has no graphql tags — it maps to connection node fields.
	// Validate that the Go struct field names match what GitHub returns for
	// Issue.subIssues(first:N).nodes[*].
	typ := reflect.TypeOf(subIssueNode{})
	expectedFields := map[string]bool{
		"ID":         true,
		"Number":     true,
		"Title":      true,
		"State":      true,
		"Repository": true,
		"Labels":     true, // labels(first: 20) — eliminates extra GetIssue calls in UpdateEpicEstimates
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !expectedFields[name] {
			t.Errorf(
				"subIssueNode has unexpected field %q — verify it exists in GitHub API Issue.subIssues.nodes schema\n"+
					"See: docs/GITHUB_API_DEPENDENCIES.md",
				name,
			)
		}
		delete(expectedFields, name)
	}
	for missing := range expectedFields {
		t.Errorf("subIssueNode is missing expected field %q", missing)
	}
}

// TestBlockingNodeFieldNames validates blockingNode field coverage.
func TestBlockingNodeFieldNames(t *testing.T) {
	typ := reflect.TypeOf(blockingNode{})
	expectedFields := map[string]bool{
		"ID":         true,
		"Number":     true,
		"Title":      true,
		"State":      true,
		"Repository": true,
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !expectedFields[name] {
			t.Errorf(
				"blockingNode has unexpected field %q — verify it exists in GitHub API Issue.blockedBy/blocking.nodes schema\n"+
					"See: docs/GITHUB_API_DEPENDENCIES.md",
				name,
			)
		}
		delete(expectedFields, name)
	}
	for missing := range expectedFields {
		t.Errorf("blockingNode is missing expected field %q", missing)
	}
}

// --- 4. Pagination Argument Validation ---------------------------------------

// TestPaginationArguments validates that connection fields use the correct
// pagination arguments. GitHub requires first:/after: for cursor-based
// pagination; missing or wrong arguments cause API errors.
//
// The exact `first:` values are also pinned here because GraphQL rate-limit
// cost is points-based — `cost = sum of nested first values / 100`. Casually
// raising these multiplies query cost across the high-frequency board scan
// path. The current values were tuned in the follow-up to #3587 after
// repeated 5000/hr quota exhaustions. If you intentionally need more nodes,
// either bump the cap AND update this test, or paginate via a separate query
// (see GetEpicProgress at types.go:903 — which keeps `first: 50` for that
// reason).
func TestPaginationArguments(t *testing.T) {
	cases := []struct {
		queryType reflect.Type
		fieldName string // Go struct field name that holds the connection
		wantTag   string // expected graphql tag
		queryName string // human label for error messages
	}{
		{
			reflect.TypeOf(issueQuery{}.Repository.Issue),
			"SubIssues",
			"subIssues(first: 25)",
			"issueQuery.Repository.Issue",
		},
		{
			reflect.TypeOf(issueQuery{}.Repository.Issue),
			"BlockedBy",
			"blockedBy(first: 5)",
			"issueQuery.Repository.Issue",
		},
		{
			reflect.TypeOf(issueQuery{}.Repository.Issue),
			"Blocking",
			"blocking(first: 5)",
			"issueQuery.Repository.Issue",
		},
	}

	for _, tc := range cases {
		t.Run(tc.fieldName, func(t *testing.T) {
			tag, ok := graphqlTagFor(tc.queryType, tc.fieldName)
			if !ok {
				t.Errorf("%s.%s has no graphql tag — pagination arguments missing", tc.queryName, tc.fieldName)
				return
			}
			if tag != tc.wantTag {
				t.Errorf(
					"%s.%s graphql tag = %q, want %q\n"+
						"Pagination limit changes affect API cost and result completeness.",
					tc.queryName, tc.fieldName, tag, tc.wantTag,
				)
			}
		})
	}
}

// TestBoardScanPaginationBudget pins the nested first values used by the
// HIGHEST-FREQUENCY GraphQL path: the board scan query (used by depgraph
// BuildGraph, ProjectBoardService board.list, autonomous runCycle). The
// query is paginated at first: 100 items per page, so every unit of nested
// `first:` value contributes 100 nodes to the query cost. The 16× cost
// reduction landed alongside #3587's dispatch headroom check; raising these
// values is the single fastest way to put the workspace back into the
// chronic-quota-exhaustion regime that motivated both fixes.
//
// Specifically pinned:
//
//	fieldValues  ≤  8 — we read 4 fields (Status, Priority, Size, Pipeline Stage)
//	labels       ≤  8 — issues carry type:/component:/priority:/size: + a few more
//	subIssues    ≤ 12 — board scan only needs IsEpic detection + short ref list;
//	                    full epic enumeration goes through GetEpicProgress (nodeQuery)
//	blockedBy    ≤  5 — issues with > 5 distinct blockers are vanishingly rare
//	blocking     ≤  5 — symmetric
//	subIssue.labels ≤ 3 — sub-issue labels are barely consumed at the board layer
func TestBoardScanPaginationBudget(t *testing.T) {
	cases := []struct {
		queryType reflect.Type
		fieldName string
		wantTag   string
		queryName string
	}{
		{
			reflect.TypeOf(projectItemNode{}),
			"FieldValues",
			"fieldValues(first: 8)",
			"projectItemNode",
		},
		{
			reflect.TypeOf(projectItemContent{}.IssueFields),
			"Labels",
			"labels(first: 8)",
			"projectItemContent.IssueFields",
		},
		{
			reflect.TypeOf(projectItemContent{}.IssueFields),
			"SubIssues",
			"subIssues(first: 12)",
			"projectItemContent.IssueFields",
		},
		{
			reflect.TypeOf(projectItemContent{}.IssueFields),
			"BlockedBy",
			"blockedBy(first: 5)",
			"projectItemContent.IssueFields",
		},
		{
			reflect.TypeOf(projectItemContent{}.IssueFields),
			"Blocking",
			"blocking(first: 5)",
			"projectItemContent.IssueFields",
		},
		{
			reflect.TypeOf(projectItemContent{}.PRFields),
			"Labels",
			"labels(first: 8)",
			"projectItemContent.PRFields",
		},
		{
			reflect.TypeOf(subIssueNode{}),
			"Labels",
			"labels(first: 3)",
			"subIssueNode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.queryName+"."+tc.fieldName, func(t *testing.T) {
			tag, ok := graphqlTagFor(tc.queryType, tc.fieldName)
			if !ok {
				t.Errorf("%s.%s has no graphql tag — board scan budget intentionally pinned", tc.queryName, tc.fieldName)
				return
			}
			if tag != tc.wantTag {
				t.Errorf(
					"%s.%s graphql tag = %q, want %q\n"+
						"Board scan is the highest-frequency GraphQL path; nested `first:` "+
						"values directly multiply query cost. If you need more nodes here, "+
						"either bump the cap AND update this test, or fetch via a dedicated "+
						"query (e.g. GetEpicProgress for full sub-issue enumeration).",
					tc.queryName, tc.fieldName, tag, tc.wantTag,
				)
			}
		})
	}
}

// --- 5. Deprecation Detection ------------------------------------------------

// deprecatedFields is the registry of known deprecated GitHub GraphQL field
// names. Update this list when GitHub announces field deprecations.
// Format: deprecated name → replacement (or "removed" if no replacement).
var deprecatedFields = map[string]string{
	// Example (not yet deprecated):
	// "subIssues": "subIssues",  // still active as of 2026-03

	// Add entries here as GitHub deprecates fields, e.g.:
	// "timeline": "timelineItems",
}

// TestNoDeprecatedFieldsUsed scans all graphql tags across every query/mutation
// type and fails if any deprecated field name is found.
func TestNoDeprecatedFieldsUsed(t *testing.T) {
	if len(deprecatedFields) == 0 {
		t.Skip("no deprecated fields registered — nothing to check")
	}

	queryTypes := []struct {
		name string
		typ  reflect.Type
	}{
		{"issueQuery", reflect.TypeOf(issueQuery{})},
		{"projectV2Query", reflect.TypeOf(projectV2Query{})},
		{"projectV2FilteredQuery", reflect.TypeOf(projectV2FilteredQuery{})},
		{"nodeQuery", reflect.TypeOf(nodeQuery{})},
		{"pullRequestQuery", reflect.TypeOf(pullRequestQuery{})},
	}

	for _, qt := range queryTypes {
		t.Run(qt.name, func(t *testing.T) {
			for _, tag := range graphqlTagsOf(qt.typ) {
				// Extract field name from tag (strip arguments and inline fragments).
				name := strings.SplitN(tag, "(", 2)[0]
				if strings.HasPrefix(name, "...") {
					continue
				}
				if replacement, deprecated := deprecatedFields[name]; deprecated {
					t.Errorf(
						"%s uses deprecated field %q (replacement: %s)\n"+
							"Update types.go to use the replacement field and remove from deprecatedFields.\n"+
							"See: docs/GITHUB_API_DEPENDENCIES.md",
						qt.name, name, replacement,
					)
				}
			}
		})
	}
}

// --- 6. Structural Invariants ------------------------------------------------

// TestSubIssueAndBlockingRefSymmetry asserts that subIssueNode and blockingNode
// share base identity fields. subIssueNode intentionally carries a Labels field
// that blockingNode does not — labels are used by UpdateEpicEstimates to avoid
// extra GetIssue calls. BlockingRef label enrichment is tracked separately.
func TestSubIssueAndBlockingRefSymmetry(t *testing.T) {
	// Fields that must be present and identical on both node types.
	commonFields := []string{"ID", "Number", "Title", "State", "Repository"}

	subType := reflect.TypeOf(subIssueNode{})
	blockType := reflect.TypeOf(blockingNode{})

	subByName := make(map[string]reflect.StructField, subType.NumField())
	for i := 0; i < subType.NumField(); i++ {
		f := subType.Field(i)
		subByName[f.Name] = f
	}
	blockByName := make(map[string]reflect.StructField, blockType.NumField())
	for i := 0; i < blockType.NumField(); i++ {
		f := blockType.Field(i)
		blockByName[f.Name] = f
	}

	for _, name := range commonFields {
		sf, ok1 := subByName[name]
		bf, ok2 := blockByName[name]
		if !ok1 {
			t.Errorf("subIssueNode missing common field %q", name)
			continue
		}
		if !ok2 {
			t.Errorf("blockingNode missing common field %q", name)
			continue
		}
		if sf.Type != bf.Type {
			t.Errorf("field %s: subIssueNode type %v != blockingNode type %v", name, sf.Type, bf.Type)
		}
	}
}

// TestProjectFieldFullNodeNoInlineBraces asserts that no graphql struct tag in
// projectFieldFullNode (or its nested types) contains an inline brace block
// ("{ ... }"). The shurcooL/graphql library does not support this syntax and
// silently produces malformed queries that GitHub rejects at parse time.
//
// This test catches the regression from #1977 where:
//
//	Iterations []struct{ ... } `graphql:"configuration { iterations { id title } }"`
//
// caused: "Expected NAME, actual: LCURLY at [1, 322]"
func TestProjectFieldFullNodeNoInlineBraces(t *testing.T) {
	typ := reflect.TypeOf(projectFieldFullNode{})
	tags := graphqlTagsOf(typ)
	for _, tag := range tags {
		if strings.Contains(tag, "{") {
			t.Errorf(
				"projectFieldFullNode has graphql tag containing inline brace syntax: %q\n"+
					"shurcooL/graphql does not support inline { } in tags — use nested Go structs instead.\n"+
					"See: #1977 for the regression this test prevents.",
				tag,
			)
		}
	}
}

// --- 4. Union Selection Validation -------------------------------------------

// unionNodeTypes registers every query struct that maps onto a GraphQL *union*
// type. Membership is deliberately hand-maintained rather than inferred from
// the presence of a `__typename` tag: `__typename` is equally legal on an
// interface, and an interface DOES permit selecting its common fields directly.
// A blanket rule would flag those correctly-written structs.
var unionNodeTypes = map[string]reflect.Type{
	// SearchResultItem — search(type: ISSUE).nodes
	"searchIssueNode": reflect.TypeOf(searchIssueNode{}),
	// ProjectV2ItemContent — projectV2Item.content
	"projectItemContent": reflect.TypeOf(projectItemContent{}),
	// ProjectV2ItemFieldValue — projectV2Item.fieldValues.nodes
	"fieldValueNode": reflect.TypeOf(fieldValueNode{}),
}

// TestUnionNodesSelectThroughInlineFragments asserts that structs mapping onto
// GraphQL unions select every field through an `... on <Type>` inline fragment,
// with `__typename` as the sole permitted exception.
//
// Regression test for #1094. searchIssueNode selected id/number/title/state/url
// at the top level of a SearchResultItem, so GitHub rejected the query with
// "Selections can't be made directly on unions" before the search ever ran —
// `nightgauge issue list --search` had never worked in any invocation. Nothing
// caught it because the error is raised server-side at query-validation time
// and no test exercised SearchIssues against a real schema.
//
// The failure is invisible to the compiler and to every mock-backed test, which
// is why it is asserted structurally here rather than left to a live call.
func TestUnionNodesSelectThroughInlineFragments(t *testing.T) {
	for name, typ := range unionNodeTypes {
		t.Run(name, func(t *testing.T) {
			fragments := 0
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				tag := f.Tag.Get("graphql")

				if tag == "__typename" {
					continue
				}
				if strings.HasPrefix(tag, "... on ") {
					fragments++
					continue
				}

				t.Errorf("%s.%s selects %q directly on a union; GitHub rejects "+
					"the whole query with \"Selections can't be made directly on "+
					"unions\". Move it inside an `... on <Type>` inline fragment.",
					name, f.Name, tag)
			}
			if fragments == 0 {
				t.Errorf("%s is registered as a union node but selects nothing "+
					"through an inline fragment — either the struct is wrong or "+
					"it does not belong in unionNodeTypes", name)
			}
		})
	}
}
