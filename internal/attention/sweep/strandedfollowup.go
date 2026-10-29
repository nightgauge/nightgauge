package sweep

// Producer: stranded follow-up PRs (issue #2684).
//
// An advisory-review follow-up is sometimes opened after its parent PR has
// merged. The parent issue is closed by then, and the scheduler dispatches
// only open issues, so nothing ever lands the follow-up: #2662, #2670 and
// #2681 each sat open with green checks and no owner. The follow-up is
// opened outside any pipeline stage, so no stage can see it being created;
// the repository is the only observer, which makes this a sweep producer.
//
// It considers only PRs whose head branch lives in the repository itself (no
// forks), since the tracking issue becomes dispatchable work. It flags an
// OPEN pull request whose head branch encodes an issue number
// (`<prefix>/<N>-...`) when issue N is CLOSED and the PR body names no open
// issue with a closing keyword. For each such PR it files one tracking issue
// and keeps it on the board, so the scheduler has open, dispatchable work that
// lands the PR. The card names the PR, the closed issue and the tracking issue.
//
// The tracking issue is filed once. The card's fingerprint records its
// number, so a later pass reads it back by number whatever its state; with no
// card, an open issue bearing the exact title the producer generates (unique
// per PR) is the tracking issue. Search results carry no body, so the hidden
// marker cannot be the lookup key.
//
// When no PR is stranded it returns an empty slice, which auto-resolves any
// card raised earlier.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// ProducerStrandedFollowUpPRs is the stable producer id. It is half of the
// sticky (producer, idempotency_key) identity, so it must never change.
const ProducerStrandedFollowUpPRs = "stranded-follow-up-prs"

// strandedSafeRefRE is the branch-name alphabet the tracking issue body will
// quote; any other ref is skipped rather than embedded.
var strandedSafeRefRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// strandedCandidate reports whether pr is one this producer may act on. The
// tracking issue it files is dispatchable work that lands the PR, so only a
// branch in this repository — which only someone with push access can
// create — qualifies. A fork PR, or a listing that does not report the head
// repository, is never a candidate.
func strandedCandidate(pr forge.OpenPRSummary, repo string) bool {
	return strings.EqualFold(pr.HeadRepo, repo) && strandedSafeRefRE.MatchString(pr.HeadRef)
}

// strandedBranchRE captures the issue number a pipeline branch encodes.
var strandedBranchRE = regexp.MustCompile(`^[a-z]+/(\d+)-`)

