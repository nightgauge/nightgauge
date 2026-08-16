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

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
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
// Exactly one forge request per repo per sweep: producers run sequentially
// under one shared 30-second budget, so a per-alert fan-out here would spend
// every other producer's time.
func (p *DependabotAlerts) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	res, err := in.Forge.Security().ListOpenAlerts(ctx, in.Owner, in.Name)
	if err != nil {
		// Wrapped, so an auth or rate-limit sentinel still reaches the sweeper's
		// repo-wide skip check. Anything else is this producer's bad day and
		// leaves its cards untouched.
		return nil, fmt.Errorf("read security alerts for %s: %w", in.Repo, err)
	}
	if res == nil {
		return nil, fmt.Errorf("read security alerts for %s: forge returned no result", in.Repo)
	}
	if !res.Enabled() {
		return nil, fmt.Errorf("%s: %w", in.Repo, errAlertsDisabled)
	}

	alerts := p.carded(res.Alerts)
	if len(alerts) == 0 {
		// A positive assertion that nothing is open — this is what retracts the
		// cards of alerts that were fixed or dismissed since the last sweep.
		return nil, nil
	}

	out := make([]attention.DecisionRequest, 0, len(alerts))
	for i := range alerts {
		a := alerts[i]
		// One condition, one card. When the forge already has a remediation PR
		// and another producer is carding that same PR, the operator's next
		// action ("go look at this PR") is already in their inbox once.
		if pr := a.Remediation.PRNumber; pr > 0 {
			if _, dup := in.OpenRequestForPR(producerBranchProtection, pr); dup {
				continue
			}
			if _, dup := in.OpenRequestForPR(ProducerHumanGate, pr); dup {
				continue
			}
		}
		out = append(out, p.request(in.Repo, a))
	}
	return out, nil
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
	pkg := a.Package
	if pkg == "" {
		pkg = a.AdvisoryID
	}
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

// alertBlocker is the one-line machine-facing summary surfaces show inline.
func alertBlocker(a forgetypes.SecurityAlert) string {
	switch a.Remediation.State {
	case forgetypes.RemediationPROpen:
		return fmt.Sprintf("%s severity advisory %s in %s — remediation PR #%d",
			a.Severity, a.AdvisoryID, a.Package, a.Remediation.PRNumber)
	default:
		return fmt.Sprintf("%s severity advisory %s in %s — no remediation PR",
			a.Severity, a.AdvisoryID, a.Package)
	}
}

// alertURL points at the single most useful destination. When a remediation PR
// exists that is the PR, because the operator's next action is to review it;
// otherwise it is the alert, then the advisory.
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

	fmt.Fprintf(&b, "%s severity advisory against %s in %s.\n\n", a.Severity, a.Package, repo)
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
