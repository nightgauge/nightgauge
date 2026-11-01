package stages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// The enqueue gate (#2730). Pipeline review findings arrive as an issue comment
// from the workflow, not as a pull request review, so reviewDecision never saw
// them: #2757 was enqueued six minutes after eight majors, and the LLM stage
// spent an unbounded number of rounds fixing every finding it read. This file
// holds both bounds: what a finding may do to an enqueue, and how many fix
// rounds a PR may spend. See docs/PR_MERGE_STAGE.md.

// MaxReviewFixRounds is the hard cap on review-fix rounds. It is a constant, not
// config: a cap an operator can raise from a stalled run is not a cap.
const MaxReviewFixRounds = 2

// reviewRoundExpiry is how long an open round holds the enqueue. A stage killed
// mid-round never closes it, and that must not wedge the PR.
const reviewRoundExpiry = 60 * time.Minute

// reviewFindingsSourceStage attributes filed findings in their provenance line.
const reviewFindingsSourceStage = "pr-merge"

const (
	// reviewCommentMarker must equal MARKER in scripts/pr-review.mjs.
	reviewCommentMarker = "<!-- nightgauge:pr-review -->"
	// reviewCommentAuthor must equal REVIEWER in .github/workflows/pr-review.yml.
	reviewCommentAuthor = "github-actions[bot]"
	// reviewCleanSentence is what the renderer prints in place of findings.
	reviewCleanSentence = "No findings from the correctness, security and plan-compliance passes."
)

// Bounds on the discovered-work record a finding becomes.
const (
	reviewSummaryMaxRunes  = 200
	reviewEvidenceMaxRunes = 2000
)

var (
	reviewCountRE   = regexp.MustCompile(`^(\d+) blocker, (\d+) major, (\d+) nitpick\.$`)
	reviewFindingRE = regexp.MustCompile("^- \\*\\*(blocker|major|nitpick)\\*\\* \\(([a-z-]+)\\) `([^`]+)` — (.+)$")
	reviewLineSufRE = regexp.MustCompile(`:\d+$`)
)

// ReviewClass is what a finding may do to an enqueue.
type ReviewClass string

const (
	// ReviewClassHold stops the enqueue and earns a fix round.
	ReviewClassHold ReviewClass = "hold"
	// ReviewClassFile becomes a tracked issue; the PR still enqueues.
	ReviewClassFile ReviewClass = "file"
	// ReviewClassIgnore affects nothing.
	ReviewClassIgnore ReviewClass = "ignore"
)

// ReviewFinding is one parsed bullet of the review comment.
type ReviewFinding struct {
	Severity string `json:"severity"`
	Pass     string `json:"pass"`
	// Where is the anchor as printed, `path` or `path:line`.
	Where string `json:"where"`
	Claim string `json:"claim"`
	Fix   string `json:"fix,omitempty"`
}

// Path is the anchored file, without the line.
func (f ReviewFinding) Path() string {
	return reviewLineSufRE.ReplaceAllString(f.Where, "")
}

// Class applies the rule recorded for #2730's documented-user-path criterion:
// every blocker holds; a correctness major anchored on a surface users follow
// (README*, docs/**, packages/*/package.json) holds; any other major is filed;
// a nitpick is ignored. It is a path-and-severity test on purpose, because a
// semantic judgement would need an LLM on the zero-token path.
func (f ReviewFinding) Class() ReviewClass {
	switch {
	case f.Severity == "blocker":
		return ReviewClassHold
	case f.Severity == "nitpick":
		return ReviewClassIgnore
	case f.Pass == "correctness" && isDocumentedUserPath(f.Path()):
		return ReviewClassHold
	}
	return ReviewClassFile
}

func isDocumentedUserPath(p string) bool {
	if strings.HasPrefix(path.Base(p), "README") || strings.HasPrefix(p, "docs/") {
		return true
	}
	ok, err := path.Match("packages/*/package.json", p)
	return err == nil && ok
}

