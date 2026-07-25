package sweep

// Producer: human gate (issue #91).
//
// The pipeline's own finished output can sit idle indefinitely. A PR that is
// green but unmergeable — review required, merge conflict, stale base — is work
// that is DONE and waiting on a person who has not been told.
//
// There was a near-miss already in the tree: the orchestrator's
// `branch-protection` producer raises exactly this card, but only when a run
// reaches its merge stage and punts. A PR opened by a human, or by a run that
// has since ended, was invisible to it. This producer closes that hole from the
// repo scan, and defers to the run-scoped one whenever both can see the same PR
// (Input.OpenRequestForPR) so the operator gets one card, not two.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ProducerHumanGate is the stable producer id.
const ProducerHumanGate = "human-gate"

// producerBranchProtection is the run-scoped producer this one defers to. It
// lives in internal/orchestrator; duplicating the literal here is deliberate,
// because importing the orchestrator from a producer would invert the
// dependency and drag the scheduler into every sweep.
const producerBranchProtection = "branch-protection"

// HumanGateMaxIndividual is how many blocked PRs are carded individually
// before the producer collapses them into one. A repo with thirty stale green
// PRs does not have thirty decisions to make; it has one backlog problem, and
// thirty cards would bury every other card in the inbox.
const HumanGateMaxIndividual = 5

// HumanGateMaxInspect bounds the producer's forge traffic. The sweep runs on
// activation, on refresh, and on a timer, so an unbounded scan of a busy repo
// would be a recurring cost with no ceiling.
const HumanGateMaxInspect = 50

// HumanGate reports open PRs that are green and waiting on a person.
type HumanGate struct {
	// MaxIndividual overrides HumanGateMaxIndividual. Zero uses the default.
	MaxIndividual int
	// Now overrides the clock (tests).
	Now func() time.Time
}

func init() { Default.Register(&HumanGate{}) }

// Name implements Producer.
func (p *HumanGate) Name() string { return ProducerHumanGate }

func (p *HumanGate) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *HumanGate) maxIndividual() int {
	if p.MaxIndividual > 0 {
		return p.MaxIndividual
	}
	return HumanGateMaxIndividual
}

// Evaluate implements Producer.
func (p *HumanGate) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	prs, err := in.Forge.PRs().ListPRs(ctx, in.Owner, in.Name, "OPEN", "")
	if err != nil {
		return nil, fmt.Errorf("list open PRs for %s: %w", in.Repo, err)
	}
	if len(prs) > HumanGateMaxInspect {
		prs = prs[:HumanGateMaxInspect]
	}

	var gated []gatedPR
	for _, pr := range prs {
		gate, ok := classifyGate(pr)
		if !ok {
			continue
		}
		// The run-scoped producer already told the operator about this PR.
		if _, dup := in.OpenRequestForPR(producerBranchProtection, pr.Number); dup {
			continue
		}
		gated = append(gated, gatedPR{pr: pr, gate: gate})
	}
	if len(gated) == 0 {
		return nil, nil
	}
	sort.Slice(gated, func(i, j int) bool { return gated[i].pr.Number < gated[j].pr.Number })

	if len(gated) > p.maxIndividual() {
		return []attention.DecisionRequest{p.aggregate(in.Repo, gated)}, nil
	}
	out := make([]attention.DecisionRequest, 0, len(gated))
	for _, g := range gated {
		out = append(out, p.individual(in.Repo, g))
	}
	return out, nil
}

// gate names why a green PR cannot merge, and who has to act.
type gate struct {
	// kind is approve when a review is the sole remaining requirement, and
	// unblock when the merge state itself is the obstacle. The distinction is
	// not cosmetic: approve means the operator IS the missing step, unblock
	// means they have to go do something first.
	kind attention.Kind
	// reason is the precise blocker, in the operator's words not the API's.
	reason string
	// code is the stable machine form, used in the fingerprint.
	code string
}

type gatedPR struct {
	pr   types.PullRequest
	gate gate
}

