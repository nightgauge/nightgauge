package hooks

// Post-merge verification of the default branch (#1249).
//
// AGENTS.md states the rule: "A green PR check is a prediction; main's own run
// is the observation." Every hand merge is followed by a check-runs query
// against the merge commit. The pipeline — which performs most merges — never
// did this: EvaluatePostMerge verified the PR reached MERGED, captured the merge
// SHA, and stopped. The three failure classes only the post-merge run can catch
// (nondeterministic tests, merge skew, environment differences) were therefore
// invisible to the pipeline, and a red `main` was found by an operator reading
// the Actions tab.
//
// VerifyMergeCommit is the pipeline's copy of the operator's idiom, with the
// three numbers evaluated in the order that makes them honest:
//
//	total   > 0   — a check-runs list with nothing in it is NOT green. GitHub
//	                creates check runs seconds after a push, so an empty list
//	                right after a merge means "CI has not started", never
//	                "there is nothing to fail" (the #1027 / #1038 lesson).
//	pending == 0  — a check that is queued or in progress has not concluded.
//	                Reading its empty conclusion as a pass is the same defect.
//	bad     == 0  — only then is a conclusion outside success / skipped /
//	                neutral a failure, and only then is `main` red.
//
// The wait is bounded and the verdict vocabulary is closed. Budget exhaustion
// with checks still pending is a distinct verdict, not a failure: still-pending
// checks are not evidence of breakage, and a card raised on them would be noise
// that trains operators to wait cards out.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// MainCheckReader is what VerifyMergeCommit needs from the forge: the check runs
// of one commit, and the names the branch requires so the verdict can say
// whether the red is a fleet blocker or advisory. Implemented by
// *github.CIService.
type MainCheckReader interface {
	GetIndividualCheckRuns(ctx context.Context, owner, repo, ref string) ([]forgetypes.CheckDetail, error)
	GetRequiredCheckNames(ctx context.Context, owner, repo, branch string) ([]string, error)
}

// MainCheckWait bounds the poll. Zero PollInterval and NoCheckGrace take the
// defaults below; a zero Timeout is a deliberate single read — the operator
// running `nightgauge hook post-merge` by hand is not made to wait out CI in
// their shell, and the verdict then honestly says `pending`.
type MainCheckWait struct {
	// Timeout is the wall-clock budget for the whole wait. Zero = one read.
	Timeout time.Duration
	// PollInterval is the gap between reads. Zero = DefaultMainCheckPollInterval.
	PollInterval time.Duration
	// NoCheckGrace bounds how long an EMPTY check-runs list is waited out
	// before the verdict is MainChecksNone. Zero = DefaultMainCheckNoCheckGrace.
	NoCheckGrace time.Duration
	// Sleep overrides the inter-poll sleep (tests). Nil sleeps on a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Progress receives one line per poll while the wait is still going.
	//
	// WHY THIS EXISTS (#1414). The wait is bounded — maxPolls below — but it
	// printed nothing until it finished, so from outside the process it was a
	// silent block at ~0.1s CPU for up to twenty minutes. Two sessions
	// independently read that as a hang; one reaped it by PID after twelve
	// minutes and recorded "the hook hangs when no daemon is reachable" in a
	// handoff. That is wrong — with zero daemons running the same binary
	// returns in seconds against a merge commit whose checks have concluded —
	// but a silent wait is indistinguishable from a stuck one, so the wrong
	// diagnosis was the reasonable one to reach.
	//
	// Nil sends progress to stderr. Tests substitute a recorder.
	Progress func(line string)
}

const (
	// DefaultMainCheckTimeout is the wall-clock budget. The repository's own
	// required suite takes ~10-15 minutes; the deterministic pr-merge runner
	// already holds the slot for a 15-minute CI budget on the PR side, so
	// holding it for main's run is the same cost paid once more, on purpose.
	DefaultMainCheckTimeout = 20 * time.Minute
	// DefaultMainCheckPollInterval matches the CI wait in internal/github/ci.go.
	DefaultMainCheckPollInterval = 30 * time.Second
	// DefaultMainCheckNoCheckGrace matches the pr-merge runner's zero-check
	// window (#1027): DefaultCINoCheckGracePolls x DefaultCIPollInterval.
	DefaultMainCheckNoCheckGrace = 2 * time.Minute
)

