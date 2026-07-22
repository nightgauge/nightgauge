package github

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// Pipeline label constants used by autonomous scheduling and refinement commands.
const (
	// LabelRefined marks issues that have been refined by the AI agent.
	LabelRefined = "pipeline:refined"
	// LabelAutoProcess marks issues for automatic processing by the autonomous scheduler.
	LabelAutoProcess = "auto-process"
	// LabelEpic identifies epic issues that group related sub-issues.
	LabelEpic = "type:epic"
)

// UnrefinedIssue is a lightweight issue result for list-unrefined output.
type UnrefinedIssue struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Labels    []string `json:"labels"`
	CreatedAt string   `json:"created_at"`
}

// dependabotLabels is the set of labels applied by Dependabot to its issues and PRs.
var dependabotLabels = map[string]bool{
	"dependencies":   true,
	"security":       true,
	"go":             true,
	"javascript":     true,
	"python":         true,
	"rust":           true,
	"java":           true,
	"ruby":           true,
	"php":            true,
	"dotnet":         true,
	"docker":         true,
	"github-actions": true,
	"npm":            true,
}

// IsDependabotIssue reports whether an issue is a Dependabot dependency update.
// Detection is label-based: presence of any known Dependabot label qualifies the issue.
func IsDependabotIssue(labels []string) bool {
	for _, l := range labels {
		if dependabotLabels[l] {
			return true
		}
	}
	return false
}

// IsSpikeIssue reports whether an issue is a research/investigation spike.
// Detection is label-based: presence of `type:spike` qualifies the issue.
func IsSpikeIssue(labels []string) bool {
	for _, l := range labels {
		if l == "type:spike" {
			return true
		}
	}
	return false
}

// DetectDependabotType classifies a Dependabot issue as "security" or "dependency".
// Returns an empty string when the issue is not a Dependabot issue.
func DetectDependabotType(labels []string) string {
	if !IsDependabotIssue(labels) {
		return ""
	}
	for _, l := range labels {
		if l == "security" {
			return "security"
		}
	}
	return "dependency"
}

// IssueService provides issue CRUD and sub-issue operations.
type IssueService struct {
	client *Client
	// repoLabelsCache caches repo label name→nodeID maps per "owner/repo" key.
	// Labels rarely change within a session, so caching avoids a redundant API
	// call on every SyncStatusLabel / MarkRefined invocation.
	repoLabelsCache map[string]map[string]string
}

// NewIssueService creates an issue service.
func NewIssueService(client *Client) *IssueService {
	return &IssueService{
		client:          client,
		repoLabelsCache: make(map[string]map[string]string),
	}
}

// GetIssue fetches a single issue with sub-issues and blocking relationships.
func (s *IssueService) GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error) {
	graphQLNumber, err := checkedGraphQLInt("issue number", number)
	if err != nil {
		return nil, err
	}
	var q issueQuery
	vars := map[string]interface{}{
		"owner":  graphql.String(owner),
		"name":   graphql.String(repo),
		"number": graphQLNumber,
	}

	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("fetch issue #%d: %w", number, err)
	}

	issue := &types.Issue{
		NodeID:      fmt.Sprintf("%v", q.Repository.Issue.ID),
		Number:      int(q.Repository.Issue.Number),
		Title:       string(q.Repository.Issue.Title),
		Body:        string(q.Repository.Issue.Body),
		State:       string(q.Repository.Issue.State),
		StateReason: string(q.Repository.Issue.StateReason),
		Repo:        owner + "/" + repo,
		URL:         string(q.Repository.Issue.URL),
	}

	if parentNumber := int(q.Repository.Issue.Parent.Number); parentNumber != 0 {
		issue.ParentIssueID = fmt.Sprintf("%v", q.Repository.Issue.Parent.ID)
		issue.ParentIssueNumber = parentNumber
	}

	for _, l := range q.Repository.Issue.Labels.Nodes {
		issue.Labels = append(issue.Labels, string(l.Name))
	}
	for _, a := range q.Repository.Issue.Assignees.Nodes {
		issue.Assignees = append(issue.Assignees, string(a.Login))
	}

	// Sub-issues
	for _, si := range q.Repository.Issue.SubIssues.Nodes {
		siRef := types.SubIssueRef{
			NodeID: fmt.Sprintf("%v", si.ID),
			Number: int(si.Number),
			Title:  string(si.Title),
			State:  string(si.State),
			Repo:   string(si.Repository.NameWithOwner),
		}
		for _, l := range si.Labels.Nodes {
			siRef.Labels = append(siRef.Labels, string(l.Name))
		}
		issue.SubIssues = append(issue.SubIssues, siRef)
	}

	issue.IsEpic = len(issue.SubIssues) > 0

	// Blocking relationships
	for _, b := range q.Repository.Issue.BlockedBy.Nodes {
		issue.BlockedBy = append(issue.BlockedBy, types.BlockingRef{
			NodeID: fmt.Sprintf("%v", b.ID),
			Number: int(b.Number),
			Title:  string(b.Title),
			State:  string(b.State),
			Repo:   string(b.Repository.NameWithOwner),
		})
	}
	for _, b := range q.Repository.Issue.Blocking.Nodes {
		issue.Blocking = append(issue.Blocking, types.BlockingRef{
			NodeID: fmt.Sprintf("%v", b.ID),
			Number: int(b.Number),
			Title:  string(b.Title),
			State:  string(b.State),
			Repo:   string(b.Repository.NameWithOwner),
		})
	}

	return issue, nil
}

