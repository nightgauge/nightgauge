package sweep

// Producer: default-branch health (issue #90).
//
// A red required check on the default branch blocks every open PR in the
// repository at once. Nothing watched for it: the pipeline noticed only when an
// individual run reached its merge stage and failed there — one run at a time,
// each rediscovering the same repo-wide fact, none of them able to tell the
// operator that the fact is repo-wide.
//
// A red NON-required check blocks nothing, and for a long time this producer
// said nothing about it on that basis (issue #1250). That was a claim about
// blocking mistaken for a claim about health: a check can run on every PR,
// fail every time, turn the default branch red, and — because it is not
// required to merge — never once produce a signal. The merge gate and the
// post-merge hook carried the same required-only blind spot, so nothing else
// would say it either. The producer now cards that condition too, at `fyi`:
// worth knowing, interrupts nobody, and never dressed up as a fleet blocker.
//
// It is not the only observer of a red default branch. `merge-commit-checks`
// (internal/hooks) raises when the pipeline's own merge turns the branch red,
// and names the merge that did it. That is one fact seen from two vantage
// points, so this producer defers whenever that card is already open for the
// same branch (issue #1573) — the same shape as `human-gate` deferring to the
// run-scoped `branch-protection` producer, and for the same reason: the more
// SPECIFIC observation wins. "main is red" is a strictly weaker sentence than
// "main is red because PR #123's merge commit failed these checks", and the
// operator who gets both learns to read neither.
//
// The card this raises deliberately has no repair affordance. Nothing in the
// verb registry can fix a red `main`, and an option that implies otherwise is
// worse than no option: the operator clicks it, nothing changes, and the next
// card they see is one they have already learned to distrust. What it can do is
// name the specific failing check and link the run, because the operator's next
// action differs entirely between a flaky integration test and a dependency
// gate that started failing with no code change.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// ProducerDefaultBranchHealth is the stable producer id. It is half of the
// sticky (producer, idempotency_key) identity, so it must never change.
const ProducerDefaultBranchHealth = "default-branch-health"

// producerMergeCommitChecks is the producer this one defers to. It lives in
// internal/hooks as ProducerMergeCommitChecks; duplicating the literal here is
// deliberate, exactly as producerBranchProtection is duplicated in humangate.go
// — importing the hooks package from a sweep producer would invert the
// dependency and drag the post-merge machinery into every sweep.
const producerMergeCommitChecks = "merge-commit-checks"

// DefaultBranchGrace is how long a failing check is left alone before it is
// carded. CI fails transiently and is re-run green within minutes often enough
// that raising on first sight would train operators to wait the card out — the
// precise habit that makes an inbox worthless. The grace is measured from the
// check's own completion time, so it costs no state of our own and survives a
// process that was not running when the check failed.
const DefaultBranchGrace = 10 * time.Minute

// DefaultBranchHealth reports a default branch whose checks are red: at
// blocking_fleet when a required check is failing, at fyi when only
// non-required checks are.
type DefaultBranchHealth struct {
	// Grace overrides DefaultBranchGrace. Zero uses the default.
	Grace time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

func init() { Default.Register(&DefaultBranchHealth{}) }

// Name implements Producer.
func (p *DefaultBranchHealth) Name() string { return ProducerDefaultBranchHealth }

func (p *DefaultBranchHealth) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *DefaultBranchHealth) grace() time.Duration {
	if p.Grace > 0 {
		return p.Grace
	}
	return DefaultBranchGrace
}