// ParseReviewComment reads the comment scripts/pr-review.mjs renders. A body it
// cannot account for is an error, never an empty list: an unread review that
// looks clean is the failure this gate exists to stop.
func ParseReviewComment(body string) ([]ReviewFinding, error) {
	if !strings.HasPrefix(body, reviewCommentMarker) {
		return nil, errors.New("comment does not start with the review marker")
	}
	var (
		findings []ReviewFinding
		counts   []int
		inFix    bool
		clean    bool
	)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := reviewFindingRE.FindStringSubmatch(line); m != nil {
			findings = append(findings, ReviewFinding{Severity: m[1], Pass: m[2], Where: m[3], Claim: m[4]})
			inFix = false
			continue
		}
		if strings.HasPrefix(line, "  - Fix: ") && len(findings) > 0 {
			findings[len(findings)-1].Fix = strings.TrimPrefix(line, "  - Fix: ")
			inFix = true
			continue
		}
		if strings.HasPrefix(line, "    ") && inFix {
			last := &findings[len(findings)-1]
			last.Fix += "\n" + strings.TrimPrefix(line, "    ")
			continue
		}
		inFix = false
		if m := reviewCountRE.FindStringSubmatch(line); m != nil {
			counts = counts[:0]
			for _, n := range m[1:] {
				v, err := strconv.Atoi(n)
				if err != nil {
					return nil, fmt.Errorf("count line %q: %w", line, err)
				}
				counts = append(counts, v)
			}
		}
		if line == reviewCleanSentence {
			clean = true
		}
	}
	if counts == nil {
		if clean && len(findings) == 0 {
			return nil, nil
		}
		return nil, errors.New("no count line and no clean-review sentence")
	}
	got := map[string]int{}
	for _, f := range findings {
		got[f.Severity]++
	}
	// Only nitpicks may be omitted from the bullets, so the blockers and majors
	// must all be present for "nothing here holds" to mean anything.
	if got["blocker"] != counts[0] || got["major"] != counts[1] || got["nitpick"] > counts[2] {
		return nil, fmt.Errorf("count line says %d blocker, %d major, %d nitpick; parsed %d, %d, %d",
			counts[0], counts[1], counts[2], got["blocker"], got["major"], got["nitpick"])
	}
	return findings, nil
}

// ReviewComment is the newest pipeline review comment on a PR.
type ReviewComment struct {
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// reviewClient is implemented by ghClients that can read the review comment. A
// client without it skips the gate.
type reviewClient interface {
	// ReviewComment returns the newest review comment, and false when there is
	// none: a PR the review skipped, or one it has not reached.
	ReviewComment(ctx context.Context, prNumber int) (ReviewComment, bool, error)
}

// lastReviewComment decodes the stream of objects `gh api --paginate --jq`
// prints, oldest first. The workflow edits one comment in place, so more than
// one is the leftover of a re-run and the newest governs.
func lastReviewComment(out []byte) (ReviewComment, bool, error) {
	var last ReviewComment
	found := false
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var c ReviewComment
		err := dec.Decode(&c)
		if errors.Is(err, io.EOF) {
			return last, found, nil
		}
		if err != nil {
			return ReviewComment{}, false, fmt.Errorf("parse review comment JSON: %w", err)
		}
		last, found = c, true
	}
}