// classifyGate decides whether an open PR is green and blocked on a human.
//
// The boundaries matter more than the mapping. A red PR is the author's
// problem and is already visible to them — carding it is noise. A draft is not
// asking to be merged. A PR whose checks are still running has not finished
// being the pipeline's problem yet. Each of those is excluded here rather than
// filtered later, so there is exactly one place to read the rule.
func classifyGate(pr types.PullRequest) (gate, bool) {
	if pr.IsDraft {
		return gate{}, false
	}
	if strings.ToUpper(pr.State) != "OPEN" {
		return gate{}, false
	}
	// Green means the rollup says SUCCESS. An empty rollup (a repo with no CI
	// at all) is not evidence of green, and treating it as such would card
	// every open PR in a repo that has no checks.
	if strings.ToUpper(pr.CheckStatus) != "SUCCESS" {
		return gate{}, false
	}

	review := strings.ToUpper(pr.ReviewStatus)
	switch strings.ToUpper(pr.MergeStateStatus) {
	case "BLOCKED":
		if review == string(types.ReviewReviewRequired) {
			return gate{kind: attention.KindApprove, reason: "a review is required", code: "review_required"}, true
		}
		if review == string(types.ReviewChangesRequested) {
			// The author was told by the reviewer. Not our card to raise.
			return gate{}, false
		}
		return gate{kind: attention.KindUnblock, reason: "branch protection is blocking the merge", code: "branch_protection"}, true
	case "DIRTY":
		return gate{kind: attention.KindUnblock, reason: "the branch has merge conflicts", code: "conflict"}, true
	case "BEHIND":
		return gate{kind: attention.KindUnblock, reason: "the base branch has moved on", code: "behind"}, true
	default:
		// CLEAN and HAS_HOOKS are mergeable. UNSTABLE means a check is failing
		// or pending despite the rollup. UNKNOWN means the forge has not
		// finished computing mergeability — asking again next sweep is free,
		// guessing is not. DRAFT is already excluded above.
		return gate{}, false
	}
}

// individual builds one card for one blocked PR.
func (p *HumanGate) individual(repo string, g gatedPR) attention.DecisionRequest {
	pr := g.pr
	waited := p.waitedFor(pr.CreatedAt)

	body := fmt.Sprintf("PR #%d (%s) passed CI and cannot merge: %s.\n\n", pr.Number, pr.Title, g.gate.reason)
	if waited != "" {
		body += fmt.Sprintf("It has been open %s. ", waited)
	}
	body += "The work is finished; the next step is a person's.\n"
	if pr.URL != "" {
		body += "\n" + pr.URL + "\n"
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s#%d", ProducerHumanGate, repo, pr.Number),
		Kind:           g.gate.kind,
		// One unit of work is stalled, not the fleet. A repo can have several
		// of these and still be shipping everything else.
		Severity:    attention.SeverityBlockingRun,
		Title:       fmt.Sprintf("PR #%d is green and waiting — %s", pr.Number, g.gate.reason),
		Body:        body,
		Fingerprint: "gate:" + g.gate.code,
		Context: attention.Context{
			Repo:    repo,
			PR:      pr.Number,
			Blocker: g.gate.reason,
			URL:     pr.URL,
		},
		Options: []attention.Option{
			// No verb in the registry can approve a PR or rebase a branch, and
			// inventing one to make the card look actionable would be a lie the
			// operator only discovers after clicking. Dismiss records the human
			// decision and suppresses re-raising until the gate itself changes.
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// aggregate collapses a backlog of blocked PRs into one card.
//
// The fingerprint is the set of PR numbers, so the card re-alerts when the
// backlog composition changes and stays quiet when it merely persists. Using
// the COUNT would hide a swap (one merges, another blocks) behind an unchanged
// number.
func (p *HumanGate) aggregate(repo string, gated []gatedPR) attention.DecisionRequest {
	nums := make([]string, 0, len(gated))
	var b strings.Builder
	fmt.Fprintf(&b, "%d green PRs are open and blocked on a person — more than the %d this surface cards individually.\n\n",
		len(gated), p.maxIndividual())
	for _, g := range gated {
		nums = append(nums, strconv.Itoa(g.pr.Number))
		fmt.Fprintf(&b, "- #%d %s — %s\n", g.pr.Number, g.pr.Title, g.gate.reason)
	}
	b.WriteString("\nThis is a backlog, not a decision. Work the list down; individual cards return below the threshold.")

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s:backlog", ProducerHumanGate, repo),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("%d green PRs are waiting on a person in %s", len(gated), repo),
		Body:           b.String(),
		Fingerprint:    "backlog:" + strings.Join(nums, ","),
		Context: attention.Context{
			Repo:    repo,
			Blocker: fmt.Sprintf("%d green PRs blocked on a human", len(gated)),
		},
		Options: []attention.Option{
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// waitedFor renders how long a PR has been open, or "" when the forge did not
// give a parseable creation time.
func (p *HumanGate) waitedFor(createdAt string) string {
	ts, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return ""
	}
	return humanizeDuration(p.now().Sub(ts))
}
