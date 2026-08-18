package sweep

// Producer: stale Dependabot remediation (issue #649).
//
// The forge writes the fix, opens the pull request, and then nothing happens.
// #345 recorded dependency PRs open 40+ days on a repository with security
// updates enabled — a working automated fix, sitting in an open PR, the whole
// time. `DependabotPRService.ts` measured exactly that (`staleDays` against a
// 7-day threshold) and rendered a count on a tab; #626 deleted that surface
// rather than ship its first revision, which exposed a `getEscalations()` API
// with zero production callers and reproduced the complaint inside the fix.
//
// A SEPARATE STANDING CONDITION FROM dependabot-alerts, and that separation is
// the whole design, not a packaging choice.
//
// `alertFingerprint` in dependabotalerts.go deliberately EXCLUDES elapsed time,
// because a fingerprint that moves on its own re-alerts on every sweep
// (docs/ATTENTION_PRODUCERS.md invariant 2). Staleness is precisely the axis
// that producer refuses to observe. Folding it in would mean either giving that
// producer a time-varying fingerprint — spamming the inbox for every open
// advisory in the workspace — or leaving this condition unreported, which is
// where it started. So this is its own producer id, its own idempotency key,
// and its own fingerprint, and the fingerprint QUANTISES the elapsed time into
// whole multiples of the threshold: it moves on a transition ("still waiting,
// and now it has been two thresholds") and never merely because the clock did.
//
// NOT RAISED FROM THE SCHEDULER, and that was considered and rejected in #626.
// `s.raiseAttention` is callable from scheduler.go, but the reconciler
// auto-resolves the cards of EVALUATED producers only — so a run-scoped raise
// of this producer id would never be retracted when the PR merged, and would
// stand until StandingExpiry (30 days). A second escalation path that cannot
// retract is worse than the gap it fills. The sweep reconciler is the one
// writer.
//
// NO REPAIR VERB, following defaultbranch.go, humangate.go and
// dependabotalerts.go: nothing in the closed verb registry
// (internal/attention/verbs.go) can merge a pull request, and a button that
// implies otherwise is worse than no button. The next action rides on
// Context.URL, which points at the PR.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// ProducerDependabotStaleRemediation is the stable producer id. It is half of
// the sticky (producer, idempotency_key) identity, so it must never change.
const ProducerDependabotStaleRemediation = "dependabot-stale-remediation"

// DefaultStaleRemediationThreshold is the compiled fallback for
// `attention.dependabot_stale_remediation_days` — the same seven days
// `DependabotPRService.ts` measured against before #626 removed it, so an
// operator who never opens config.yaml gets the behaviour that was already
// being computed for them.
const DefaultStaleRemediationThreshold = 7 * 24 * time.Hour

// StaleRemediationMaxInspect bounds this producer's per-PR forge traffic.
//
// The alert read is one request for the whole repo; confirming that a
// remediation PR is still open costs one request PER PR, and producers share a
// single 30-second sweep budget. The cheap upper-bound filter below means the
// common repo spends ZERO of these — a PR can never be older than the advisory
// that caused it, so an advisory inside the threshold is skipped without any PR
// lookup at all. Only advisories already past the threshold are inspected, and
// those are exactly the ones about to be carded.
const StaleRemediationMaxInspect = 20

// errStaleRemediationTooMany is returned when more remediation PRs are past the
// threshold than one sweep will inspect.
//
// An ERROR rather than a truncated answer, for the same reason as
// errAlertsTruncated: reconciling a partial observation auto-resolves the cards
// this producer did not get to, with the reason "condition_cleared", for PRs
// that are still sitting there. Not looking is not the same as looking and
// finding nothing (docs/ATTENTION_PRODUCERS.md invariant 1).
var errStaleRemediationTooMany = errors.New("more remediation PRs are past the staleness threshold than one sweep inspects")

// DependabotStaleRemediation cards a Dependabot remediation PR that has been
// open past the configured threshold, one card per PR.
type DependabotStaleRemediation struct {
	// Now overrides the clock (tests).
	Now func() time.Time
}

func init() { Default.Register(&DependabotStaleRemediation{}) }

// Name implements Producer.
func (p *DependabotStaleRemediation) Name() string { return ProducerDependabotStaleRemediation }