// ReviewLedger is pr-merge-review-{N}.json: what this PR has spent on review.
// It lives in the pipeline state directory, which outlives the worktree, so a
// re-dispatched run inherits the count instead of a fresh cap.
type ReviewLedger struct {
	Rounds     int        `json:"rounds"`
	InProgress bool       `json:"in_progress"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	// Filed maps a discovered-work record id to its issue, so a retried gate
	// spends no forge calls on findings it already filed.
	Filed map[string]int `json:"filed,omitempty"`
}

func reviewLedgerPath(workdir string, issueNumber int) (string, error) {
	p := pipelineContextPath(workdir, fmt.Sprintf("pr-merge-review-%d.json", issueNumber))
	if p == "" {
		return "", fmt.Errorf("resolve pipeline state directory for %s", workdir)
	}
	return p, nil
}

// LoadReviewLedger reads the ledger; a missing file is an empty ledger.
func LoadReviewLedger(workdir string, issueNumber int) (ReviewLedger, error) {
	p, err := reviewLedgerPath(workdir, issueNumber)
	if err != nil {
		return ReviewLedger{}, err
	}
	data, err := readFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return ReviewLedger{}, nil
	}
	if err != nil {
		return ReviewLedger{}, err
	}
	var l ReviewLedger
	if err := json.Unmarshal(data, &l); err != nil {
		return ReviewLedger{}, fmt.Errorf("parse review ledger: %w", err)
	}
	return l, nil
}

func saveReviewLedger(workdir string, issueNumber int, l ReviewLedger) error {
	p, err := reviewLedgerPath(workdir, issueNumber)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(p, append(data, '\n'), 0o644)
}

// roundOpen reports whether a round is still running at now. An open round older
// than reviewRoundExpiry belonged to a stage that died.
func (l ReviewLedger) roundOpen(now time.Time) bool {
	return l.InProgress && l.StartedAt != nil && now.Sub(*l.StartedAt) < reviewRoundExpiry
}

// BeginReviewRound opens a fix round. It refuses past the cap and beside an open
// round, so the LLM stage cannot spend a third round whatever it concludes.
func BeginReviewRound(workdir string, issueNumber int, now time.Time) (ReviewLedger, error) {
	l, err := LoadReviewLedger(workdir, issueNumber)
	if err != nil {
		return l, err
	}
	if l.roundOpen(now) {
		return l, fmt.Errorf("%s: round %d is still open", ReasonReviewFixInProgress, l.Rounds)
	}
	if l.Rounds >= MaxReviewFixRounds {
		return l, fmt.Errorf("%s: %d of %d rounds spent", ReasonReviewRoundCap, l.Rounds, MaxReviewFixRounds)
	}
	l.Rounds++
	l.InProgress = true
	l.StartedAt = &now
	l.EndedAt = nil
	return l, saveReviewLedger(workdir, issueNumber, l)
}

// EndReviewRound closes the open round, after its fix is pushed.
func EndReviewRound(workdir string, issueNumber int, now time.Time) (ReviewLedger, error) {
	l, err := LoadReviewLedger(workdir, issueNumber)
	if err != nil {
		return l, err
	}
	if !l.InProgress {
		return l, errors.New("no review round is open")
	}
	l.InProgress = false
	l.EndedAt = &now
	return l, saveReviewLedger(workdir, issueNumber, l)
}

// ReviewVerdict is DecideEnqueue's answer.
type ReviewVerdict string

const (
	// ReviewEnqueue: nothing holds. Any `file` findings are filed first.
	ReviewEnqueue ReviewVerdict = "enqueue"
	// ReviewFixRound: a hold finding, with rounds left.
	ReviewFixRound ReviewVerdict = "fix-round"
	// ReviewHoldAtCap: a hold finding, with none left.
	ReviewHoldAtCap ReviewVerdict = "hold-at-cap"
	// ReviewWaitRound: a round is open, so the head is about to change.
	ReviewWaitRound ReviewVerdict = "wait-round"
	// ReviewUnreadable: no verdict could be reached.
	ReviewUnreadable ReviewVerdict = "unreadable"
)

// DecideEnqueue is the pure gate. An open round comes first whatever the
// findings say, because its head is not the one being reviewed.
//
// Hold findings in a comment older than the last round's end are the findings
// that round answered: the comment has not caught up with the new head, and
// reading it again would spend a round on work already done. Only a review
// posted after the round can hold the enqueue again. Rounds exist only for hold
// findings, so a green PR with `file` findings never starts one.
func DecideEnqueue(findings []ReviewFinding, ledger ReviewLedger, commentAt, now time.Time) ReviewVerdict {
	if ledger.roundOpen(now) {
		return ReviewWaitRound
	}
	if ledger.EndedAt != nil && commentAt.Before(*ledger.EndedAt) {
		return ReviewEnqueue
	}
	for _, f := range findings {
		if f.Class() != ReviewClassHold {
			continue
		}
		if ledger.Rounds >= MaxReviewFixRounds {
			return ReviewHoldAtCap
		}
		return ReviewFixRound
	}
	return ReviewEnqueue
}

// ReviewFiling files a capture of review findings. See followup.ReviewFiling.
type ReviewFiling func(ctx context.Context, workdir, repo string, capture *discoveredwork.Capture) (*followup.Result, error)

// ReviewGateOutcome is the gate's result. Proceed false carries the PRMergePath
// and reason the runner stops with.
type ReviewGateOutcome struct {
	Proceed  bool            `json:"proceed"`
	Verdict  ReviewVerdict   `json:"verdict"`
	Path     PRMergePath     `json:"path,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	PRNumber int             `json:"pr_number,omitempty"`
	Rounds   int             `json:"rounds"`
	Hold     []ReviewFinding `json:"hold,omitempty"`
	Filed    []int           `json:"filed,omitempty"`
}

func (o ReviewGateOutcome) stop(path PRMergePath, verdict ReviewVerdict, reason string) ReviewGateOutcome {
	o.Proceed, o.Path, o.Verdict, o.Reason = false, path, verdict, reason
	return o
}