// Evaluate implements Producer.
func (p *DefaultBranchHealth) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	meta, err := in.Forge.Repo().RepoMetadata(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve default branch for %s: %w", in.Repo, err)
	}
	branch := ""
	if meta != nil {
		branch = strings.TrimSpace(meta.DefaultBranch)
	}
	if branch == "" {
		// An empty repository, or an adapter that cannot report the default
		// branch. Guessing "main" here would produce a 404 that reads as a
		// producer failure forever; declining to observe is the honest answer.
		return nil, nil
	}

	if _, dup := in.OpenRequestForBranch(producerMergeCommitChecks, in.Repo, branch); dup {
		// A more specific producer already cards this exact branch, and names
		// the merge that turned it red. Returning nil here is the positive
		// assertion that THIS producer has nothing to say, so an older
		// default-branch-health card retracts and the operator is left with the
		// one that carries the merge — which is the outcome we want, not a
		// side effect to be worked around. It also saves the two check-run
		// reads below, which is real sweep budget.
		return nil, nil
	}

	required, err := in.Forge.CI().GetRequiredCheckNames(ctx, in.Owner, in.Name, branch)
	if err != nil {
		return nil, fmt.Errorf("read required checks for %s@%s: %w", in.Repo, branch, err)
	}

	runs, err := in.Forge.CI().GetIndividualCheckRuns(ctx, in.Owner, in.Name, branch)
	if err != nil {
		return nil, fmt.Errorf("read check runs for %s@%s: %w", in.Repo, branch, err)
	}

	blocking, advisory := p.failingChecks(runs, required)
	switch {
	case len(blocking) > 0:
		// A required check is red: nothing can land. This card is exactly what
		// it was before advisory failures were observed at all — its fingerprint
		// is the required set alone, so a non-required check flapping alongside
		// cannot re-alert a muted fleet blocker.
		return []attention.DecisionRequest{p.blockingRequest(in.Repo, branch, blocking)}, nil
	case len(advisory) > 0:
		// Only non-required checks are red. Nothing is blocked, so this is not
		// blocking_fleet and must not read like it — but the branch IS red, and
		// a repo with no required checks at all is the one configuration in
		// which no other mechanism will ever say so.
		return []attention.DecisionRequest{p.advisoryRequest(in.Repo, branch, advisory)}, nil
	default:
		// Green, pending, or no checks at all. An empty slice with a nil error
		// is the positive assertion that clears any standing card.
		return nil, nil
	}
}

// failedCheck is one failing required check, with enough context to name it.
type failedCheck struct {
	name        string
	url         string
	sha         string
	completedAt time.Time
	hasTime     bool
}

// failingChecks partitions the check runs that are conclusively failing and past
// the grace period into required (blocking) and non-required (advisory), each
// sorted by name so the fingerprint is stable.
//
// Two exclusions carry the AC, and apply to both classes alike:
//   - A check that is queued or in progress is NOT failing. A pending check on
//     a fresh commit is the normal state of a healthy branch someone just
//     pushed to.
//   - A check that completed inside the grace window is not yet carded, so a
//     failure that is re-run green in the next few minutes never surfaces at
//     all. A failure whose completion time is unknown is treated as past the
//     grace: the alternative is suppressing a real blocker indefinitely because
//     an adapter did not populate a timestamp.
//
// A third exclusion is the latest-run rule (issue #1572): a check name whose
// most recent completed run PASSED is not failing, however many older failing
// runs the forge still reports for it. See latestOutcomes.
func (p *DefaultBranchHealth) failingChecks(runs []forgetypes.CheckDetail, required []string) (blocking, advisory []failedCheck) {
	isRequired := make(map[string]bool, len(required))
	for _, name := range required {
		isRequired[name] = true
	}
	cutoff := p.now().Add(-p.grace())
	latest := latestOutcomes(runs)

	for _, run := range runs {
		if !isFailedConclusion(run.Conclusion) {
			continue
		}
		out, ok := latest[run.Name]
		if !ok || !out.failed {
			// A more recent completed run of this same check name passed, so
			// the check is green now and this run is history.
			continue
		}
		if out.hasTime && out.at.After(cutoff) {
			// The grace window belongs to the run that DECIDES the verdict, not
			// to each run of the name: an old failure cannot card a check whose
			// deciding failure happened a minute ago and may yet be re-run green.
			continue
		}
		fc := failedCheck{name: run.Name, url: run.DetailsURL, sha: run.HeadSHA}
		if ts, err := time.Parse(time.RFC3339, run.CompletedAt); err == nil {
			fc.completedAt, fc.hasTime = ts, true
			if out.hasPass && ts.Before(out.passedAt) {
				// An earlier failure episode the check has since recovered from.
				// Listing it would claim in the prose that the check has been
				// failing far longer than it has.
				continue
			}
		}
		if isRequired[run.Name] {
			blocking = append(blocking, fc)
		} else {
			advisory = append(advisory, fc)
		}
	}
	// Stable, so repeated runs of one check keep the order the forge reported
	// them in. The name sort alone leaves duplicates interchangeable, and the
	// representative run that ends up on the card would then depend on the sort
	// implementation rather than on the data.
	byName := func(out []failedCheck) func(i, j int) bool {
		return func(i, j int) bool { return out[i].name < out[j].name }
	}
	sort.SliceStable(blocking, byName(blocking))
	sort.SliceStable(advisory, byName(advisory))
	return blocking, advisory
}

