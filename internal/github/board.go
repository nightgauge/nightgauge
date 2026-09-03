package github

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// hasTypeEpicLabel reports whether the given label set contains the canonical
// `type:epic` marker that identifies an epic on the project board.
func hasTypeEpicLabel(labels []string) bool {
	for _, l := range labels {
		if l == "type:epic" {
			return true
		}
	}
	return false
}

// BoardService provides project board read operations.
type BoardService struct {
	client        *Client
	owner         string
	ownerType     OwnerType
	projectNumber int
}

// NewBoardService creates a board service for the given owner and project number.
// ownerType distinguishes organizations ("org") from user accounts ("user").
func NewBoardService(client *Client, owner string, projectNumber int, ownerType ...OwnerType) *BoardService {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	return &BoardService{
		client:        client,
		owner:         owner,
		ownerType:     ot,
		projectNumber: projectNumber,
	}
}

// ListItems fetches project board items, optionally filtered by status.
// When a status filter is provided, uses GitHub's server-side query parameter
// for efficient single-page fetches instead of paginating all items locally.
func (b *BoardService) ListItems(ctx context.Context, statusFilter string) ([]types.BoardItem, error) {
	if statusFilter != "" {
		return b.listItemsFiltered(ctx, statusFilter)
	}
	return b.listItemsAll(ctx)
}

// listItemsFiltered uses server-side filtering via the query: parameter.
// This typically returns a single page (e.g., 2-15 items for "Ready") instead
// of paginating through all 400+ items.
func (b *BoardService) listItemsFiltered(ctx context.Context, statusFilter string) ([]types.BoardItem, error) {
	allItems := make([]types.BoardItem, 0)
	var cursor *graphql.String

	// "Done" items are typically closed issues — don't filter by is:open
	// or they vanish from the board after merge.
	queryStr := fmt.Sprintf("status:\"%s\"", statusFilter)
	if statusFilter != "Done" {
		queryStr += " is:open"
	}

	for {
		vars := map[string]interface{}{
			"owner":         graphql.String(b.owner),
			"projectNumber": graphql.Int(b.projectNumber),
			"first":         graphql.Int(100),
			"after":         cursor,
			"query":         graphql.String(queryStr),
		}

		result, err := queryProjectItemsFiltered(ctx, b.client, b.ownerType, vars)
		if err != nil {
			return nil, fmt.Errorf("fetch board items (filtered): %w", err)
		}

		for _, node := range result.Items.Nodes {
			item := b.nodeToItem(node)
			if item == nil {
				continue
			}
			allItems = append(allItems, *item)
		}

		if !bool(result.Items.PageInfo.HasNextPage) {
			break
		}
		endCursor := result.Items.PageInfo.EndCursor
		cursor = &endCursor
	}

	return allItems, nil
}

// ListOpenItems fetches only open items from the board using server-side
// "is:open" filtering. Much faster than ListItems("") for boards with many
// closed entries — avoids paginating through hundreds of archived items.
// Returns the filtered items, the total raw node count from GraphQL (before
// nodeToItem filtering), and any error.
func (b *BoardService) ListOpenItems(ctx context.Context) ([]types.BoardItem, int, error) {
	allItems := make([]types.BoardItem, 0)
	rawCount := 0
	var cursor *graphql.String

	for {
		vars := map[string]interface{}{
			"owner":         graphql.String(b.owner),
			"projectNumber": graphql.Int(b.projectNumber),
			"first":         graphql.Int(100),
			"after":         cursor,
			"query":         graphql.String("is:open"),
		}

		result, err := queryProjectItemsFiltered(ctx, b.client, b.ownerType, vars)
		if err != nil {
			return nil, 0, fmt.Errorf("fetch board items (open): %w", err)
		}

		rawCount += len(result.Items.Nodes)
		for _, node := range result.Items.Nodes {
			item := b.nodeToItem(node)
			if item == nil {
				continue
			}
			allItems = append(allItems, *item)
		}

		if !bool(result.Items.PageInfo.HasNextPage) {
			break
		}
		endCursor := result.Items.PageInfo.EndCursor
		cursor = &endCursor
	}

	return allItems, rawCount, nil
}

// listItemsAll fetches all project board items without filtering.
func (b *BoardService) listItemsAll(ctx context.Context) ([]types.BoardItem, error) {
	allItems := make([]types.BoardItem, 0)
	var cursor *graphql.String

	for {
		vars := map[string]interface{}{
			"owner":         graphql.String(b.owner),
			"projectNumber": graphql.Int(b.projectNumber),
			"first":         graphql.Int(100),
			"after":         cursor,
		}

		result, err := queryProjectItems(ctx, b.client, b.ownerType, vars)
		if err != nil {
			return nil, fmt.Errorf("fetch board items: %w", err)
		}

		for _, node := range result.Items.Nodes {
			item := b.nodeToItem(node)
			if item == nil {
				continue
			}
			allItems = append(allItems, *item)
		}

		if !bool(result.Items.PageInfo.HasNextPage) {
			break
		}
		endCursor := result.Items.PageInfo.EndCursor
		cursor = &endCursor
	}

	return allItems, nil
}

