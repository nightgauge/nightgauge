package sweep

// Workspace producer: dependabot coverage (issue #344).
//
// #343 made the pipeline able to report an open advisory. It did not make the
// pipeline able to report that it is not looking, and those two silences are
// byte-identical: a producer that finds no alerts and a producer that CANNOT
// find alerts both raise nothing. An operator reads a quiet security surface as
// "clean", and for a repository with scanning switched off that reading is
// exactly wrong — nobody looked.
//
// This is the same blind spot coveragegap.go exists for, one level in. #260
// asks whether a repository is in the configured list at all; this asks whether
// a repository that IS in the list can answer the security question. The second
// failure is the quieter one, because the operator has every reason to believe
// a configured repo is covered.
//
// WORKSPACE-SCOPED, not repo-scoped, for the reason workspace.go gives: the
// question is about the configured repo LIST and its coverage. A repo-scoped
// producer sees one repo at a time and cannot say anything about the shape of
// the set; and, more concretely, this producer's answer for a repo it could not
// read depends on what it knows about the other repos in the same pass (see
// blockedByUnobserved).
//
// THREE OUTCOMES, told apart by #343's status field rather than by an error
// string — this producer is the reason that field is a field:
//
//	enabled, any number of alerts  → covered. NO card. What is IN the alert
//	                                 list is dependabot-alerts' job, not this
//	                                 producer's; here, an enabled repo with a
//	                                 hundred open advisories is a success.
//	status disabled                → BLIND. One card. The whole issue.
//	read failed / unsupported      → UNOBSERVED. No card and no retraction.
//	                                 "I could not read it" is not "it is not
//	                                 being scanned", and a token blip must never
//	                                 card a repository for being unreadable.
//
// A REPAIR VERB, unlike dependabotalerts.go and coveragegap.go — deliberately,
// and the difference is the whole reason it is allowed here. Those two card
// conditions no closed allowlist can fix: nothing deterministic patches a
// vulnerability, and nothing in the registry edits the workspace manifest, so
// both ship an honest dismiss. Scanning being off is one setting on one
// repository already under configuration, which is precisely the shape a
// bounded verb is for. See attention.ExecuteEnableAlerts for the target rules.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// ProducerDependabotCoverage is the stable producer id. It is half of the
// sticky (producer, idempotency_key) identity, so it must never change.
const ProducerDependabotCoverage = "dependabot-coverage"

// coverageFingerprint is deliberately CONSTANT.
//
// The fingerprint is the material state of the condition, and this condition
// has exactly one carded state: scanning is off. There is no worse version of
// it to escalate to and no better version short of it being fixed, which
// retracts the card outright. A fingerprint that moved — the count of blind
// repos, say, in the style of coverage-gap — would re-alert every repository's
// card whenever any other repository's setting changed, which is noise about a
// fact that did not change for the repo being re-alerted. Constant means: raise
// once, stay quiet, and honour a dismissal until the condition itself clears.
const coverageFingerprint = "dependabot-alerts:disabled"

// DependabotCoverage reports configured repositories that cannot report
// dependency alerts because scanning is switched off.
type DependabotCoverage struct {
	// ListAlerts overrides the forge read (tests). Nil uses the security
	// service on WorkspaceInput.Forge.
	ListAlerts func(ctx context.Context, in WorkspaceInput, owner, name string) (*forgetypes.SecurityAlerts, error)
	// Logf receives per-repo degradation messages. Nil uses the standard
	// logger.
	Logf func(format string, args ...any)
}

func init() { Default.RegisterWorkspace(&DependabotCoverage{}) }

// Name implements WorkspaceProducer.
func (p *DependabotCoverage) Name() string { return ProducerDependabotCoverage }