// strandedClosesRE matches a closing keyword and the issue it names, the
// keywords GitHub honours in a PR body.
var strandedClosesRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)\b`)

// StrandedFollowUpMarker is the hidden body marker that identifies the
// tracking issue filed for one PR.
func StrandedFollowUpMarker(pr int) string {
	return fmt.Sprintf("<!-- nightgauge:stranded-follow-up pr=%d -->", pr)
}

// StrandedFollowUpPRs files a tracking issue for, and cards, each open PR
// whose head-branch issue is closed.
type StrandedFollowUpPRs struct {
	// ListOpenPRs overrides the forge PR listing (tests).
	ListOpenPRs func(ctx context.Context, in Input) ([]forge.OpenPRSummary, error)
	// GetIssues overrides the batched forge issue read (tests).
	GetIssues func(ctx context.Context, in Input, numbers []int) (map[int]*forgetypes.Issue, error)
	// FindTracking overrides the title search (tests). It returns the open
	// tracking issue titled title, or nil.
	FindTracking func(ctx context.Context, in Input, title string) (*forgetypes.Issue, error)
	// FileTracking overrides issue creation (tests).
	FileTracking func(ctx context.Context, in Input, title, body string) (*forgetypes.Issue, error)
	// AddToBoard overrides the board sync (tests).
	AddToBoard func(ctx context.Context, in Input, number int) error
}

func init() { Default.Register(&StrandedFollowUpPRs{}) }

// Name implements Producer.
func (p *StrandedFollowUpPRs) Name() string { return ProducerStrandedFollowUpPRs }

// Evaluate implements Producer.
func (p *StrandedFollowUpPRs) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	prs, err := p.listOpenPRs(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("%s: list open PRs for %s: %w", ProducerStrandedFollowUpPRs, in.Repo, err)
	}
	// One batched read for every issue a candidate PR names: its branch's
	// issue and any issue its body closes.
	var numbers []int
	candidates := prs[:0:0]
	for _, pr := range prs {
		if strandedCandidate(pr, in.Repo) {
			candidates = append(candidates, pr)
		}
	}
	prs = candidates
	for _, pr := range prs {
		if m := strandedBranchRE.FindStringSubmatch(pr.HeadRef); m != nil {
			n, _ := strconv.Atoi(m[1])
			numbers = append(numbers, n)
			for _, c := range strandedClosesRE.FindAllStringSubmatch(pr.Body, -1) {
				k, _ := strconv.Atoi(c[1])
				numbers = append(numbers, k)
			}
		}
	}
	if len(numbers) == 0 {
		return nil, nil
	}
	states, err := p.getIssues(ctx, in, numbers)
	if err != nil {
		return nil, fmt.Errorf("%s: read issues for %s: %w", ProducerStrandedFollowUpPRs, in.Repo, err)
	}
	issue := func(n int) *forgetypes.Issue { return states[n] }

	var reqs []attention.DecisionRequest
	for _, pr := range prs {
		m := strandedBranchRE.FindStringSubmatch(pr.HeadRef)
		if m == nil {
			continue
		}
		parent, _ := strconv.Atoi(m[1])
		if parentIssue := issue(parent); parentIssue == nil || !strings.EqualFold(parentIssue.State, "closed") {
			continue
		}
		if closesOpenIssue(pr.Body, issue) {
			continue
		}
		title, body := strandedTrackingIssue(pr, parent)
		tracking, err := p.trackingFromCard(ctx, in, pr.Number)
		if err != nil {
			return nil, fmt.Errorf("%s: read tracking issue for PR #%d: %w", ProducerStrandedFollowUpPRs, pr.Number, err)
		}
		if tracking == nil {
			if tracking, err = p.findTracking(ctx, in, title); err != nil {
				return nil, fmt.Errorf("%s: search tracking issue for PR #%d: %w", ProducerStrandedFollowUpPRs, pr.Number, err)
			}
		}
		if tracking == nil {
			if tracking, err = p.fileTracking(ctx, in, title, body); err != nil {
				return nil, fmt.Errorf("%s: file tracking issue for PR #%d: %w", ProducerStrandedFollowUpPRs, pr.Number, err)
			}
		}
		// A filed issue off the board is invisible to the scheduler, so a board
		// failure is an error, and the sync runs on every pass an open tracking
		// issue is seen: a pass that filed the issue but failed to board it is
		// repaired by the next one. Adding an item already on the board is a
		// no-op. A just-created issue reports no state, so the test is "not closed".
		if !strings.EqualFold(tracking.State, "closed") {
			if err := p.addToBoard(ctx, in, tracking.Number); err != nil {
				return nil, fmt.Errorf("%s: add tracking issue #%d for PR #%d to the board: %w", ProducerStrandedFollowUpPRs, tracking.Number, pr.Number, err)
			}
		}
		reqs = append(reqs, strandedFollowUpRequest(in.Repo, pr, parent, tracking))
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].IdempotencyKey < reqs[j].IdempotencyKey })
	return reqs, nil
}

// closesOpenIssue reports whether body names an OPEN issue with a closing
// keyword: such a PR already has dispatchable work that will land it.
func closesOpenIssue(body string, issue func(int) *forgetypes.Issue) bool {
	for _, m := range strandedClosesRE.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.Atoi(m[1])
		if is := issue(n); is != nil && strings.EqualFold(is.State, "open") {
			return true
		}
	}
	return false
}

func strandedTrackingIssue(pr forge.OpenPRSummary, parent int) (string, string) {
	title := fmt.Sprintf("fix: land stranded follow-up PR #%d (for #%d)", pr.Number, parent)
	body := fmt.Sprintf("%s\n\nPR #%d (`%s`) is open, but issue #%d, the issue its branch "+
		"names, is closed. The scheduler dispatches only open issues, so nothing lands the PR.\n\n"+
		"Land its change: rebase it onto the default branch, resolve conflicts, and merge it, "+
		"or carry its content in a PR that closes this issue and close #%d with a comment "+
		"naming that PR.\n",
		StrandedFollowUpMarker(pr.Number), pr.Number, pr.HeadRef, parent, pr.Number)
	return title, body
}

func strandedFollowUpRequest(repo string, pr forge.OpenPRSummary, parent int, tracking *forgetypes.Issue) attention.DecisionRequest {
	trackingNum := 0
	if tracking != nil {
		trackingNum = tracking.Number
	}
	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("pr-%d", pr.Number),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("PR #%d is stranded: its issue #%d is closed", pr.Number, parent),
		Body: fmt.Sprintf("PR #%d (`%s`) is open, but issue #%d is closed, so the scheduler will "+
			"never dispatch work that lands it. Tracking issue #%d now carries it.",
			pr.Number, pr.HeadRef, parent, trackingNum),
		// The fingerprint is which issue tracks the PR, never the observation
		// time, so re-observing the same stranded PR does not re-alert.
		Fingerprint: fmt.Sprintf("parent:%d:tracking:%d", parent, trackingNum),
		Context: attention.Context{
			Repo:    repo,
			Issue:   trackingNum,
			PR:      pr.Number,
			Blocker: fmt.Sprintf("PR #%d's issue #%d is closed; tracked by #%d", pr.Number, parent, trackingNum),
			URL:     pr.URL,
		},
		Options: []attention.Option{
			{ID: "dismiss", Label: "Dismiss", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

func (p *StrandedFollowUpPRs) listOpenPRs(ctx context.Context, in Input) ([]forge.OpenPRSummary, error) {
	if p.ListOpenPRs != nil {
		return p.ListOpenPRs(ctx, in)
	}
	// The GitHub adapter answers from the conditional REST read the human-gate
	// producer already makes, so an unchanged PR list costs nothing.
	if s, ok := in.Forge.PRs().(openPRSummaries); ok {
		return s.ListOpenPRSummaries(ctx, in.Owner, in.Name)
	}
	// ListPRs does not report the head repository, so with no summary read
	// nothing is a candidate: fail closed rather than trust a fork.
	return nil, nil
}

// openPRSummaries is the optional capability of listing open PRs' numbers,
// head branches and bodies from a cheap read.
type openPRSummaries interface {
	ListOpenPRSummaries(ctx context.Context, owner, repo string) ([]forge.OpenPRSummary, error)
}

func (p *StrandedFollowUpPRs) getIssues(ctx context.Context, in Input, numbers []int) (map[int]*forgetypes.Issue, error) {
	if p.GetIssues != nil {
		return p.GetIssues(ctx, in, numbers)
	}
	return in.Forge.Issues().GetIssuesByNumbers(ctx, in.Owner, in.Name, numbers)
}

// trackingFromCard returns the tracking issue this producer's open card for pr
// already names, read by number so a closed tracking issue is still found and
// never re-filed. It returns nil when there is no such card.
func (p *StrandedFollowUpPRs) trackingFromCard(ctx context.Context, in Input, pr int) (*forgetypes.Issue, error) {
	card, ok := in.OpenRequestForPR(ProducerStrandedFollowUpPRs, pr)
	if !ok {
		return nil, nil
	}
	var parent, n int
	if _, err := fmt.Sscanf(card.Fingerprint, "parent:%d:tracking:%d", &parent, &n); err != nil || n <= 0 {
		return nil, nil
	}
	issues, err := p.getIssues(ctx, in, []int{n})
	if err != nil {
		return nil, err
	}
	return issues[n], nil
}

func (p *StrandedFollowUpPRs) findTracking(ctx context.Context, in Input, title string) (*forgetypes.Issue, error) {
	if p.FindTracking != nil {
		return p.FindTracking(ctx, in, title)
	}
	found, err := in.Forge.Issues().SearchIssues(ctx, in.Owner, in.Name, fmt.Sprintf("%q in:title", title), 20)
	if err != nil {
		return nil, err
	}
	for i := range found {
		if found[i].Title == title {
			return &found[i], nil
		}
	}
	return nil, nil
}

func (p *StrandedFollowUpPRs) addToBoard(ctx context.Context, in Input, number int) error {
	if p.AddToBoard != nil {
		return p.AddToBoard(ctx, in, number)
	}
	_, err := in.Forge.Project().AddIssueByNumber(ctx, in.Owner, in.Name, number)
	return err
}

func (p *StrandedFollowUpPRs) fileTracking(ctx context.Context, in Input, title, body string) (*forgetypes.Issue, error) {
	if p.FileTracking != nil {
		return p.FileTracking(ctx, in, title, body)
	}
	repoID, err := strandedRepoID(ctx, in)
	if err != nil {
		return nil, err
	}
	labels, err := in.Forge.Issues().GetRepoLabels(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, fmt.Errorf("fetch repo labels: %w", err)
	}
	var labelIDs []string
	for _, name := range []string{"bug", "pipeline-gap"} {
		if id, ok := labels[name]; ok {
			labelIDs = append(labelIDs, id)
		}
	}
	return in.Forge.Issues().CreateIssue(ctx, repoID, title, body, labelIDs)
}

// strandedRepoID resolves the repository node id CreateIssue needs through
// the forge's optional GraphQL surface.
func strandedRepoID(ctx context.Context, in Input) (string, error) {
	gql, ok := in.Forge.(forge.GraphQLService)
	if !ok {
		return "", fmt.Errorf("forge client has no GraphQL surface to resolve the repository id")
	}
	const query = `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}`
	raw, err := gql.ExecuteGraphQL(ctx, query, map[string]interface{}{"owner": in.Owner, "name": in.Name})
	if err != nil {
		return "", fmt.Errorf("resolve repository id: %w", err)
	}
	var env struct {
		Data struct {
			Repository struct {
				ID string `json:"id"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Data.Repository.ID == "" {
		return "", fmt.Errorf("resolve repository id for %s: empty or unparseable response", in.Repo)
	}
	return env.Data.Repository.ID, nil
}
