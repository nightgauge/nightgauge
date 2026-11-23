package orchestrator

// Closing a met partial-delivery remainder (#3423).
//
// A partial-delivery re-run whose feature-dev writes nothing was booked
// not_pipeline_actionable and parked in Backlog, even when every remaining
// criterion was already met by the earlier PR — the #3422 case, where
// pr-create misread a preservation criterion as asking for change. #3089 made
// closing over a recorded remainder a human decision; this file overrides it
// only on a deterministic verdict.
//
// The verdict is drawn from the earlier PR's merged diff — the merge commit
// the partial-delivery marker's PR produced, against its first parent — never
// from the bare default branch, where a preservation criterion would pass on
// the name's existence alone and a target that was never defined would read
// as removed. Only a preservation or a removal can be met there
// (stages.VerifyACOnMerge); anything undecidable is unmet, and one unmet
// criterion keeps the issue open for a human. A model's claim — dev-{N}.json,
// stage output — is never an input.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/intelligence/acparse"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// partialCloseTimeout bounds every forge write behind one close.
const partialCloseTimeout = 60 * time.Second

// PartialRemainderClosedMarker tags the evidence comment a close posts. It is
// distinct from the re-admit vet's partial-remainder-met marker, which asks a
// human to close.
const PartialRemainderClosedMarker = "<!-- nightgauge:partial-remainder-closed"

var (
	// ErrRemainderNotMet: the verdict is not all met, so nothing is closed.
	ErrRemainderNotMet = errors.New("the remaining criteria are not all met")
	// ErrNoIssueToClose: the issue's node id is unknown.
	ErrNoIssueToClose = errors.New("no issue node id to close")
	// ErrNoCloseDeps: no issue service to comment and close with.
	ErrNoCloseDeps = errors.New("no issue service to close with")
)

// fullCommitSHARE is a full commit id (SHA-1 or SHA-256).
var fullCommitSHARE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// mergedDiffUnreadable is the judge's reason for a merged diff git could not
// read; what follows it in the evidence is a local error.
const mergedDiffUnreadable = "the merged diff could not be read"

// CriterionVerdict is one remaining criterion's answer on the merged diff.
type CriterionVerdict struct {
	Text     string `json:"text"`
	Met      bool   `json:"met"`
	Evidence string `json:"evidence"`
}

// RemainderVerdict is the deterministic re-judge of a partial-delivery
// remainder. AllMet is true only when there is at least one criterion and
// every one is met.
type RemainderVerdict struct {
	MergeSHA string             `json:"merge_sha,omitempty"`
	AllMet   bool               `json:"all_met"`
	Criteria []CriterionVerdict `json:"criteria"`
}

// Unmet returns the criteria the verdict did not find met.
func (v RemainderVerdict) Unmet() []CriterionVerdict {
	var out []CriterionVerdict
	for _, c := range v.Criteria {
		if !c.Met {
			out = append(out, c)
		}
	}
	return out
}

// JudgeRemainder re-judges criteria against the merged diff of mergeSHA in the
// repository at ws: `<mergeSHA>^1..<mergeSHA>`. No merge SHA, a merge commit
// missing from the object store, or a diff that cannot be read leaves every
// criterion unmet as undecidable. No criteria is never all met.
func JudgeRemainder(ctx context.Context, ws, mergeSHA string, criteria []string) RemainderVerdict {
	v := RemainderVerdict{MergeSHA: mergeSHA, Criteria: []CriterionVerdict{}}
	undecidable := func(why string) RemainderVerdict {
		for _, c := range criteria {
			v.Criteria = append(v.Criteria, CriterionVerdict{Text: c, Evidence: "undecidable: " + why})
		}
		return v
	}
	if len(criteria) == 0 {
		return v
	}
	if !fullCommitSHARE.MatchString(mergeSHA) {
		return undecidable("the partial PR's merge commit is unknown")
	}
	d, err := pmstages.CollectACDiffRange(ctx, ws, mergeSHA+"^1", mergeSHA)
	if err != nil {
		return undecidable(fmt.Sprintf("%s: %v", mergedDiffUnreadable, err))
	}
	all := true
	for _, c := range criteria {
		met, ev := pmstages.VerifyACOnMerge(c, d)
		v.Criteria = append(v.Criteria, CriterionVerdict{Text: c, Met: met, Evidence: ev})
		all = all && met
	}
	v.AllMet = all
	return v
}

// PartialCloseIssues is the issue surface a close writes through;
// gh.IssueService satisfies it.
type PartialCloseIssues interface {
	AddComment(ctx context.Context, subjectID, body string) error
	EditIssue(ctx context.Context, nodeID, body string) (*types.Issue, error)
	CloseIssue(ctx context.Context, issueID string) error
}

