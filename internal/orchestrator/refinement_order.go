package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// Refinement candidate tiers, in dispatch order (#1514).
//
// The refinement scan used to take whatever ListIssuesExcludingLabels returned
// first — GitHub's default order, i.e. the OLDEST open issues — which is the
// exact opposite of the order the dispatch scan will consume them in. On a
// workspace with a real backlog that spends the entire hourly refinement rail
// on issues that may never dispatch while the issues about to run stay
// unrefined and unsized.
//
// The tiers below are read off the SAME board graph the dispatch scan already
// built (as.graphCache): no extra GraphQL is spent to know an issue's board
// status or priority (#482).
const (
	// refinementTierReady: on a project board with Status "Ready" — the next
	// thing the dispatch scan will pick up.
	refinementTierReady = 1
	// refinementTierPrioritizedBacklog: on the board in "Backlog" with a
	// Priority set — triaged work waiting for a slot.
	refinementTierPrioritizedBacklog = 2
	// refinementTierBacklog: everything else open, oldest first (the pre-#1514
	// order). Gated by autonomous.refinement_backlog, default false.
	refinementTierBacklog = 3
)

// refinementCandidatesPerCycle is how many candidates one repo may dispatch in
// a single cycle. Unchanged by #1514 — what changed is WHICH five.
const refinementCandidatesPerCycle = 5

// Skip classes. Each is a condition under which the issue can never dispatch,
// so refining it spends a model call on a body no pipeline will read. Logged
// once per class per repo per cycle, never once per issue.
const (
	refinementSkipExcludedLabel = "excluded-label"
	refinementSkipBoardStatus   = "board-status-not-dispatchable"
	refinementSkipOpenPR        = "open-pr"
	refinementSkipHumanHold     = "human-hold"
	refinementSkipBacklogOff    = "backlog-refinement-disabled"
	refinementSkipTierCap       = "tier-cap-higher-tier-pending"
)

// refinementNode is the immutable slice of one board node the refinement scan
// needs. Copied under as.mu; never a pointer into the live graph.
type refinementNode struct {
	boardStatus     string
	priority        string
	labels          []string
	labelsTruncated bool
	state           string
}

// snapshotRefinementNodes copies the fields the refinement scan reads out of a
// built graph.
func snapshotRefinementNodes(nodes map[string]*depgraph.Node) map[string]refinementNode {
	out := make(map[string]refinementNode, len(nodes))
	for key, n := range nodes {
		if n == nil {
			continue
		}
		labels := make([]string, len(n.Labels))
		copy(labels, n.Labels)
		out[key] = refinementNode{
			boardStatus:     n.BoardStatus,
			priority:        n.Priority,
			labels:          labels,
			labelsTruncated: n.LabelsTruncated,
			state:           n.State,
		}
	}
	return out
}

// refinementCandidate is one issue with the ordering keys derived from the
// board graph.
type refinementCandidate struct {
	issue        gh.UnrefinedIssue
	tier         int
	priorityRank int
}

// refinementDispatchView is the query-less view of the workspace that decides
// refinement order and skips. Every field is either already in memory or
// derived from the cached dependency graph — building one costs no forge call.
type refinementDispatchView struct {
	// nodes is a SNAPSHOT of the cached board graph, keyed "owner/repo#number".
	// A copy, not the graph's own nodes: the refinement scan runs on its own
	// goroutine and the dispatch cycle mutates node fields in place (the
	// in-review recovery rewrites BoardStatus), so holding pointers into the
	// live graph would be a cross-goroutine read of mutating state.
	//
	// Empty when no dispatch cycle has built a graph yet; every issue then
	// falls to tier 3, which is the pre-#1514 behaviour and is gated off by
	// default.
	nodes map[string]refinementNode
	// holds maps an issue key to the human hold it is waiting on.
	holds map[string]string
	// openPR is the union of "open PR is BLOCKED" and "In review, PR-backed":
	// in both cases the work already shipped a PR and refining its body now
	// rewrites text the PR was written against.
	openPR map[string]bool
	// excludeLabels is the resolved autonomous.exclude_labels list.
	excludeLabels []string
	// backlogEnabled is autonomous.refinement_backlog.
	backlogEnabled bool
	// higherTierPending is true when a tier-1 or tier-2 issue anywhere in the
	// workspace is still unrefined. While it holds, tier 3 is not refined at
	// all — the hourly rate rail is reserved for work that is about to run.
	higherTierPending bool
}