func (p *DependabotStaleRemediation) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// staleRemediationThreshold resolves the operator's threshold from config.
//
// Every failure — no workspace root, no config file, an unparseable one, a
// zero or negative value — falls back to the compiled default rather than
// erroring. A configuration problem is not a failure to OBSERVE THE REPO, and
// routing it into Evaluate's error return would freeze every card this producer
// has open for as long as the config stayed broken. The config file has loud
// readers elsewhere; this one is deliberately quiet.
func staleRemediationThreshold(workspaceRoot string) time.Duration {
	if strings.TrimSpace(workspaceRoot) == "" {
		return DefaultStaleRemediationThreshold
	}
	cfg, err := config.Load(workspaceRoot)
	if err != nil || cfg == nil || cfg.Attention == nil {
		return DefaultStaleRemediationThreshold
	}
	if days := cfg.Attention.DependabotStaleRemediationDays; days > 0 {
		return time.Duration(days) * 24 * time.Hour
	}
	return DefaultStaleRemediationThreshold
}

// staleRemediation is one remediation PR and every advisory it fixes.
//
// ONE CARD PER PR, not per advisory, and this grouping is load-bearing rather
// than cosmetic. A single Dependabot PR routinely fixes several advisories at
// once (one bump, several CVEs in the same package). The idempotency key is
// keyed on the PR, so emitting one observation per advisory would put the same
// key in one sweep more than once — which ReconcileStanding rejects outright
// ("duplicate idempotency_key in one sweep"), failing the WHOLE sweep for the
// repo and taking every other producer's reconciliation down with it.
type staleRemediation struct {
	pr      int
	prTitle string
	prURL   string
	// waited is measured from the OLDEST advisory the PR fixes: if the PR has
	// been carrying an unmerged fix for two of them, the wait that matters is
	// the longer one.
	waited  time.Duration
	alerts  []forgetypes.SecurityAlert
	alertAt time.Time
}

// Evaluate implements Producer.
//
// One security read per repo per sweep (shared with dependabot-alerts only in
// the sense that both make the same call — producers do not share results, by
// design: a producer that depended on another's observation would be evaluated
// twice or not at all depending on registration order). Then at most
// StaleRemediationMaxInspect PR reads, and in a repo with nothing overdue,
// none.
func (p *DependabotStaleRemediation) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	threshold := staleRemediationThreshold(in.WorkspaceRoot)

	res, err := in.Forge.Security().ListOpenAlerts(ctx, in.Owner, in.Name)
	if err != nil {
		// Shared with dependabot-alerts deliberately: it is the same forge
		// surface, so a security-scope denial must be downgraded to this
		// producer's own bad day here too, rather than switching every other
		// producer off for the repository. See alertReadError.
		return nil, alertReadError(in.Repo, err)
	}
	if res == nil {
		return nil, fmt.Errorf("read security alerts for %s: forge returned no result", in.Repo)
	}
	if !res.Enabled() {
		// Scanning off asserts nothing about whether a remediation PR is
		// sitting there — nobody looked. An empty observation here would
		// retract live cards on a repository setting being toggled.
		return nil, fmt.Errorf("%s: %w", in.Repo, errAlertsDisabled)
	}
	if res.Truncated {
		return nil, fmt.Errorf("%s: %w (%d open, %d returned per request)",
			in.Repo, errAlertsTruncated, res.TotalOpen, forge.MaxSecurityAlertsPerRequest)
	}

	candidates := p.overdue(res.Alerts, threshold)
	if len(candidates) == 0 {
		// A positive assertion that no remediation PR is overdue — this is what
		// retracts the card of a PR that was merged, closed, or whose advisory
		// was dismissed since the last sweep.
		return nil, nil
	}
	if len(candidates) > StaleRemediationMaxInspect {
		return nil, fmt.Errorf("%s: %w (%d past the threshold, %d inspected per sweep)",
			in.Repo, errStaleRemediationTooMany, len(candidates), StaleRemediationMaxInspect)
	}

	prs := in.Forge.PRs()
	if prs == nil {
		return nil, fmt.Errorf("%s: forge client exposes no pull-request service", in.Repo)
	}

	out := make([]attention.DecisionRequest, 0, len(candidates))
	for _, c := range candidates {
		// THE RETRACTION SIGNAL. The advisory alone cannot say whether the PR
		// is still open: the forge's dependabotUpdate carries the pull request
		// whatever state it is in, so a PR somebody closed unmerged still reads
		// as `pr_open` while the alert stays open. Asking the PR itself is the
		// only forge-neutral answer, and it is authoritative per PR — unlike
		// ListPRs, which returns a bounded page with no truncation signal and
		// would therefore let a busy repo's open PR read as "gone".
		pr, err := prs.GetPR(ctx, in.Owner, in.Name, c.pr)
		if err != nil {
			// "I could not look" — never an empty observation. The existing
			// cards are left exactly where they are.
			return nil, fmt.Errorf("read remediation PR #%d for %s: %w", c.pr, in.Repo, err)
		}
		if pr == nil {
			return nil, fmt.Errorf("read remediation PR #%d for %s: forge returned no pull request", c.pr, in.Repo)
		}
		if !strings.EqualFold(pr.State, "OPEN") {
			// Merged or closed: the condition this card describes is over.
			// Omitting it from the observation is what auto-resolves the card.
			continue
		}
		if pr.URL != "" {
			c.prURL = pr.URL
		}
		if pr.Title != "" {
			c.prTitle = pr.Title
		}
		out = append(out, p.request(in, c, threshold))
	}
	return out, nil
}