// DefaultMainCheckWait is the pipeline's wait: the full bounded budget.
func DefaultMainCheckWait() MainCheckWait {
	return MainCheckWait{
		Timeout:      DefaultMainCheckTimeout,
		PollInterval: DefaultMainCheckPollInterval,
		NoCheckGrace: DefaultMainCheckNoCheckGrace,
	}
}

func (w MainCheckWait) pollInterval() time.Duration {
	if w.PollInterval > 0 {
		return w.PollInterval
	}
	return DefaultMainCheckPollInterval
}

// progress reports one line of wait progress. Stderr by default: stdout is
// reserved for the hook's own machine-readable output.
func (w MainCheckWait) progress(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if w.Progress != nil {
		w.Progress(line)
		return
	}
	fmt.Fprintln(os.Stderr, line)
}

func (w MainCheckWait) noCheckGrace() time.Duration {
	if w.NoCheckGrace > 0 {
		return w.NoCheckGrace
	}
	return DefaultMainCheckNoCheckGrace
}

func (w MainCheckWait) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// MainCheckVerdict is the closed outcome vocabulary of one verification.
type MainCheckVerdict string

const (
	// MainChecksGreen: every check run on the merge commit concluded, and every
	// conclusion is success, skipped or neutral.
	MainChecksGreen MainCheckVerdict = "green"
	// MainChecksRed: every check run concluded and at least one conclusion is
	// something else. This is the only verdict that raises a card.
	MainChecksRed MainCheckVerdict = "red"
	// MainChecksPending: the budget ran out with check runs still queued or in
	// progress. Not evidence of breakage; never carded.
	MainChecksPending MainCheckVerdict = "pending"
	// MainChecksNone: no check run appeared on the merge commit within the
	// no-check grace. A repo with no CI, or CI that never started — either way
	// NOT green, because nothing was observed.
	MainChecksNone MainCheckVerdict = "no_checks"
	// MainChecksError: the forge could not be read. The verification says
	// nothing about main; the error is recorded so the gap is visible.
	MainChecksError MainCheckVerdict = "error"
	// MainChecksSkipped: no reader was wired or no merge SHA was captured, so
	// nothing was attempted.
	MainChecksSkipped MainCheckVerdict = "skipped"
)

// MainCheckResult is the verdict plus the three numbers it was derived from.
type MainCheckResult struct {
	Verdict        MainCheckVerdict `json:"verdict"`
	MergeCommitSha string           `json:"mergeCommitSha,omitempty"`
	// Total / Pending / Bad are the last poll's three numbers, in the order the
	// idiom evaluates them. Bad counts distinct failing check NAMES.
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Bad     int `json:"bad"`
	// Failing names every distinct failing check, sorted by name.
	Failing []FailingCheck `json:"failing,omitempty"`
	// Polls is how many reads were made.
	Polls int `json:"polls"`
	// Error is the read failure behind MainChecksError.
	Error string `json:"error,omitempty"`
}

// FailingNames returns the failing check names in stable order.
func (r MainCheckResult) FailingNames() []string {
	names := make([]string, 0, len(r.Failing))
	for _, f := range r.Failing {
		names = append(names, f.Name)
	}
	return names
}

// AnyRequiredFailing reports whether a required check is among the failures.
func (r MainCheckResult) AnyRequiredFailing() bool {
	for _, f := range r.Failing {
		if f.Required {
			return true
		}
	}
	return false
}

// FailingCheck is one failing check run on the merge commit.
type FailingCheck struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
	// Required is true when the branch's protection or rulesets require this
	// check — the difference between "nothing can land" and "the branch is red
	// and nothing else will say so" (#1250).
	Required bool `json:"required,omitempty"`
}