// PartialCloseDeps are what CloseMetRemainder and CloseRemainderByOperator
// write with. Board and ProjectNumber are optional: without them a run's close
// leaves the board to its issue_closed booking, and an operator's close leaves
// it unsynced.
type PartialCloseDeps struct {
	// Comments, when set, lets a retried operator close skip the comment it
	// already posted (#3487).
	Comments      PartialCloseComments
	Issues        PartialCloseIssues
	Board         hooks.BoardSyncer
	Owner, Repo   string
	ProjectNumber int
}

// PartialCloseTarget names the issue a close acts on: its number, node id and
// current body (whose boxes are ticked) and state, and the partial PR.
type PartialCloseTarget struct {
	Number int
	NodeID string
	Body   string
	State  string
	PR     int
	// BodyRead is true only when Body was read from the forge
	// (ReadPartialCloseTarget sets it). A target without it holds every
	// close: an unread body cannot show the Delivery order is not refs
	// (#3510).
	BodyRead bool
}

// PartialCloseComments lists an issue's comments; *gh.TrustForge satisfies it.
type PartialCloseComments interface {
	IssueComments(ctx context.Context, owner, repo string, number int) ([]trust.Comment, error)
}

// PartialCloseIssueReader reads the issue a close acts on;
// gh.IssueService satisfies it.
type PartialCloseIssueReader interface {
	GetIssueWithRelations(ctx context.Context, owner, repo string, number int, rels gh.IssueRelations) (*types.Issue, error)
}

// ReadPartialCloseTarget reads owner/repo#number's node id, state and current body
// for a close of the remainder partial PR pr left. The scheduler and
// `nightgauge issue partial-noop --apply-close` both read through it.
func ReadPartialCloseTarget(ctx context.Context, r PartialCloseIssueReader, owner, repo string, number, pr int) (PartialCloseTarget, error) {
	iss, err := r.GetIssueWithRelations(ctx, owner, repo, number, gh.NoRelations)
	if err != nil {
		return PartialCloseTarget{}, fmt.Errorf("read %s/%s#%d: %w", owner, repo, number, err)
	}
	if iss == nil || iss.NodeID == "" {
		return PartialCloseTarget{}, ErrNoIssueToClose
	}
	return PartialCloseTarget{Number: number, NodeID: iss.NodeID, Body: iss.Body, State: iss.State, PR: pr, BodyRead: true}, nil
}

// PartialCloseResult reports what a close did.
type PartialCloseResult struct {
	Ticked []int `json:"ticked,omitempty"`
	// Held names why a met remainder was left open instead of closed
	// (HoldDeliveryOrderRefs or HoldIssueBodyUnread); empty otherwise.
	Held   string                 `json:"held,omitempty"`
	Closed bool                   `json:"closed"`
	Board  hooks.BoardSyncOutcome `json:"board,omitempty"`
}

// Reasons a close holds instead of closing (#3510).
const (
	// HoldDeliveryOrderRefs: the issue body's Delivery order says refs, so
	// only a human closes the issue.
	HoldDeliveryOrderRefs = "delivery_order_refs"
	// HoldIssueBodyUnread: the issue body could not be read, so the
	// Delivery order cannot be checked.
	HoldIssueBodyUnread = "issue_body_unread"
)

// deliveryOrderHold returns why an issue with this body must not be closed
// by the pipeline, or "" when it may be: its Delivery order says refs, or its
// body was not read (#3510).
func deliveryOrderHold(body string, bodyRead bool) string {
	switch {
	case !bodyRead:
		return HoldIssueBodyUnread
	case acparse.DeliveryOrderSaysRefs(body):
		return HoldDeliveryOrderRefs
	}
	return ""
}

// holdPhrase renders a hold reason for a comment or detail.
func holdPhrase(issue int, reason string) string {
	if reason == HoldIssueBodyUnread {
		return fmt.Sprintf("#%d's body could not be read to check its Delivery order", issue)
	}
	return fmt.Sprintf("#%d's Delivery order says refs", issue)
}

