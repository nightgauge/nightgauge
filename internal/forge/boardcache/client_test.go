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
		{"fragment first", `fragment F on Repository { id } query { viewer { login } }`, nil, true},
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