// checkOutcome is the current verdict for one check NAME on the default branch,
// derived from the most recent run of that name that reached a pass/fail
// conclusion.
type checkOutcome struct {
	// failed is the deciding run's verdict.
	failed bool
	// at and hasTime are the deciding run's completion instant, when it
	// reported one. The grace period is measured from here.
	at      time.Time
	hasTime bool
	// passedAt and hasPass are the most recent SUCCESS for this name. When the
	// verdict is a failure, every failing run older than this belongs to an
	// episode the check has already recovered from.
	passedAt time.Time
	hasPass  bool
}

// latestOutcomes reduces the raw check runs to one verdict per check NAME: the
// most recent run that reached a pass/fail conclusion decides, and every older
// run of that name is history (issue #1572).
//
// Without this rule a scheduled workflow that failed once and has passed on
// every run since reads as "main is red" forever. The failing run never leaves
// the forge's list for the commit, so a producer that cards any failing run
// keeps carding it until someone pushes. Observed on a repository whose `sync`
// check failed at 01:13Z and then succeeded twice on the same unchanged commit,
// at 05:59Z and 10:36Z — the fleet blocker was still standing at 11:39Z, and
// nothing but a new commit would ever have cleared it.
//
// Three ordering rules, each chosen so the error falls toward carding a real
// failure rather than suppressing one:
//
//   - Only failure-class and SUCCESS conclusions vote. CANCELLED, SKIPPED,
//     NEUTRAL and STALE are non-verdicts — they are not evidence the check
//     passed, so they must neither raise a card nor clear one, and a cancelled
//     re-run therefore leaves the previous failure standing.
//   - A run carrying a completion time always outranks one that does not. An
//     adapter that omits the timestamp must not be able to pass an undated
//     success off as newer than a dated failure.
//   - When no run of a name is dated, no ordering exists at all, so any failure
//     wins. That is the pre-#1572 behaviour, kept for exactly the case the new
//     rule has no information to decide.
//
// This is upstream of distinctFailing and does not disturb it: the fingerprint
// is still the SET of failing check names, and re-runs of a still-failing check
// still collapse to one name. What changes is only WHICH names are failing.
func latestOutcomes(runs []forgetypes.CheckDetail) map[string]checkOutcome {
	out := make(map[string]checkOutcome, len(runs))
	for _, run := range runs {
		failed := isFailedConclusion(run.Conclusion)
		passed := isPassedConclusion(run.Conclusion)
		if !failed && !passed {
			continue
		}
		cand := checkOutcome{failed: failed}
		if ts, err := time.Parse(time.RFC3339, run.CompletedAt); err == nil {
			cand.at, cand.hasTime = ts, true
		}
		cur, seen := out[run.Name]
		if passed && cand.hasTime && (!cur.hasPass || cand.at.After(cur.passedAt)) {
			cur.passedAt, cur.hasPass = cand.at, true
		}
		if !seen || supersedes(cand, cur) {
			// The pass history is a property of the NAME, not of the deciding
			// run, so it survives the verdict being replaced.
			cand.passedAt, cand.hasPass = cur.passedAt, cur.hasPass
			cur = cand
		}
		out[run.Name] = cur
	}
	return out
}

// supersedes reports whether cand is the more recent verdict of the two. See
// latestOutcomes for why an undated run always loses and why a tie goes to the
// failure.
func supersedes(cand, cur checkOutcome) bool {
	if cand.hasTime != cur.hasTime {
		return cand.hasTime
	}
	if cand.hasTime && !cand.at.Equal(cur.at) {
		return cand.at.After(cur.at)
	}
	return cand.failed && !cur.failed
}