// refinementView snapshots everything the refinement scan needs to order and
// skip candidates. Takes as.mu once; makes no GitHub call.
func (as *AutonomousScheduler) refinementView() refinementDispatchView {
	v := refinementDispatchView{
		nodes:          map[string]refinementNode{},
		holds:          map[string]string{},
		openPR:         map[string]bool{},
		excludeLabels:  resolvedExcludeLabels(as.config.ExcludeLabels),
		backlogEnabled: as.config.RefinementBacklog,
	}

	as.mu.Lock()
	if as.graphCache != nil {
		v.nodes = snapshotRefinementNodes(as.graphCache.Nodes)
	}
	for _, f := range as.state.Failed {
		if hold := HoldForTerminalKind(f.Kind); hold != HoldNone {
			v.holds[fmt.Sprintf("%s#%d", f.Repo, f.Number)] = hold
		}
	}
	for k, blocked := range as.blockedReadyPRIssues {
		if blocked {
			v.openPR[k] = true
		}
	}
	for k, backed := range as.inReviewPRBacked {
		if backed {
			v.openPR[k] = true
		}
	}
	as.mu.Unlock()

	v.higherTierPending = v.hasHigherTierPending()
	return v
}

// hasHigherTierPending reports whether any tier-1/tier-2 issue in the whole
// workspace is still unrefined and not itself skippable. This is the tier cap
// of #1514 point 3: it is answered from the cached graph alone, so asking it
// every cycle costs nothing.
//
// A node whose label list the board scan truncated is NOT counted. Its
// refinement state cannot be proven either way, and counting it would starve
// tier 3 permanently on a single over-labelled issue.
func (v refinementDispatchView) hasHigherTierPending() bool {
	for key, node := range v.nodes {
		if !strings.EqualFold(node.state, "OPEN") || node.labelsTruncated {
			continue
		}
		if labelSetHas(node.labels, gh.LabelRefined) || labelSetHas(node.labels, gh.LabelEpic) {
			continue
		}
		if refinementTierFor(node, true) > refinementTierPrioritizedBacklog {
			continue
		}
		// Tier gates are deliberately not consulted here: this function is
		// what feeds them, and consulting them would be circular.
		if v.baseSkipReason(key, node, true, node.labels) != "" {
			continue
		}
		return true
	}
	return false
}

// refinementTierFor classifies one issue. An issue on no board the workspace
// polls (onBoard false) is tier 3.
func refinementTierFor(node refinementNode, onBoard bool) int {
	if !onBoard {
		return refinementTierBacklog
	}
	if isReadyStatus(node.boardStatus) {
		return refinementTierReady
	}
	if isBacklogStatus(node.boardStatus) && strings.TrimSpace(node.priority) != "" {
		return refinementTierPrioritizedBacklog
	}
	return refinementTierBacklog
}

// refinementBoardStatusRefinable reports whether a board status leaves any
// dispatch ahead. "In progress", "In review" and "Done" do not: the issue is
// being worked, has shipped, or is finished, and rewriting its body now edits
// text a running stage or a merged PR was written against.
//
// An empty status (not on a board) IS refinable — that is the cold-workspace
// case tier 3 exists for.
func refinementBoardStatusRefinable(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in progress", "in review", "done":
		return false
	}
	return true
}

