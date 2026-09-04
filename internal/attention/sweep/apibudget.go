package sweep

// Workspace producer: GitHub API budget exhaustion (#1347).
//
// An exhausted GraphQL window does not look like an error. Every board read,
// every sweep and every PR check fails for the rest of the hour, and what the
// operator sees is a queue with nothing moving — which is byte-identical to an
// idle queue with nothing to do. That silence is the whole problem: the one
// state where the Action Center MUST speak is the state in which every
// producer that could speak has just lost its ability to call the API.
//
// This producer answers from the ledger on disk instead of from the API, which
// is why it can report the outage at all. The ledger is written unattended
// (#1347), so the evidence for an exhaustion is already recorded by the time
// anyone looks — and it names the caller, which is the part no post-hoc
// reconstruction has ever managed.
//
// WORKSPACE-SCOPED, not repo-scoped, because the budget is not a property of
// any repository. One token has one hourly GraphQL quota, and every repo in
// the workspace spends out of it; carding a repo would attribute a
// workspace-wide outage to whichever repo happened to be swept first.
//
// FYI severity, not a blocker: by the time this is read the window has almost
// certainly reset, and nothing is waiting on a human decision. What the card
// carries is the attribution — the caller that spent the budget — because that
// is the input to the only durable fix, and it is unrecoverable once the
// ledger rotates past it.
//
// NO REPAIR VERB. There is no bounded, deterministic action that fixes "a
// caller is too expensive"; the fix is a code change to that caller. A button
// that silently does nothing is worse than no button (Invariant 3), so this
// card ships a dismiss and the attribution, and nothing else.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/github"
)

// ProducerAPIBudget is the stable producer id. It is half of the sticky
// (producer, idempotency_key) identity, so it must never change.
const ProducerAPIBudget = "api-budget"

// apiBudgetLookback is how far back the producer looks for an exhaustion.
//
// Six hours rather than the one-hour window `doctor` reports: a sweep runs on
// its own cadence and an operator reads the Action Center when they happen to
// open it, so a one-hour window would routinely miss an exhaustion that
// happened between sweeps. The card says when it happened, so a stale one is
// legible rather than misleading.
const apiBudgetLookback = 6 * time.Hour

// APIBudget reports a GraphQL quota exhaustion recorded in the request ledger.
type APIBudget struct {
	// ReadWindow is a seam for tests. Nil uses the real ledger.
	ReadWindow func(workspaceRoot string, d time.Duration, now time.Time) (github.LedgerWindow, error)
	// Now is a seam for tests. Nil uses the wall clock.
	Now func() time.Time
}

func init() { Default.RegisterWorkspace(&APIBudget{}) }

// Name implements WorkspaceProducer.
func (p *APIBudget) Name() string { return ProducerAPIBudget }

// Evaluate implements WorkspaceProducer.
//
// Invariant 1 applies with an extra wrinkle worth naming: an ABSENT ledger is
// not an error and not a condition. A workspace with the ledger switched off,
// or one that has made no GitHub requests, has genuinely observed no
// exhaustion — returning an error there would leave a stale card standing
// forever on a workspace that opted out of the instrument.
func (p *APIBudget) Evaluate(_ context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error) {
	if strings.TrimSpace(in.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("api-budget: no workspace root — the ledger cannot be located, which is not the same as no exhaustion")
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	read := github.ReadWindow
	if p.ReadWindow != nil {
		read = p.ReadWindow
	}

	w, err := read(in.WorkspaceRoot, apiBudgetLookback, now)
	if err != nil {
		if isNoLedger(err) {
			return nil, nil // opted out, or nothing has called GitHub yet
		}
		return nil, fmt.Errorf("api-budget: could not read the request ledger: %w", err)
	}
	if !w.Exhausted {
		return nil, nil // positive observation: the budget held
	}

	return []attention.DecisionRequest{p.request(w, now)}, nil
}

func isNoLedger(err error) bool {
	return err != nil && strings.Contains(err.Error(), github.ErrNoLedger.Error())
}

func (p *APIBudget) request(w github.LedgerWindow, now time.Time) attention.DecisionRequest {
	top := "none attributed (no priced call in the window)"
	var topName string
	if named := namedSpenders(w); len(named) > 0 {
		topName = named[0].Caller
		parts := make([]string, 0, len(named))
		for _, c := range named {
			parts = append(parts, fmt.Sprintf("%s — %d points over %d calls", c.Caller, c.Points, c.Calls))
		}
		top = strings.Join(parts, "\n  ")
	}

	when := "recently"
	if !w.ExhaustedAt.IsZero() {
		when = fmt.Sprintf("%s (%s ago)",
			w.ExhaustedAt.Format(time.RFC1123), now.Sub(w.ExhaustedAt).Round(time.Minute))
	}

	title := "GitHub GraphQL quota was exhausted"
	if topName != "" {
		title = fmt.Sprintf("GitHub GraphQL quota was exhausted — %s spent the most", topName)
	}

	body := fmt.Sprintf(
		"The %s quota reached ZERO %s. While a window is exhausted every board read, sweep and PR "+
			"check fails, and the symptom is a pipeline where nothing moves — indistinguishable "+
			"from an idle queue, which is why this outage has historically gone unreported.\n\n"+
			"Biggest spenders in the %s before it ran out:\n  %s\n\n"+
			"Total %d GraphQL point(s) over %d request(s)%s.\n\n"+
			"There is no button that fixes this: the repair is a change to the caller above, or to "+
			"how often it runs. Run `nightgauge api-usage --since 6h --by op` for the query-level "+
			"breakdown, and `nightgauge doctor` for the current hour.\n\n"+
			"Dismissing mutes this until a DIFFERENT exhaustion is recorded.",
		w.ExhaustedResource, when, apiBudgetLookback, top, w.Points, w.Calls, cachedNote(w))

	// The fingerprint is the exhaustion's own timestamp plus the top spender:
	// re-observing the SAME outage on the next sweep must not re-alert, while a
	// new exhaustion — or the same one now attributed to a different caller
	// after a fix — is a genuine change worth surfacing again.
	fingerprint := fmt.Sprintf("exhausted:%s:%s", w.ExhaustedAt.UTC().Format(time.RFC3339), topName)

	return attention.DecisionRequest{
		IdempotencyKey: ProducerAPIBudget + ":graphql",
		Kind:           attention.KindHandoff,
		Severity:       attention.SeverityFYI,
		Title:          title,
		Body:           body,
		Fingerprint:    fingerprint,
		Context:        attention.Context{Blocker: "GitHub GraphQL quota exhausted"},
		Options: []attention.Option{
			{ID: "dismiss", Label: "Dismiss — I've seen the attribution", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// namedSpenders returns up to three callers that actually cost points. A
// zero-point caller is not a spender and padding the card with it hides the
// row that is the answer.
func namedSpenders(w github.LedgerWindow) []github.LedgerCallerSpend {
	out := make([]github.LedgerCallerSpend, 0, 3)
	for _, c := range w.TopCallers {
		if c.Points == 0 {
			continue
		}
		out = append(out, c)
		if len(out) == 3 {
			break
		}
	}
	return out
}

func cachedNote(w github.LedgerWindow) string {
	if w.Cached == 0 {
		return ""
	}
	return fmt.Sprintf(", %d of them served free from the ETag cache", w.Cached)
}