// ReviewGate evaluates the gate for issueNumber's PR, for the LLM stage: the
// same code the runner calls, reached through `pr-stage review-gate`.
func (r *DeterministicRunner) ReviewGate(ctx context.Context, issueNumber int, workdir string) ReviewGateOutcome {
	prNumber, err := r.prContextRead(workdir, issueNumber)
	if err != nil {
		return ReviewGateOutcome{}.stop(PathPunt, ReviewUnreadable, classifyContextError(err))
	}
	gh := r.gh
	if wb, ok := gh.(workdirBoundClient); ok {
		gh = wb.withWorkdir(workdir)
	}
	return r.reviewGate(ctx, gh, workdir, issueNumber, prNumber)
}

// reviewGate reads the review and the ledger, decides, and on enqueue files
// every open finding before the caller may merge. A client that cannot read
// reviews, or a PR with no review comment, proceeds: the gate reads what exists
// at enqueue and does not wait for a review that was never going to run.
func (r *DeterministicRunner) reviewGate(ctx context.Context, gh ghClient, workdir string, issueNumber, prNumber int) ReviewGateOutcome {
	out := ReviewGateOutcome{Proceed: true, Verdict: ReviewEnqueue, PRNumber: prNumber}
	rc, ok := gh.(reviewClient)
	if !ok {
		return out
	}
	ledger, err := LoadReviewLedger(workdir, issueNumber)
	if err != nil {
		return out.stop(PathPunt, ReviewUnreadable, fmt.Sprintf("%s: %s", ReasonReviewReadFailed, truncateErr(err, 200)))
	}
	out.Rounds = ledger.Rounds

	now := r.now()
	comment, found, err := rc.ReviewComment(ctx, prNumber)
	if err != nil {
		if isRateLimitErr(err) {
			return out.stop(PathPunt, ReviewUnreadable, ReasonRateLimited)
		}
		return out.stop(PathPunt, ReviewUnreadable, fmt.Sprintf("%s: %s", ReasonReviewReadFailed, truncateErr(err, 200)))
	}
	if !found {
		// An open round still holds: its push has not landed.
		if ledger.roundOpen(now) {
			return out.stop(PathRefused, ReviewWaitRound, ReasonReviewFixInProgress)
		}
		return out
	}
	findings, err := ParseReviewComment(comment.Body)
	if err != nil {
		return out.stop(PathRefused, ReviewUnreadable, fmt.Sprintf("%s: %s", ReasonReviewUnparseable, truncateErr(err, 200)))
	}

	verdict := DecideEnqueue(findings, ledger, comment.UpdatedAt, now)
	out.Verdict = verdict
	for _, f := range findings {
		if f.Class() == ReviewClassHold {
			out.Hold = append(out.Hold, f)
		}
	}
	switch verdict {
	case ReviewWaitRound:
		return out.stop(PathRefused, verdict, ReasonReviewFixInProgress)
	case ReviewFixRound:
		return out.stop(PathPunt, verdict, fmt.Sprintf("%s: %d hold finding(s), round %d of %d spent",
			ReasonReviewFixNeeded, len(out.Hold), ledger.Rounds, MaxReviewFixRounds))
	case ReviewHoldAtCap:
		return out.stop(PathRefused, verdict, fmt.Sprintf("%s: %d hold finding(s) after %d rounds",
			ReasonReviewRoundCap, len(out.Hold), ledger.Rounds))
	}
	filed, err := r.fileReviewFindings(ctx, workdir, issueNumber, prNumber, findings, ledger)
	out.Filed = filed
	if err != nil {
		return out.stop(PathRefused, verdict, fmt.Sprintf("%s: %s", ReasonReviewFilingFailed, truncateErr(err, 300)))
	}
	return out
}

