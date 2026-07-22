package attention

import (
	"context"
	"fmt"
	"sort"
)

// The verb registry is the security boundary (ADR 015 §B/§J): every
// `option.verb` MUST resolve to an entry here — a closed allowlist of
// deterministic operations the fleet already trusts. A resolution can trigger
// ONLY a registered verb, with args bounded by the request. A resolve naming an
// unknown option, or an option whose verb is not registered, is rejected — a
// surface can never conjure a verb or arg the producer did not declare.

// Verb is a registered deterministic operation an option may bind to.
type Verb = string

const (
	// --- Verbs that exist today (wired to existing trusted primitives) ---

	// VerbQueueAdd enqueues an issue for dispatch (Scheduler.QueueAddItem).
	VerbQueueAdd Verb = "queue.add"
	// VerbIssueRemoveBlockedBy removes a stale blockedBy edge
	// (IssueService.RemoveBlockedByNumber).
	VerbIssueRemoveBlockedBy Verb = "issue.removeBlockedBy"
	// VerbAutonomousResume resumes a paused fleet (AutonomousScheduler.Resume).
	VerbAutonomousResume Verb = "autonomous.resume"
	// VerbAutonomousRescan wakes the scheduler loop immediately.
	VerbAutonomousRescan Verb = "autonomous.rescan"
	// VerbAutonomousComplete marks a human-only task done and requeues
	// dependents (NotifyComplete → promoteUnblockedToReady).
	VerbAutonomousComplete Verb = "autonomous.complete"
	// VerbAutonomousClearIssueFailures clears an issue's failure cooldown so it
	// can be retried after manual triage.
	VerbAutonomousClearIssueFailures Verb = "autonomous.clearIssueFailures"
	// VerbProjectSyncStatus sets a board Status field (e.g. promote to Ready).
	VerbProjectSyncStatus Verb = "project.syncStatus"
	// VerbIssueClose closes a GitHub issue.
	VerbIssueClose Verb = "issue.close"

	// --- Verbs E1 adds, each fronting an existing enforcement path (ADR §B) ---

	// VerbBudgetRaiseCeiling applies a runtime budget-ceiling override honored
	// by safety_rails before the budget_ceiling_hit terminal.
	VerbBudgetRaiseCeiling Verb = "budget.raiseCeiling"
	// VerbRunRetryWithEscalation clears the failure cooldown AND forces the next
	// model tier for the retry.
	VerbRunRetryWithEscalation Verb = "run.retryWithEscalation"

	// VerbNoop is the explicit "do nothing but resolve" choice — the registry
	// binding for the ADR's leave / keep-paused / wait / halt options, where the
	// operator deliberately declines to mutate the fleet. Registry-gated like any
	// other verb so a surface cannot smuggle an unregistered no-action.
	VerbNoop Verb = "noop"
)

// registry is the closed allowlist. A verb absent from this map cannot be
// executed by any resolution.
var registry = map[Verb]struct{}{
	VerbQueueAdd:                     {},
	VerbIssueRemoveBlockedBy:         {},
	VerbAutonomousResume:             {},
	VerbAutonomousRescan:             {},
	VerbAutonomousComplete:           {},
	VerbAutonomousClearIssueFailures: {},
	VerbProjectSyncStatus:            {},
	VerbIssueClose:                   {},
	VerbBudgetRaiseCeiling:           {},
	VerbRunRetryWithEscalation:       {},
	VerbNoop:                         {},
}

// IsRegisteredVerb reports whether v is in the closed allowlist.
func IsRegisteredVerb(v string) bool {
	_, ok := registry[v]
	return ok
}

// RegisteredVerbs returns the sorted allowlist, for diagnostics and tests.
func RegisteredVerbs() []string {
	out := make([]string, 0, len(registry))
	for v := range registry {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// ValidateOption re-validates a resolve against the persisted request and the
// registry (ADR 015 §J defense-in-depth). It returns the resolved option on
// success, or an error when the option id is unknown or its verb is not
// registered. Callers surface a generic client error (§J error hygiene) and log
// the detail.
func ValidateOption(req *DecisionRequest, optionID string) (Option, error) {
	opt := req.FindOption(optionID)
	if opt == nil {
		return Option{}, fmt.Errorf("attention: option %q is not declared on request %s", optionID, req.ID)
	}
	if !IsRegisteredVerb(opt.Verb) {
		return Option{}, fmt.Errorf("attention: option %q binds unregistered verb %q", optionID, opt.Verb)
	}
	return *opt, nil
}

// VerbExecutor executes a registered verb with the option's bounded args
// against the request's context. It is implemented by surfaces that hold the
// capabilities a verb needs: the IPC server (scheduler + GitHub clients) is the
// full executor; the CLI provides a subset. An executor MUST reject any verb it
// cannot perform rather than silently succeed.
//
// The store calls ExecuteVerb AFTER it has applied and persisted the resolution
// (CAS), so a verb failure is audited but never leaves the request half-open.
type VerbExecutor interface {
	ExecuteVerb(ctx context.Context, req *DecisionRequest, opt Option) error
}

// NoopExecutor records resolutions without side effects. Used by read-only
// surfaces and tests; every verb is a no-op (still registry-validated upstream).
type NoopExecutor struct{}

// ExecuteVerb implements VerbExecutor.
func (NoopExecutor) ExecuteVerb(_ context.Context, _ *DecisionRequest, _ Option) error {
	return nil
}
