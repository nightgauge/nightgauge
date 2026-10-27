package github

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/shurcooL/graphql"
)

// EpicService provides epic-level operations: completion checking, summary generation,
// and closed-to-done board synchronization.
type EpicService struct {
	client *Client
	// checkout resolves the epic repository's checkout so every close can
	// integrate the epic branch first (#2560). prs overrides the PR service
	// in tests.
	checkout EpicCheckoutResolver
	prs      epicPRMerger
}

// NewEpicService creates an epic service.
func NewEpicService(client *Client) *EpicService {
	return &EpicService{client: client}
}

// EpicValidationGap categorizes a single validation finding.
//
// SubIssueRepo and BlockerRepo name the repository each number lives in,
// "owner/name". Issue numbers are per repository and an epic's sub-issues and
// their blockers can live in several, so a repair that acts on the numbers
// alone, in the epic's repository, can remove a relationship of an unrelated
// issue (#2369 review).
type EpicValidationGap struct {
	SubIssueNumber int    `json:"subIssueNumber"`
	SubIssueRepo   string `json:"subIssueRepo"`
	SubIssueTitle  string `json:"subIssueTitle"`
	GapType        string `json:"gapType"` // "circular_blocker" | "stale_blocker"
	BlockerNumber  int    `json:"blockerNumber,omitempty"`
	BlockerRepo    string `json:"blockerRepo,omitempty"`
	Detail         string `json:"detail"`
}

// EpicValidationFailure is a sub-issue EpicService.Validate could not check,
// because the sub-issue or its blocker list could not be read.
type EpicValidationFailure struct {
	SubIssueNumber int    `json:"subIssueNumber"`
	SubIssueRepo   string `json:"subIssueRepo"`
	SubIssueTitle  string `json:"subIssueTitle"`
	Error          string `json:"error"`
}

// EpicValidationResult is returned by EpicService.Validate.
//
// TotalSubIssues counts the sub-issues the epic lists; CheckedSubIssues the
// ones read and checked, and FailedSubIssues the ones that could not be, each
// named in Failures. The two add up to TotalSubIssues. Valid holds only when
// every sub-issue was checked and none has a gap: a gap on a sub-issue that
// was never read is not absent, only unseen.
type EpicValidationResult struct {
	EpicNumber       int                     `json:"epicNumber"`
	Title            string                  `json:"title"`
	Repo             string                  `json:"repo"`
	TotalSubIssues   int                     `json:"totalSubIssues"`
	CheckedSubIssues int                     `json:"checkedSubIssues"`
	FailedSubIssues  int                     `json:"failedSubIssues"`
	Valid            bool                    `json:"valid"`
	Gaps             []EpicValidationGap     `json:"gaps"`
	Failures         []EpicValidationFailure `json:"failures"`
}

// blockerIsEpic reports whether blocker, on a sub-issue living in subRepo, is
// the epic epicRepo#epicNumber itself. The repository counts as well as the
// number (#2369): a sub-issue in another repository can be blocked by an
// unrelated issue that only shares the epic's number, and reporting that as a
// circular blocker sends the issue-audit repair (remove-blocked-by) after a
// legitimate relationship. An empty blocker Repo means the sub-issue's own.
func blockerIsEpic(blocker types.BlockingRef, subRepo, epicRepo string, epicNumber int) bool {
	if blocker.Number != epicNumber {
		return false
	}
	blockerRepo := blocker.Repo
	if blockerRepo == "" {
		blockerRepo = subRepo
	}
	return strings.EqualFold(blockerRepo, epicRepo)
}