// reviewRecord builds the discovered-work record for a finding. The summary
// carries no line number, so a push that moves the code does not re-file it, and
// its id is therefore stable across retries.
func reviewRecord(prNumber, issueNumber int, f ReviewFinding, now time.Time) discoveredwork.Record {
	summary := truncateRunes(sanitizeReviewText(fmt.Sprintf("Review finding on PR #%d: %s: %s", prNumber, f.Path(), f.Claim)), reviewSummaryMaxRunes)
	evidence := fmt.Sprintf("%s (%s) at %s\n\n%s", f.Severity, f.Pass, f.Where, f.Claim)
	if f.Fix != "" {
		evidence += "\n\nFix: " + f.Fix
	}
	rec := discoveredwork.Record{
		ID:                discoveredwork.ID(reviewFindingsSourceStage, summary),
		Summary:           summary,
		Evidence:          []string{truncateRunes(sanitizeReviewText(evidence), reviewEvidenceMaxRunes)},
		SourceStage:       reviewFindingsSourceStage,
		IssueNumber:       issueNumber,
		SuggestedType:     "bug",
		SuggestedPriority: "high",
		SuggestedSize:     "S",
		RecordedAt:        now.UTC().Format(time.RFC3339),
	}
	rec.CandidateTargetFiles = []string{f.Path()}
	if err := discoveredwork.Validate(&rec); err != nil {
		// The anchor is model output; the finding still has to be filed.
		rec.CandidateTargetFiles = []string{}
	}
	if id := os.Getenv("NIGHTGAUGE_RUN_ID"); id != "" {
		rec.RunID = &id
	}
	return rec
}

// sanitizeReviewText drops what the record schema rejects. The text is model
// output: the renderer escapes it for Markdown, not for a control or bidi
// character, and filing must not fail on one.
func sanitizeReviewText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r >= 0x7f && r <= 0x9f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func truncateRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max]))
	}
	return s
}

// fileReviewFindings files every `file` finding not already in the ledger and
// returns their issues. It saves the ledger after whatever it filed, including a
// partial run, so a retry resumes rather than duplicates. Anything short of
// tracked refuses: a merge may not outrun a finding it cannot record. Report
// mode is the operator's choice not to file, so its findings go to the run's
// capture, where the post-merge stage reports them, and the merge proceeds.
func (r *DeterministicRunner) fileReviewFindings(ctx context.Context, workdir string, issueNumber, prNumber int, findings []ReviewFinding, ledger ReviewLedger) ([]int, error) {
	now := r.now()
	capture := &discoveredwork.Capture{SchemaVersion: "1.0", IssueNumber: issueNumber}
	var filed []int
	seen := map[string]bool{}
	for _, f := range findings {
		if f.Class() != ReviewClassFile {
			continue
		}
		rec := reviewRecord(prNumber, issueNumber, f, now)
		if seen[rec.ID] {
			continue
		}
		seen[rec.ID] = true
		if n, ok := ledger.Filed[rec.ID]; ok {
			filed = append(filed, n)
			continue
		}
		capture.Records = append(capture.Records, rec)
	}
	if len(capture.Records) == 0 {
		return filed, nil
	}
	if r.reviewFiling == nil {
		return filed, errors.New("no issue filer is configured")
	}

	res, fileErr := r.reviewFiling(ctx, workdir, readPRContextRepo(workdir, issueNumber), capture)
	if res == nil {
		if fileErr == nil {
			fileErr = errors.New("filer returned no result")
		}
		return filed, fileErr
	}

	reported := map[string]bool{}
	var problems []string
	for _, it := range res.Items {
		switch it.Outcome {
		case followup.OutcomeFiled, followup.OutcomeAlreadyExists, followup.OutcomeDeduped:
			if ledger.Filed == nil {
				ledger.Filed = map[string]int{}
			}
			ledger.Filed[it.ID] = it.IssueNumber
			filed = append(filed, it.IssueNumber)
		case followup.OutcomeWouldFile:
			reported[it.ID] = true
		default:
			problems = append(problems, fmt.Sprintf("%s %s %s", it.Outcome, it.ID, it.Detail))
		}
	}
	if err := saveReviewLedger(workdir, issueNumber, ledger); err != nil {
		log.Printf("pr-merge: could not record filed review findings for #%d: %v", issueNumber, err)
	}
	if fileErr != nil {
		return filed, fileErr
	}
	if len(problems) > 0 {
		return filed, errors.New(strings.Join(problems, "; "))
	}
	if len(reported) == 0 {
		return filed, nil
	}
	log.Printf("pr-merge: follow_up_filing is report-only (%s): %d review finding(s) on PR #%d were not filed",
		res.Reason, len(reported), prNumber)
	dir, err := layout.PipelineStateDir(workdir)
	if err != nil {
		return filed, err
	}
	for _, rec := range capture.Records {
		if !reported[rec.ID] {
			continue
		}
		if _, _, err := discoveredwork.Append(dir, rec, now); err != nil {
			return filed, fmt.Errorf("record report-only finding %s: %w", rec.ID, err)
		}
	}
	return filed, nil
}
