package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// A reader collects every such connection its read returned (a board scan,
// a batch of issues, one issue) into a relationWalk, and the walk reads the
// next page of all of them in one aliased request per round. The client
// allows about one request a second, so one request per connection would
// make a board scan pay a second for every oversized item on the board.
//
// The walk is bounded. A connection that still reports hasNextPage after
// maxRelationPages pages is ErrConnectionTruncated, and so is one whose later
// page cannot be read. A caller never receives part of a connection as if it
// were the whole: epic rollup would call an epic complete, and the blocker
// check would call an issue unblocked, from the children and blockers it
// never saw. Callers that read only an issue's body or state use
// GetIssuesByNumbersWithoutRelations, which selects no connection and so can
// neither pay for nor fail on pages it would discard.

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

	// relationFollowUpsPerRequest caps the connections one follow-up request
	// reads a page of, which bounds the request's node count at
	// relationFollowUpsPerRequest × relationFollowUpPageSize.
	relationFollowUpsPerRequest = 50
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

// The selections a follow-up page reads, written the way the GraphQL client
// renders subIssuePage and blockingPage so a follow-up page decodes into the
// same nodes as the first page. TestRelationPageSelectionsMatchStructs pins
// them to the structs.
const (
	subIssuePageSelection = `{pageInfo{hasNextPage,endCursor},nodes{id,number,title,state,` +
		`repository{nameWithOwner},labels(first: 3){nodes{name}}}}`
	blockingPageSelection = `{pageInfo{hasNextPage,endCursor},nodes{id,number,title,state,` +
		`repository{nameWithOwner}}}`
)

// relationWalk collects the relationship connections of one read whose first
// page reported a next page, and reads them to their ends together.
type relationWalk struct {
	open []*openRelation
}

// openRelation is one connection still being read.
type openRelation struct {
	field     string // subIssues, blockedBy or blocking
	selection string
	issueID   string
	issue     string // names the issue in errors, e.g. "acme/widgets#7"
	pages     int    // pages read so far, the first included
	cursor    func() pageInfo
	absorb    func(json.RawMessage) error
}

func (r *openRelation) String() string {
	return r.field + " of " + r.issue
}

// add registers an issue's relationship pages, as its query returned them.
// A nil page is a connection the query did not select; a page with no next
// page is already whole. issue names the issue in errors.
func (w *relationWalk) add(issueID, issue string, subIssues *subIssuePage, blockedBy, blocking *blockingPage) {
	watchRelation(w, "subIssues", subIssuePageSelection, issueID, issue, subIssues)
	watchRelation(w, "blockedBy", blockingPageSelection, issueID, issue, blockedBy)
	watchRelation(w, "blocking", blockingPageSelection, issueID, issue, blocking)
}

func watchRelation[N any](w *relationWalk, field, selection, issueID, issue string, p *relationPage[N]) {
	if p == nil || !bool(p.PageInfo.HasNextPage) {
		return
	}
	w.open = append(w.open, &openRelation{
		field:     field,
		selection: selection,
		issueID:   issueID,
		issue:     issue,
		pages:     1,
		cursor:    func() pageInfo { return p.PageInfo },
		absorb: func(raw json.RawMessage) error {
			var next relationPage[N]
			if err := json.Unmarshal(raw, &next); err != nil {
				return err
			}
			p.Nodes = append(p.Nodes, next.Nodes...)
			p.PageInfo = next.PageInfo
			return nil
		},
	})
}

// completeRelations reads every connection in w to its end, in place. On
// success each page w was given holds its whole connection. Any connection
// that cannot be read whole fails the call with ErrConnectionTruncated.
func (c *Client) completeRelations(ctx context.Context, w *relationWalk) error {
	open := w.open
	for len(open) > 0 {
		for _, r := range open {
			if r.pages >= maxRelationPages {
				return fmt.Errorf("%s: %w: more remain after the %d-page cap",
					r, ErrConnectionTruncated, maxRelationPages)
			}
			if r.issueID == "" || r.cursor().EndCursor == "" {
				return fmt.Errorf("%s: %w: page %d has a next page but no issue id or cursor to continue from",
					r, ErrConnectionTruncated, r.pages)
			}
		}
		for start := 0; start < len(open); start += relationFollowUpsPerRequest {
			end := min(start+relationFollowUpsPerRequest, len(open))
			if err := c.readNextRelationPages(ctx, open[start:end]); err != nil {
				return err
			}
		}
		var still []*openRelation
		for _, r := range open {
			if bool(r.cursor().HasNextPage) {
				still = append(still, r)
			}
		}
		open = still
	}
	return nil
}

// completeIssueRelations is completeRelations for the connections of one issue.
func (c *Client) completeIssueRelations(ctx context.Context, issueID, issue string, subIssues *subIssuePage, blockedBy, blocking *blockingPage) error {
	var w relationWalk
	w.add(issueID, issue, subIssues, blockedBy, blocking)
	return c.completeRelations(ctx, &w)
}

// readNextRelationPages reads the next page of each connection in batch with
// one aliased query, and appends it. A failed request, a GraphQL error, or an
// alias that is not an Issue with the connection fails the whole batch.
func (c *Client) readNextRelationPages(ctx context.Context, batch []*openRelation) error {
	var sb strings.Builder
	vars := make(map[string]interface{}, 2*len(batch))
	sb.WriteString("query(")
	for i, r := range batch {
		fmt.Fprintf(&sb, "$id%d: ID!, $after%d: String!", i, i)
		if i < len(batch)-1 {
			sb.WriteString(", ")
		}
		vars[fmt.Sprintf("id%d", i)] = r.issueID
		vars[fmt.Sprintf("after%d", i)] = string(r.cursor().EndCursor)
	}
	sb.WriteString(") {\n")
	for i, r := range batch {
		fmt.Fprintf(&sb, "  r%d: node(id: $id%d) { __typename ... on Issue { %s(first: %d, after: $after%d) %s } }\n",
			i, i, r.field, relationFollowUpPageSize, i, r.selection)
	}
	sb.WriteString("}\n")

	failed := func(format string, args ...interface{}) error {
		what := batch[0].String()
		if len(batch) > 1 {
			what = fmt.Sprintf("%s and %d more", what, len(batch)-1)
		}
		return fmt.Errorf("%s: %w: read page %d: %s",
			what, ErrConnectionTruncated, batch[0].pages+1, fmt.Sprintf(format, args...))
	}

	raw, err := c.queryRaw(ctx, sb.String(), vars)
	if err != nil {
		return failed("%v", err)
	}
	var env struct {
		Data   map[string]map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return failed("decode response: %v", err)
	}
	if len(env.Errors) > 0 {
		return failed("%s", env.Errors[0].Message)
	}
	for i, r := range batch {
		node := env.Data[fmt.Sprintf("r%d", i)]
		var typeName string
		if t, ok := node["__typename"]; ok {
			_ = json.Unmarshal(t, &typeName)
		}
		if typeName != "Issue" {
			return fmt.Errorf("%s: %w: page %d: node %s resolved to %q, not an Issue",
				r, ErrConnectionTruncated, r.pages+1, r.issueID, typeName)
		}
		page, ok := node[r.field]
		if !ok || string(page) == "null" {
			return fmt.Errorf("%s: %w: page %d: response has no %s page",
				r, ErrConnectionTruncated, r.pages+1, r.field)
		}
		if err := r.absorb(page); err != nil {
			return fmt.Errorf("%s: %w: page %d: decode: %v",
				r, ErrConnectionTruncated, r.pages+1, err)
		}
		r.pages++
	}
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
