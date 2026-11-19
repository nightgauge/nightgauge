package boardcache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// gqlClient is a fakeClient that also has the optional GraphQL surface.
type gqlClient struct {
	fakeClient
	err   error
	calls int
}

func (c *gqlClient) ExecuteGraphQL(context.Context, string, map[string]interface{}) ([]byte, error) {
	c.calls++
	return []byte(`{"data":{}}`), c.err
}

// #2684 / #2697 review: a raw GraphQL write through the wrapper used to leave
// the board snapshot in place, so this process read back a board it had just
// changed. A write drops it, on success and on failure; a read keeps it.
func TestExecuteGraphQLInvalidatesOnlyAfterAWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		doc     string
		err     error
		dropped bool
	}{
		{"mutation", `mutation { updateProjectV2ItemFieldValue(input: {}) { clientMutationId } }`, nil, true},
		{"failed mutation", `mutation M { deleteProjectV2Item(input: {}) { deletedItemId } }`, errors.New("boom"), true},
		{"named query", `query Repo($owner: String!) { repository(owner: $owner) { id } }`, nil, false},
		{"query shorthand", `{ viewer { login } }`, nil, false},
		{"comment then query", "# read the id\n  query { viewer { login } }", nil, false},
		{"fragment beside a query cannot write", `fragment F on Repository { id } query { viewer { login } }`, nil, false},
		{"fragment beside a mutation", `fragment F on R { id } mutation { x }`, nil, true},
		{"query then mutation", `query A { a } mutation B { b }`, nil, true},
		{"query then subscription", `query A { a } subscription S { s }`, nil, true},
		{"shorthand then mutation", `{ a } mutation { b }`, nil, true},
		{"subscription only", `subscription { s }`, nil, true},
		{"unnamed query", `query { a }`, nil, false},
		{"two queries", `query A { a } query B { b }`, nil, false},
		{"query-prefixed name is not the keyword", `queryish { x }`, nil, true},
		{"empty document", ``, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(time.Hour)
			inner := &countingBoard{items: sampleItems()}
			gql := &gqlClient{fakeClient: fakeClient{board: inner, project: &fakeProject{}}, err: tc.err}
			client := WrapClient(c, gql, "acme", 3)
			ctx := context.Background()
			if _, _, err := client.Board().ListOpenItems(ctx); err != nil {
				t.Fatal(err)
			}

			svc, ok := client.(forge.GraphQLService)
			if !ok {
				t.Fatal("the wrapped client hides the inner GraphQL surface")
			}
			if _, err := svc.ExecuteGraphQL(ctx, tc.doc, nil); !errors.Is(err, tc.err) {
				t.Fatalf("ExecuteGraphQL error = %v, want %v", err, tc.err)
			}
			if gql.calls != 1 {
				t.Fatalf("inner ExecuteGraphQL called %d times, want 1", gql.calls)
			}
			if _, cached := c.Peek("acme", 3, "open"); cached == tc.dropped {
				t.Errorf("snapshot cached = %v after %q, want %v", cached, tc.doc, !tc.dropped)
			}
		})
	}
}

// #2732: every definition is classified, and brackets or keywords inside
// strings and comments do not move the scan.
func TestIsReadDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		read bool
	}{
		{"brace and keyword in a string", `query { a(x: "} mutation { b }") }`, true},
		{"brace and keyword in a block string", `query { a(x: """} \""" mutation { b }""") }`, true},
		{"escaped quote in a string", `query { a(x: "\"}") }`, true},
		{"comment inside a selection set", "query {\n  a # } mutation { b }\n}", true},
		{"object default in variables", `query Q($x: In = {a: 1}) { a(x: $x) }`, true},
		{"directive with args", `query Q @cached(ttl: 60) { a }`, true},
		{"mutation after a string with a brace", `query { a(x: "{") } mutation { b }`, false},
		{"unbalanced brace", `query { a { b }`, false},
		{"unterminated string", `query { a(x: "oops) }`, false},
		{"unterminated block string", `query { a(x: """oops) }`, false},
		{"stray top-level brace", `query { a } }`, false},
		{"mismatched bracket", `query { a(x: 1} }`, false},
		{"header with no selection set", `query Q`, false},
		{"unbalanced variable parens", `query Q($x: Int { a }`, false},
		{"string in a header", `query Q "x" { a }`, false},
		{"stray bracket in a header", `query Q ] { a }`, false},
		{"newline inside a string", "query { a(x: \"oops\n\") } mutation { b }", false},
		{"selection set left open before a comment", "query { a  # tail", false},
		{"empty", ``, false},
		{"comment only", "# nothing here", false},
		{"BOM and commas then query", "\ufeff,, query { a }", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadDocument(tc.doc); got != tc.read {
				t.Errorf("isReadDocument(%q) = %v, want %v", tc.doc, got, tc.read)
			}
		})
	}
}

// A raw document does not say which board it writes, and one Cache holds several
// boards, so a write through any wrapper, bound or not, drops all of them.
func TestExecuteGraphQLWriteDropsEveryBoard(t *testing.T) {
	for _, project := range []int{3, 0} {
		c := New(time.Hour)
		ctx := context.Background()
		if _, _, err := c.Wrap(&countingBoard{items: sampleItems()}, "other", 5).ListOpenItems(ctx); err != nil {
			t.Fatal(err)
		}
		client := WrapClient(c, &gqlClient{fakeClient: fakeClient{board: &countingBoard{}}}, "acme", project)
		if _, err := client.(forge.GraphQLService).ExecuteGraphQL(ctx, `mutation { x }`, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Peek("other", 5, "open"); ok {
			t.Errorf("a GraphQL write through project %d left another board cached", project)
		}
	}
}

func TestExecuteGraphQLWithoutInnerSurfaceIsUnsupported(t *testing.T) {
	client := WrapClient(New(time.Hour), &fakeClient{board: &countingBoard{}}, "acme", 3)
	_, err := client.(forge.GraphQLService).ExecuteGraphQL(context.Background(), `{ viewer { login } }`, nil)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("err = %v, want one wrapping forge.ErrUnsupported", err)
	}
}