// CloseMetRemainder closes an issue whose remainder v found all met: it posts
// the evidence comment, ticks each matching unchecked box, closes the issue
// as completed, and moves its board row to Done. It refuses a verdict that is
// not all met. An issue whose Delivery order says refs, or whose body was not
// read, is held: nothing is written and res.Held names why (#3510). A comment
// or close failure is returned and the issue stays open; a failed tick or board sync is logged, as the issue is closed either
// way and the run's booking moves the board.
func CloseMetRemainder(ctx context.Context, deps PartialCloseDeps, target PartialCloseTarget, v RemainderVerdict) (PartialCloseResult, error) {
	var res PartialCloseResult
	if !v.AllMet || len(v.Criteria) == 0 {
		return res, ErrRemainderNotMet
	}
	if target.NodeID == "" {
		return res, ErrNoIssueToClose
	}
	if deps.Issues == nil {
		return res, ErrNoCloseDeps
	}
	if res.Held = deliveryOrderHold(target.Body, target.BodyRead); res.Held != "" {
		return res, nil
	}
	ctx, cancel := context.WithTimeout(ctx, partialCloseTimeout)
	defer cancel()
	if err := deps.Issues.AddComment(ctx, target.NodeID, partialRemainderClosedComment(target, v)); err != nil {
		return res, fmt.Errorf("post the evidence comment on #%d: %w", target.Number, err)
	}
	if indices := partialTickIndices(target.Body, v); len(indices) > 0 {
		body, marked := acparse.Mark(target.Body, indices)
		if len(marked.Changed) > 0 {
			if _, err := deps.Issues.EditIssue(ctx, target.NodeID, body); err != nil {
				log.Printf("#%d: could not tick the met criteria: %v [#3423]", target.Number, err)
			} else {
				res.Ticked = marked.Changed
			}
		}
	}
	if err := deps.Issues.CloseIssue(ctx, target.NodeID); err != nil {
		return res, fmt.Errorf("close #%d: %w", target.Number, err)
	}
	res.Closed = true
	res.Board = hooks.BoardSyncNotAttempted
	if deps.Board != nil && deps.ProjectNumber > 0 {
		res.Board = hooks.SyncIssueDone(ctx, deps.Board, hooks.PostMergeInput{
			IssueNumber: target.Number, RepositoryOwner: deps.Owner, RepositoryName: deps.Repo,
			ProjectNumber: deps.ProjectNumber,
		})
	}
	return res, nil
}

// partialTickIndices returns the unchecked boxes in body that name a met
// criterion, under the rule the partial-delivery writer ticks by.
func partialTickIndices(body string, v RemainderVerdict) []int {
	var out []int
	for _, it := range acparse.List(body) {
		if it.Checked {
			continue
		}
		for _, c := range v.Criteria {
			if c.Met && hooks.RecordedCriterionMatches(it.Text, c.Text) {
				out = append(out, it.Index)
				break
			}
		}
	}
	return out
}

func partialRemainderClosedComment(target PartialCloseTarget, v RemainderVerdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s pr=%d merge=%s -->\n", PartialRemainderClosedMarker, target.PR, v.MergeSHA)
	b.WriteString("## Remaining criteria met\n\n")
	fmt.Fprintf(&b, "A partial-delivery re-run of #%d wrote no changes. Each criterion PR #%d left open was "+
		"re-judged deterministically against that PR's merged diff (`%s^1..%s`), and every one is met, "+
		"so the issue is closed as completed.\n\n", target.Number, target.PR, pmstages.ShortRev(v.MergeSHA), pmstages.ShortRev(v.MergeSHA))
	for _, c := range v.Criteria {
		fmt.Fprintf(&b, "- [x] %s — %s\n", pmstages.CommentSafeACText(c.Text), pmstages.CommentSafeACText(c.Evidence))
	}
	return b.String()
}

// PartialClosedDetail is the run detail for a remainder closed as met.
func PartialClosedDetail(issue, pr int, v RemainderVerdict) string {
	return fmt.Sprintf("partial-delivery re-run of #%d (after PR #%d) wrote no changes; every one of its %d remaining criteria is met on %s's merged diff, so #%d is closed as completed [#3423]",
		issue, pr, len(v.Criteria), pmstages.ShortRev(v.MergeSHA), issue)
}

// PartialCloseFailedDetail is the not_pipeline_actionable detail for a met
// remainder whose close failed: the issue waits in Backlog for a human. The
// scheduler and `nightgauge issue partial-noop --apply-close` share it.
func PartialCloseFailedDetail(issue, pr int, err error) string {
	return fmt.Sprintf("[not-pipeline-actionable] feature-dev: partial-delivery re-run of #%d (after PR #%d): every remaining criterion is met on the merged diff, but closing failed: %v; #%d waits in Backlog for a human [#3423]",
		issue, pr, err, issue)
}

// PartialCloseHeldDetail is the not_pipeline_actionable detail for a met
// remainder whose close was held (#3510): the issue stays open for a human.
// The scheduler and `nightgauge issue partial-noop --apply-close` share it.
func PartialCloseHeldDetail(issue, pr int, reason string) string {
	return fmt.Sprintf("[not-pipeline-actionable] feature-dev: partial-delivery re-run of #%d (after PR #%d): every remaining criterion is met on the merged diff, but %s, so #%d stays open for a human [#3510]",
		issue, pr, holdPhrase(issue, reason), issue)
}