// baseSkipReason returns the skip class for conditions that are independent of
// the tier gates: a human-only label, a human hold, an open PR, or a board
// status with no dispatch ahead of it. Empty string means "not skipped".
func (v refinementDispatchView) baseSkipReason(key string, node refinementNode, onBoard bool, labels []string) string {
	if _, excluded := excludedLabelMatch(labels, v.excludeLabels); excluded {
		return refinementSkipExcludedLabel
	}
	if _, held := v.holds[key]; held {
		return refinementSkipHumanHold
	}
	if v.openPR[key] {
		return refinementSkipOpenPR
	}
	if onBoard && !refinementBoardStatusRefinable(node.boardStatus) {
		return refinementSkipBoardStatus
	}
	return ""
}

// skipReason returns the skip class for a candidate, including the two tier
// gates. `auto-process` is exempt from both: the label is the operator saying
// "this one, now", and honouring it is what lets a cold workspace get useful
// work refined without turning the whole backlog sweep on.
func (v refinementDispatchView) skipReason(key string, node refinementNode, onBoard bool, labels []string, tier int) string {
	if reason := v.baseSkipReason(key, node, onBoard, labels); reason != "" {
		return reason
	}
	if tier != refinementTierBacklog || labelSetHas(labels, gh.LabelAutoProcess) {
		return ""
	}
	if !v.backlogEnabled {
		return refinementSkipBacklogOff
	}
	if v.higherTierPending {
		return refinementSkipTierCap
	}
	return ""
}

// labelSetHas reports case-insensitive membership.
func labelSetHas(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}

// planRefinement turns one repo's raw candidate listing into the ordered,
// capped set the cycle will actually refine, plus the per-class skip counts.
//
// Ordering: tier ascending, then the dispatch scan's own priority ordering
// (P0 → P3, unset last), then oldest issue number first. The listing itself is
// unchanged — one query per repo per cycle — so the whole reordering is free.
func (v refinementDispatchView) planRefinement(fullRepo string, issues []gh.UnrefinedIssue) ([]refinementCandidate, map[string][]int) {
	skips := map[string][]int{}
	var kept []refinementCandidate

	for _, issue := range issues {
		key := fmt.Sprintf("%s#%d", fullRepo, issue.Number)
		node, onBoard := v.nodes[key]
		tier := refinementTierFor(node, onBoard)
		if reason := v.skipReason(key, node, onBoard, issue.Labels, tier); reason != "" {
			skips[reason] = append(skips[reason], issue.Number)
			continue
		}
		rank := candidatePriorityRank("")
		if onBoard {
			rank = candidatePriorityRank(node.priority)
		}
		kept = append(kept, refinementCandidate{issue: issue, tier: tier, priorityRank: rank})
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].tier != kept[j].tier {
			return kept[i].tier < kept[j].tier
		}
		if kept[i].priorityRank != kept[j].priorityRank {
			return kept[i].priorityRank < kept[j].priorityRank
		}
		return kept[i].issue.Number < kept[j].issue.Number
	})

	if len(kept) > refinementCandidatesPerCycle {
		kept = kept[:refinementCandidatesPerCycle]
	}
	return kept, skips
}

