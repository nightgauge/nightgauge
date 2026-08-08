package ipc

// attention.raise — the run-scoped producer entry point for the EXTENSION
// operating mode (#305).
//
// Before this, every run-scoped producer hung off the Go scheduler
// (internal/orchestrator/attention_wiring.go) and the IPC surface exposed only
// list/resolve/acknowledge/mute/unmute/sweep. A headless run — the operating
// mode for the overwhelming majority of dispatches — could therefore produce
// ZERO run-scoped Action Center cards, by construction: budget-ceiling stops,
// branch-protection blocks, and abandoned dispatches were detected, logged, and
// dropped. The fleet-scoped producers worked on both paths only because they
// route through autonomous.complete → Go.
//
// This is NOT a second attention system. The handler builds the card with the
// SAME exported builders the Go scheduler calls and writes it through the SAME
// single authoritative store (Store.Raise), so dedup by idempotency_key,
// standing-fingerprint semantics, the journal, the attention.event push, and
// resolve/verb execution are all inherited unchanged. What the extension gains
// is a way to say "this condition happened" — not a way to describe a card.
//
// The producer set is a CLOSED ALLOWLIST and the params carry no options, no
// verb, and no args map. See AttentionRaiseParams for why that is load-bearing.
//
// Nor do they carry any NUMBER a card's options act on. Every figure behind the
// budget-ceiling card — the enforced ceiling and the run's spend — is read from
// daemon-side state here, not from the request. See derivedFacts.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Raiseable producer ids. A producer absent from this set cannot be raised over
// IPC no matter what a caller sends.
const (
	// ProducerBudgetCeiling mirrors the Go scheduler's budget-ceiling producer
	// (ADR 015 §F #4).
	ProducerBudgetCeiling = "budget-ceiling"
	// ProducerBranchProtection mirrors the Go scheduler's branch-protection
	// producer (ADR 015 §F #6).
	ProducerBranchProtection = "branch-protection"
	// ProducerAbandonedDispatch is extension-only by design — there is no Go
	// force-clear funnel. See orchestrator.BuildAbandonedDispatch.
	ProducerAbandonedDispatch = orchestrator.ProducerAbandonedDispatch
)

// raiseableProducers is the closed allowlist. Adding an entry here is the
// declaration that a producer may be raised from outside the Go scheduler; it
// is deliberately a short, reviewable list rather than a registry lookup.
var raiseableProducers = map[string]struct{}{
	ProducerBudgetCeiling:     {},
	ProducerBranchProtection:  {},
	ProducerAbandonedDispatch: {},
}