// nodeToItem converts a GraphQL project item node to a BoardItem.
func (b *BoardService) nodeToItem(node projectItemNode) *types.BoardItem {
	var item types.BoardItem
	item.ID = fmt.Sprintf("%v", node.ID)

	switch node.Content.TypeName {
	case "Issue":
		f := node.Content.IssueFields
		item.Number = int(f.Number)
		item.Title = string(f.Title)
		item.State = string(f.State)
		item.URL = string(f.URL)
		item.Repo = string(f.Repository.NameWithOwner)
		item.CreatedAt, _ = time.Parse(time.RFC3339, string(f.CreatedAt))
		item.UpdatedAt, _ = time.Parse(time.RFC3339, string(f.UpdatedAt))
		item.IsPR = false
		item.AuthorAssociation = string(f.AuthorAssociation)
		for _, l := range f.Labels.Nodes {
			item.Labels = append(item.Labels, string(l.Name))
		}
		// Sub-issue relationships (GitHub native)
		for _, si := range f.SubIssues.Nodes {
			item.SubIssues = append(item.SubIssues, types.SubIssueRef{
				NodeID: fmt.Sprintf("%v", si.ID),
				Number: int(si.Number),
				Title:  string(si.Title),
				State:  string(si.State),
				Repo:   string(si.Repository.NameWithOwner),
			})
		}
		// An epic is identified by the canonical `type:epic` label OR by the
		// presence of native sub-issues. Label is the source of truth — children
		// are added after creation, so brand-new epics with zero sub-issues must
		// still report IsEpic=true so views render them as epic group headers
		// instead of filtering them out (Issue #3329).
		item.IsEpic = len(f.SubIssues.Nodes) > 0 || hasTypeEpicLabel(item.Labels)
		// Parent epic (for sub-issues whose parent epic is in a different status)
		if parentNum := int(f.Parent.Number); parentNum != 0 {
			item.ParentNumber = parentNum
			item.ParentTitle = string(f.Parent.Title)
		}
		// Blocking relationships (GitHub native)
		for _, b := range f.BlockedBy.Nodes {
			item.BlockedBy = append(item.BlockedBy, types.BlockingRef{
				NodeID: fmt.Sprintf("%v", b.ID),
				Number: int(b.Number),
				Title:  string(b.Title),
				State:  string(b.State),
				Repo:   string(b.Repository.NameWithOwner),
			})
		}
		for _, b := range f.Blocking.Nodes {
			item.Blocking = append(item.Blocking, types.BlockingRef{
				NodeID: fmt.Sprintf("%v", b.ID),
				Number: int(b.Number),
				Title:  string(b.Title),
				State:  string(b.State),
				Repo:   string(b.Repository.NameWithOwner),
			})
		}
	case "PullRequest":
		f := node.Content.PRFields
		item.Number = int(f.Number)
		item.Title = string(f.Title)
		item.State = string(f.State)
		item.URL = string(f.URL)
		item.Repo = string(f.Repository.NameWithOwner)
		item.CreatedAt, _ = time.Parse(time.RFC3339, string(f.CreatedAt))
		item.UpdatedAt, _ = time.Parse(time.RFC3339, string(f.UpdatedAt))
		item.IsPR = true
		for _, l := range f.Labels.Nodes {
			item.Labels = append(item.Labels, string(l.Name))
		}
	default:
		log.Printf("depgraph: board: nodeToItem dropping item id=%v type=%q (DraftIssue or unknown)", node.ID, node.Content.TypeName)
		return nil
	}

	// Extract field values (Status, Priority, Size, Pipeline Stage)
	for _, fv := range node.FieldValues.Nodes {
		switch fv.TypeName {
		case "ProjectV2ItemFieldSingleSelectValue":
			fieldName := string(fv.ProjectV2ItemFieldSingleSelect.Field.ProjectV2SingleSelectField.Name)
			value := string(fv.ProjectV2ItemFieldSingleSelect.Name)
			switch fieldName {
			case "Status":
				item.Status = value
			case "Priority":
				item.Priority = types.Priority(value)
			case "Size":
				item.Size = types.Size(value)
			}
		case "ProjectV2ItemFieldTextValue":
			fieldName := string(fv.ProjectV2ItemFieldText.Field.ProjectV2Field.Name)
			value := string(fv.ProjectV2ItemFieldText.Text)
			switch fieldName {
			case "Pipeline Stage":
				item.PipelineStage = value
			}
		}
	}

	// Extract priority/size from labels if not set via project fields
	if item.Priority == "" {
		item.Priority = priorityFromLabels(item.Labels)
	}
	if item.Size == "" {
		item.Size = sizeFromLabels(item.Labels)
	}

	return &item
}

