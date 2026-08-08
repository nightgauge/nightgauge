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
	case attention.OutcomeCreated, attention.OutcomeUpdated, attention.OutcomeRefreshed:
		// `refreshed` re-entered this contract in review, and it is NOT the
		// standing branch coming back. Store.Raise now refuses to let any raise
		// strip a remedy off an open card: when the stored record offers an
		// option bound to a real verb and this one offers only noops, the
		// observation is recorded and the payload is kept. That is reachable on
		// this verb by construction — the uncorroborated budget-ceiling card is
		// exactly a noop-only variant of a key the corroborated card also
		// raises — so declaring it is the honest contract, not drift.
		return AttentionRaiseResult{Outcome: string(outcome), ID: liveID}, nil
	default:
		// `suppressed` only. Unreachable while every raiseable producer is
		// event-shaped, which TestNoRaiseableProducerIsStandingWithoutRetraction
		// enforces. Loud rather than silent: a standing producer added to the
		// allowlist would otherwise start returning an outcome this contract
		// never declared, and the TS union would accept it as `never`.
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
// whether a run record produced by the NORMAL run flow corroborates it.
//
// THE TRUST MODEL THIS SITS INSIDE (ADR-015 §N). The daemon socket is a
// trusted-operator channel: every pre-existing bookkeeping verb —
// `pipeline.notifyStageTransition`, `queue.*`, `autonomous.*`,
// `workspace.setRoot` — already accepts caller data unauthenticated, and
// putting an identity on that channel is #370's rework, not this verb's job.
// What THIS path must guarantee inside that model is narrower and absolute:
// **the raise must not be able to mint or inflate the state it is corroborated
// against.** Round 3 failed exactly there — a single
// `notifyStageTransition{status:"complete", costUsd:1e6}` created a runtime out
// of nothing, and the very next raise built a $1.5M `budget.raiseCeiling`
// option out of it. Two rules close that, and neither depends on the socket
// being authenticated:
//
//  1. EXACT REPO, BOTH ARMS. The runtime map is keyed by bare issue number
//     (#370's re-keying target), so a raise naming repo A must not be
//     corroborated by a run of repo B's issue with the same number. Round 3
//     cross-checked the repo but accepted an EMPTY one — and
//     `notifyStageTransition` seeds `rt.Repo` only when the caller sends it, so
//     omitting one field re-opened the hole for every configured repo at once.
//     An unattributed runtime now corroborates nothing, which is the same call
//     #307 made when it refused to PERSIST one (server.go's `repo != ""` gate).
//
//  2. REAL PROGRESSION. A record only corroborates when it shows a stage the
//     daemon watched BEGIN and then finish: `CompletedStages` entries whose
//     `StartedAt` is non-zero. That timestamp is stamped by `BeginStage`, which
//     only the "running" transition calls — so the created-on-miss "complete"
//     that mints a runtime in one call books a stage with a ZERO StartedAt and
//     an empty Stage, and corroborates nothing. The figure returned is the sum
//     over those stages rather than the `TotalCostUSD` accumulator, so a spend
//     booked onto a run with no begun stage cannot reach a card even when other
//     stages did run.
//
// Residual exposure, stated rather than implied: a caller willing to spend two
// calls (`running`, then `complete`) can still book a stage. That is the
// pre-existing telemetry-forgery surface of `notifyStageTransition` itself,
// unchanged by #305 and owned by #370 — see ADR-015 §N.
//
// Two sources, checked in order: the live RuntimeState the extension path
// accumulates through `pipeline.notifyStageTransition`, and the same runtime
// persisted to the run's own repo `.nightgauge/pipeline` dir. Both go through
// the identical predicate, because the persisted file is written from the same
// runtime.
func (s *Server) recordedRunSpendUSD(repo string, issue int) (float64, bool) {
	runtimeKey := fmt.Sprintf("%d", issue)
	s.runtimesMu.Lock()
	rt, ok := s.activeRuntimes[runtimeKey]
	s.runtimesMu.Unlock()
	if ok {
		if spend, corroborated := corroboratedRunSpendUSD(rt.Snapshot(), repo); corroborated {
			return spend, true
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
	return corroboratedRunSpendUSD(persisted, repo)
}

// corroboratedRunSpendUSD applies the two rules above to one run record and
// returns the spend attributable to stages that actually ran.
//
// Takes a snapshot/loaded copy, never the live registry entry, so it never
// reads a RuntimeState another goroutine is mutating.
func corroboratedRunSpendUSD(rt *state.RuntimeState, repo string) (float64, bool) {
	if rt == nil || rt.Repo == "" || rt.Repo != repo {
		return 0, false
	}
	total := 0.0
	begun := 0
	for _, sr := range rt.CompletedStages {
		// StartedAt is BeginStage's stamp and Stage is what BeginStage set; a
		// terminal transition that created its own runtime carries neither.
		if sr.StartedAt.IsZero() || sr.Stage == "" {
			continue
		}
		begun++
		total += sr.CostUSD
	}
	if begun == 0 || total <= 0 {
		return 0, false
	}
	return total, true
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
		// SERVER-DERIVED, STRICTER THAN THE SCHEDULER. scheduler.go's call site
		// is `ProposedCeilingUSD(PipelineBudgetCeilingUSD(root),
		// runtime.TotalCostUSD)`. The ceiling input is the same in-process read;
		// the spend input is DELIBERATELY not `TotalCostUSD` — that accumulator
		// is writable by a single created-on-miss transition, so this path sums
		// only CompletedStages entries with a BeginStage-stamped StartedAt (see
		// corroboratedRunSpendUSD). The scheduler trusts its own accumulator
		// because it wrote it; this handler cannot, because any socket caller
		// can have.
		//
		// THE ROOT IS THE RUN'S REPO, not s.workspaceRoot (fixed in review).
		// `s.workspaceRoot` is a MUTABLE pointer to whichever repo owns the
		// focused editor (`workspace.setRoot`, sent from
		// `resolveActiveRepository`), so in a multi-repo workspace a raise and
		// its resolve could read and write two different budget-override.json
		// files — the remedy inert again, in the way #305 exists to close.
		// `repoRoot` is the same per-repo registry that already decides where a
		// run's runtime-{N}.json lives (#215/#307), and `ExecuteVerb`'s
		// `budget.raiseCeiling` arm now writes through it too, so read and write
		// agree per repo and stop moving with the operator's cursor.
		facts := derivedFacts{enforcedCeilingUSD: orchestrator.PipelineBudgetCeilingUSD(s.repoRoot(p.Repo))}
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
		// REQUIRED, and validated against the closed set rather than defaulted.
		// One producer covers three force-clear situations whose operator-facing
		// facts differ — whether a stage ran, whether there is a worktree
		// holding uncommitted work, whether the dispatch's terminal outcome was
		// booked by anyone. Defaulting an unrecognised value would print a
		// confident wrong body, which is the defect this parameter fixes; the
		// caller always knows which arm it is in, synchronously.
		if !orchestrator.IsAbandonedDispatchSituation(p.Situation) {
			return attention.DecisionRequest{}, false,
				fmt.Errorf("attention.raise: %s requires situation to be one of %s",
					ProducerAbandonedDispatch,
					strings.Join(orchestrator.AbandonedDispatchSituations(), ", "))
		}
		return orchestrator.BuildAbandonedDispatch(p.Repo, p.Issue, p.RunID, p.Stage,
			orchestrator.AbandonedDispatchSituation(p.Situation)), true, nil
	}
	// Unreachable: the allowlist check ran first. Kept so a producer added to
	// the allowlist and not to the switch is a loud failure, not a silent
	// "raised nothing".
	return attention.DecisionRequest{}, false,
		fmt.Errorf("attention.raise: producer %q is allowlisted but has no builder", p.Producer)
}
