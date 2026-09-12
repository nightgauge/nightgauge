package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// Issue relationship connections (subIssues, blockedBy, blocking) are read as
// a small first page inside a larger query, because nested `first:` values
// multiply GraphQL cost (see TestBoardScanPaginationBudget). A first page is
// not the connection: an epic can have more children, and an issue more
// blockers, than one page holds. Every reader therefore selects pageInfo on
// the connection and, while a page reports hasNextPage, reads the next one
// through a node(id:) query until the connection is exhausted.
//
// The walk is bounded. A connection that still reports hasNextPage after
// maxRelationPages pages is ErrConnectionTruncated, and so is one whose later
// page cannot be read. A caller never receives part of a connection as if it
// were the whole: epic rollup would call an epic complete, and the blocker
// check would call an issue unblocked, from the children and blockers it
// never saw.

const (
	// maxRelationPages caps the pages read for one connection, the caller's
	// first page included. With 100-node follow-up pages this is room for
	// well over a thousand relationships while still bounding the API calls
	// one misbehaving connection can spend.
	maxRelationPages = 20

	// relationFollowUpPageSize is the page size of every page after the
	// first: GitHub's maximum, because a follow-up only runs for a
	// connection already known to be larger than its first page.
	relationFollowUpPageSize = 100
)

// ErrConnectionTruncated reports a relationship connection that could not be
// read to its end: the page cap was reached, or a later page failed. Callers
// that plan or roll up from relationships must treat it as a hard error.
var ErrConnectionTruncated = errors.New("relationship connection truncated")

// relationPage is one page of an issue relationship connection. PageInfo is
// selected before Nodes so the struct reads the same in every query.
type relationPage[N any] struct {
	PageInfo pageInfo
	Nodes    []N
}

// subIssuePage is one page of an issue's subIssues connection.
type subIssuePage = relationPage[subIssueNode]

// blockingPage is one page of an issue's blockedBy or blocking connection.
type blockingPage = relationPage[blockingNode]

// The follow-up queries: one page of one connection, after a cursor, of the
// issue with the given node id. __typename proves the node resolved to an
// issue; a null or non-issue node would otherwise decode as an empty, final
// page.

type subIssuesAfterQuery struct {
	Node struct {
		TypeName string `graphql:"__typename"`
		Issue    struct {
			SubIssues subIssuePage `graphql:"subIssues(first: $first, after: $after)"`
		} `graphql:"... on Issue"`
	} `graphql:"node(id: $id)"`
}

type blockedByAfterQuery struct {
	Node struct {
		TypeName string `graphql:"__typename"`
		Issue    struct {
			BlockedBy blockingPage `graphql:"blockedBy(first: $first, after: $after)"`
		} `graphql:"... on Issue"`
	} `graphql:"node(id: $id)"`
}

type blockingAfterQuery struct {
	Node struct {
		TypeName string `graphql:"__typename"`
		Issue    struct {
			Blocking blockingPage `graphql:"blocking(first: $first, after: $after)"`
		} `graphql:"... on Issue"`
	} `graphql:"node(id: $id)"`
}

// completeRelations reads the rest of each relationship connection an issue
// query returned the first page of, in place: on success every non-nil page
// holds the whole connection. A nil page is a connection the query did not
// select. issueID is the issue's node id.
func (c *Client) completeRelations(ctx context.Context, issueID string, subIssues *subIssuePage, blockedBy, blocking *blockingPage) error {
	if subIssues != nil {
		if err := completeRelation(ctx, c, "subIssues", issueID, subIssues,
			func(q *subIssuesAfterQuery) (string, subIssuePage) {
				return q.Node.TypeName, q.Node.Issue.SubIssues
			}); err != nil {
			return err
		}
	}
	if blockedBy != nil {
		if err := completeRelation(ctx, c, "blockedBy", issueID, blockedBy,
			func(q *blockedByAfterQuery) (string, blockingPage) {
				return q.Node.TypeName, q.Node.Issue.BlockedBy
			}); err != nil {
			return err
		}
	}
	if blocking != nil {
		if err := completeRelation(ctx, c, "blocking", issueID, blocking,
			func(q *blockingAfterQuery) (string, blockingPage) {
				return q.Node.TypeName, q.Node.Issue.Blocking
			}); err != nil {
			return err
		}
	}
	return nil
}

// completeRelation walks one connection from the page p already holds to its
// end, reading each later page with the follow-up query Q. It stops with
// ErrConnectionTruncated rather than read more than maxRelationPages pages.
func completeRelation[N any, Q any](
	ctx context.Context,
	c *Client,
	conn, issueID string,
	p *relationPage[N],
	page func(*Q) (typeName string, next relationPage[N]),
) error {
	nodes, info := p.Nodes, p.PageInfo
	for read := 1; bool(info.HasNextPage); read++ {
		if read >= maxRelationPages {
			return fmt.Errorf("%s of issue %s: %w: more remain after the %d-page cap (%d read)",
				conn, issueID, ErrConnectionTruncated, maxRelationPages, len(nodes))
		}
		if issueID == "" || info.EndCursor == "" {
			return fmt.Errorf("%s of issue %q: %w: page %d has a next page but no issue id or cursor to continue from",
				conn, issueID, ErrConnectionTruncated, read)
		}
		var q Q
		vars := map[string]interface{}{
			"id":    graphql.ID(issueID),
			"first": graphql.Int(relationFollowUpPageSize),
			"after": info.EndCursor,
		}
		if err := c.query(ctx, &q, vars); err != nil {
			return fmt.Errorf("%s of issue %s: %w: read page %d: %w",
				conn, issueID, ErrConnectionTruncated, read+1, err)
		}
		typeName, next := page(&q)
		if typeName != "Issue" {
			return fmt.Errorf("%s of issue %s: %w: page %d resolved to %q, not an Issue",
				conn, issueID, ErrConnectionTruncated, read+1, typeName)
		}
		nodes = append(nodes, next.Nodes...)
		info = next.PageInfo
	}
	p.Nodes, p.PageInfo = nodes, pageInfo{}
	return nil
}

// nodeIDString renders a decoded node id, or "" when the query returned none,
// so a missing id reads as missing rather than as the string "<nil>".
func nodeIDString(id graphql.ID) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%v", id)
}

// subIssueRef converts a sub-issue node to its reference, without labels:
// the readers that consume sub-issue labels add them themselves.
func subIssueRef(n subIssueNode) types.SubIssueRef {
	return types.SubIssueRef{
		NodeID: fmt.Sprintf("%v", n.ID),
		Number: int(n.Number),
		Title:  string(n.Title),
		State:  string(n.State),
		Repo:   string(n.Repository.NameWithOwner),
	}
}

// blockingRef converts a blockedBy or blocking node to its reference.
func blockingRef(n blockingNode) types.BlockingRef {
	return types.BlockingRef{
		NodeID: fmt.Sprintf("%v", n.ID),
		Number: int(n.Number),
		Title:  string(n.Title),
		State:  string(n.State),
		Repo:   string(n.Repository.NameWithOwner),
	}
}