// VerifyMergeCommit polls the merge commit's check runs until they conclude,
// the budget runs out, or the read fails. It never returns an error: every
// outcome is a verdict, because the caller is a non-blocking hook whose only
// output is what it records.
func VerifyMergeCommit(ctx context.Context, reader MainCheckReader, owner, repo, branch, sha string, wait MainCheckWait) MainCheckResult {
	res := MainCheckResult{Verdict: MainChecksSkipped, MergeCommitSha: sha}
	if reader == nil || strings.TrimSpace(sha) == "" {
		return res
	}

	interval := wait.pollInterval()
	// Poll counts, not wall-clock: a budget expressed in reads is deterministic
	// under an injected sleep and still bounded under the real one. Timeout 0
	// is exactly one read.
	maxPolls := 1 + int(wait.Timeout/interval)
	gracePolls := 1 + int(wait.noCheckGrace()/interval)
	if gracePolls > maxPolls {
		gracePolls = maxPolls
	}

	// State the bound BEFORE blocking. "It is waiting and here is for how long"
	// is the difference between a legible wait and an apparent hang.
	// Names the escape hatch, because this line is read at exactly the moment
	// someone is deciding whether to kill the process. `--main-check-wait 0`
	// already existed and neither session that reaped one had found it — a
	// bounded wait nobody knows how to skip is, in practice, an unbounded one.
	wait.progress("Post-merge: waiting for %s@%s checks — up to %d poll(s) every %s (%s budget; "+
		"--main-check-wait 0 records the verdict from one immediate read instead)",
		repo, shortSHA(sha), maxPolls, interval, wait.Timeout)

	var last []forgetypes.CheckDetail
	for {
		runs, err := reader.GetIndividualCheckRuns(ctx, owner, repo, sha)
		res.Polls++
		if err != nil {
			res.Verdict = MainChecksError
			res.Error = err.Error()
			return res
		}
		last = runs
		total, pending, bad := threeNumbers(runs)
		res.Total, res.Pending, res.Bad = total, pending, bad

		switch {
		case total == 0:
			if res.Polls >= gracePolls {
				res.Verdict = MainChecksNone
				return res
			}
		case pending > 0:
			if res.Polls >= maxPolls {
				res.Verdict = MainChecksPending
				return res
			}
		default:
			// total > 0 and pending == 0: the numbers are final.
			res.Failing = failingChecks(last)
			res.Verdict = MainChecksGreen
			if bad > 0 {
				res.Verdict = MainChecksRed
				markRequired(ctx, reader, owner, repo, branch, res.Failing)
			}
			return res
		}

		if res.Polls >= maxPolls {
			// Reachable only from the total == 0 arm when the grace exceeds the
			// budget; the verdict is still "nothing observed".
			res.Verdict = MainChecksNone
			return res
		}
		// Emitted only when another sleep follows, so a wait that concludes on
		// its first read stays a single summary line.
		if total == 0 {
			wait.progress("Post-merge: poll %d/%d — no check runs on %s yet",
				res.Polls, maxPolls, shortSHA(sha))
		} else {
			wait.progress("Post-merge: poll %d/%d — %d of %d check(s) still running on %s",
				res.Polls, maxPolls, pending, total, shortSHA(sha))
		}
		if err := wait.sleep(ctx, interval); err != nil {
			res.Verdict = MainChecksError
			res.Error = err.Error()
			return res
		}
	}
}

// threeNumbers is the operator's idiom over one check-runs response: how many
// runs there are, how many have not concluded, and how many distinct check
// names concluded badly.
func threeNumbers(runs []forgetypes.CheckDetail) (total, pending, bad int) {
	total = len(runs)
	badNames := map[string]struct{}{}
	for _, run := range runs {
		if !isConcluded(run) {
			pending++
			continue
		}
		if isBadConclusion(run.Conclusion) {
			badNames[run.Name] = struct{}{}
		}
	}
	return total, pending, len(badNames)
}

