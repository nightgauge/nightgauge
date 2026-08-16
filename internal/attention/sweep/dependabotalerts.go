package sweep

// Producer: dependabot alerts (issue #343).
//
// Before this, the only thing the pipeline knew about dependency security was
// whether an issue or PR carried a `security` label. Severity was therefore
// fabricated — a critical RCE and a low-severity ReDoS were the same object —
// and, worse, an alert with no remediation PR was reported by NOTHING. The
// forge opens a PR only when it can reach a non-vulnerable version through the
// manifest; when it cannot (no published patch, a transitive pin, an ecosystem
// it cannot bump) there is no PR and no issue, and every code path keyed on one
// or the other. The alerts that most need a human were exactly the invisible
// ones.
//
// The card names the advisory's OWN severity and identifier, the package and
// the manifest that pulls it in, and either the remediation PR or the explicit
// reason there is none. Those last two lead to opposite operator actions —
// "review and merge what is already written" versus "there is nothing to merge,
// go decide something" — so they must never render the same.
//
// NO REPAIR VERB, following the deliberate precedent in defaultbranch.go and
// humangate.go: nothing in the closed verb registry can patch a vulnerability,
// and a button that implies otherwise is worse than no button. The only option
// is an honest dismiss; the real next action is the URL. Bounded operations
// arrive with the sibling issues that implement them.
//
// NO CROSS-PRODUCER DEDUPE, unlike humangate.go — deliberately, and the
// difference is not an oversight. Human-gate defers to branch-protection
// because the two producers observe ONE condition ("this PR cannot merge") from
// two vantage points. An open advisory and a PR that cannot merge are two
// conditions; suppressing this observation because another producer carded the
// remediation PR made the reconciler auto-resolve a live vulnerability as
// "condition_cleared", and re-raise it as a new card once the other card was
// dismissed. See the comment in Evaluate.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// ProducerDependabotAlerts is the stable producer id. It is half of the sticky
// (producer, idempotency_key) identity, so it must never change.
const ProducerDependabotAlerts = "dependabot-alerts"

// DependabotAlertGrace is how long a newly raised alert is left alone before it
// is carded.
//
// The forge raises the alert first and opens the remediation PR seconds to
// minutes later. Carding on first sight would publish "no remediation PR" for
// an alert whose PR is already on its way — the one rendering this producer
// exists to get right — and the correction would arrive as a fingerprint change
// that re-alerts the operator about a card that was never true. The window is
// measured from the alert's OWN first-seen timestamp, so it costs no local
// state and survives a process that was not running when the alert appeared.
const DependabotAlertGrace = 30 * time.Minute

// errAlertsDisabled is returned when the forge reports dependency scanning is
// switched off for the repository.
//
// This is deliberately an ERROR rather than an empty observation. An empty
// slice is a positive assertion that no condition holds, and scanning being off
// asserts nothing of the kind — nobody looked. Returning the empty slice would
// retract every open vulnerability card the moment somebody toggled the setting
// off, which turns a repository setting into a way to silence security cards.
// A producer error is logged and excluded from reconciliation instead, so the
// existing cards are left exactly as they were.
//
// Coverage itself — "this repository is not being scanned at all" — is a
// workspace-scoped question and belongs to the coverage producer, not here.
var errAlertsDisabled = errors.New("dependency scanning is disabled for this repository")

// errAlertsUnreadable marks a forge-confirmed denial that is scoped to the
// SECURITY surface alone.
//
// It deliberately does not wrap forge.ErrPermissionDenied — see
// alertReadError, where the whole point is to keep this failure out of
// isSweepFatal.
var errAlertsUnreadable = errors.New("the token cannot read this repository's security alerts")

// errAlertsTruncated marks a partial answer: the forge holds more open alerts
// than one request returns.
//
// Truncation is a failure to OBSERVE, not an observation of fewer alerts. The
// reconciler auto-resolves every card of an evaluated producer whose condition
// is absent from the observation, so returning a page as if it were the open
// set retracts live vulnerability cards with the reason "condition_cleared" —
// docs/ATTENTION_PRODUCERS.md invariant 1, in the producer least able to afford
// it. An error leaves every existing card exactly where it was and reports the
// shortfall on the sweep result instead.
var errAlertsTruncated = errors.New("the forge returned only part of the open alert set")

// DependabotAlerts reports open dependency security advisories, one card each.
type DependabotAlerts struct {
	// Grace overrides DependabotAlertGrace. Zero uses the default.
	Grace time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

func init() { Default.Register(&DependabotAlerts{}) }

// Name implements Producer.
func (p *DependabotAlerts) Name() string { return ProducerDependabotAlerts }

func (p *DependabotAlerts) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *DependabotAlerts) grace() time.Duration {
	if p.Grace > 0 {
		return p.Grace
	}
	return DependabotAlertGrace
}