// distinctFailing collapses failing check RUNS into the set of distinct failing
// check NAMES, sorted, and picks the representative run URL to link.
//
// The distinction is load-bearing, not cosmetic (issue #538). A scheduled
// workflow that re-runs the same job against an unchanged red commit yields one
// failedCheck per RUN, so a single stuck check accumulates N entries. Every
// consumer of this list — the plural title arm, the blocker line, and above all
// the fingerprint — is written as though it were the SET of failing checks.
// Feeding it the multiset makes the fingerprint move every time the scheduler
// fires, which defeats mute-until-changed on the highest-severity card class:
// the operator mutes a known-red branch and is re-alerted the next morning by
// the same check failing the same way.
//
// The sort is part of the contract, not tidiness. The forge does not guarantee
// an ordering for check runs, so an unsorted set would reintroduce exactly the
// same fingerprint churn by another route.
//
// Only the name list is deduplicated. The body deliberately still lists every
// run, because how long and how often a check has been failing is the useful
// part of the prose.
func distinctFailing(failing []failedCheck) (names []string, url string) {
	// name -> representative run URL. Presence in the map is the dedup key, so
	// a check whose first run carries no link still counts as seen.
	urls := make(map[string]string, len(failing))
	names = make([]string, 0, len(failing))
	for _, f := range failing {
		if _, seen := urls[f.name]; !seen {
			names = append(names, f.name)
		}
		if urls[f.name] == "" {
			// First run of this check that actually carries a link. Dedup must
			// not strand Context.URL on a run that reported none.
			urls[f.name] = f.url
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		url = urls[names[0]]
	}
	return names, url
}

// isFailedConclusion reports whether a check-run conclusion is a definite
// failure. Adapters uppercase the forge's own vocabulary; anything that is not
// a recognised failure (including the empty conclusion of a running check) is
// not a blocker.
func isFailedConclusion(conclusion string) bool {
	switch strings.ToUpper(strings.TrimSpace(conclusion)) {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return true
	default:
		// CANCELLED, SKIPPED, NEUTRAL, STALE and SUCCESS all leave the merge
		// path open or are the operator's own doing.
		return false
	}
}

// isPassedConclusion reports whether a check-run conclusion is a definite pass.
// It is deliberately narrower than "not a failure": only a real SUCCESS is
// evidence that a check has recovered and may therefore retire an older
// failing run of the same name.
func isPassedConclusion(conclusion string) bool {
	return strings.EqualFold(strings.TrimSpace(conclusion), "SUCCESS")
}

// blockingRequest builds the standing observation for a default branch whose
// required checks are red.
func (p *DefaultBranchHealth) blockingRequest(repo, branch string, failing []failedCheck) attention.DecisionRequest {
	names, url := distinctFailing(failing)

	// Distinct checks, not runs: five re-runs of one stuck job are one failing
	// check, and reading "5 required checks failing" sends the operator looking
	// for four problems that do not exist.
	title := fmt.Sprintf("%s is red — %q is failing on %s", branch, names[0], repo)
	if len(names) > 1 {
		title = fmt.Sprintf("%s is red — %d required checks failing on %s", branch, len(names), repo)
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s:%s", ProducerDefaultBranchHealth, repo, branch),
		Kind:           attention.KindUnblock,
		// Nothing can land until this clears — that is the definition of
		// blocking_fleet, and the reason this is not one card per stalled run.
		Severity: attention.SeverityBlockingFleet,
		Title:    title,
		Body:     p.body(repo, branch, failing),
		// The fingerprint is WHICH checks are failing, nothing else — the sorted
		// SET of names, never the multiset of runs. Elapsed time moves on its
		// own and would re-alert every sweep; the commit SHA moves on every push
		// and would re-alert on unrelated commits while the same check stays
		// red; another run of an already-failing check is not news either. A
		// second check starting to fail is a genuine change, and does alert.
		Fingerprint: "checks:" + strings.Join(names, ","),
		Context: attention.Context{
			Repo:    repo,
			Branch:  branch,
			Blocker: fmt.Sprintf("required check(s) failing on %s: %s", branch, strings.Join(names, ", ")),
			URL:     url,
		},
		Options:       dismissOnly(),
		DefaultAction: attention.ExpireNoop,
	}
}

// advisoryRequest builds the standing observation for a default branch that is
// red only on checks nothing requires. Same identity as the blocking card —
// one card per branch, whichever class it is in — but a different fingerprint
// namespace: a check moving between "required and failing" and "not required
// and failing" is a material change of condition even when its name is not.
func (p *DefaultBranchHealth) advisoryRequest(repo, branch string, failing []failedCheck) attention.DecisionRequest {
	names, url := distinctFailing(failing)

	title := fmt.Sprintf("%s is red — non-required check %q is failing on %s", branch, names[0], repo)
	if len(names) > 1 {
		title = fmt.Sprintf("%s is red — %d non-required checks failing on %s", branch, len(names), repo)
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s:%s", ProducerDefaultBranchHealth, repo, branch),
		Kind:           attention.KindUnblock,
		// Nothing is blocked. Below blocking_run too: no unit of work waits on
		// this. It is a badge, not an interruption.
		Severity: attention.SeverityFYI,
		Title:    title,
		Body:     p.advisoryBody(repo, branch, failing),
		// The sorted SET of failing names, as for the blocking card — never a
		// run count, a commit, or a timestamp. See blockingRequest.
		Fingerprint: "advisory-checks:" + strings.Join(names, ","),
		Context: attention.Context{
			Repo:    repo,
			Branch:  branch,
			Blocker: fmt.Sprintf("non-required check(s) failing on %s: %s", branch, strings.Join(names, ", ")),
			URL:     url,
		},
		Options:       dismissOnly(),
		DefaultAction: attention.ExpireNoop,
	}
}

// dismissOnly is the only honest affordance for a red branch. It does not
// repair anything; it records that a human looked and decided this is not
// worth acting on. Resolving suppresses the card until the set of failing
// checks changes, so it cannot be used to silence a NEW failure.
func dismissOnly() []attention.Option {
	return []attention.Option{
		{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
	}
}

func (p *DefaultBranchHealth) body(repo, branch string, failing []failedCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Required checks are failing on %s (%s), so every open PR in the repository is blocked until this clears.\n\n", branch, repo)
	p.writeRuns(&b, failing)
	b.WriteString("\nNightgauge cannot fix this: the next action is a human's. ")
	b.WriteString("Mute the card while you work on it (`nightgauge attention mute <id>`) — it re-alerts if a different check starts failing.")
	return b.String()
}

func (p *DefaultBranchHealth) advisoryBody(repo, branch string, failing []failedCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Checks that are not required to merge are failing on %s (%s). Nothing is blocked, but the branch is red and nothing else will say so.\n\n", branch, repo)
	p.writeRuns(&b, failing)
	b.WriteString("\nNightgauge cannot fix this: the next action is a human's. ")
	b.WriteString("Mute the card while you work on it (`nightgauge attention mute <id>`) — it re-alerts if a different check starts failing.")
	return b.String()
}

// writeRuns lists every failing RUN, deliberately not deduplicated: how long and
// how often a check has been failing is the useful part of the prose.
func (p *DefaultBranchHealth) writeRuns(b *strings.Builder, failing []failedCheck) {
	for _, f := range failing {
		fmt.Fprintf(b, "- %s", f.name)
		if f.hasTime {
			fmt.Fprintf(b, " — failing for %s", humanizeDuration(p.now().Sub(f.completedAt)))
		}
		if f.sha != "" {
			fmt.Fprintf(b, " (commit %s)", shortSHA(f.sha))
		}
		if f.url != "" {
			fmt.Fprintf(b, "\n  %s", f.url)
		}
		b.WriteString("\n")
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// humanizeDuration renders a coarse "how long" for card prose. Deliberately
// low-resolution: it appears only in the body, never in the fingerprint, and a
// precise figure would invite readers to treat it as stable.
func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