func (p *DependabotCoverage) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Evaluate implements WorkspaceProducer.
//
// Exactly one forge request per configured repo per sweep. Workspace producers
// share the same 30-second budget as every repo-scoped producer (see
// DefaultTimeout), so the cost has to be linear in the repo list and nothing
// more — no per-alert fan-out, no pagination, no second confirming call.
func (p *DependabotCoverage) Evaluate(ctx context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error) {
	if p.ListAlerts == nil && (in.Forge == nil || in.Forge.Security() == nil) {
		// Invariant 1: no security service means we can look at NOTHING. An
		// empty slice here would assert "every configured repo is covered" on
		// the strength of having asked nobody, and auto-resolve every real
		// card.
		return nil, fmt.Errorf("%s: no security service on the forge client — coverage is unknown, not clean", ProducerDependabotCoverage)
	}

	targets := coverageTargets(in)
	if len(targets) == 0 {
		// Nothing configured is not a coverage failure, it is nothing to
		// measure — and it is already coverage-gap's story to tell. A positive
		// empty observation, which correctly retracts stale cards.
		return nil, nil
	}

	var blind, unobserved []string
	for _, spec := range targets {
		if err := ctx.Err(); err != nil {
			// The shared budget ran out part-way down the list. Everything
			// after this point is unread, so this is a partial view of the
			// workspace and must not reach reconciliation at all.
			return nil, fmt.Errorf("%s: %w after %d of %d repos", ProducerDependabotCoverage, err, len(blind)+len(unobserved), len(targets))
		}

		owner, name, err := splitRepo(spec)
		if err != nil {
			// A configured entry with no owner cannot be turned into a forge
			// request. That is a failure to look, not an observation.
			p.logf("attention sweep: %s cannot query %q: %v", ProducerDependabotCoverage, spec, err)
			unobserved = append(unobserved, spec)
			continue
		}

		res, err := p.listAlerts(ctx, in, owner, name)
		switch {
		case err != nil:
			// Includes forge.ErrPermissionDenied, forge.ErrUnauthorized and
			// forge.ErrUnsupported. All three mean the same thing HERE: this
			// repository's coverage was not measured. Unsupported is worth
			// naming — a forge with no security surface at all (the GitLab
			// adapter today) would otherwise have every one of its repos carded
			// as "scanning is off" with a repair button that cannot fire, which
			// is the dead-end affordance this producer is allowed a verb
			// precisely for avoiding.
			p.logf("attention sweep: %s could not read %s (no card, nothing retracted): %v", ProducerDependabotCoverage, spec, err)
			unobserved = append(unobserved, spec)
		case res == nil:
			p.logf("attention sweep: %s got no result for %s (no card, nothing retracted)", ProducerDependabotCoverage, spec)
			unobserved = append(unobserved, spec)
		case res.Enabled():
			// Covered. Truncated is deliberately NOT consulted: it qualifies
			// the alert LIST, and this producer never reads the list. A repo
			// holding more open alerts than one request returns is emphatically
			// being scanned, and erroring on truncation would make the busiest
			// repositories the ones whose coverage can never be confirmed.
		case res.Status == forgetypes.SecurityAlertsDisabled:
			blind = append(blind, spec)
		default:
			// A status this build does not know is not a licence to guess
			// "disabled" and card the repo for it.
			p.logf("attention sweep: %s got unknown status %q for %s (no card, nothing retracted)", ProducerDependabotCoverage, res.Status, spec)
			unobserved = append(unobserved, spec)
		}
	}

	// A repo this pass could not read must lose nothing.
	//
	// SweepWorkspace calls Store.AutoResolveUnobserved once per producer after
	// a successful Evaluate, and that pass retracts every open card whose key
	// is absent from the returned set. So for an unreadable repo there is no
	// "leave it out and nothing happens": leaving it out IS the retraction. The
	// two honest ways to keep its card are to re-emit an observation nobody
	// made, or to declare the whole observation incomplete — and only the
	// second is true. Declining the pass costs one sweep's worth of new cards;
	// re-emitting would put a fabricated observation into the audit trail, and
	// retracting would clear a live security card on a token blip.
	//
	// The guard is conditional on purpose. When no unreadable repo holds a
	// card, their absence retracts nothing and the readable repos' answer
	// stands on its own — otherwise one permanently unreadable repository
	// (archived, or outside the token's security scope) would silence this
	// producer for the whole workspace forever, which is the failure it exists
	// to fix, one level up.
	if held := p.blockedByUnobserved(in, unobserved); len(held) > 0 {
		return nil, fmt.Errorf("%s: could not read %s, which %s an open coverage card — "+
			"reporting a partial view would retract it",
			ProducerDependabotCoverage, strings.Join(held, ", "), plural(len(held), "holds", "hold"))
	}

	sort.Strings(blind)
	if len(blind) == 0 {
		// Positive observation: every repo that answered is being scanned.
		return nil, nil
	}
	out := make([]attention.DecisionRequest, 0, len(blind))
	for _, repo := range blind {
		out = append(out, p.request(repo))
	}
	return out, nil
}

func (p *DependabotCoverage) listAlerts(ctx context.Context, in WorkspaceInput, owner, name string) (*forgetypes.SecurityAlerts, error) {
	if p.ListAlerts != nil {
		return p.ListAlerts(ctx, in, owner, name)
	}
	return in.Forge.Security().ListOpenAlerts(ctx, owner, name)
}