// overdue groups the open alerts that already have a remediation PR by that PR,
// keeps the ones whose wait has passed the threshold, and returns them in PR
// order so a sweep's forge traffic and its observation list are reproducible.
//
// THE WAIT IS MEASURED FROM THE ADVISORY'S first-seen TIMESTAMP, not from the
// pull request's creation date, and that is a constraint of the forge
// abstraction rather than a preference. `forgetypes.PullRequest.CreatedAt` is
// populated by GitHub's ListPRs alone: GitHub's GetPR does not select it and
// the GitLab adapter never sets it at all (internal/gitlab/mrs.go's
// toForgePR), so a producer keyed on it would silently measure zero on GitLab
// and card every remediation PR the instant it appeared. The alert's
// FirstSeenAt is required of every adapter, costs no extra request, and
// survives a process that was not running when the alert appeared — the same
// argument DependabotAlertGrace already makes one file over.
//
// What it measures is therefore "this advisory has been open this long and its
// fix is still unmerged", which the forge opens within minutes of raising the
// alert. The one case where the two diverge is a patch published long after the
// advisory (RemediationNone → RemediationPROpen), where the advisory's age
// exceeds the PR's. The card states the measurement it made rather than
// implying the other one.
//
// An alert whose first-seen the forge did not populate is SKIPPED, not carded:
// with no timestamp there is no elapsed time, and this producer's entire claim
// is about elapsed time. dependabot-alerts still cards the advisory itself in
// that case, so the operator is not left with nothing — the opposite call to
// the one that file makes for the same missing field, because the two producers
// are asserting different things.
func (p *DependabotStaleRemediation) overdue(alerts []forgetypes.SecurityAlert, threshold time.Duration) []*staleRemediation {
	now := p.now()
	byPR := make(map[int]*staleRemediation)
	for _, a := range alerts {
		if a.Remediation.State != forgetypes.RemediationPROpen || a.Remediation.PRNumber <= 0 {
			continue
		}
		seen, err := time.Parse(time.RFC3339, a.FirstSeenAt)
		if err != nil {
			continue
		}
		g, ok := byPR[a.Remediation.PRNumber]
		if !ok {
			g = &staleRemediation{
				pr:      a.Remediation.PRNumber,
				prTitle: a.Remediation.PRTitle,
				prURL:   a.Remediation.PRURL,
				alertAt: seen,
			}
			byPR[a.Remediation.PRNumber] = g
		}
		if seen.Before(g.alertAt) {
			g.alertAt = seen
		}
		g.alerts = append(g.alerts, a)
	}

	out := make([]*staleRemediation, 0, len(byPR))
	for _, g := range byPR {
		g.waited = now.Sub(g.alertAt)
		if g.waited < threshold {
			continue
		}
		sort.Slice(g.alerts, func(i, j int) bool {
			if ri, rj := g.alerts[i].Severity.Rank(), g.alerts[j].Severity.Rank(); ri != rj {
				return ri > rj
			}
			return g.alerts[i].Number < g.alerts[j].Number
		})
		out = append(out, g)
	}
	// Go randomises map iteration, so without this the observation order — and
	// anything derived from it — would differ between two sweeps of an
	// unchanged repo.
	sort.Slice(out, func(i, j int) bool { return out[i].pr < out[j].pr })
	return out
}