// logRefinementSkips prints one line per skip class per repo per cycle — never
// one per issue, which on a 165-issue backlog is a log nobody reads.
func logRefinementSkips(fullRepo string, skips map[string][]int) {
	if len(skips) == 0 {
		return
	}
	classes := make([]string, 0, len(skips))
	for c := range skips {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for _, c := range classes {
		numbers := skips[c]
		sort.Ints(numbers)
		shown := numbers
		suffix := ""
		if len(shown) > 10 {
			shown = shown[:10]
			suffix = ", …"
		}
		log.Printf("[refinement] %s: skipped %d candidate(s) — %s: %v%s",
			fullRepo, len(numbers), c, shown, suffix)
	}
}

// refineBeforeDispatch is the pre-dispatch refinement hook (#1514 point 4).
//
// The dispatch scan calls it for the item it is about to enqueue. When that
// item is unrefined and a refinement slot is free — bounded by the SAME
// scheduler-wide semaphore and the same hourly rate rail the refinement scan
// uses — the issue is refined inline, then dispatched. When no slot is free,
// the rail is spent, or refinement has no way to execute, the issue is
// dispatched anyway and one line records that it went unrefined.
//
// Returns true only when a refinement actually ran. Deliberately synchronous:
// "refine it FIRST" is the whole point, and refineIssue is bounded by the
// issue-refine stage's 5-minute timeout.
func (as *AutonomousScheduler) refineBeforeDispatch(ctx context.Context, g *depgraph.Graph, item CandidateItem) bool {
	if g == nil {
		return false
	}
	key := fmt.Sprintf("%s#%d", item.Repo, item.Number)
	node := g.Nodes[key]
	if node == nil {
		return false
	}
	// Fail closed on a truncated label list, exactly as the candidate listing
	// does (#993/#998): we cannot prove the issue is unrefined, and a wrong
	// answer here rewrites a human-reviewed body.
	if node.LabelsTruncated || labelSetHas(node.Labels, gh.LabelRefined) {
		return false
	}

	owner, repo, ok := splitRepoSlug(item.Repo)
	if !ok {
		return false
	}

	unrefined := func(reason string) bool {
		log.Printf("[refinement] pre-dispatch: dispatching %s unrefined — %s", key, reason)
		return false
	}

	if !as.config.RefinementEnabled {
		return unrefined("refinement is disabled (autonomous.refinement_enabled)")
	}
	if !as.refinementIsViable() {
		// Reached only when refinement has no execution path at all — no
		// registered runner and no CLI adapter. In extension (IPC) mode
		// WithIPCRefinement registers one (#1529), so this line is the
		// exception it was always meant to be rather than every dispatch.
		return unrefined("no refinement runner and no CLI adapter to run the refine skill")
	}
	// The author-trust gate (#270) is not a slot question — a stranger's issue
	// text must never reach the model, whatever the rail says.
	if !isTrustedAuthor(node.AuthorAssociation, as.config.TrustedAuthorAssociations) {
		return unrefined(fmt.Sprintf("untrusted author (author_association=%q)", node.AuthorAssociation))
	}
	if as.refinementExhausted(key) {
		return unrefined(fmt.Sprintf("refinement already failed %d times", maxRefinementFailures))
	}

	// Non-blocking acquire: a busy refinement slot must delay the DISPATCH by
	// nothing at all.
	select {
	case as.refinementSem <- struct{}{}:
	default:
		return unrefined("no refinement slot free")
	}

	if as.safetyRails != nil {
		allowed, reason := as.safetyRails.CheckBeforeRefine()
		if !allowed {
			<-as.refinementSem
			return unrefined("refinement rate limit: " + reason)
		}
		as.safetyRails.RecordRefinementStart()
	}

	log.Printf("[refinement] pre-dispatch: refining %s before dispatch", key)
	// refineIssue releases the semaphore itself.
	as.refineIssue(ctx, owner, repo, gh.UnrefinedIssue{
		Number:            node.Number,
		Title:             node.Title,
		Labels:            node.Labels,
		AuthorAssociation: node.AuthorAssociation,
	}, refinementOrigin{
		// The tier is read off the same board fields the cycle's view reads,
		// so the two sources report the same vocabulary.
		tier: refinementTierFor(refinementNode{
			boardStatus: node.BoardStatus,
			priority:    node.Priority,
		}, true),
		source: refinementSourcePreDispatch,
	})
	return true
}

// splitRepoSlug splits "owner/name" into its two halves.
func splitRepoSlug(slug string) (owner, repo string, ok bool) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// planNumbersOf renders a plan as issue numbers for one log line.
func planNumbersOf(plan []refinementCandidate) []int {
	out := make([]int, 0, len(plan))
	for _, c := range plan {
		out = append(out, c.issue.Number)
	}
	return out
}