// GetItem fetches a single board item by issue number. Uses the issue's
// projectItems connection — one targeted GraphQL request rather than paging
// the whole board. Returns forge.ErrNotFound when the issue exists but is
// not on the bound project board.
//
// owner and repo identify the issue's repository; the BoardService is bound
// to a single project (b.owner / b.projectNumber), and the returned item is
// the project item for this issue on that board.
func (b *BoardService) GetItem(ctx context.Context, owner, repo string, issueNumber int) (*types.BoardItem, error) {
	// Use a server-side query filter for the issue number, then walk the
	// returned items looking for the matching repo + number. This keeps the
	// implementation aligned with the existing list-items code path and
	// reuses nodeToItem for field extraction.
	//
	// The filter MUST be the `repo:<owner>/<repo> #<N>` form. The Projects V2
	// item search does not parse a bare `owner/repo#N` slug — it returns zero
	// rows for a row that is on the board, without an error — so the earlier
	// shape made every caller (RunQueue, `project field-get`, the board
	// cache) report a Ready issue as "not found on board". The repo qualifier
	// keeps a shared board honest: two repos at the same number stay apart
	// server-side, and the Repo+Number match below is the identity check.
	queryStr := boardItemQuery(owner, repo, issueNumber)
	vars := map[string]interface{}{
		"owner":         graphql.String(b.owner),
		"projectNumber": graphql.Int(b.projectNumber),
		"first":         graphql.Int(20),
		"after":         (*graphql.String)(nil),
		"query":         graphql.String(queryStr),
	}

	result, err := queryProjectItemsFiltered(ctx, b.client, b.ownerType, vars)
	if err != nil {
		return nil, fmt.Errorf("get board item #%d: %w", issueNumber, err)
	}

	wantRepo := owner + "/" + repo
	for _, node := range result.Items.Nodes {
		item := b.nodeToItem(node)
		if item == nil {
			continue
		}
		if item.Number == issueNumber && item.Repo == wantRepo {
			return item, nil
		}
	}
	return nil, fmt.Errorf("board item %s/%s#%d: %w", owner, repo, issueNumber, forge.ErrNotFound)
}

// boardItemQuery is the Projects V2 `items(query:)` filter that selects one
// issue on a board: `repo:<owner>/<repo> #<N>`. Kept as a function so a test
// can pin the exact string the server is asked for.
func boardItemQuery(owner, repo string, issueNumber int) string {
	return fmt.Sprintf("repo:%s/%s #%d", owner, repo, issueNumber)
}

func priorityFromLabels(labels []string) types.Priority {
	for _, l := range labels {
		switch l {
		case "priority:critical":
			return types.PriorityP0
		case "priority:high":
			return types.PriorityP1
		case "priority:medium":
			return types.PriorityP2
		case "priority:low":
			return types.PriorityP3
		}
	}
	return ""
}

func sizeFromLabels(labels []string) types.Size {
	for _, l := range labels {
		switch l {
		case "size:XS":
			return types.SizeXS
		case "size:S":
			return types.SizeS
		case "size:M":
			return types.SizeM
		case "size:L":
			return types.SizeL
		case "size:XL":
			return types.SizeXL
		}
	}
	return ""
}

// ProjectUpdatedAt reports when the board last changed, for one point.
//
// This is the change probe behind #847's read gate. What it observes was
// measured against live boards rather than assumed, because a detector that
// answers a slightly different question than the read it gates is worse than
// no detector at all — it reports "nothing moved" with confidence while the
// board has moved. (The mechanism this issue originally specified,
// `/repos/{o}/{r}/events` with If-None-Match, fails exactly that way: the repo
// event feed carries no ProjectsV2 events at all.)
//
// It DOES move on:
//
//   - a field-value change, including the Backlog -> Ready transition the
//     scheduler dispatches on;
//   - an item being added to or removed from the board;
//   - a linked issue being CLOSED or reopened, even when nothing writes a
//     board field. Verified on a board holding 17 issues closed while their
//     Status stayed Backlog/Ready: every one bumped the item, and the project,
//     within a second of closedAt.
//
// It does NOT move on:
//
//   - an edit to a linked issue's own content — title, labels, body, comments.
//     Verified on a board item whose issue was edited a full WEEK after the
//     item's own updatedAt last moved.
//
// That last exclusion is why boardcache renews a snapshot only up to
// MaxRenewedAge rather than indefinitely: BoardItem carries Title, Labels,
// BlockedBy and SubIssues, and this probe is structurally blind to all four.
// Reads never bump it, so polling the probe cannot make the probe fire.
func (b *BoardService) ProjectUpdatedAt(ctx context.Context) (time.Time, error) {
	vars := map[string]interface{}{
		"owner":         graphql.String(b.owner),
		"projectNumber": graphql.Int(b.projectNumber),
	}
	raw, err := queryProjectUpdatedAt(ctx, b.client, b.ownerType, vars)
	if err != nil {
		return time.Time{}, fmt.Errorf("probe board updatedAt: %w", err)
	}
	if raw == "" {
		// A present-but-empty timestamp is not "the board never changed"; it is
		// a shape we do not understand. Report it so the caller refetches
		// rather than trusting a zero value.
		return time.Time{}, fmt.Errorf("probe board updatedAt: empty timestamp")
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("probe board updatedAt: parse %q: %w", raw, err)
	}
	return ts, nil
}
