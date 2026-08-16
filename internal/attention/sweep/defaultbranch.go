package sweep

// Producer: default-branch health (issue #90).
//
// A red required check on the default branch blocks every open PR in the
// repository at once. Nothing watched for it: the pipeline noticed only when an
// individual run reached its merge stage and failed there — one run at a time,
// each rediscovering the same repo-wide fact, none of them able to tell the
// operator that the fact is repo-wide.
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

// DefaultBranchGrace is how long a failing check is left alone before it is
// carded. CI fails transiently and is re-run green within minutes often enough
// that raising on first sight would train operators to wait the card out — the
// precise habit that makes an inbox worthless. The grace is measured from the
// check's own completion time, so it costs no state of our own and survives a
// process that was not running when the check failed.
const DefaultBranchGrace = 10 * time.Minute

// DefaultBranchHealth reports a default branch whose required checks are red.
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

	required, err := in.Forge.CI().GetRequiredCheckNames(ctx, in.Owner, in.Name, branch)
	if err != nil {
		return nil, fmt.Errorf("read required checks for %s@%s: %w", in.Repo, branch, err)
	}
	if len(required) == 0 {
		// No check is required to merge, so no check failing can block a PR.
		// The branch may still be red and that may still be worth someone's
		// attention — but not at blocking_fleet, and not from a producer whose
		// entire claim is "nothing can land". Say nothing rather than cry wolf.
		return nil, nil
	}

	runs, err := in.Forge.CI().GetIndividualCheckRuns(ctx, in.Owner, in.Name, branch)
	if err != nil {
		return nil, fmt.Errorf("read check runs for %s@%s: %w", in.Repo, branch, err)
	}

	failing := p.failingRequired(runs, required)
	if len(failing) == 0 {
		return nil, nil
	}

	req := p.request(in.Repo, branch, failing)
	return []attention.DecisionRequest{req}, nil
}

// failedCheck is one failing required check, with enough context to name it.
type failedCheck struct {
	name        string
	url         string
	sha         string
	completedAt time.Time
	hasTime     bool
}

// failingRequired returns the required checks that are conclusively failing and
// past the grace period, sorted by name so the fingerprint is stable.
//
// Two exclusions carry the AC:
//   - A check that is queued or in progress is NOT failing. A pending check on
//     a fresh commit is the normal state of a healthy branch someone just
//     pushed to.
//   - A check that completed inside the grace window is not yet carded, so a
//     failure that is re-run green in the next few minutes never surfaces at
//     all. A failure whose completion time is unknown is treated as past the
//     grace: the alternative is suppressing a real blocker indefinitely because
//     an adapter did not populate a timestamp.
func (p *DefaultBranchHealth) failingRequired(runs []forgetypes.CheckDetail, required []string) []failedCheck {
	isRequired := make(map[string]bool, len(required))
	for _, name := range required {
		isRequired[name] = true
	}
	cutoff := p.now().Add(-p.grace())

	var out []failedCheck
	for _, run := range runs {
		if !isRequired[run.Name] {
			continue
		}
		if !isFailedConclusion(run.Conclusion) {
			continue
		}
		fc := failedCheck{name: run.Name, url: run.DetailsURL, sha: run.HeadSHA}
		if ts, err := time.Parse(time.RFC3339, run.CompletedAt); err == nil {
			fc.completedAt, fc.hasTime = ts, true
			if ts.After(cutoff) {
				continue // still inside the grace window
			}
		}
		out = append(out, fc)
	}
	// Stable, so repeated runs of one check keep the order the forge reported
	// them in. The name sort alone leaves duplicates interchangeable, and the
	// representative run that ends up on the card would then depend on the sort
	// implementation rather than on the data.
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
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

// request builds the standing observation for a red default branch.
func (p *DefaultBranchHealth) request(repo, branch string, failing []failedCheck) attention.DecisionRequest {
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
			Blocker: fmt.Sprintf("required check(s) failing on %s: %s", branch, strings.Join(names, ", ")),
			URL:     url,
		},
		Options: []attention.Option{
			// The only honest button. It does not repair anything; it records
			// that a human looked and decided this is not worth blocking on.
			// Resolving suppresses the card until the set of failing checks
			// changes, so it cannot be used to silence a NEW failure.
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

func (p *DefaultBranchHealth) body(repo, branch string, failing []failedCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Required checks are failing on %s (%s), so every open PR in the repository is blocked until this clears.\n\n", branch, repo)
	for _, f := range failing {
		fmt.Fprintf(&b, "- %s", f.name)
		if f.hasTime {
			fmt.Fprintf(&b, " — failing for %s", humanizeDuration(p.now().Sub(f.completedAt)))
		}
		if f.sha != "" {
			fmt.Fprintf(&b, " (commit %s)", shortSHA(f.sha))
		}
		if f.url != "" {
			fmt.Fprintf(&b, "\n  %s", f.url)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nNightgauge cannot fix this: the next action is a human's. ")
	b.WriteString("Mute the card while you work on it (`nightgauge attention mute <id>`) — it re-alerts if a different check starts failing.")
	return b.String()
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