// maxUnmetInDetail caps the unmet criteria a booking detail lists.
const maxUnmetInDetail = 5

// unmetSummary renders up to maxUnmetInDetail unmet criteria with their evidence, for a
// booking detail.
func unmetSummary(v RemainderVerdict) string {
	unmet := v.Unmet()
	if len(unmet) == 0 {
		return ""
	}
	var parts []string
	for i, c := range unmet {
		if i == maxUnmetInDetail {
			parts = append(parts, fmt.Sprintf("and %d more", len(unmet)-maxUnmetInDetail))
			break
		}
		parts = append(parts, fmt.Sprintf("%q (%s)", c.Text, c.Evidence))
	}
	return strings.Join(parts, "; ")
}

// judgePartialRemainder re-judges this run's partial-delivery scope against
// the earlier PR's merged diff, reading the merge SHA recorded at scope time
// in ws's partial-delivery-{N}.json. An unscoped run, or a record naming
// another PR, yields a verdict that is not all met.
func (s *Scheduler) judgePartialRemainder(ctx context.Context, scope *state.PartialRerun, issue int, ws string) RemainderVerdict {
	if scope == nil || scope.PR <= 0 {
		return RemainderVerdict{}
	}
	mergeSHA := ""
	rec, err := ReadPartialDeliveryContext(ws, issue)
	switch {
	case err != nil:
		log.Printf("#%d: partial re-run: no partial-delivery record to judge the remainder with: %v [#3423]", issue, err)
	case rec.PR != scope.PR:
		log.Printf("#%d: partial re-run: the partial-delivery record names PR #%d, not #%d — the remainder is not judged [#3423]",
			issue, rec.PR, scope.PR)
	default:
		mergeSHA = rec.MergeSHA
	}
	v := JudgeRemainder(ctx, ws, mergeSHA, scope.Criteria)
	log.Printf("#%d: partial re-run: remainder of PR #%d re-judged on its merged diff — %d of %d criteria met [#3423]",
		issue, scope.PR, len(v.Criteria)-len(v.Unmet()), len(v.Criteria))
	return v
}

// closePartialRemainder closes item through the forge for a met remainder,
// or through the partialRemainderClose seam in tests.
func (s *Scheduler) closePartialRemainder(ctx context.Context, item types.BoardItem, pr int, v RemainderVerdict) (PartialCloseResult, error) {
	if s.partialRemainderClose != nil {
		return s.partialRemainderClose(ctx, item, pr, v)
	}
	owner, name := splitRepo(item.Repo)
	client, err := s.repoClient(ctx, owner, name)
	if err != nil {
		return PartialCloseResult{}, err
	}
	svc := gh.NewIssueService(client)
	target, err := ReadPartialCloseTarget(ctx, svc, owner, name, item.Number, pr)
	if err != nil {
		return PartialCloseResult{}, err
	}
	deps := PartialCloseDeps{Issues: svc, Owner: owner, Repo: name, ProjectNumber: s.projectNumber}
	if s.projectNumber > 0 {
		// Through boardcache.WrapProject, as checkEpicCompletion does, so
		// moving the issue to Done drops the daemon's open board snapshot.
		deps.Board = boardcache.WrapProject(s.boardCache, gh.NewProjectService(client, owner, s.projectNumber), owner, s.projectNumber)
	}
	return CloseMetRemainder(ctx, deps, target, v)
}

// endPartialRemainderMet runs the close for a met remainder and returns the
// stage detail and terminal kind the run ends with: issue_closed (a
// non-failure) once closed, not_pipeline_actionable when the close failed or
// was held (#3510).
func (s *Scheduler) endPartialRemainderMet(ctx context.Context, item types.BoardItem, pr int, v RemainderVerdict) (detail, kind string) {
	res, err := s.closePartialRemainder(ctx, item, pr, v)
	if err != nil {
		detail = PartialCloseFailedDetail(item.Number, pr, err)
		log.Printf("#%d: %s", item.Number, detail)
		return detail, TerminalKindNotPipelineActionable
	}
	if res.Held != "" {
		detail = PartialCloseHeldDetail(item.Number, pr, res.Held)
		log.Printf("#%d: %s", item.Number, detail)
		return detail, TerminalKindNotPipelineActionable
	}
	detail = PartialClosedDetail(item.Number, pr, v)
	log.Printf("#%d: %s (ticked %d box(es), board %s)", item.Number, detail, len(res.Ticked), res.Board)
	return detail, TerminalKindIssueClosed
}