// Evaluate implements Producer.
//
// Exactly one call into the forge per repo per sweep: producers run
// sequentially under one shared 30-second budget, so a per-alert fan-out here
// would spend every other producer's time. (The GitHub adapter spends one
// additional REST request behind that call on the ambiguous empty answer, to
// confirm the emptiness is real rather than a permission filter — see
// internal/github/security.go.)
func (p *DependabotAlerts) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	res, err := in.Forge.Security().ListOpenAlerts(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, alertReadError(in.Repo, err)
	}
	if res == nil {
		return nil, fmt.Errorf("read security alerts for %s: forge returned no result", in.Repo)
	}
	if !res.Enabled() {
		return nil, fmt.Errorf("%s: %w", in.Repo, errAlertsDisabled)
	}
	if res.Truncated {
		return nil, fmt.Errorf("%s: %w (%d open, %d returned per request)",
			in.Repo, errAlertsTruncated, res.TotalOpen, forge.MaxSecurityAlertsPerRequest)
	}

	alerts := p.carded(res.Alerts)
	if len(alerts) == 0 {
		// A positive assertion that nothing is open — this is what retracts the
		// cards of alerts that were fixed or dismissed since the last sweep.
		return nil, nil
	}

	// EVERY open alert is observed, including one whose remediation PR another
	// producer is already carding.
	//
	// Declining to re-observe it does not merely suppress a duplicate: the
	// reconciler reads the missing observation as "the condition cleared" and
	// AUTO-RESOLVES the existing security card, then re-creates it as a brand
	// new card the moment the other producer's card is dismissed — a live
	// advisory retracted and re-alerted with its fingerprint unmoved. The two
	// cards are also not interchangeable: a branch-protection or human-gate card
	// says a PR cannot merge and names no severity, GHSA, CVE or package, so
	// retracting this one removes the only place the vulnerability is stated.
	out := make([]attention.DecisionRequest, 0, len(alerts))
	for i := range alerts {
		out = append(out, p.request(in.Repo, alerts[i]))
	}
	return out, nil
}

// alertReadError decides how far one failed security read is allowed to travel.
//
// isSweepFatal treats forge.ErrPermissionDenied as REPO-WIDE: the sweeper
// abandons the cycle before reconciling, so no producer evaluates, no card is
// created, and no open card can auto-resolve. That is right for a credential
// the forge rejected outright and for a rate limit — nothing else would fare
// better against either. It is wrong for THIS surface's denial, which is
// surface-specific: reading Dependabot alerts requires a scope (GitHub's
// `security_events`) that reading pull requests, checks and rulesets does not.
// A token that legitimately cannot see the security tab would otherwise switch
// off default-branch-health, human-gate and stranded-ready for that repository
// on every cycle, permanently — and freeze whatever cards are already open,
// since nothing can retract them until StandingExpiry.
//
// So a permission denial is downgraded to this producer's own bad day: its
// cards are left untouched and every other producer still runs. ErrUnauthorized
// and ErrRateLimited stay repo-wide, because both really are.
func alertReadError(repo string, err error) error {
	if errors.Is(err, forge.ErrPermissionDenied) &&
		!errors.Is(err, forge.ErrUnauthorized) &&
		!errors.Is(err, forge.ErrRateLimited) {
		// %v rather than %w on the cause: dropping the repo-wide sentinel from
		// the chain is the entire point, and errAlertsUnreadable carries the
		// meaning on without it.
		return fmt.Errorf("read security alerts for %s: %v: %w", repo, err, errAlertsUnreadable)
	}
	return fmt.Errorf("read security alerts for %s: %w", repo, err)
}