// isConcluded reports whether a check run has a conclusion at all. Adapters
// uppercase the forge's status vocabulary; anything other than COMPLETED is
// queued, in progress, waiting or requested.
func isConcluded(run forgetypes.CheckDetail) bool {
	return strings.EqualFold(strings.TrimSpace(run.Status), "COMPLETED")
}

// isBadConclusion is AGENTS.md's filter verbatim: a conclusion that is not
// success, skipped or neutral. Deliberately broader than the default-branch
// sweep's blocker test — CANCELLED and STALE count here — because this is an
// observation of ONE commit the pipeline just landed, and a cancelled run on it
// is still not a run that passed.
func isBadConclusion(conclusion string) bool {
	switch strings.ToUpper(strings.TrimSpace(conclusion)) {
	case "SUCCESS", "SKIPPED", "NEUTRAL":
		return false
	default:
		return true
	}
}

// failingChecks collapses failing RUNS into distinct failing NAMES, sorted, so
// the card fingerprint is a set and not a multiset (#538).
func failingChecks(runs []forgetypes.CheckDetail) []FailingCheck {
	byName := map[string]FailingCheck{}
	for _, run := range runs {
		if !isConcluded(run) || !isBadConclusion(run.Conclusion) {
			continue
		}
		if _, seen := byName[run.Name]; seen {
			continue
		}
		byName[run.Name] = FailingCheck{
			Name:       run.Name,
			Conclusion: strings.ToLower(strings.TrimSpace(run.Conclusion)),
			URL:        run.DetailsURL,
		}
	}
	out := make([]FailingCheck, 0, len(byName))
	for _, fc := range byName {
		out = append(out, fc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// markRequired flags the failing checks the branch requires. Best-effort: a
// failed lookup leaves every check advisory, which is the conservative reading
// (a card that under-states severity beats one that shouts about a check
// nothing requires).
func markRequired(ctx context.Context, reader MainCheckReader, owner, repo, branch string, failing []FailingCheck) {
	if branch == "" || len(failing) == 0 {
		return
	}
	required, err := reader.GetRequiredCheckNames(ctx, owner, repo, branch)
	if err != nil {
		return
	}
	isRequired := make(map[string]bool, len(required))
	for _, name := range required {
		isRequired[name] = true
	}
	for i := range failing {
		failing[i].Required = isRequired[failing[i].Name]
	}
}

// ProducerMergeCommitChecks is the attention producer id for a merge that
// turned the default branch red. Half of the sticky (producer, idempotency_key)
// identity — never change it.
const ProducerMergeCommitChecks = "merge-commit-checks"

// MergeCommitChecksKey is the standing identity: one card per (repo, branch).
// The condition is "the pipeline's latest merge onto this branch is red"; a
// second red merge onto the same branch updates the card (new fingerprint, so
// it re-alerts), and a green one retracts it.
func MergeCommitChecksKey(owner, repo, branch string) string {
	return fmt.Sprintf("%s:%s/%s:%s", ProducerMergeCommitChecks, owner, repo, branch)
}

// BuildMainRedCard renders the card for a red verdict. Exported so both writers
// — the CLI hook and the in-process scheduler — render one card for one
// condition (the dual-path-drift rule in docs/ATTENTION_PRODUCERS.md).
//
// Severity follows the default-branch sweep's #1250 distinction: a failing
// REQUIRED check blocks every open PR (blocking_fleet); a failing non-required
// check blocks nothing but the branch is red and nothing else will say which
// merge did it (fyi).
//
// The card deliberately offers no repair. Nothing in the verb registry fixes a
// red main; what it can do is name the merge, the PR, and the failing checks,
// because the operator's next move differs entirely between a flaky test and a
// suite that started failing with the code this merge landed.
func BuildMainRedCard(owner, repo, branch string, issue, pr int, res MainCheckResult) attention.DecisionRequest {
	fullRepo := owner + "/" + repo
	names := res.FailingNames()
	short := shortSHA(res.MergeCommitSha)

	title := fmt.Sprintf("%s is red after PR #%d merged — %q failed on %s", branch, pr, names[0], fullRepo)
	if len(names) > 1 {
		title = fmt.Sprintf("%s is red after PR #%d merged — %d checks failed on %s", branch, pr, len(names), fullRepo)
	}
	severity := attention.SeverityFYI
	if res.AnyRequiredFailing() {
		severity = attention.SeverityBlockingFleet
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The pipeline merged PR #%d (issue #%d) onto %s as commit %s. ", pr, issue, branch, short)
	b.WriteString("The PR's own checks were green; the merge commit's run on the branch was not.\n\n")
	for _, f := range res.Failing {
		fmt.Fprintf(&b, "- %s — %s", f.Name, f.Conclusion)
		if f.Required {
			b.WriteString(" (required)")
		}
		if f.URL != "" {
			fmt.Fprintf(&b, "\n  %s", f.URL)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nA green PR check is a prediction; this is the observation. ")
	b.WriteString("Three things only the post-merge run can catch: a nondeterministic test, merge skew with another PR, or an environment difference (secrets and permissions main has and PR runs do not). ")
	b.WriteString("Nightgauge cannot fix this: the next action is a human's. ")
	b.WriteString("The card retracts on its own when the next pipeline merge onto this branch observes green.")

	var url string
	for _, f := range res.Failing {
		if f.URL != "" {
			url = f.URL
			break
		}
	}

	return attention.DecisionRequest{
		IdempotencyKey: MergeCommitChecksKey(owner, repo, branch),
		Producer:       ProducerMergeCommitChecks,
		Kind:           attention.KindUnblock,
		Severity:       severity,
		Title:          title,
		Body:           b.String(),
		Standing:       true,
		// The fingerprint is WHICH merge and WHICH checks. A different merge
		// going red is a new fact and re-alerts; the same merge re-observed is
		// not and refreshes silently. A human who dismissed this exact red
		// merge is not handed it back (ADR-015 §M).
		Fingerprint: "sha:" + res.MergeCommitSha + ";checks:" + strings.Join(names, ","),
		ExpiresAt:   time.Now().UTC().Add(attention.StandingExpiry).Format(time.RFC3339Nano),
		Context: attention.Context{
			Repo:    fullRepo,
			Issue:   issue,
			PR:      pr,
			Blocker: fmt.Sprintf("check(s) failing on %s at %s: %s", branch, short, strings.Join(names, ", ")),
			URL:     url,
		},
		Options: []attention.Option{
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// ReportMainChecks turns a verdict into Action Center state: a red merge raises
// (or updates) the branch's standing card; a green one retracts it. Every other
// verdict — pending, no checks, error, skipped — says nothing about the branch
// and therefore neither raises nor retracts. Nil-safe and fail-open: the hook
// is non-blocking, and a card that cannot be written must not turn a
// successful merge into an error. Returns a one-line description of what it
// did, for the caller's log.
func ReportMainChecks(store *attention.Store, owner, repo, branch string, issue, pr int, res MainCheckResult) string {
	if store == nil || branch == "" {
		return ""
	}
	switch res.Verdict {
	case MainChecksRed:
		req := BuildMainRedCard(owner, repo, branch, issue, pr, res)
		id, err := attention.NewID()
		if err != nil {
			return fmt.Sprintf("could not mint an attention id: %v", err)
		}
		req.ID = id
		outcome, _, err := store.Raise(req)
		if err != nil {
			return fmt.Sprintf("could not raise the %s card: %v", ProducerMergeCommitChecks, err)
		}
		return fmt.Sprintf("%s card %s", ProducerMergeCommitChecks, outcome)
	case MainChecksGreen:
		retracted, err := store.AutoResolveKey(ProducerMergeCommitChecks, MergeCommitChecksKey(owner, repo, branch))
		if err != nil {
			return fmt.Sprintf("could not retract the %s card: %v", ProducerMergeCommitChecks, err)
		}
		if retracted {
			return fmt.Sprintf("%s card retracted — %s is green again", ProducerMergeCommitChecks, branch)
		}
		return ""
	default:
		return ""
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
