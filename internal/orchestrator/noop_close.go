package orchestrator

// The operator's close of a partial-delivery no-op (#3487).
//
// CloseMetRemainder closes only on a deterministic all-met verdict. A
// remainder the judge cannot decide stays open for a human (#3089), and this
// is that human's decision: the Action Center card's close option.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/hooks"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

var (
	// ErrRemainderRevised refuses a close over criteria the issue no longer has.
	ErrRemainderRevised = errors.New(
		"the issue's criteria changed after this card was raised; review the issue and close it by hand, or move it to Ready")
	// ErrRemainderSuperseded refuses a close a later run overtook.
	ErrRemainderSuperseded = errors.New(
		"a later run replaced the remainder this card was raised over; review the issue and close it by hand")
)

// OperatorRemainderClosedMarker tags the comment an operator's close posts.
// It shares no prefix with PartialRemainderClosedMarker, which records the
// judge's verdict.
const OperatorRemainderClosedMarker = "<!-- nightgauge:operator-closed-remainder"

// RemainderDecision is one operator's close, as the card offered it.
type RemainderDecision struct {
	// StateRoot is the repository root that holds partial-delivery-{N}.json.
	StateRoot string
	// Fingerprint identifies the issue's criteria the close was offered over.
	Fingerprint string
	// Login is the forge user the close is written as, "" when none is known.
	// It is the only name the comment may carry.
	Login string
	// Criteria are the open criteria the card listed.
	Criteria []attention.RemainderCriterion
}

// CloseRemainderByOperator closes target as completed because an operator
// decided its open criteria are met.
//
// An open issue is first checked against the card: ErrRemainderRevised when
// its criteria changed, ErrRemainderSuperseded when the recorded remainder is
// no longer the card's. Then it gets a comment, the close and the board move
// to Done. The comment comes first so a closed issue never lacks it; it states
// the decision, which holds if the close then fails, and a retry does not
// repeat it. No box is ticked, as nothing judged the criteria met.
//
// An issue already closed only has its board row synced. A comment or close
// failure is returned; a failed board sync is logged, as the issue is closed
// either way.
func CloseRemainderByOperator(ctx context.Context, deps PartialCloseDeps, target PartialCloseTarget, d RemainderDecision) error {
	if target.NodeID == "" {
		return ErrNoIssueToClose
	}
	if deps.Issues == nil {
		return ErrNoCloseDeps
	}
	if !strings.EqualFold(target.State, "closed") {
		if d.Fingerprint == "" || preflight.ACFingerprint(target.Body) != d.Fingerprint {
			return ErrRemainderRevised
		}
		if remainderSuperseded(target, d) {
			return ErrRemainderSuperseded
		}
		if err := postOperatorDecision(ctx, deps, target, operatorRemainderClosedComment(target, d)); err != nil {
			return err
		}
		if err := deps.Issues.CloseIssue(ctx, target.NodeID); err != nil {
			return fmt.Errorf("close #%d: %w", target.Number, err)
		}
	}
	board := hooks.BoardSyncNotAttempted
	if deps.Board != nil && deps.ProjectNumber > 0 {
		board = hooks.SyncIssueDone(ctx, deps.Board, hooks.PostMergeInput{
			IssueNumber: target.Number, RepositoryOwner: deps.Owner, RepositoryName: deps.Repo,
			ProjectNumber: deps.ProjectNumber,
		})
	}
	log.Printf("#%d: remainder closed by an operator (board %s) [#3487]", target.Number, board)
	return nil
}

// remainderSuperseded reports whether the current partial-delivery record no
// longer holds what the card was raised over. Every run removes or rewrites
// that record when it starts, so this stands in for retracting the card. An
// unreadable record supersedes too; its cause names a local path, so it is
// logged and not returned.
func remainderSuperseded(target PartialCloseTarget, d RemainderDecision) bool {
	rec, err := ReadPartialDeliveryContext(d.StateRoot, target.Number)
	if err != nil {
		log.Printf("#%d: operator close refused, no current remainder: %v [#3487]", target.Number, err)
		return true
	}
	if rec.PR != target.PR {
		return true
	}
	recorded := make(map[string]bool, len(rec.Criteria))
	for _, text := range rec.Criteria {
		recorded[partialRemainderKey(text)] = true
	}
	for _, c := range d.Criteria {
		if !recorded[partialRemainderKey(c.Text)] {
			return true
		}
	}
	return false
}

// postOperatorDecision posts body unless an earlier attempt already did. Only
// an identical body counts, so a comment forging the marker suppresses nothing.
func postOperatorDecision(ctx context.Context, deps PartialCloseDeps, target PartialCloseTarget, body string) error {
	if deps.Comments != nil {
		posted, err := deps.Comments.IssueComments(ctx, deps.Owner, deps.Repo, target.Number)
		if err != nil {
			return fmt.Errorf("read the comments on #%d: %w", target.Number, err)
		}
		for _, c := range posted {
			if strings.TrimSpace(c.Body) == strings.TrimSpace(body) {
				return nil
			}
		}
	}
	if err := deps.Issues.AddComment(ctx, target.NodeID, body); err != nil {
		return fmt.Errorf("post the close comment on #%d: %w", target.Number, err)
	}
	return nil
}

func operatorRemainderClosedComment(target PartialCloseTarget, d RemainderDecision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s pr=%d -->\n", OperatorRemainderClosedMarker, target.PR)
	b.WriteString("## Operator decision: close as completed\n\n")
	who := "An operator"
	if forgeLoginRE.MatchString(d.Login) {
		who = "`" + d.Login + "`"
	}
	fmt.Fprintf(&b, "%s decided that the criteria left open after PR #%d are met, ", who, target.PR)
	b.WriteString("and chose to close the issue as completed from the Action Center. " +
		"This is a human decision, not the judge's verdict. The criteria that were open, with the judge's evidence:\n\n")
	for _, c := range d.Criteria {
		fmt.Fprintf(&b, "- %s — %s\n", quoteForComment(c.Text), quoteForComment(c.Evidence))
	}
	return b.String()
}

// forgeLoginRE is a forge user's login: at most 39 characters, with the
// underscore a managed user's carries. An email address, a display name and a
// bot's `name[bot]` are not one.
var forgeLoginRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)

var (
	// commentMarkupEscaper makes markdown and HTML literal, and breaks what
	// would mention, reference or link. One pass, so no escape is re-escaped.
	commentMarkupEscaper = strings.NewReplacer(
		"&", "&amp;", "<", "&lt;",
		`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`,
		"[", `\[`, "]", `\]`, "(", `\(`, ")", `\)`,
		"~", `\~`, "|", `\|`, "!", `\!`, ">", `\>`,
		"+", `\+`, "-", `\-`, "$", `\$`,
		"#", "\\#\u200b",
		"@", "@\u200b",
		"://", ":\u200b//",
	)
	// wwwAutolinkRE matches the scheme-less autolink's prefix.
	wwwAutolinkRE = regexp.MustCompile(`(?i)(www)\.`)
	// orderedItemRE matches a list number opening the quoted text.
	orderedItemRE = regexp.MustCompile(`^(\d+)\.`)
)

// quoteForComment renders untrusted text as one line of plain text in a
// public comment. Whitespace is collapsed first so no closing keyword forms
// after UntrustedACText has looked for one.
func quoteForComment(s string) string {
	text := pmstages.UntrustedACText(strings.Join(strings.Fields(s), " "))
	text = commentMarkupEscaper.Replace(text)
	text = wwwAutolinkRE.ReplaceAllString(text, "${1}\u200b.")
	return orderedItemRE.ReplaceAllString(text, `${1}\.`)
}