// coverageTargets collapses the configured list into the repos this producer
// will actually query.
//
// Collapsing matters because the configured list is a UNION of sources
// (workspaceConfiguredRepos merges the manifest, autonomous.enabled_repos and
// the primary repo), and normalizeRepos de-duplicates exact strings only. A
// manifest that says `web` beside a config that says `acme/web` therefore
// arrives as two entries naming one repository — which without this would cost
// two forge requests out of a shared budget and raise two cards for one
// setting.
//
// WorkspaceInput.Covers is the matcher, applied to the growing kept set rather
// than to the input list, because that is precisely the "these two spellings
// name the same repo" question it already answers. Qualified specs are offered
// first so a bare name never takes the slot from the owner/name form, which is
// the only form that can be turned into a forge request.
func coverageTargets(in WorkspaceInput) []string {
	kept := []string{}
	add := func(spec string) {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return
		}
		if (WorkspaceInput{ConfiguredRepos: kept}).Covers(spec) {
			return
		}
		kept = append(kept, spec)
	}
	for _, spec := range in.ConfiguredRepos {
		if strings.Contains(spec, "/") {
			add(spec)
		}
	}
	for _, spec := range in.ConfiguredRepos {
		if !strings.Contains(spec, "/") {
			add(spec)
		}
	}
	sort.Strings(kept)
	return kept
}

// blockedByUnobserved returns the repos this pass failed to read that ALREADY
// carry an open card from this producer — the ones whose cards a partial
// observation would silently retract.
//
// Matching goes through Covers rather than ==: a card's repo string was written
// by an earlier sweep, under whatever spelling configuration used then, and an
// equality test would miss `acme/web` against a list that now says `web` and
// retract the card anyway — the exact outcome this function exists to prevent.
func (p *DependabotCoverage) blockedByUnobserved(in WorkspaceInput, unobserved []string) []string {
	if len(unobserved) == 0 {
		return nil
	}
	probe := WorkspaceInput{ConfiguredRepos: unobserved}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range in.Existing {
		if r.Producer != ProducerDependabotCoverage || r.Lifecycle.State.IsTerminal() {
			continue
		}
		repo := strings.TrimSpace(r.Context.Repo)
		if repo == "" || seen[repo] || !probe.Covers(repo) {
			continue
		}
		seen[repo] = true
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// request builds the standing observation for one repository that cannot report
// alerts.
func (p *DependabotCoverage) request(repo string) attention.DecisionRequest {
	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s", ProducerDependabotCoverage, repo),
		// KindApprove, not the KindHandoff its two sibling security producers
		// use: handoff means the fleet cannot do it, and here the fleet can.
		// The operator is deciding between two real outcomes — turn scanning
		// on, or confirm it is off deliberately — which is what approve names.
		Kind: attention.KindApprove,
		// fyi. The severity vocabulary describes what is BLOCKED, and a repo
		// with scanning off blocks no run and stalls no fleet. It also keeps
		// the card dismissable without friction, which matters because "off on
		// purpose" is a legitimate state for a fork, a mirror or a repo whose
		// dependencies are vendored.
		Severity:    attention.SeverityFYI,
		Title:       fmt.Sprintf("%s cannot report dependency alerts — scanning is off", repo),
		Body:        coverageBody(repo),
		Fingerprint: coverageFingerprint,
		Context: attention.Context{
			// Set per card, and load-bearing: SweepWorkspace groups
			// observations by Context.Repo before calling ReconcileStanding,
			// so a card with no repo is dropped with a warning and never
			// reconciled at all.
			Repo: repo,
			// No URL. Unlike the alert card, whose only honest action is "go
			// look at the advisory", this card's action is the button below it.
			// A settings deep-link would also have to be invented per forge,
			// and inventing a GitHub URL for a GitLab repo is worse than none.
			Blocker: "dependency alert scanning is disabled — this repo's silence is not evidence of safety",
		},
		Options: []attention.Option{
			{
				ID:    "enable",
				Label: "Enable Dependabot alerts",
				Verb:  attention.VerbDependabotEnableAlerts,
				Style: attention.StylePrimary,
				// NO ARGS, deliberately: the executor reads the target from
				// this request's Context.Repo and requires configuration to
				// already cover it. An args map here would be the first step
				// toward a surface naming its own repository.
			},
			{
				ID:    "dismiss",
				Label: "Dismiss — scanning is off on purpose",
				Verb:  attention.VerbNoop,
				Style: attention.StyleDefault,
			},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

func coverageBody(repo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is configured and swept, but the forge reports dependency alert scanning is "+
		"DISABLED for it, so it can never report a vulnerability.\n\n", repo)
	b.WriteString("This repo currently produces the same silence as a repo with nothing wrong. " +
		"Every security card Nightgauge can raise comes from the alert feed, and there is no feed " +
		"here — so \"no alerts\" for this repo means \"nobody looked\", not \"nothing found\".\n\n")
	b.WriteString("Enabling scanning is one setting on this repository and nothing else: the button " +
		"below turns it on for this repo, which is already in the configured repo list, and takes no " +
		"other input. The next sweep will read the alert feed and retract this card on its own — " +
		"no dismissal needed.\n\n")
	b.WriteString("If scanning is off on purpose (a fork, a mirror, vendored dependencies), dismiss " +
		"the card. It stays dismissed until the condition itself changes.")
	return b.String()
}

// plural picks the verb form for a count, so a degradation message reads as
// English in the single-repo case that is by far the most common.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