// RaiseableProducers returns the allowlist sorted, for diagnostics, error
// text, and tests. Derived from the map rather than re-listed, so a producer
// added to the allowlist cannot be missing from what the daemon reports.
func RaiseableProducers() []string {
	out := make([]string, 0, len(raiseableProducers))
	for p := range raiseableProducers {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// outcomeNotApplicable is the fifth genuine result: the daemon evaluated the
// producer's own precondition and it does not hold, so no card was raised and
// nothing failed. Distinct from every RaiseOutcome AND from an error —
// "branch protection is not what is blocking this PR" is an answer, not a
// write and not a fault.
const outcomeNotApplicable = "not_applicable"

// derivedFacts are the numbers behind a card, resolved from DAEMON-SIDE state
// and never from the request.
//
// This type is the fix for the round-2 security finding, and its existence is
// the point: before it, `costUsd` and `ceilingUsd` were params. A card's option
// args are executed verbatim by Server.ExecuteVerb on resolve, and
// `budget.raiseCeiling` writes .nightgauge/pipeline/budget-override.json, which
// orchestrator.PipelineBudgetCeilingUSD takes as max(config, override) — a
// workspace-global spend control. Accepting either number over the socket (#263
// — reachable by any local process, including a pipeline agent's Bash, which is
// the prompt-injection threat model in docs/security/WORKFLOW_FANOUT_SECURITY.md)
// made that control caller-settable on an arbitrary repo and issue, with no
// breach having occurred.
type derivedFacts struct {
	// enforcedCeilingUSD is what the ceiling actually IS right now, read
	// in-process exactly as the scheduler reads it.
	enforcedCeilingUSD float64
	// spentUSD is the run's own recorded spend, and spendCorroborated says
	// whether the daemon found such a record at all. An uncorroborated spend
	// does NOT suppress the card — the operator still learns their run stopped
	// — it suppresses the raise-and-retry OPTION, because that option's
	// magnitude would otherwise have no source but the report itself.
	spentUSD          float64
	spendCorroborated bool
}

// handleAttentionRaise validates a closed-producer raise and writes it through
// the single authoritative store.
func (s *Server) handleAttentionRaise(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionRaiseParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("attention.raise: parse params: %w", err)
	}
	if _, ok := raiseableProducers[p.Producer]; !ok {
		// Names the allowlist rather than echoing the rejected value, so a
		// caller learns what IS raiseable without the daemon reflecting
		// arbitrary input back into its own error text.
		return nil, fmt.Errorf("attention.raise: producer must be one of %s",
			strings.Join(RaiseableProducers(), ", "))
	}
	if !strings.Contains(p.Repo, "/") {
		return nil, fmt.Errorf("attention.raise: repo must be \"owner/name\"")
	}
	if p.Issue <= 0 {
		return nil, fmt.Errorf("attention.raise: issue is required")
	}
	// A (repo, issue) the daemon knows nothing about is not a condition it can
	// have observed. Without this, the verb is an unbounded card-injection
	// primitive: dedup is per (producer, repo, issue) and issue numbers are
	// unbounded, so a caller could fill .nightgauge/attention/ with plausible
	// cards naming repos this workspace has never heard of.
	if !s.isConfiguredRepo(p.Repo) {
		return nil, fmt.Errorf("attention.raise: repo is not configured in this workspace")
	}

	req, applicable, err := s.buildRaise(p)
	if err != nil {
		return nil, err
	}
	if !applicable {
		return AttentionRaiseResult{Outcome: outcomeNotApplicable}, nil
	}

	store := s.attentionStore()
	if store == nil {
		// An ERROR, not another outcome: the caller asked for a card and none
		// exists anywhere. Callers are fail-open and swallow this, but they
		// swallow a fault, not a decision.
		return nil, fmt.Errorf("attention.raise: attention store not configured")
	}
	id, err := attention.NewID()
	if err != nil {
		return nil, fmt.Errorf("attention.raise: %w", err)
	}
	req.ID = id
	outcome, liveID, err := store.Raise(req)
	if err != nil {
		return nil, fmt.Errorf("attention.raise: %w", err)
	}
	switch outcome {
	case attention.OutcomeCreated, attention.OutcomeUpdated:
		return AttentionRaiseResult{Outcome: string(outcome), ID: liveID}, nil
	default:
		// Unreachable while every raiseable producer is event-shaped, which
		// TestNoRaiseableProducerIsStandingWithoutRetraction enforces. Loud
		// rather than silent: a standing producer added to the allowlist would
		// otherwise start returning an outcome this contract never declared,
		// and the TS union would accept it as `never`.
		return nil, fmt.Errorf(
			"attention.raise: store returned %q, which only standing producers can produce — "+
				"no raiseable producer may be standing (see RaiseableProducers)", outcome)
	}
}

// isConfiguredRepo reports whether "owner/name" is a repo this daemon has a
// filesystem root for. Server.repoRoot falls back to workspaceRoot for an
// unknown repo, so it cannot answer this — the registry must be asked directly.
//
// NOT A NEW CONSTRAINT on the paths that raise. The same registry already
// decides where a run's runtime-{N}.json is written (`pipelineStateDir` →
// `repoRoot`), and #307 records the fallback as cross-contamination: an
// unregistered repo's run state lands in the daemon's launch root. Every repo
// a run can legitimately be dispatched for is registered at daemon start by
// cmd/nightgauge's registerWorkspaceReposInResolver (the primary repo plus each
// sibling carrying .nightgauge/config.yaml). A raise for a repo outside that
// set is dropped — the caller is fail-open, so the run is unaffected — and that
// is the correct trade against an unbounded card-injection primitive.
func (s *Server) isConfiguredRepo(repo string) bool {
	if s.resolver == nil {
		// No registry to check against. Fail CLOSED: a server with no repo
		// configuration has no run to have observed a condition for, and the
		// only production wiring that leaves resolver nil is a partially
		// constructed test server.
		return false
	}
	return s.resolver.RepoPath(repo) != ""
}

// recordedRunSpendUSD returns the spend the DAEMON recorded for this run, and
// whether such a record exists.
//
// Two sources, both written by this process and neither by the caller: the live
// RuntimeState the extension path accumulates through
// pipeline.notifyStageTransition (`rt.CompleteStageWithCost` → TotalCostUSD),
// and the same runtime persisted to the repo's own .nightgauge/pipeline dir.
// The runtime map is keyed by bare issue number (#370's re-keying target), so
// the repo is cross-checked before the figure is trusted — otherwise a raise
// naming repo A could be corroborated by a live run of repo B's issue with the
// same number.
func (s *Server) recordedRunSpendUSD(repo string, issue int) (float64, bool) {
	runtimeKey := fmt.Sprintf("%d", issue)
	s.runtimesMu.Lock()
	rt, ok := s.activeRuntimes[runtimeKey]
	s.runtimesMu.Unlock()
	if ok {
		snap := rt.Snapshot()
		if (snap.Repo == "" || snap.Repo == repo) && snap.TotalCostUSD > 0 {
			return snap.TotalCostUSD, true
		}
	}
	stateDir := s.pipelineStateDir(repo)
	if stateDir == "" {
		return 0, false
	}
	persisted, err := state.LoadPersistedState(stateDir, issue)
	if err != nil || persisted == nil {
		return 0, false
	}
	if persisted.Repo != "" && persisted.Repo != repo {
		return 0, false
	}
	if persisted.TotalCostUSD <= 0 {
		return 0, false
	}
	return persisted.TotalCostUSD, true
}

// buildRaise validates the producer-specific fields and constructs the card
// through the shared orchestrator builder. The second return reports whether
// the producer's own precondition holds; a false with a nil error is the
// not_applicable answer, never a swallowed failure.
//
// A METHOD, not a function, because the budget-ceiling card's numbers come from
// the server (see derivedFacts) — the round-2 fix that stopped a caller from
// choosing the ceiling a resolve would persist.
func (s *Server) buildRaise(p AttentionRaiseParams) (attention.DecisionRequest, bool, error) {
	switch p.Producer {
	case ProducerBudgetCeiling:
		// SERVER-DERIVED, EXACTLY AS THE SCHEDULER DERIVES IT. scheduler.go's
		// call site is `ProposedCeilingUSD(PipelineBudgetCeilingUSD(root),
		// runtime.TotalCostUSD)`; this is the same two inputs read the same two
		// ways, in-process. The workspace root is deliberately s.workspaceRoot,
		// the SAME root ExecuteVerb hands to WriteBudgetCeilingOverride — read
		// and write must agree about which budget-override.json is the live
		// one, or a raise would propose against a ceiling the resolve then
		// ignores.
		facts := derivedFacts{enforcedCeilingUSD: orchestrator.PipelineBudgetCeilingUSD(s.workspaceRoot)}
		facts.spentUSD, facts.spendCorroborated = s.recordedRunSpendUSD(p.Repo, p.Issue)

		proposed := 0.0
		if facts.spendCorroborated {
			proposed = orchestrator.ProposedCeilingUSD(facts.enforcedCeilingUSD, facts.spentUSD)
		}
		// proposed == 0 builds the card WITHOUT the raiseCeiling option. The
		// card still goes up: "your run stopped on budget" is worth telling an
		// operator even when the daemon cannot corroborate the amount, and
		// staying silent would reintroduce exactly the hole #305 exists to
		// close. What it must not do is offer a one-click write whose number
		// nothing but the request vouches for.
		return orchestrator.BuildBudgetCeilingHit(p.Repo, p.Issue, p.RunID, facts.spentUSD, proposed), true, nil

	case ProducerBranchProtection:
		if p.PR <= 0 {
			return attention.DecisionRequest{}, false,
				fmt.Errorf("attention.raise: %s requires pr", ProducerBranchProtection)
		}
		// The merge state is REQUIRED, not defaulted. Filling a missing field
		// with "UNKNOWN" would send Decide down the not-mergeable branch and
		// produce a card asserting the PR cannot merge, when the truth is that
		// nobody looked. A caller that could not read the PR must not raise.
		if strings.TrimSpace(p.PRState) == "" || strings.TrimSpace(p.Mergeable) == "" ||
			strings.TrimSpace(p.MergeStateStatus) == "" {
			return attention.DecisionRequest{}, false,
				fmt.Errorf("attention.raise: %s requires prState, mergeable and mergeStateStatus", ProducerBranchProtection)
		}
		// ONE TABLE, N INTERPRETERS (#306's discipline). The caller sends the
		// `gh pr view` projection; stages.Decide — the same pure decision
		// matrix the deterministic pr-merge runner uses — produces the reason
		// string, and IsBranchProtectionPunt gates on it. The extension has a
		// prose renderer of its own (describeMergeBlocker) for the operator-
		// facing error line; it is deliberately NOT what reaches the card,
		// because two renderers would put two different sentences on the same
		// condition depending on which path observed it.
		snap := pmstages.PRViewSnapshot{
			State:            p.PRState,
			Mergeable:        p.Mergeable,
			MergeStateStatus: p.MergeStateStatus,
			// Empty is MEANINGFUL, not missing: GitHub returns "" when the
			// branch ruleset requires no reviewers, which Decide reads as
			// "review not blocking".
			ReviewDecision: p.ReviewDecision,
		}
		for _, c := range p.Checks {
			snap.StatusCheckRollup = append(snap.StatusCheckRollup,
				pmstages.PRStatusCheckRow{Name: c.Name, Conclusion: c.Conclusion})
		}
		// IN-FLIGHT CI IS NOT BRANCH PROTECTION, and Decide() cannot tell you
		// that. The Go path's pending-CI arm lives OUTSIDE the matrix, in
		// DeterministicRunner.Run: it tests MergeBlockedByPendingCI first,
		// waits out the bounded CI budget, and on exhaustion punts
		// `ci-wait-timeout` — a reason IsBranchProtectionPunt deliberately does
		// not match. Classifying with bare Decide() therefore read a queued
		// required check as `dirty-merge-state: BLOCKED` and carded it as
		// branch protection, telling the operator to fix a failing check that
		// does not exist. That is not an edge case: prmerge.go's own comment
		// records that pr-merge starts right after pr-create, so on repos whose
		// CI takes minutes the FIRST snapshot is always BLOCKED/UNSTABLE with
		// pending checks (#297).
		//
		// Gate on the exported predicate, never a re-implementation: three
		// copies of this matrix is how the paths drift apart again (#257).
		if pmstages.MergeBlockedByPendingCI(snap) {
			return attention.DecisionRequest{}, false, nil
		}
		d := pmstages.Decide(snap)
		if !d.Punt || !orchestrator.IsBranchProtectionPunt(d.Reason) {
			// Not blocked at all, or blocked by something outside the
			// human-needed class. The Go path stays silent here too.
			return attention.DecisionRequest{}, false, nil
		}
		return orchestrator.BuildBranchProtectionBlock(p.Repo, p.Issue, p.PR, p.RunID, d.Reason), true, nil

	case ProducerAbandonedDispatch:
		return orchestrator.BuildAbandonedDispatch(p.Repo, p.Issue, p.RunID, p.Stage), true, nil
	}
	// Unreachable: the allowlist check ran first. Kept so a producer added to
	// the allowlist and not to the switch is a loud failure, not a silent
	// "raised nothing".
	return attention.DecisionRequest{}, false,
		fmt.Errorf("attention.raise: producer %q is allowlisted but has no builder", p.Producer)
}