// request builds the standing observation for one overdue remediation PR.
func (p *DependabotStaleRemediation) request(in Input, g *staleRemediation, threshold time.Duration) attention.DecisionRequest {
	buckets := staleBuckets(g.waited, threshold)
	return attention.DecisionRequest{
		// Sticky identity is the PR number: it is stable for the life of the
		// pull request, and a remediation the forge re-opens as a new PR is
		// genuinely a new condition rather than a resurrection of this one.
		IdempotencyKey: fmt.Sprintf("%s:%s#%d", ProducerDependabotStaleRemediation, in.Repo, g.pr),
		// The fix is written. Nothing here is a decision about WHAT to do; a
		// person has to go and merge or close it.
		Kind: attention.KindUnblock,
		// blocking_run, where dependabot-alerts is fyi — and the difference is
		// the escalation this producer exists to be. That vocabulary describes
		// what is BLOCKED, not what is important: an open advisory blocks
		// nothing (there may be no fix to apply), while a finished, mergeable
		// unit of work nobody has merged is precisely one stalled unit of work.
		// human-gate makes the identical call, in the identical words, for a
		// green PR waiting on a person.
		Severity:    attention.SeverityBlockingRun,
		Title:       staleTitle(g, buckets, threshold),
		Body:        staleBody(in, g, threshold),
		Fingerprint: staleFingerprint(g, buckets, threshold),
		Context: attention.Context{
			Repo: in.Repo,
			PR:   g.pr,
			// The same PR another producer's Context.PR names, so surfaces that
			// group by pull request put this card next to the advisory it
			// escalates instead of in a second place.
			Blocker: staleBlocker(g, buckets, threshold),
			URL:     staleURL(g),
		},
		Options: []attention.Option{
			// The only honest button — see the file header. Resolving it
			// suppresses the card until the fingerprint moves, so a dismissal
			// cannot silence the NEXT threshold multiple.
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// staleBuckets quantises the wait into whole multiples of the threshold, and is
// the reason this producer can observe elapsed time at all.
//
// docs/ATTENTION_PRODUCERS.md invariant 2 forbids a fingerprint containing
// anything that moves on its own, because it re-alerts on every sweep. A
// quantised duration does not move on its own: it is constant for a whole
// threshold window and steps exactly when the condition genuinely changes
// character ("a week overdue" → "two weeks overdue"), which is a transition an
// operator acts on differently. The floor is 1 — the producer only ever sees
// waits that already passed the threshold.
func staleBuckets(waited, threshold time.Duration) int {
	if threshold <= 0 {
		return 1
	}
	n := int(waited / threshold)
	if n < 1 {
		n = 1
	}
	return n
}

// staleFingerprint is the MATERIAL state: how many whole thresholds the fix has
// been waiting, and which advisories it is the fix FOR.
//
// The threshold itself is part of it, because it is the unit the bucket count
// is expressed in: an operator who halves `dependabot_stale_remediation_days`
// has changed what "2" means, and a fingerprint that hid that would leave the
// card asserting the old figure until the next multiple came round.
//
// The advisory set is included and SORTED, so a second advisory attaching to
// the same PR is a real transition, and so the forge's own ordering cannot fake
// one. Severity is deliberately excluded: dependabot-alerts already re-alerts
// on a severity change with a card that names the severity, and repeating that
// here would alert twice for one event.
func staleFingerprint(g *staleRemediation, buckets int, threshold time.Duration) string {
	keys := make([]string, 0, len(g.alerts))
	for _, a := range g.alerts {
		keys = append(keys, advisoryKey(a))
	}
	sort.Strings(keys)
	return fmt.Sprintf("waited:%dx%dd;alerts:%s", buckets, thresholdDays(threshold), strings.Join(keys, ","))
}

// thresholdDays renders the threshold in whole days, the unit config states it
// in. A sub-day threshold (only reachable from a test) floors to 0 rather than
// rounding up, so the rendered unit never claims a size the operator did not
// configure.
func thresholdDays(threshold time.Duration) int { return int(threshold / (24 * time.Hour)) }

// advisoryKey identifies one advisory in a way that survives the forge omitting
// fields, falling through the same chain dependabot-alerts uses for its prose.
func advisoryKey(a forgetypes.SecurityAlert) string {
	switch {
	case a.AdvisoryID != "":
		return a.AdvisoryID
	case a.CVE != "":
		return a.CVE
	default:
		return fmt.Sprintf("alert#%d", a.Number)
	}
}

// staleWaitedLabel renders the bucket as the operator's unit — "7+ days",
// "14+ days" — rather than the exact elapsed time.
//
// The title has to be STABLE inside a bucket for the same reason the
// fingerprint does: a title that counted real days would rewrite the card every
// sweep, and although a refresh does not alert, a surface that renders a
// different sentence every thirty seconds reads as activity where there is
// none. The exact figure belongs in the body, as prose (invariant 2).
func staleWaitedLabel(buckets int, threshold time.Duration) string {
	days := buckets * thresholdDays(threshold)
	if days <= 0 {
		return fmt.Sprintf("%d× the staleness threshold", buckets)
	}
	return fmt.Sprintf("%d+ days", days)
}

// staleSubject names what the PR is fixing in one token and never renders
// empty: the package when one PR covers one package, the count when it covers
// several, and the PR itself when the forge reported no package at all.
func staleSubject(g *staleRemediation) string {
	pkgs := make([]string, 0, len(g.alerts))
	seen := make(map[string]bool, len(g.alerts))
	for _, a := range g.alerts {
		if a.Package == "" || seen[a.Package] {
			continue
		}
		seen[a.Package] = true
		pkgs = append(pkgs, a.Package)
	}
	sort.Strings(pkgs)
	switch len(pkgs) {
	case 0:
		return fmt.Sprintf("PR #%d", g.pr)
	case 1:
		return pkgs[0]
	default:
		return fmt.Sprintf("%d packages", len(pkgs))
	}
}

// staleTitle names the package, the wait and the PR — the three facts that
// decide whether the operator opens it now.
func staleTitle(g *staleRemediation, buckets int, threshold time.Duration) string {
	return fmt.Sprintf("Dependabot's fix for %s has been waiting %s — PR #%d",
		staleSubject(g), staleWaitedLabel(buckets, threshold), g.pr)
}

// staleBlocker is the one-line machine-facing summary surfaces show inline.
func staleBlocker(g *staleRemediation, buckets int, threshold time.Duration) string {
	return fmt.Sprintf("remediation PR #%d unmerged for %s (%s in %s)",
		g.pr, staleWaitedLabel(buckets, threshold), advisoryKey(g.alerts[0]), staleSubject(g))
}

// staleURL points at the one place the operator can act: the pull request.
// The alert page is the fallback, because a card about an unmerged fix that
// cannot link to the fix is still worth more than no link.
func staleURL(g *staleRemediation) string {
	if g.prURL != "" {
		return g.prURL
	}
	for _, a := range g.alerts {
		if a.URL != "" {
			return a.URL
		}
	}
	return ""
}

// staleBody states what was OBSERVED, and — using Input.Existing — states it
// WITHOUT re-writing a card the operator already has.
//
// THE NON-DUPLICATION RULE, and why it is a content rule rather than a skip.
// dependabot-alerts cards EVERY open advisory, so for every alert this producer
// can see there is, in the ordinary case, an open card from that producer whose
// Context.PR is this same PR. The human-gate shape — `OpenRequestForPR(other,
// pr)` and `continue` — is therefore not available here: it would suppress this
// producer for as long as the advisory card stayed open, which is the entire
// 40-day window #649 exists to catch, and it would make this producer's own
// card come and go with ANOTHER producer's card lifecycle. That second part is
// not hypothetical: dependabotalerts.go's header records the same suppression
// being tried from the other direction, where it made the reconciler
// auto-resolve a live vulnerability as "condition_cleared" and re-raise it as a
// new card once the other card was dismissed.
//
// The two are genuinely different conditions — "there is an advisory" and "the
// fix for it has not been merged in two weeks" — and they retract on different
// events. What must not be duplicated is the advisory's OWN prose, which the
// advisory card exists to carry. So an advisory that already has an open
// dependabot-alerts card is named and pointed at; one that does NOT (dismissed,
// expired, or raised before this producer existed) is stated in full here,
// because then this card is the only place it is stated.
//
// Nothing on this path reaches the fingerprint, so the body moving when the
// advisory card is dismissed is a refresh — content, not an alert.
func staleBody(in Input, g *staleRemediation, threshold time.Duration) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Dependabot opened a fix for %s and nobody has merged it.\n\n", in.Repo)
	fmt.Fprintf(&b, "- Pull request: #%d", g.pr)
	if g.prTitle != "" {
		fmt.Fprintf(&b, " (%s)", g.prTitle)
	}
	b.WriteString("\n")
	if g.prURL != "" {
		fmt.Fprintf(&b, "  %s\n", g.prURL)
	}
	fmt.Fprintf(&b, "- Unmerged for: %s (threshold: %d days)\n",
		humanizeDuration(g.waited), thresholdDays(threshold))
	b.WriteString("- Measured from the advisory's first-seen timestamp, which is the only creation\n")
	b.WriteString("  time both forge adapters report. The forge opens the remediation PR within\n")
	b.WriteString("  minutes of raising the alert; where a patch was published long afterwards this\n")
	b.WriteString("  figure is the advisory's age, which is the older of the two.\n")

	fmt.Fprintf(&b, "\nAdvisories this PR fixes (%d):\n", len(g.alerts))
	for _, a := range g.alerts {
		fmt.Fprintf(&b, "- %s — %s severity", advisoryKey(a), a.Severity)
		if a.Package != "" {
			fmt.Fprintf(&b, " in %s", a.Package)
		}
		if a.FirstPatchedVersion != "" {
			fmt.Fprintf(&b, ", fixed in %s", a.FirstPatchedVersion)
		}
		if advisoryAlreadyCarded(in, a) {
			b.WriteString("\n  Already in the Action Center as its own advisory card.")
		} else {
			// No advisory card is open for this one, so this card is the only
			// place it is described. State it rather than assume it is stated
			// elsewhere.
			if a.Summary != "" {
				fmt.Fprintf(&b, "\n  %s", a.Summary)
			}
			if a.AdvisoryURL != "" {
				fmt.Fprintf(&b, "\n  %s", a.AdvisoryURL)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\nNightgauge cannot merge this for you: no verb in the registry merges a pull ")
	b.WriteString("request, and the fix still has to pass CI. ")
	fmt.Fprintf(&b, "The next action is to review and merge PR #%d, or close it and decide "+
		"something else. ", g.pr)
	b.WriteString("Mute the card while you work on it (`nightgauge attention mute <id>`) — it ")
	b.WriteString("re-alerts at the next whole threshold, and when the set of advisories changes.")
	return b.String()
}

// advisoryAlreadyCarded reports whether dependabot-alerts has an OPEN card for
// this exact advisory, by rebuilding that producer's own idempotency key.
//
// Keyed on the ALERT rather than on Context.PR: an advisory and its remediation
// PR are not in one-to-one correspondence (one PR fixes several advisories), so
// a PR-shaped lookup would report "already carded" for advisories that are not.
// The key literal duplicates dependabotalerts.go's format deliberately — it is
// that producer's sticky identity, and a helper shared between the two would
// invite changing it, which is the one thing a sticky identity may never do.
func advisoryAlreadyCarded(in Input, a forgetypes.SecurityAlert) bool {
	key := fmt.Sprintf("%s:%s#%d", ProducerDependabotAlerts, in.Repo, a.Number)
	for _, r := range in.Existing {
		if r.Producer == ProducerDependabotAlerts && r.IdempotencyKey == key &&
			!r.Lifecycle.State.IsTerminal() {
			return true
		}
	}
	return false
}