// GetIssuesByNumbers fetches multiple issues from a single repository in one
// GraphQL request using query aliases. Returns a map of issue number →
// *types.Issue. Issues that GitHub reports as null (deleted, inaccessible) are
// silently omitted from the result.
//
// This avoids the per-issue round-trip pattern (N GetIssue calls) used on hot
// paths such as Scheduler.refreshBlockerStates, ProjectService.UpdateEpicEstimates,
// and IssueService.ValidateEpic.
//
// The returned issues populate the same fields as GetIssue: Labels, Assignees,
// SubIssues, BlockedBy, Blocking, Parent. Numbers are deduplicated.
func (s *IssueService) GetIssuesByNumbers(ctx context.Context, owner, repo string, numbers []int) (map[int]*types.Issue, error) {
	if len(numbers) == 0 {
		return map[int]*types.Issue{}, nil
	}

	// Deduplicate while preserving determinism for tests.
	seen := make(map[int]struct{}, len(numbers))
	ordered := make([]int, 0, len(numbers))
	for _, n := range numbers {
		if n <= 0 {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		ordered = append(ordered, n)
	}
	if len(ordered) == 0 {
		return map[int]*types.Issue{}, nil
	}
	sort.Ints(ordered)

	// Build aliased GraphQL: i100: issue(number: 100) { ...IssueFields }
	var sb strings.Builder
	sb.WriteString("query($owner: String!, $name: String!) {\n")
	sb.WriteString("  repository(owner: $owner, name: $name) {\n")
	for _, n := range ordered {
		fmt.Fprintf(&sb, "    i%d: issue(number: %d) { ...IssueFields }\n", n, n)
	}
	sb.WriteString("  }\n}\n")
	// nested-first values mirror issueQuery in types.go — kept in sync because
	// this batched aliased query is invoked once per repo from depgraph body
	// fetching and aliases multiply per-issue cost by N. See #3587 for the
	// cost-reduction rationale.
	sb.WriteString(`fragment IssueFields on Issue {
  id
  number
  title
  body
  state
  url
  parent { id number title }
  labels(first: 10) { nodes { name } }
  assignees(first: 5) { nodes { login } }
  subIssues(first: 25) { nodes { id number title state repository { nameWithOwner } } }
  blockedBy(first: 5) { nodes { id number title state repository { nameWithOwner } } }
  blocking(first: 5) { nodes { id number title state repository { nameWithOwner } } }
}
`)

	vars := map[string]interface{}{
		"owner": owner,
		"name":  repo,
	}

	raw, err := s.client.queryRaw(ctx, sb.String(), vars)
	if err != nil {
		return nil, fmt.Errorf("batch fetch issues: %w", err)
	}

	// Decode the aliased response. Each alias becomes a key like "i100".
	type rawNode struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Parent *struct {
			ID     string `json:"id"`
			Number int    `json:"number"`
			Title  string `json:"title"`
		} `json:"parent"`
		Labels struct {
			Nodes []struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"labels"`
		Assignees struct {
			Nodes []struct {
				Login string `json:"login"`
			} `json:"nodes"`
		} `json:"assignees"`
		SubIssues struct {
			Nodes []struct {
				ID         string `json:"id"`
				Number     int    `json:"number"`
				Title      string `json:"title"`
				State      string `json:"state"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"nodes"`
		} `json:"subIssues"`
		BlockedBy struct {
			Nodes []struct {
				ID         string `json:"id"`
				Number     int    `json:"number"`
				Title      string `json:"title"`
				State      string `json:"state"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"nodes"`
		} `json:"blockedBy"`
		Blocking struct {
			Nodes []struct {
				ID         string `json:"id"`
				Number     int    `json:"number"`
				Title      string `json:"title"`
				State      string `json:"state"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"nodes"`
		} `json:"blocking"`
	}

	type envelope struct {
		Data struct {
			Repository map[string]*rawNode `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}
	// GraphQL may return partial data alongside errors (e.g., one alias 404s
	// while others succeed). Surface as a non-fatal warning by logging only
	// when there is no data at all.
	if len(env.Data.Repository) == 0 && len(env.Errors) > 0 {
		return nil, fmt.Errorf("batch fetch issues: %s", env.Errors[0].Message)
	}

	out := make(map[int]*types.Issue, len(ordered))
	for _, node := range env.Data.Repository {
		if node == nil || node.Number == 0 {
			continue
		}
		issue := &types.Issue{
			NodeID: node.ID,
			Number: node.Number,
			Title:  node.Title,
			Body:   node.Body,
			State:  node.State,
			Repo:   owner + "/" + repo,
			URL:    node.URL,
		}
		if node.Parent != nil && node.Parent.Number != 0 {
			issue.ParentIssueID = node.Parent.ID
			issue.ParentIssueNumber = node.Parent.Number
		}
		for _, l := range node.Labels.Nodes {
			issue.Labels = append(issue.Labels, l.Name)
		}
		for _, a := range node.Assignees.Nodes {
			issue.Assignees = append(issue.Assignees, a.Login)
		}
		for _, si := range node.SubIssues.Nodes {
			issue.SubIssues = append(issue.SubIssues, types.SubIssueRef{
				NodeID: si.ID,
				Number: si.Number,
				Title:  si.Title,
				State:  si.State,
				Repo:   si.Repository.NameWithOwner,
			})
		}
		issue.IsEpic = len(issue.SubIssues) > 0
		for _, b := range node.BlockedBy.Nodes {
			issue.BlockedBy = append(issue.BlockedBy, types.BlockingRef{
				NodeID: b.ID,
				Number: b.Number,
				Title:  b.Title,
				State:  b.State,
				Repo:   b.Repository.NameWithOwner,
			})
		}
		for _, b := range node.Blocking.Nodes {
			issue.Blocking = append(issue.Blocking, types.BlockingRef{
				NodeID: b.ID,
				Number: b.Number,
				Title:  b.Title,
				State:  b.State,
				Repo:   b.Repository.NameWithOwner,
			})
		}
		out[issue.Number] = issue
	}
	return out, nil
}

// ListIssues lists open issues for a repository.
// When labels is nil or empty, all open issues are returned (no label filter).
func (s *IssueService) ListIssues(ctx context.Context, owner, repo string, labels []string) ([]types.Issue, error) {
	// labels(first: 8) — issues rarely carry more than the canonical
	// type:/component:/priority:/size: set. Was 20 before #3587 follow-up;
	// paginated query at first: 100 makes labels-per-item the dominant
	// nested cost.
	type issueNode struct {
		ID     graphql.ID
		Number graphql.Int
		Title  graphql.String
		State  graphql.String
		URL    graphql.String
		Labels struct {
			Nodes []labelNode
		} `graphql:"labels(first: 8)"`
		Milestone struct {
			Title graphql.String
		}
	}

	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(repo),
	}

	var nodes []issueNode

	if len(labels) > 0 {
		// Filtered query — labels: $labels requires [String!]!
		var q struct {
			Repository struct {
				Issues struct {
					PageInfo pageInfo
					Nodes    []issueNode
				} `graphql:"issues(first: 100, states: OPEN, labels: $labels)"`
			} `graphql:"repository(owner: $owner, name: $name)"`
		}
		var labelArgs []graphql.String
		for _, l := range labels {
			labelArgs = append(labelArgs, graphql.String(l))
		}
		vars["labels"] = labelArgs
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		nodes = q.Repository.Issues.Nodes
	} else {
		// Unfiltered query — no labels parameter to avoid null [String!]! error
		var q struct {
			Repository struct {
				Issues struct {
					PageInfo pageInfo
					Nodes    []issueNode
				} `graphql:"issues(first: 100, states: OPEN)"`
			} `graphql:"repository(owner: $owner, name: $name)"`
		}
		if err := s.client.query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		nodes = q.Repository.Issues.Nodes
	}

	var issues []types.Issue
	for _, n := range nodes {
		issue := types.Issue{
			NodeID:    fmt.Sprintf("%v", n.ID),
			Number:    int(n.Number),
			Title:     string(n.Title),
			State:     string(n.State),
			Repo:      owner + "/" + repo,
			URL:       string(n.URL),
			Milestone: string(n.Milestone.Title),
		}
		for _, l := range n.Labels.Nodes {
			issue.Labels = append(issue.Labels, string(l.Name))
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

// CreateIssue creates a new issue in the given repository.
func (s *IssueService) CreateIssue(ctx context.Context, repoID, title, body string, labelIDs []string) (*types.Issue, error) {
	var m createIssueMutation

	labelGraphIDs := make([]graphql.ID, len(labelIDs))
	for i, id := range labelIDs {
		labelGraphIDs[i] = graphql.ID(id)
	}

	input := map[string]interface{}{
		"input": CreateIssueInput{
			RepositoryID: graphql.ID(repoID),
			Title:        graphql.String(title),
			Body:         graphql.String(body),
			LabelIds:     labelGraphIDs,
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	return &types.Issue{
		NodeID: fmt.Sprintf("%v", m.CreateIssue.Issue.ID),
		Number: int(m.CreateIssue.Issue.Number),
		URL:    string(m.CreateIssue.Issue.URL),
		Title:  title,
	}, nil
}

// CloseIssue closes an issue by node ID.
func (s *IssueService) CloseIssue(ctx context.Context, issueID string) error {
	var m closeIssueMutation
	input := map[string]interface{}{
		"input": CloseIssueInput{
			IssueID: graphql.ID(issueID),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("close issue: %w", err)
	}
	return nil
}

// ReopenIssue reopens a closed issue by node ID.
func (s *IssueService) ReopenIssue(ctx context.Context, issueID string) error {
	var m reopenIssueMutation
	input := map[string]interface{}{
		"input": ReopenIssueInput{
			IssueID: graphql.ID(issueID),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("reopen issue: %w", err)
	}
	return nil
}

// AddComment adds a comment to an issue or PR by node ID.
func (s *IssueService) AddComment(ctx context.Context, subjectID, body string) error {
	var m addCommentMutation
	input := map[string]interface{}{
		"input": AddCommentInput{
			SubjectID: graphql.ID(subjectID),
			Body:      graphql.String(body),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("add comment: %w", err)
	}
	return nil
}

// AddSubIssue links a sub-issue to a parent issue using GitHub's native sub-issue API.
func (s *IssueService) AddSubIssue(ctx context.Context, parentID, childID string) error {
	var m addSubIssueMutation
	input := map[string]interface{}{
		"input": AddSubIssueInput{
			IssueID:    graphql.ID(parentID),
			SubIssueID: graphql.ID(childID),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("add sub-issue: %w", err)
	}
	return nil
}

// RemoveSubIssue unlinks a sub-issue from its parent.
func (s *IssueService) RemoveSubIssue(ctx context.Context, parentID, childID string) error {
	var m removeSubIssueMutation
	input := map[string]interface{}{
		"input": RemoveSubIssueInput{
			IssueID:    graphql.ID(parentID),
			SubIssueID: graphql.ID(childID),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("remove sub-issue: %w", err)
	}
	return nil
}

// AddBlockedBy adds a blocking relationship between issues.
func (s *IssueService) AddBlockedBy(ctx context.Context, blockedID, blockerID string) error {
	var m addBlockedByMutation
	input := map[string]interface{}{
		"input": AddBlockedByInput{
			IssueID:         graphql.ID(blockedID),
			BlockingIssueID: graphql.ID(blockerID),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("add blockedBy: %w", err)
	}
	return nil
}

// RemoveBlockedBy removes a blocking relationship between issues.
func (s *IssueService) RemoveBlockedBy(ctx context.Context, blockedID, blockerID string) error {
	var m removeBlockedByMutation
	input := map[string]interface{}{
		"input": RemoveBlockedByInput{
			IssueID:         graphql.ID(blockedID),
			BlockingIssueID: graphql.ID(blockerID),
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("remove blockedBy: %w", err)
	}
	return nil
}

// AddLabels adds labels to an issue by node IDs.
func (s *IssueService) AddLabels(ctx context.Context, issueID string, labelIDs []string) error {
	var m addLabelsMutation
	gqlLabelIDs := make([]graphql.ID, len(labelIDs))
	for i, id := range labelIDs {
		gqlLabelIDs[i] = graphql.ID(id)
	}

	input := map[string]interface{}{
		"input": AddLabelsToLabelableInput{
			LabelableID: graphql.ID(issueID),
			LabelIDs:    gqlLabelIDs,
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("add labels: %w", err)
	}
	return nil
}

// RemoveLabels removes labels from an issue by node IDs.
func (s *IssueService) RemoveLabels(ctx context.Context, issueID string, labelIDs []string) error {
	var m removeLabelsMutation
	gqlLabelIDs := make([]graphql.ID, len(labelIDs))
	for i, id := range labelIDs {
		gqlLabelIDs[i] = graphql.ID(id)
	}

	input := map[string]interface{}{
		"input": RemoveLabelsFromLabelableInput{
			LabelableID: graphql.ID(issueID),
			LabelIDs:    gqlLabelIDs,
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return fmt.Errorf("remove labels: %w", err)
	}
	return nil
}

// SyncStatusLabel atomically swaps the status label on an issue.
// Removes all status:* labels and adds the specified one.
func (s *IssueService) SyncStatusLabel(ctx context.Context, owner, repo string, number int, newStatus string) error {
	issue, err := s.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return err
	}

	// Get all label node IDs for the repo
	repoLabels, err := s.GetRepoLabels(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("fetch repo labels: %w", err)
	}

	// Find status:* label IDs to remove
	var removeIDs []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "status:") {
			if id, ok := repoLabels[label]; ok {
				removeIDs = append(removeIDs, id)
			}
		}
	}

	// Remove old status labels
	if len(removeIDs) > 0 {
		if err := s.RemoveLabels(ctx, issue.NodeID, removeIDs); err != nil {
			return fmt.Errorf("remove old status labels: %w", err)
		}
	}

	// Add new status label
	newLabel := "status:" + newStatus
	if id, ok := repoLabels[newLabel]; ok {
		if err := s.AddLabels(ctx, issue.NodeID, []string{id}); err != nil {
			return fmt.Errorf("add new status label: %w", err)
		}
	} else {
		return fmt.Errorf("label %q not found in repo %s/%s", newLabel, owner, repo)
	}

	return nil
}

// CreateSubIssue creates a new issue and links it as a sub-issue to the parent.
// If projectSvc is non-nil, the new issue is also added to the project board and
// its labels are synced to board fields. Board sync failure is non-fatal: the
// returned error wraps the sync error but the issue and link are preserved.
func (s *IssueService) CreateSubIssue(ctx context.Context, owner, repo string, parentNumber int, title, body string, labelIDs []string, projectSvc *ProjectService) (*types.Issue, error) {
	// Get repo ID for issue creation
	repoID, err := s.client.GetRepositoryID(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	// Add "Part of #N" to body
	bodyWithRef := body
	if bodyWithRef != "" {
		bodyWithRef += "\n\n"
	}
	bodyWithRef += fmt.Sprintf("Part of #%d", parentNumber)

	// Create the issue
	newIssue, err := s.CreateIssue(ctx, repoID, title, bodyWithRef, labelIDs)
	if err != nil {
		return nil, err
	}

	// Get parent issue node ID
	parent, err := s.GetIssue(ctx, owner, repo, parentNumber)
	if err != nil {
		return newIssue, fmt.Errorf("issue created (#%d) but failed to fetch parent: %w", newIssue.Number, err)
	}

	// Link as sub-issue
	if err := s.AddSubIssue(ctx, parent.NodeID, newIssue.NodeID); err != nil {
		return newIssue, fmt.Errorf("issue created (#%d) but failed to link: %w", newIssue.Number, err)
	}

	// Board sync (best-effort)
	if projectSvc != nil {
		if _, syncErr := projectSvc.AddIssueByNumber(ctx, owner, repo, newIssue.Number); syncErr != nil {
			return newIssue, fmt.Errorf("issue #%d created and linked but board sync failed: %w", newIssue.Number, syncErr)
		}
	}

	return newIssue, nil
}

// LinkSubIssue links an existing issue as a sub-issue of a parent.
func (s *IssueService) LinkSubIssue(ctx context.Context, owner, repo string, parentNumber, childNumber int) error {
	parent, err := s.GetIssue(ctx, owner, repo, parentNumber)
	if err != nil {
		return fmt.Errorf("fetch parent #%d: %w", parentNumber, err)
	}

	child, err := s.GetIssue(ctx, owner, repo, childNumber)
	if err != nil {
		return fmt.Errorf("fetch child #%d: %w", childNumber, err)
	}

	return s.AddSubIssue(ctx, parent.NodeID, child.NodeID)
}

// ListIssuesExcludingLabels lists open issues that do NOT have any of the given labels.
// Issues are fetched without a label filter (up to 100 per call), then filtered client-side.
// Note: only the first 100 open issues are fetched; repos with more than 100 open issues
// may return incomplete results when the pre-filter population exceeds this cap.
// limit caps the number of returned results after filtering (0 = no limit).
func (s *IssueService) ListIssuesExcludingLabels(ctx context.Context, owner, repo string, excludeLabels []string, limit int) ([]UnrefinedIssue, error) {
	// labels(first: 8) — caller filters by presence of label names; rare to
	// have >8 labels on a single issue. See #3587 follow-up for rationale.
	type issueNode struct {
		Number    graphql.Int
		Title     graphql.String
		CreatedAt graphql.String
		Labels    struct {
			Nodes []labelNode
		} `graphql:"labels(first: 8)"`
	}

	var q struct {
		Repository struct {
			Issues struct {
				PageInfo pageInfo
				Nodes    []issueNode
			} `graphql:"issues(first: 100, states: OPEN)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(repo),
	}

	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	excludeSet := make(map[string]bool, len(excludeLabels))
	for _, l := range excludeLabels {
		excludeSet[l] = true
	}

	var results []UnrefinedIssue
	for _, n := range q.Repository.Issues.Nodes {
		labels := make([]string, 0, len(n.Labels.Nodes))
		for _, l := range n.Labels.Nodes {
			labels = append(labels, string(l.Name))
		}

		skip := false
		for _, l := range labels {
			if excludeSet[l] {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		results = append(results, UnrefinedIssue{
			Number:    int(n.Number),
			Title:     string(n.Title),
			Labels:    labels,
			CreatedAt: string(n.CreatedAt),
		})

		if limit > 0 && len(results) >= limit {
			break
		}
	}

	return results, nil
}

// HasLabel reports whether a specific issue has a given label by name.
func (s *IssueService) HasLabel(ctx context.Context, owner, repo string, number int, label string) (bool, error) {
	issue, err := s.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return false, err
	}
	for _, l := range issue.Labels {
		if l == label {
			return true, nil
		}
	}
	return false, nil
}

// MarkRefined adds the pipeline:refined label to an issue.
// Idempotent: GitHub's addLabelsToLabelable mutation silently ignores duplicate label additions,
// so calling this on an already-refined issue is safe and produces no error.
func (s *IssueService) MarkRefined(ctx context.Context, owner, repo string, number int) error {
	issue, err := s.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return err
	}

	repoLabels, err := s.GetRepoLabels(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("fetch repo labels: %w", err)
	}

	labelID, ok := repoLabels[LabelRefined]
	if !ok {
		return fmt.Errorf("label %q not found in repo %s/%s — run repo-init to create labels", LabelRefined, owner, repo)
	}

	return s.AddLabels(ctx, issue.NodeID, []string{labelID})
}

// GetRepoLabels fetches all labels for a repo, returning name → nodeID map.
// Results are cached per "owner/repo" key for the lifetime of the IssueService
// instance. Labels rarely change, so this avoids a redundant API call on every
// SyncStatusLabel / MarkRefined invocation.
func (s *IssueService) GetRepoLabels(ctx context.Context, owner, repo string) (map[string]string, error) {
	cacheKey := owner + "/" + repo
	if cached, ok := s.repoLabelsCache[cacheKey]; ok {
		return cached, nil
	}

	var q struct {
		Repository struct {
			Labels struct {
				Nodes []struct {
					ID   graphql.ID
					Name graphql.String
				}
			} `graphql:"labels(first: 100)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(repo),
	}

	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, err
	}

	labels := make(map[string]string, len(q.Repository.Labels.Nodes))
	for _, l := range q.Repository.Labels.Nodes {
		labels[string(l.Name)] = fmt.Sprintf("%v", l.ID)
	}
	s.repoLabelsCache[cacheKey] = labels
	return labels, nil
}

// SearchIssues searches issues by keyword using GitHub's top-level search query.
// The query is scoped to the given owner/repo and open issues. Results are capped
// by limit (max 100, GitHub API enforced). The query string is treated as keywords —
// GitHub search qualifiers in the query may override the built-in repo/state scope.
func (s *IssueService) SearchIssues(ctx context.Context, owner, repo, query string, limit int) ([]types.Issue, error) {
	if limit <= 0 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}
	fullQuery := fmt.Sprintf("type:issue repo:%s/%s is:open %s", owner, repo, query)

	var q searchIssuesQuery
	vars := map[string]interface{}{
		"q":     graphql.String(fullQuery),
		"limit": graphql.Int(limit),
	}

	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	var issues []types.Issue
	for _, n := range q.Search.Nodes {
		if n.TypeName != "Issue" {
			continue
		}
		issue := types.Issue{
			NodeID: fmt.Sprintf("%v", n.ID),
			Number: int(n.Number),
			Title:  string(n.Title),
			State:  string(n.State),
			Repo:   string(n.Repository.NameWithOwner),
			URL:    string(n.URL),
		}
		for _, l := range n.Labels.Nodes {
			issue.Labels = append(issue.Labels, string(l.Name))
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

// EditIssue updates an issue's body by node ID (GraphQL node ID from GetIssue,
// not an issue number) using the updateIssue mutation.
func (s *IssueService) EditIssue(ctx context.Context, nodeID, body string) (*types.Issue, error) {
	if nodIDErr := requireNodeID(nodeID, "edit issue"); nodIDErr != nil {
		return nil, nodIDErr
	}
	b := graphql.String(body)
	var m updateIssueMutation
	input := map[string]interface{}{
		"input": UpdateIssueInput{
			ID:   graphql.ID(nodeID),
			Body: &b,
		},
	}

	if err := s.client.mutate(ctx, &m, input); err != nil {
		return nil, fmt.Errorf("edit issue: %w", err)
	}

	return &types.Issue{
		NodeID: fmt.Sprintf("%v", m.UpdateIssue.Issue.ID),
		Number: int(m.UpdateIssue.Issue.Number),
		Title:  string(m.UpdateIssue.Issue.Title),
		Body:   string(m.UpdateIssue.Issue.Body),
	}, nil
}

// requireNodeID returns a sentinel-style error when nodeID is empty.
func requireNodeID(nodeID, op string) error {
	if nodeID == "" {
		return fmt.Errorf("%s: nodeID is required", op)
	}
	return nil
}

// UpdateIssue patches the documented attributes of an issue identified by
// node ID. Title/Body/MilestoneID/LabelIDs/AssigneeIDs flow through the
// updateIssue GraphQL mutation in a single round-trip; State changes are
// dispatched through closeIssue/reopenIssue because GitHub's updateIssue
// rejects the state argument when other fields are also patched.
//
// Forge-agnostic state values are normalised:
//   - "opened" / "OPEN"   → reopenIssue
//   - "closed" / "CLOSED" → closeIssue
//
// Labels and Assignees in the forge.UpdateIssueOptions are GitHub node IDs
// (LabelIDs / AssigneeIDs); the github adapter does not perform name → ID
// resolution here. Callers needing label-by-name should look up the IDs
// via GetRepoLabels and pass the resulting node IDs.
func (s *IssueService) UpdateIssue(ctx context.Context, nodeID string, opts forge.UpdateIssueOptions) (*types.Issue, error) {
	if err := requireNodeID(nodeID, "update issue"); err != nil {
		return nil, err
	}

	in := UpdateIssueInput{ID: graphql.ID(nodeID)}
	hasField := false
	if opts.Title != nil {
		t := graphql.String(*opts.Title)
		in.Title = &t
		hasField = true
	}
	if opts.Body != nil {
		b := graphql.String(*opts.Body)
		in.Body = &b
		hasField = true
	}
	if opts.Labels != nil {
		ids := make([]graphql.ID, 0, len(*opts.Labels))
		for _, l := range *opts.Labels {
			ids = append(ids, graphql.ID(l))
		}
		in.LabelIDs = &ids
		hasField = true
	}
	if opts.Assignees != nil {
		ids := make([]graphql.ID, 0, len(*opts.Assignees))
		for _, a := range *opts.Assignees {
			ids = append(ids, graphql.ID(a))
		}
		in.AssigneeIDs = &ids
		hasField = true
	}
	if opts.Milestone != nil && *opts.Milestone != "" {
		mid := graphql.ID(*opts.Milestone)
		in.MilestoneID = &mid
		hasField = true
	}

	var result *types.Issue
	if hasField {
		var m updateIssueMutation
		input := map[string]interface{}{"input": in}
		if err := s.client.mutate(ctx, &m, input); err != nil {
			return nil, fmt.Errorf("update issue: %w", err)
		}
		result = &types.Issue{
			NodeID: fmt.Sprintf("%v", m.UpdateIssue.Issue.ID),
			Number: int(m.UpdateIssue.Issue.Number),
			Title:  string(m.UpdateIssue.Issue.Title),
			Body:   string(m.UpdateIssue.Issue.Body),
			State:  string(m.UpdateIssue.Issue.State),
		}
	}

	if opts.State != nil {
		switch strings.ToLower(*opts.State) {
		case "opened", "open":
			if err := s.ReopenIssue(ctx, nodeID); err != nil {
				return nil, fmt.Errorf("update issue (reopen): %w", err)
			}
			if result != nil {
				result.State = "OPEN"
			}
		case "closed", "close":
			if err := s.CloseIssue(ctx, nodeID); err != nil {
				return nil, fmt.Errorf("update issue (close): %w", err)
			}
			if result != nil {
				result.State = "CLOSED"
			}
		default:
			return nil, fmt.Errorf("update issue: unknown state %q (want opened|closed)", *opts.State)
		}
	}

	if result == nil {
		// Caller passed only State; return a minimal Issue with NodeID so
		// the contract method has a non-nil return on success.
		state := ""
		if opts.State != nil {
			state = strings.ToUpper(*opts.State)
			if state == "OPENED" {
				state = "OPEN"
			}
		}
		result = &types.Issue{NodeID: nodeID, State: state}
	}
	return result, nil
}

// IterateIssues returns an iterator over the issues matching the given
// labels. The current implementation is slice-backed: it eagerly fetches
// the result set via ListIssues and yields entries one at a time. Cursor-
// driven streaming will replace this in a follow-up — the surface stays
// the same.
func (s *IssueService) IterateIssues(ctx context.Context, owner, repo string, labels []string) forge.Iterator[types.Issue] {
	issues, err := s.ListIssues(ctx, owner, repo, labels)
	return newSliceIterator(issues, err)
}

// GetEpicProgress fetches an epic by node ID and aggregates sub-issue progress
// across repos using the node query (cross-repo safe).
func (s *IssueService) GetEpicProgress(ctx context.Context, epicNodeID string) (*types.EpicProgress, error) {
	var q nodeQuery
	vars := map[string]interface{}{
		"id": graphql.ID(epicNodeID),
	}

	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("fetch epic node: %w", err)
	}

	if q.Node.TypeName != "Issue" {
		return nil, fmt.Errorf("node %s is not an Issue (got %s)", epicNodeID, q.Node.TypeName)
	}

	epic := &types.EpicProgress{
		EpicNodeID: epicNodeID,
		Number:     int(q.Node.Issue.Number),
		Title:      string(q.Node.Issue.Title),
		Repo:       string(q.Node.Issue.Repository.NameWithOwner),
	}

	for _, si := range q.Node.Issue.SubIssues.Nodes {
		ref := types.SubIssueRef{
			NodeID: fmt.Sprintf("%v", si.ID),
			Number: int(si.Number),
			Title:  string(si.Title),
			State:  string(si.State),
			Repo:   string(si.Repository.NameWithOwner),
		}
		for _, l := range si.Labels.Nodes {
			ref.Labels = append(ref.Labels, string(l.Name))
		}
		epic.SubIssues = append(epic.SubIssues, ref)
		epic.Total++
		if strings.EqualFold(ref.State, "CLOSED") {
			epic.Closed++
		} else {
			epic.Open++
		}
	}

	if epic.Total > 0 {
		epic.PercentComplete = float64(epic.Closed) / float64(epic.Total) * 100
	}

	return epic, nil
}

// GetEpicProgressByNumber fetches an epic by owner/repo/number and returns progress.
func (s *IssueService) GetEpicProgressByNumber(ctx context.Context, owner, repo string, number int) (*types.EpicProgress, error) {
	issue, err := s.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}

	epic := &types.EpicProgress{
		EpicNodeID: issue.NodeID,
		Number:     issue.Number,
		Title:      issue.Title,
		Repo:       issue.Repo,
	}

	for _, si := range issue.SubIssues {
		epic.SubIssues = append(epic.SubIssues, si)
		epic.Total++
		if strings.EqualFold(si.State, "CLOSED") {
			epic.Closed++
		} else {
			epic.Open++
		}
	}

	if epic.Total > 0 {
		epic.PercentComplete = float64(epic.Closed) / float64(epic.Total) * 100
	}

	return epic, nil
}