// Validate checks an epic's sub-issue structure for circular blockers (sub-issue
// blocked by its own parent epic) and stale blockers (blocked by a closed issue).
// Sub-issues are fetched in one batched GraphQL request per repository.
//
// The check uses the epic's sub-issue list and each sub-issue's blocker list,
// so it reads those whole and no other list: a sub-issue that blocks many
// siblings, or has sub-issues of its own, cannot fail the validation.
//
// An epic whose sub-issue list cannot be read is an error. A sub-issue that
// cannot be read, or whose blocker list cannot be read whole, is not checked:
// it is recorded in Failures, the rest are still checked, and the result is
// not Valid.
func (e *EpicService) Validate(ctx context.Context, owner, repo string, epicNumber int) (*EpicValidationResult, error) {
	issueSvc := NewIssueService(e.client)
	epic, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, RelationSubIssues)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	result := &EpicValidationResult{
		EpicNumber:     epic.Number,
		Title:          epic.Title,
		Repo:           epic.Repo,
		TotalSubIssues: len(epic.SubIssues),
		Gaps:           []EpicValidationGap{},
		Failures:       []EpicValidationFailure{},
	}

	// Group sub-issue numbers by the repo they live in. Cross-repo epics fan
	// out into one batched request per repo.
	byRepo := make(map[string][]int)
	repoOwnerName := make(map[string][2]string)
	for _, si := range epic.SubIssues {
		// When SubIssueRef.Repo is empty, fall back to the parent epic's repo.
		repoKey := si.Repo
		var siOwner, siRepo string
		if repoKey == "" {
			siOwner, siRepo = owner, repo
			repoKey = owner + "/" + repo
		} else {
			siOwner, siRepo = splitOwnerRepo(repoKey)
		}
		byRepo[repoKey] = append(byRepo[repoKey], si.Number)
		repoOwnerName[repoKey] = [2]string{siOwner, siRepo}
	}

	// Fetch all sub-issues in batched requests (one per repo), keyed by
	// repository and number: two sub-issues in different repositories can
	// share a number, and keyed by number alone one would be judged by the
	// other's blockers (#2369 review).
	//
	// A batch that fails leaves every sub-issue in it unchecked, and so does
	// one the batch does not return; readErrs keeps each repository's error
	// to name in their failures. A batch fails whole when any one of its
	// sub-issues has a blocker list that cannot be read whole.
	fetched := make(map[string]*types.Issue, len(epic.SubIssues))
	readErrs := make(map[string]error)
	subKey := func(repo string, number int) string {
		return strings.ToLower(repo) + "#" + strconv.Itoa(number)
	}
	for repoKey, numbers := range byRepo {
		on := repoOwnerName[repoKey]
		issues, err := issueSvc.getIssuesByNumbers(ctx, on[0], on[1], numbers, RelationBlockedBy)
		if err != nil {
			readErrs[strings.ToLower(repoKey)] = err
			continue
		}
		for n, iss := range issues {
			fetched[subKey(repoKey, n)] = iss
		}
	}

	epicRepo := owner + "/" + repo
	// ref names an issue in a gap's detail: "#N" in the epic's repository,
	// "owner/repo#N" in any other.
	ref := func(repo string, number int) string {
		if strings.EqualFold(repo, epicRepo) {
			return "#" + strconv.Itoa(number)
		}
		return repo + "#" + strconv.Itoa(number)
	}
	for _, si := range epic.SubIssues {
		subRepo := si.Repo
		if subRepo == "" {
			subRepo = epicRepo
		}
		subIssue, ok := fetched[subKey(subRepo, si.Number)]
		if !ok {
			reason := fmt.Sprintf("the read of %s returned no issue %s", subRepo, ref(subRepo, si.Number))
			if err := readErrs[strings.ToLower(subRepo)]; err != nil {
				reason = fmt.Sprintf("read sub-issues of %s: %v", subRepo, err)
			}
			result.Failures = append(result.Failures, EpicValidationFailure{
				SubIssueNumber: si.Number,
				SubIssueRepo:   subRepo,
				SubIssueTitle:  si.Title,
				Error:          reason,
			})
			continue
		}
		result.CheckedSubIssues++
		for _, blocker := range subIssue.BlockedBy {
			blockerRepo := blocker.Repo
			if blockerRepo == "" {
				blockerRepo = subRepo
			}
			if blockerIsEpic(blocker, subRepo, epicRepo, epicNumber) {
				result.Gaps = append(result.Gaps, EpicValidationGap{
					SubIssueNumber: si.Number,
					SubIssueRepo:   subRepo,
					SubIssueTitle:  si.Title,
					GapType:        "circular_blocker",
					BlockerNumber:  blocker.Number,
					BlockerRepo:    blockerRepo,
					Detail: fmt.Sprintf("sub-issue %s is blocked by its own epic %s",
						ref(subRepo, si.Number), ref(epicRepo, epicNumber)),
				})
			} else if strings.EqualFold(blocker.State, "CLOSED") {
				result.Gaps = append(result.Gaps, EpicValidationGap{
					SubIssueNumber: si.Number,
					SubIssueRepo:   subRepo,
					SubIssueTitle:  si.Title,
					GapType:        "stale_blocker",
					BlockerNumber:  blocker.Number,
					BlockerRepo:    blockerRepo,
					Detail: fmt.Sprintf("sub-issue %s blocked by closed issue %s",
						ref(subRepo, si.Number), ref(blockerRepo, blocker.Number)),
				})
			}
		}
	}

	result.FailedSubIssues = len(result.Failures)
	result.Valid = len(result.Gaps) == 0 && len(result.Failures) == 0
	return result, nil
}

// EpicCompletionResult holds the result of an epic completion check.
type EpicCompletionResult struct {
	EpicNumber int    `json:"epicNumber"`
	Title      string `json:"title"`
	Repo       string `json:"repo"`
	Total      int    `json:"total"`
	Closed     int    `json:"closed"`
	Open       int    `json:"open"`
	Complete   bool   `json:"complete"`
	OpenIssues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"openIssues,omitempty"`
	// SubIssues is the epic's full sub-issue list, each carrying its own
	// repository. It is the single membership record both epic paths read
	// (#1181): the nightly sweep and the post-merge hook now answer "does this
	// epic have sub-issues, and is issue X one of them?" from the same struct,
	// so they cannot disagree the way they did when the hook looked the epic up
	// in the wrong repository.
	SubIssues []types.SubIssueRef `json:"subIssues,omitempty"`
	// PendingTasks lists the epic body's unchecked task-list items that no
	// closed sub-issue satisfies (#1581). A filed child being closed does not
	// make an epic complete while planned-but-unfiled work or unticked
	// acceptance criteria remain in its body.
	PendingTasks []string `json:"pendingTasks,omitempty"`
}

// HasSubIssue reports whether the epic lists owner/repo#number among its
// sub-issues. An empty repo matches on number alone (callers that genuinely do
// not know the sub-issue's repository); a non-empty repo must match
// case-insensitively, because a number alone is not an identity across repos.
func (r *EpicCompletionResult) HasSubIssue(repo string, number int) bool {
	if r == nil {
		return false
	}
	for _, si := range r.SubIssues {
		if si.Number != number {
			continue
		}
		if repo == "" || si.Repo == "" || strings.EqualFold(si.Repo, repo) {
			return true
		}
	}
	return false
}

// CheckCompletion checks if all sub-issues of an epic are closed.
// Uses a single GraphQL query (no N+1). Only the sub-issue list is read
// whole, so a long blocking list on the epic cannot fail the rollup.
func (e *EpicService) CheckCompletion(ctx context.Context, owner, repo string, epicNumber int) (*EpicCompletionResult, error) {
	issueSvc := NewIssueService(e.client)
	issue, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, RelationSubIssues)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	result := &EpicCompletionResult{
		EpicNumber: issue.Number,
		Title:      issue.Title,
		Repo:       issue.Repo,
		Total:      len(issue.SubIssues),
	}

	result.SubIssues = append(result.SubIssues, issue.SubIssues...)

	for _, si := range issue.SubIssues {
		if strings.EqualFold(si.State, "CLOSED") {
			result.Closed++
		} else {
			result.Open++
			result.OpenIssues = append(result.OpenIssues, struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
			}{si.Number, si.Title})
		}
	}

	result.PendingTasks = pendingBodyTasks(issue.Body, issue.Repo, issue.SubIssues)
	result.Complete = result.Open == 0 && result.Total > 0 && len(result.PendingTasks) == 0
	return result, nil
}

// reasonUnfinishedDecomposition is closeOneEpic's skip reason when every filed
// sub-issue is closed but the epic body still carries pending task-list items
// (#1581).
const reasonUnfinishedDecomposition = "unfinished_decomposition"

var (
	taskListItemRe = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+\[([ xX])\]\s+(.*)$`)
	issueRefRe     = regexp.MustCompile(`(?:^|[^\w/])(?:([\w.-]+/[\w.-]+))?#(\d+)\b`)
	fenceRe        = regexp.MustCompile("^\\s*(```|~~~)")
)

