package doctor

// The `github_api_budget` arm (#1347).
//
// Every GraphQL exhaustion this workspace has hit was unattributable after the
// fact. The ledger (#843) prices each call at the transport, but it was opt-in,
// so it was reliably off during the hours that mattered and reliably on only
// while someone watched. #1347 turns it on by default; this arm is the half
// that makes the file mean something to an operator who never runs
// `nightgauge api-usage`.
//
// It answers three questions from one hour of the ledger:
//
//	How much did this workspace spend?   → points, and the projected hourly rate
//	On what?                             → the top three callers by points
//	Did it run out?                      → a window that saw remaining == 0
//
// The exhaustion arm is the important one, and it is deliberately a WARNING
// rather than a silent detail: an exhausted window means every board read,
// every PR check and every sweep failed for the rest of that hour, and the
// symptom an operator actually sees is "nothing is happening" — which looks
// exactly like an idle queue.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// apiBudgetWindow is the span the arm reports on. One hour, because that is
// GitHub's own budget period: a shorter window cannot say whether the hourly
// quota is at risk, and a longer one averages an exhausted hour away against
// quiet ones.
const apiBudgetWindow = time.Hour

// maxAPIBudgetCallers is how many callers the finding names. Three, because
// the finding has to fit in a terminal line and because the tail of this
// distribution is always long and always irrelevant — the fix is nearly
// always one caller.
const maxAPIBudgetCallers = 3

// checkGitHubAPIBudget summarizes the last hour of the request ledger.
//
// An absent ledger is NOT a failure: a fresh workspace that has made no GitHub
// requests has no file, and a workspace that deliberately set
// `github.api_ledger.enabled: false` chose this. Reporting either as a problem
// would make the arm noise on exactly the installs that are fine.
func checkGitHubAPIBudget(workspaceRoot string, now time.Time) (CheckItem, string) {
	if workspaceRoot == "" {
		return CheckItem{OK: true, Detail: "API budget not checked (no workspace root)"}, ""
	}

	w, err := github.ReadWindow(workspaceRoot, apiBudgetWindow, now)
	if err != nil {
		if errors.Is(err, github.ErrNoLedger) {
			return CheckItem{
				OK:     true,
				Detail: "no API ledger yet (no GitHub requests recorded in this workspace)",
			}, ""
		}
		// Unreadable is not "spent nothing" — say so rather than reporting a
		// clean bill derived from a file we could not open.
		msg := fmt.Sprintf("github api budget unverifiable: %v", err)
		return CheckItem{OK: false, Detail: "could not read the API ledger", Error: msg}, msg
	}

	if w.Calls == 0 {
		return CheckItem{
			OK:     true,
			Detail: "0 GitHub requests in the last hour",
		}, ""
	}

	// GraphQLCalls, not Calls: the point total above is GraphQL-only, and
	// pairing it with an all-resource request count reads as an arithmetic
	// error to anyone who checks it. The REST traffic is reported separately.
	detail := fmt.Sprintf("%d GraphQL point(s) over %d GraphQL request(s) in the last hour (%.0f/h of %d)%s%s%s",
		w.Points, w.GraphQLCalls, w.PointsPerHour(), github.GraphQLHourlyLimit,
		restSuffix(w), cachedSuffix(w), remainingSuffix(w))

	switch {
	case w.Exhausted:
		msg := fmt.Sprintf(
			"github-api-budget-exhausted: the %s quota hit ZERO %s. Every board read, sweep and "+
				"PR check failed for the rest of that window — which presents as an idle queue, not "+
				"as an error. Top spender(s): %s. Run `nightgauge api-usage --since 1h` for the full "+
				"breakdown",
			w.ExhaustedResource, exhaustedWhen(w, now), topCallerList(w))
		return CheckItem{OK: false, Detail: detail, Error: msg}, msg

	case w.OverIdleBudget():
		msg := fmt.Sprintf(
			"github-api-budget-high: %.0f GraphQL points/hour projected — past %.0f%% of the %d/hour "+
				"quota, which one open workspace can exhaust on its own. Top spender(s): %s. Run "+
				"`nightgauge api-usage --since 1h --by op` to see which query is repeating",
			w.PointsPerHour(), github.IdleBudgetWarnFraction*100, github.GraphQLHourlyLimit,
			topCallerList(w))
		return CheckItem{OK: false, Detail: detail, Error: msg}, msg
	}

	return CheckItem{OK: true, Detail: detail + "; top: " + topCallerList(w)}, ""
}

// restSuffix names the REST traffic without adding it to the GraphQL bill.
// It is a separate quota with its own hourly limit, so a sum of the two is not
// a budget; but a window whose REST calls dwarf its GraphQL calls is still
// worth seeing, and omitting them entirely would hide it.
func restSuffix(w github.LedgerWindow) string {
	rest := w.Calls - w.GraphQLCalls
	if rest <= 0 {
		return ""
	}
	return fmt.Sprintf(", plus %d REST request(s) on the separate core quota", rest)
}

func cachedSuffix(w github.LedgerWindow) string {
	if w.Cached == 0 {
		return ""
	}
	return fmt.Sprintf(", %d served from cache", w.Cached)
}

func remainingSuffix(w github.LedgerWindow) string {
	if w.LowWaterRemaining < 0 {
		return ""
	}
	return fmt.Sprintf(", low water %d remaining", w.LowWaterRemaining)
}

func exhaustedWhen(w github.LedgerWindow, now time.Time) string {
	if w.ExhaustedAt.IsZero() {
		return "in the last hour"
	}
	return fmt.Sprintf("%d minute(s) ago", int(now.Sub(w.ExhaustedAt).Minutes()))
}

// topCallerList renders the biggest spenders. Callers with zero points are
// dropped: a caller that cost nothing is not a spender, and listing it pads
// the finding with the one row that cannot be the answer.
func topCallerList(w github.LedgerWindow) string {
	var parts []string
	for _, c := range w.TopCallers {
		if c.Points == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%d pts / %d calls)", c.Caller, c.Points, c.Calls))
		if len(parts) == maxAPIBudgetCallers {
			break
		}
	}
	if len(parts) == 0 {
		return "none attributed (no priced call in the window)"
	}
	return strings.Join(parts, ", ")
}