// carded filters the forge's open set down to the alerts worth a card and puts
// them in a deterministic order — most severe first, then by alert number so
// two equally severe advisories never swap places between sweeps.
//
// An alert inside the grace window is skipped entirely: see
// DependabotAlertGrace. An alert whose first-seen timestamp the forge did not
// populate is carded immediately, because suppressing a real vulnerability
// forever over a missing field is the worse failure.
func (p *DependabotAlerts) carded(alerts []forgetypes.SecurityAlert) []forgetypes.SecurityAlert {
	cutoff := p.now().Add(-p.grace())
	out := make([]forgetypes.SecurityAlert, 0, len(alerts))
	for _, a := range alerts {
		if ts, err := time.Parse(time.RFC3339, a.FirstSeenAt); err == nil && ts.After(cutoff) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := out[i].Severity.Rank(), out[j].Severity.Rank(); ri != rj {
			return ri > rj
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// request builds the standing observation for one open alert.
func (p *DependabotAlerts) request(repo string, a forgetypes.SecurityAlert) attention.DecisionRequest {
	return attention.DecisionRequest{
		// Sticky identity is the forge's own alert number: it is stable for the
		// life of the alert, and a re-raised alert after a dismissal gets a new
		// number, which is genuinely a new card rather than a resurrection.
		IdempotencyKey: fmt.Sprintf("%s:%s#%d", ProducerDependabotAlerts, repo, a.Number),
		// A human-only task the fleet cannot perform. Nothing here is blocked
		// waiting on the answer; someone has to go and decide.
		Kind: attention.KindHandoff,
		// FYI, at every advisory severity. The severity vocabulary describes
		// what is BLOCKED, not what is important, and a vulnerability blocks no
		// merge and stalls no run. Claiming blocking_fleet for a critical CVE
		// would be false in the one dimension the field measures; the advisory's
		// real severity leads the title and drives the ordering instead.
		Severity:    attention.SeverityFYI,
		Title:       alertTitle(a),
		Body:        alertBody(repo, a),
		Fingerprint: alertFingerprint(a),
		Context: attention.Context{
			Repo: repo,
			// The remediation PR when there is one — which is also how another
			// producer carding that PR recognises this card as the same next
			// action.
			PR:      a.Remediation.PRNumber,
			Blocker: alertBlocker(a),
			URL:     alertURL(a),
		},
		Options: []attention.Option{
			// The only honest button. It repairs nothing; it records that a
			// human looked. Resolving suppresses the card until the fingerprint
			// moves, so it cannot silence a severity or remediation change.
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// alertFingerprint is the MATERIAL state of the advisory: how bad it is, and
// whether something already exists that fixes it.
//
// Deliberately excluded: elapsed time and the alert's updated_at, both of which
// move on their own and would re-alert every sweep for a condition that has not
// changed. Deliberately included: the first patched version, because a fix
// being published where none existed is a real transition an operator acts on.
func alertFingerprint(a forgetypes.SecurityAlert) string {
	fix := string(a.Remediation.State)
	if a.Remediation.PRNumber > 0 {
		fix = fmt.Sprintf("%s#%d", fix, a.Remediation.PRNumber)
	}
	return fmt.Sprintf("sev:%s;fix:%s;patch:%s", a.Severity, fix, a.FirstPatchedVersion)
}

// alertTitle leads with the advisory's severity and the package, then states
// the remediation situation — the two facts that decide what the operator does
// next.
func alertTitle(a forgetypes.SecurityAlert) string {
	pkg := alertSubject(a)
	switch a.Remediation.State {
	case forgetypes.RemediationPROpen:
		return fmt.Sprintf("%s severity in %s — fix is waiting in PR #%d",
			a.Severity, pkg, a.Remediation.PRNumber)
	case forgetypes.RemediationNotPossible:
		return fmt.Sprintf("%s severity in %s — no remediation PR, and the forge reports it cannot make one",
			a.Severity, pkg)
	default:
		return fmt.Sprintf("%s severity in %s — no remediation PR exists", a.Severity, pkg)
	}
}

// alertSubject names what the alert is ABOUT in one token, and never renders
// empty.
//
// The forge's securityVulnerability (and therefore the package name) is
// nullable — an advisory can be withdrawn or restructured while the alert
// stays open — and a title reading "unknown severity in  — no remediation PR
// exists" is a card the operator cannot even identify. Falling through
// package → advisory id → CVE → alert number always leaves something that
// distinguishes this card from the next one.
func alertSubject(a forgetypes.SecurityAlert) string {
	switch {
	case a.Package != "":
		return a.Package
	case a.AdvisoryID != "":
		return a.AdvisoryID
	case a.CVE != "":
		return a.CVE
	default:
		return fmt.Sprintf("alert #%d", a.Number)
	}
}

// alertBlocker is the one-line machine-facing summary surfaces show inline.
func alertBlocker(a forgetypes.SecurityAlert) string {
	switch a.Remediation.State {
	case forgetypes.RemediationPROpen:
		return fmt.Sprintf("%s severity advisory %s in %s — remediation PR #%d",
			a.Severity, identifierLine(a), alertSubject(a), a.Remediation.PRNumber)
	default:
		return fmt.Sprintf("%s severity advisory %s in %s — no remediation PR",
			a.Severity, identifierLine(a), alertSubject(a))
	}
}

// alertURL points at the single most useful destination. When a remediation PR
// exists that is the PR, because the operator's next action is to review it;
// otherwise it is the ALERT — the page that names this repository, this
// manifest and this version, and offers a dismiss.
//
// AdvisoryURL is last and is a genuine fallback, not an equivalent: it is a
// public advisory-database page that mentions neither the repository nor the
// manifest, so sending a "there is nothing to merge, go decide something" card
// there gives its reader nothing to decide with. forgetypes.SecurityAlert.URL
// is required of every adapter for exactly this reason.
func alertURL(a forgetypes.SecurityAlert) string {
	if a.Remediation.State == forgetypes.RemediationPROpen && a.Remediation.PRURL != "" {
		return a.Remediation.PRURL
	}
	if a.URL != "" {
		return a.URL
	}
	return a.AdvisoryURL
}

// alertBody states what was OBSERVED. The remediation paragraph is the load
// bearing half: the three states produce three different paragraphs, never one
// averaged over them.
func alertBody(repo string, a forgetypes.SecurityAlert) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s severity advisory against %s in %s.\n\n", a.Severity, alertSubject(a), repo)
	if a.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Summary)
	}

	fmt.Fprintf(&b, "- Advisory: %s", identifierLine(a))
	if a.AdvisoryURL != "" {
		fmt.Fprintf(&b, "\n  %s", a.AdvisoryURL)
	}
	b.WriteString("\n")
	if a.ManifestPath != "" {
		fmt.Fprintf(&b, "- Manifest: %s", a.ManifestPath)
		if qual := dependencyQualifier(a); qual != "" {
			fmt.Fprintf(&b, " (%s)", qual)
		}
		b.WriteString("\n")
	}
	if a.VulnerableRange != "" {
		fmt.Fprintf(&b, "- Affected versions: %s\n", a.VulnerableRange)
	}
	if a.FirstPatchedVersion != "" {
		fmt.Fprintf(&b, "- First patched version: %s\n", a.FirstPatchedVersion)
	} else {
		b.WriteString("- First patched version: none published\n")
	}

	b.WriteString("\n")
	switch a.Remediation.State {
	case forgetypes.RemediationPROpen:
		fmt.Fprintf(&b, "A remediation PR is already open: #%d", a.Remediation.PRNumber)
		if a.Remediation.PRTitle != "" {
			fmt.Fprintf(&b, " (%s)", a.Remediation.PRTitle)
		}
		b.WriteString(".\nThe next action is to review and merge it.\n")
		if a.Remediation.PRURL != "" {
			fmt.Fprintf(&b, "\n%s\n", a.Remediation.PRURL)
		}
	case forgetypes.RemediationNotPossible:
		b.WriteString("NO REMEDIATION PR EXISTS, and the forge reports it cannot open one")
		if a.Remediation.Reason != "" {
			fmt.Fprintf(&b, " (%s)", a.Remediation.Reason)
		}
		b.WriteString(".\n")
		if a.Remediation.ReasonDetail != "" {
			fmt.Fprintf(&b, "The forge's own explanation: %s\n", a.Remediation.ReasonDetail)
		}
		b.WriteString("There is nothing to merge. Upgrading the dependants, pinning an override, or accepting the risk are all human decisions.\n")
	default:
		b.WriteString("NO REMEDIATION PR EXISTS, and the forge reports no update attempt for this alert.\n")
		b.WriteString("There is nothing to merge yet. Check whether the ecosystem or manifest is one the forge can update at all.\n")
	}

	b.WriteString("\nNightgauge cannot patch this: the next action is a human's. ")
	b.WriteString("Mute the card while you work on it (`nightgauge attention mute <id>`) — it re-alerts if the severity or the remediation situation changes.")
	return b.String()
}

// identifierLine renders the advisory identifier plus its CVE when the forge
// reported one, and says so plainly when it did not.
func identifierLine(a forgetypes.SecurityAlert) string {
	switch {
	case a.AdvisoryID != "" && a.CVE != "":
		return a.AdvisoryID + " (" + a.CVE + ")"
	case a.AdvisoryID != "":
		return a.AdvisoryID
	case a.CVE != "":
		return a.CVE
	default:
		return "unidentified"
	}
}

// dependencyQualifier renders the forge's scope/relationship facts, which
// change how urgent the alert is: a transitive dev-only dependency is a very
// different call from a direct runtime one.
func dependencyQualifier(a forgetypes.SecurityAlert) string {
	parts := make([]string, 0, 2)
	if a.Relationship != "" {
		parts = append(parts, a.Relationship)
	}
	if a.Scope != "" {
		parts = append(parts, a.Scope+" scope")
	}
	return strings.Join(parts, ", ")
}