// pendingBodyTasks returns the text of every unchecked task-list item in an
// epic body that is not satisfied by a closed sub-issue (#1581). An unchecked
// item referencing issues is satisfied when each referenced issue is a CLOSED
// sub-issue (GitHub never ticks body boxes itself); any other unchecked item
// is outstanding work. Checked items and lines inside fenced code blocks are
// ignored. epicRepo ("owner/repo") resolves bare #N references.
func pendingBodyTasks(body, epicRepo string, subs []types.SubIssueRef) []string {
	var pending []string
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := taskListItemRe.FindStringSubmatch(line)
		if m == nil || m[1] != " " {
			continue
		}
		text := strings.TrimSpace(m[2])
		refs := issueRefRe.FindAllStringSubmatch(text, -1)
		if len(refs) > 0 && allRefsClosedSubs(refs, epicRepo, subs) {
			continue
		}
		pending = append(pending, text)
	}
	return pending
}

func allRefsClosedSubs(refs [][]string, epicRepo string, subs []types.SubIssueRef) bool {
	for _, ref := range refs {
		repo := ref[1]
		if repo == "" {
			repo = epicRepo
		}
		n, err := strconv.Atoi(ref[2])
		if err != nil {
			return false
		}
		found := false
		for _, si := range subs {
			siRepo := si.Repo
			if siRepo == "" {
				siRepo = epicRepo
			}
			if si.Number == n && strings.EqualFold(siRepo, repo) && strings.EqualFold(si.State, "CLOSED") {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// SweepEpics checks completion for all open epics in a repo concurrently.
// Individual epic check failures are logged to stderr and skipped rather than
// aborting the entire sweep — only a total failure (all epics errored) returns
// a non-nil error.
func (e *EpicService) SweepEpics(ctx context.Context, owner, repo string) ([]EpicCompletionResult, error) {
	issueSvc := NewIssueService(e.client)
	issues, err := issueSvc.ListIssues(ctx, owner, repo, []string{"type:epic"})
	if err != nil {
		return nil, fmt.Errorf("list epics: %w", err)
	}

	type checkResult struct {
		idx    int
		result *EpicCompletionResult
		err    error
	}
	ch := make(chan checkResult, len(issues))
	var wg sync.WaitGroup

	for i, issue := range issues {
		wg.Add(1)
		go func(idx int, num int) {
			defer wg.Done()
			result, err := e.CheckCompletion(ctx, owner, repo, num)
			ch <- checkResult{idx, result, err}
		}(i, issue.Number)
	}

	wg.Wait()
	close(ch)

	results := make([]EpicCompletionResult, len(issues))
	var errCount int
	for res := range ch {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Warning: epic #%d check failed: %v\n", issues[res.idx].Number, res.err)
			errCount++
		} else {
			results[res.idx] = *res.result
		}
	}

	// Only return error when every epic check failed; partial failures are logged above.
	if errCount > 0 && errCount == len(issues) {
		return nil, fmt.Errorf("all %d epic checks failed", len(issues))
	}

	// Filter out zero-value results (from failed goroutines or empty epics)
	var valid []EpicCompletionResult
	for _, r := range results {
		if r.Total > 0 || r.EpicNumber > 0 {
			valid = append(valid, r)
		}
	}

	return valid, nil
}

// EpicSummary holds a generated epic completion summary.
type EpicSummary struct {
	EpicNumber int      `json:"epicNumber"`
	Title      string   `json:"title"`
	Repo       string   `json:"repo"`
	Progress   float64  `json:"progress"`
	Total      int      `json:"total"`
	Closed     int      `json:"closed"`
	Open       int      `json:"open"`
	Tier       string   `json:"tier"` // "minimal", "standard", "detailed"
	Summary    string   `json:"summary"`
	Phases     []string `json:"phases,omitempty"`
}

// GenerateSummary creates a completion summary for an epic.
func (e *EpicService) GenerateSummary(ctx context.Context, owner, repo string, epicNumber int) (*EpicSummary, error) {
	issueSvc := NewIssueService(e.client)
	progress, err := issueSvc.GetEpicProgressByNumber(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch epic progress: %w", err)
	}

	summary := &EpicSummary{
		EpicNumber: progress.Number,
		Title:      progress.Title,
		Repo:       progress.Repo,
		Progress:   progress.PercentComplete / 100,
		Total:      progress.Total,
		Closed:     progress.Closed,
		Open:       progress.Open,
	}

	summary.Tier = classifyTier(summary.Total, summary.Progress)
	summary.Summary = buildSummaryText(summary)

	return summary, nil
}

// classifyTier determines the summary verbosity tier based on epic size and progress.
func classifyTier(total int, progress float64) string {
	if total <= 3 {
		return "minimal"
	}
	if total <= 10 || progress >= 0.8 {
		return "standard"
	}
	return "detailed"
}

// buildSummaryText generates human-readable summary text.
func buildSummaryText(s *EpicSummary) string {
	pct := s.Progress * 100
	if s.Progress >= 1.0 {
		return fmt.Sprintf("Epic #%d (%s) is complete — all %d sub-issues closed.", s.EpicNumber, s.Title, s.Total)
	}
	return fmt.Sprintf("Epic #%d (%s): %.0f%% complete (%d/%d sub-issues closed, %d remaining).",
		s.EpicNumber, s.Title, pct, s.Closed, s.Total, s.Open)
}

// SyncClosedToDone moves closed sub-issues to "Done" status on the project board.
// ownerType defaults to "org" when not provided (backward compatible).
func (e *EpicService) SyncClosedToDone(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...OwnerType) (int, error) {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	issueSvc := NewIssueService(e.client)
	issue, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, RelationSubIssues)
	if err != nil {
		return 0, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	// Find closed sub-issues and sync their board status. Status writes go
	// through ProjectService — the single board-writing path with one cached
	// field-ID fetch — instead of the deleted setProjectStatus, which
	// re-fetched project fields inline on every call (#61).
	projSvc := NewProjectService(e.client, owner, projectNumber, ot)
	var synced int
	for _, si := range issue.SubIssues {
		if !strings.EqualFold(si.State, "CLOSED") {
			continue
		}

		siOwner, siRepo := splitEpicOwnerRepo(si.Repo)
		itemID, err := findProjectItemID(ctx, e.client, owner, projectNumber, ot, siOwner, siRepo, si.Number)
		if err != nil {
			continue // Skip items not on the board
		}

		if err := projSvc.SetSingleSelectField(ctx, itemID, "Status", "Done"); err != nil {
			continue
		}
		synced++
	}

	return synced, nil
}

// findProjectItemID finds a project item by issue number (utility for epic service).
func findProjectItemID(ctx context.Context, client *Client, orgOwner string, projectNumber int, ownerType OwnerType, issueOwner, issueRepo string, issueNumber int) (string, error) {
	repoFull := issueOwner + "/" + issueRepo
	var cursor *graphql.String

	// Inner struct for both org and user query result nodes
	type itemNode struct {
		ID      graphql.String
		Content struct {
			TypeName string `graphql:"__typename"`
			Issue    struct {
				Number     graphql.Int
				Repository struct {
					NameWithOwner graphql.String
				}
			} `graphql:"... on Issue"`
		}
	}

	for {
		vars := map[string]interface{}{
			"owner":         graphql.String(orgOwner),
			"projectNumber": graphql.Int(projectNumber),
			"after":         cursor,
		}

		var nodes []itemNode
		var hasNextPage graphql.Boolean
		var endCursorVal graphql.String

		if ownerType.IsUser() {
			var q struct {
				User struct {
					ProjectV2 struct {
						Items struct {
							PageInfo pageInfo
							Nodes    []itemNode
						} `graphql:"items(first: 100, after: $after)"`
					} `graphql:"projectV2(number: $projectNumber)"`
				} `graphql:"user(login: $owner)"`
			}
			if err := client.query(ctx, &q, vars); err != nil {
				return "", err
			}
			nodes = q.User.ProjectV2.Items.Nodes
			hasNextPage = q.User.ProjectV2.Items.PageInfo.HasNextPage
			endCursorVal = q.User.ProjectV2.Items.PageInfo.EndCursor
		} else {
			var q struct {
				Organization struct {
					ProjectV2 struct {
						Items struct {
							PageInfo pageInfo
							Nodes    []itemNode
						} `graphql:"items(first: 100, after: $after)"`
					} `graphql:"projectV2(number: $projectNumber)"`
				} `graphql:"organization(login: $owner)"`
			}
			if err := client.query(ctx, &q, vars); err != nil {
				return "", err
			}
			nodes = q.Organization.ProjectV2.Items.Nodes
			hasNextPage = q.Organization.ProjectV2.Items.PageInfo.HasNextPage
			endCursorVal = q.Organization.ProjectV2.Items.PageInfo.EndCursor
		}

		for _, node := range nodes {
			if node.Content.TypeName == "Issue" &&
				int(node.Content.Issue.Number) == issueNumber &&
				string(node.Content.Issue.Repository.NameWithOwner) == repoFull {
				return string(node.ID), nil
			}
		}

		if !bool(hasNextPage) {
			break
		}
		cursor = &endCursorVal
	}

	return "", fmt.Errorf("issue #%d not found on project board", issueNumber)
}

// EpicTransitionResult holds the result of an epic status transition.
type EpicTransitionResult struct {
	EpicNumber    int    `json:"epicNumber"`
	NewStatus     string `json:"newStatus"`
	EpicSynced    bool   `json:"epicSynced"`
	SubIssueTotal int    `json:"subIssueTotal"`
	SubIssueMoved int    `json:"subIssueMoved"`
	Failures      []struct {
		Number int    `json:"number"`
		Error  string `json:"error"`
	} `json:"failures,omitempty"`
}

// TransitionStatus moves an epic and all its sub-issues to a new status on the project board.
// This is the deterministic function called when an epic is dragged between columns.
// It handles the full cascade: epic status + all sub-issue statuses in a single call.
// ownerType defaults to "org" when not provided (backward compatible).
func (e *EpicService) TransitionStatus(ctx context.Context, owner, repo string, epicNumber, projectNumber int, newStatus string, ownerType ...OwnerType) (*EpicTransitionResult, error) {
	ot := OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	_ = ot // used below
	issueSvc := NewIssueService(e.client)
	issue, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, RelationSubIssues)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	result := &EpicTransitionResult{
		EpicNumber:    epicNumber,
		NewStatus:     newStatus,
		SubIssueTotal: len(issue.SubIssues),
	}

	// Use ProjectService for field mutations — it handles field introspection and caching
	projSvc := NewProjectService(e.client, owner, projectNumber, ot)

	// Move the epic itself
	if err := projSvc.SyncStatus(ctx, owner, repo, epicNumber, newStatus); err != nil {
		return nil, fmt.Errorf("set epic #%d status: %w", epicNumber, err)
	}
	result.EpicSynced = true

	// Move all sub-issues concurrently
	type subResult struct {
		number int
		err    error
	}
	ch := make(chan subResult, len(issue.SubIssues))

	for _, si := range issue.SubIssues {
		go func(siNumber int, siRepo string) {
			siOwner, siRepoName := splitEpicOwnerRepo(siRepo)
			setErr := projSvc.SyncStatus(ctx, siOwner, siRepoName, siNumber, newStatus)
			ch <- subResult{siNumber, setErr}
		}(si.Number, si.Repo)
	}

	for range issue.SubIssues {
		r := <-ch
		if r.err != nil {
			result.Failures = append(result.Failures, struct {
				Number int    `json:"number"`
				Error  string `json:"error"`
			}{r.number, r.err.Error()})
		} else {
			result.SubIssueMoved++
		}
	}

	return result, nil
}

// EpicCompleteResult holds the result of the full epic completion flow.
type EpicCompleteResult struct {
	EpicNumber int  `json:"epicNumber"`
	Complete   bool `json:"complete"`
	Total      int  `json:"total"`
	Closed     int  `json:"closed"`
	Open       int  `json:"open"`
	// Action: "not_complete", "closed_and_merged", "already_merged",
	// "no_epic_branch", or "integration_<outcome>" when the epic stayed open.
	Action      string           `json:"action"`
	Integration *EpicIntegration `json:"integration,omitempty"`
	PRURL       string           `json:"prUrl,omitempty"`
	PRNumber    int              `json:"prNumber,omitempty"`
	Error       string           `json:"error,omitempty"`
	OpenIssues  []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"openIssues,omitempty"`
}

// CompleteEpic runs the full epic completion flow: check that every
// sub-issue is closed, integrate the epic branch into main (merge main into
// it, open or reuse the epic PR, merge it), and only then close the epic. A
// failed integration leaves the epic open (#2560). With no checkout at
// repoPath the epic is closed only when no branch can be inspected at all.
func (e *EpicService) CompleteEpic(ctx context.Context, owner, repo string, epicNumber int, repoPath string) (*EpicCompleteResult, error) {
	// epic/<N>-* names #N of the repository it was pushed to: in a checkout
	// of another repository it is that repository's #N's branch. Refuse such
	// a checkout before anything changes (#2388).
	if gitSvc, err := git.NewService(repoPath); err == nil {
		if err := gitSvc.RequireOriginRepo(owner + "/" + repo); err != nil {
			return nil, fmt.Errorf("epic complete %s/%s#%d: %w", owner, repo, epicNumber, err)
		}
		if e.checkout == nil {
			e.checkout = CheckoutResolverForPath(repoPath)
		}
	}

	check, err := e.CheckCompletion(ctx, owner, repo, epicNumber)
	if err != nil {
		return nil, err
	}

	result := &EpicCompleteResult{
		EpicNumber: epicNumber,
		Complete:   check.Complete,
		Total:      check.Total,
		Closed:     check.Closed,
		Open:       check.Open,
		OpenIssues: check.OpenIssues,
	}

	if !check.Complete {
		result.Action = "not_complete"
		return result, nil
	}

	issueSvc := NewIssueService(e.client)
	epicIssue, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, NoRelations)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	// Integrate FIRST, close after (#2560).
	integration := e.integrateEpic(ctx, EpicRef{Owner: owner, Repo: repo, Number: epicNumber}, epicIssue.Title)
	result.Integration = &integration
	result.PRURL, result.PRNumber = integration.PRURL, integration.PRNumber
	switch integration.Outcome {
	case IntegrationMerged:
		result.Action = "closed_and_merged"
	case IntegrationAlreadyOnBase:
		result.Action = "already_merged"
	case IntegrationNoBranch:
		result.Action = "no_epic_branch"
	default:
		result.Action = "integration_" + integration.Outcome
		result.Error = integration.Error
		return result, nil
	}

	if !strings.EqualFold(epicIssue.State, "CLOSED") {
		if err := issueSvc.CloseIssue(ctx, epicIssue.NodeID); err != nil {
			result.Error = fmt.Sprintf("failed to close epic: %v", err)
			return result, nil
		}
	}
	return result, nil
}

// AutoCloseResult holds the result of the auto-close operation.
type AutoCloseResult struct {
	Checked int                    `json:"checked"`
	Closed  int                    `json:"closed"`
	Skipped int                    `json:"skipped"`
	Summary []AutoCloseSummaryItem `json:"summary,omitempty"`
}

// AutoCloseSummaryItem is one epic's outcome in an AutoCloseResult.
type AutoCloseSummaryItem struct {
	EpicNumber  int              `json:"epicNumber"`
	Title       string           `json:"title"`
	Status      string           `json:"status"` // "closed", "skipped", "error"
	Reason      string           `json:"reason,omitempty"`
	Error       string           `json:"error,omitempty"`
	Integration *EpicIntegration `json:"integration,omitempty"`
	// PendingTasks lists the body items holding an unfinished_decomposition
	// epic open (#1581).
	PendingTasks []string `json:"pendingTasks,omitempty"`
}

// AutoClose checks all open epics, closes completed ones, and moves them to Done.
// If listing epics fails (e.g. label absent, access error), the error is logged
// to stderr and an empty result is returned so the caller can exit 0 and the
// nightly sweep can continue to the next repo.
func (e *EpicService) AutoClose(ctx context.Context, owner, repo string, projectNumber int) (*AutoCloseResult, error) {
	issueSvc := NewIssueService(e.client)

	// states: OPEN is already enforced by the GraphQL query inside ListIssues;
	// "state:open" must not appear as a label filter or it silently drops all results
	// on repos that lack that label.
	issues, err := issueSvc.ListIssues(ctx, owner, repo, []string{"type:epic"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to list open epics for %s/%s: %v\n", owner, repo, err)
		return &AutoCloseResult{Checked: 0}, nil
	}

	result := &AutoCloseResult{
		Checked: len(issues),
	}

	type closeResult struct {
		index       int
		status      string
		reason      string
		err         error
		integration *EpicIntegration
		pending     []string
	}

	ch := make(chan closeResult, len(issues))
	var wg sync.WaitGroup

	for idx, epic := range issues {
		wg.Add(1)
		go func(i int, epicNumber int) {
			defer wg.Done()
			status, reason, integ, pending, cerr := e.closeOneEpic(ctx, EpicRef{
				Owner:  owner,
				Repo:   repo,
				Number: epicNumber,
			}, projectNumber)
			ch <- closeResult{i, status, reason, cerr, integ, pending}
		}(idx, epic.Number)
	}

	wg.Wait()
	close(ch)

	type summaryItem struct {
		EpicNumber  int
		Title       string
		Status      string
		Reason      string
		Error       string
		Integration *EpicIntegration
		Pending     []string
	}
	summaryByIndex := make(map[int]summaryItem)

	for res := range ch {
		item := summaryItem{
			EpicNumber:  issues[res.index].Number,
			Title:       issues[res.index].Title,
			Status:      res.status,
			Reason:      res.reason,
			Integration: res.integration,
			Pending:     res.pending,
		}
		if res.err != nil {
			item.Status = "error"
			item.Error = res.err.Error()
		} else if res.status == "closed" {
			result.Closed++
		} else if res.status == "skipped" {
			result.Skipped++
		}
		summaryByIndex[res.index] = item
	}

	for i := 0; i < len(issues); i++ {
		item := summaryByIndex[i]
		result.Summary = append(result.Summary, AutoCloseSummaryItem{item.EpicNumber, item.Title, item.Status, item.Reason, item.Error, item.Integration, item.Pending})
	}

	return result, nil
}

// EpicRef is the full coordinate of an epic: its OWN repository plus its
// number. It exists because #1181 was caused by the number travelling without
// the repository - closeOneEpic took (owner, repo, epicNumber) and the
// post-merge hook passed the SUB-ISSUE's owner/repo, so a cross-repo epic
// number resolved against the wrong repository. Issue numbers are per-repo, so
// that never failed cleanly: it either hit a pull request (a loud
// "Could not resolve to an Issue") or, far worse, hit an unrelated real issue
// that happened to occupy the number and reported success.
//
// Passing a struct rather than two more positional strings is deliberate: a
// caller must now name the fields it is filling, so handing over the
// sub-issue's repository is a thing you have to write down rather than
// something you get by default.
type EpicRef struct {
	// Owner and Repo are the epic's own repository.
	Owner string
	Repo  string
	// Number is the epic's issue number WITHIN Owner/Repo.
	Number int

	// ExpectSubIssueNumber and ExpectSubIssueRepo describe a sub-issue that the
	// caller already knows belongs to this epic - on the post-merge path, the
	// issue whose PR just merged. When set, the epic MUST list it among its
	// sub-issues; if it does not, the coordinate resolved to the wrong issue
	// and the check fails loudly ("wrong_epic") instead of reporting the silent
	// success that #1181 is named for. Leave zero on the sweep path, where
	// there is no triggering sub-issue and "no sub-issues" is a real answer.
	ExpectSubIssueNumber int
	ExpectSubIssueRepo   string
}

// String renders the epic as owner/repo#number for diagnostics.
func (r EpicRef) String() string {
	if r.Owner == "" || r.Repo == "" {
		return fmt.Sprintf("#%d", r.Number)
	}
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// closeOneEpic closes a single epic if all its sub-issues are closed.
// Returns (status, reason, error).
//
// The epic is always resolved against ref.Owner/ref.Repo - its own repository -
// never against a caller's ambient repo (#1181).
//
// When the initial check finds open sub-issues but the total is non-zero,
// this is likely a GitHub eventual-consistency window after the final sub-issue
// merge. We retry up to 3 times with exponential backoff (2s, 4s, 8s) to let
// the API catch up before giving up.
//
// pending carries the epic body's outstanding task-list items when the close
// is declined as unfinished_decomposition (#1581); nil otherwise.
func (e *EpicService) closeOneEpic(ctx context.Context, ref EpicRef, projectNumber int) (status, reason string, integ *EpicIntegration, pending []string, err error) {
	if ref.Owner == "" || ref.Repo == "" {
		return "", "epic_repo_missing", nil, nil, fmt.Errorf("epic #%d: no repository given; an epic number without its repository is not a coordinate", ref.Number)
	}
	owner, repo, epicNumber := ref.Owner, ref.Repo, ref.Number

	completion, err := e.CheckCompletion(ctx, owner, repo, epicNumber)
	if err != nil {
		return "", "check_failed", nil, nil, err
	}

	// Identity guard (#1181). The caller told us a sub-issue that belongs to
	// this epic; if the issue we just fetched does not list it, we are looking
	// at a different issue than the one intended and every answer below --
	// "no_subs" most of all -- would be a confident lie. This is checked BEFORE
	// the Total == 0 branch precisely because Total == 0 was the silent face of
	// the bug.
	if ref.ExpectSubIssueNumber > 0 && !completion.HasSubIssue(ref.ExpectSubIssueRepo, ref.ExpectSubIssueNumber) {
		expected := fmt.Sprintf("#%d", ref.ExpectSubIssueNumber)
		if ref.ExpectSubIssueRepo != "" {
			expected = fmt.Sprintf("%s#%d", ref.ExpectSubIssueRepo, ref.ExpectSubIssueNumber)
		}
		return "", "wrong_epic", nil, nil, fmt.Errorf(
			"epic %s does not list %s among its %d sub-issue(s): the epic number resolved to the wrong issue",
			ref, expected, completion.Total)
	}

	if completion.Total == 0 {
		return "skipped", "no_subs", nil, nil, nil
	}

	// Retry on eventual-consistency: GitHub may not have propagated the final
	// sub-issue close by the time the on-merge trigger fires.
	const maxRetries = 3
	baseDelay := 2 * time.Second
	for attempt := 1; attempt <= maxRetries && !completion.Complete && completion.Open > 0; attempt++ {
		select {
		case <-ctx.Done():
			return "", "check_failed", nil, nil, ctx.Err()
		case <-time.After(baseDelay * time.Duration(1<<uint(attempt-1))):
		}
		completion, err = e.CheckCompletion(ctx, owner, repo, epicNumber)
		if err != nil {
			return "", "check_failed", nil, nil, err
		}
	}

	if !completion.Complete && completion.Open == 0 && len(completion.PendingTasks) > 0 {
		return "skipped", reasonUnfinishedDecomposition, nil, completion.PendingTasks, nil
	}
	if !completion.Complete {
		return "skipped", "has_open", nil, nil, nil
	}

	// Only the epic's state and node id are used here; completion already
	// read its sub-issue list, so no list is read again.
	issueSvc := NewIssueService(e.client)
	epicIssue, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, NoRelations)
	if err != nil {
		return "", "fetch_failed", nil, nil, err
	}

	if strings.EqualFold(epicIssue.State, "CLOSED") {
		return "skipped", "already_closed", nil, nil, nil
	}

	// Never lock in a pre-#2602 orphan close (#2602). An open epic whose
	// sub-issues the old closer closed on the epic's state alone is open
	// because a human reopened it; closing it again would re-assert work that
	// may never have shipped. The rollup holds until those sub-issues are
	// reopened or the epic is closed by hand.
	if blocking, err := e.orphanClosedSub(ctx, owner, repo, completion.SubIssues); err != nil {
		return "", "evidence_check_failed", nil, nil, err
	} else if blocking != "" {
		fmt.Fprintf(os.Stderr, "Not closing %s: sub-issue %s was auto-closed without evidence its work shipped\n", ref, blocking)
		return "skipped", reasonOrphanClosedWithoutEvidence, nil, nil, nil
	}

	// Integrate before closing (#2560): an epic whose branch holds commits
	// not on main is never closed. #2085 was closed with its sub-issues' code
	// stranded on the epic branch.
	integration := e.integrateEpic(ctx, ref, epicIssue.Title)
	if !integration.AllowsClose() {
		msg := fmt.Sprintf("Not closing: all %d sub-issues are complete, but the epic branch could not be integrated into main (%s).", completion.Total, integration.Outcome)
		if integration.PRURL != "" {
			msg += "\n\nEpic PR: " + integration.PRURL
		}
		if len(integration.ConflictFiles) > 0 {
			msg += "\n\nConflicting files:\n- `" + strings.Join(integration.ConflictFiles, "`\n- `") + "`"
		}
		// integration.Error is raw git/exec text that can carry remote URLs and
		// local paths; it stays in the result and attention card, never in a
		// public issue comment. A pending integration retries every cycle,
		// so only a blocked one comments (#2584).
		if integration.Blocked() {
			if err := issueSvc.AddComment(ctx, epicIssue.NodeID, msg); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add comment to %s: %v\n", ref, err)
			}
		}
		status := "error"
		if integration.Outcome == IntegrationPending {
			status = "skipped"
		}
		return status, "integration_" + integration.Outcome, &integration, nil, nil
	}

	if err := issueSvc.CloseIssue(ctx, epicIssue.NodeID); err != nil {
		return "", "close_failed", &integration, nil, fmt.Errorf("close issue: %w", err)
	}

	commentBody := fmt.Sprintf("Auto-closed: all %d sub-issues are complete (epic branch: %s).", completion.Total, integration.Outcome)
	if err := issueSvc.AddComment(ctx, epicIssue.NodeID, commentBody); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to add comment to %s: %v\n", ref, err)
	}

	if projectNumber > 0 {
		if err := e.moveToProjectDone(ctx, owner, repo, epicNumber, projectNumber); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to move %s to Done: %v\n", ref, err)
		}
	}

	return "closed", "all_closed", &integration, nil, nil
}

// reasonOrphanClosedWithoutEvidence is closeOneEpic's skip reason when a
// sub-issue was closed by the pre-#2602 orphan closer (#2602).
const reasonOrphanClosedWithoutEvidence = "orphan_closed_without_evidence"

// orphanClosedSub returns the first closed sub-issue, as "owner/repo#N", that
// the orphan closer closed without evidence its work shipped and that no pull
// request has closed since; "" when there is none. A read error is returned,
// so the caller holds the epic open instead of guessing.
func (e *EpicService) orphanClosedSub(ctx context.Context, owner, repo string, subs []types.SubIssueRef) (string, error) {
	issueSvc := NewIssueService(e.client)
	for _, si := range subs {
		if !strings.EqualFold(si.State, "CLOSED") {
			continue
		}
		siOwner, siRepo := owner, repo
		if si.Repo != "" {
			siOwner, siRepo = splitOwnerRepo(si.Repo)
		}
		ev, err := issueSvc.ShipEvidence(ctx, siOwner, siRepo, si.Number)
		if err != nil {
			return "", err
		}
		if ev.OrphanClosedWithoutEvidence && ev.Closer == nil {
			return fmt.Sprintf("%s/%s#%d", siOwner, siRepo, si.Number), nil
		}
	}
	return "", nil
}

// moveToProjectDone moves an epic to "Done" status on the project board.
func (e *EpicService) moveToProjectDone(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...OwnerType) error {
	projSvc := NewProjectService(e.client, owner, projectNumber, ownerType...)
	return projSvc.SyncStatus(ctx, owner, repo, epicNumber, "Done")
}

// AutoCloseSingleResult holds the result of auto-closing a single epic.
type AutoCloseSingleResult struct {
	EpicNumber int `json:"epicNumber"`
	// EpicRepo is the repository the epic was actually resolved against - the
	// epic's own, not the caller's (#1181). Reported so an operator reading the
	// hook's output can see which repo answered.
	EpicRepo string `json:"epicRepo,omitempty"`
	Status   string `json:"status"` // "closed", "skipped", "error"
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
	// Integration reports how the epic branch was brought onto main before
	// the close, or why the epic stayed open (#2560).
	Integration *EpicIntegration `json:"integration,omitempty"`
	// PendingTasks lists the epic body's outstanding task-list items when the
	// close was declined as unfinished_decomposition (#1581).
	PendingTasks []string `json:"pendingTasks,omitempty"`
}

// AutoCloseSingle checks and closes a single epic if all its sub-issues are closed.
// It wraps closeOneEpic for use by the post-merge hook and other callers that target
// a specific epic rather than sweeping all open epics.
//
// ref carries the epic's OWN repository. Callers on the post-merge path must
// also set ref.ExpectSubIssueNumber/Repo to the merged sub-issue so a
// mis-resolved epic is rejected rather than silently reported as having no
// sub-issues (#1181).
func (e *EpicService) AutoCloseSingle(ctx context.Context, ref EpicRef, projectNumber int) (*AutoCloseSingleResult, error) {
	status, reason, integration, pending, err := e.closeOneEpic(ctx, ref, projectNumber)
	epicRepo := ""
	if ref.Owner != "" && ref.Repo != "" {
		epicRepo = ref.Owner + "/" + ref.Repo
	}
	result := &AutoCloseSingleResult{
		EpicNumber:   ref.Number,
		EpicRepo:     epicRepo,
		Status:       status,
		Reason:       reason,
		Integration:  integration,
		PendingTasks: pending,
	}
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
	}
	return result, nil
}

func splitEpicOwnerRepo(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 {
		return "", full
	}
	return parts[0], parts[1]
}

// FilterByEpicNumber returns a new LifecycleAuditResult scoped to findings for a
// single epic. Used by epicCheckLifecycleCmd to narrow a full audit run to one issue.
func (r *LifecycleAuditResult) FilterByEpicNumber(epicNumber int) *LifecycleAuditResult {
	filtered := &LifecycleAuditResult{
		Dimension: r.Dimension,
		Repo:      r.Repo,
		RunAt:     r.RunAt,
		FixMode:   r.FixMode,
		Findings:  []LifecycleFinding{},
	}
	for _, f := range r.Findings {
		if f.IssueNumber == epicNumber {
			filtered.Findings = append(filtered.Findings, f)
		}
	}
	filtered.Summary = buildAuditSummary(filtered.Findings)
	return filtered
}
