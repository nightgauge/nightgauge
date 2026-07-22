// Package recovery implements the FailureRecovery registry (Issue #3268) —
// a deterministic auto-triage framework consulted by the orchestrator on
// stage failure. Each registered RecoveryAction inspects a typed StageFailure
// and decides whether to perform a deterministic recovery (deterministic-only
// per .claude/rules/scripts.md — no LLM calls).
//
// The registry is orthogonal to RetryEngine and stall-recovery: it runs after
// stall-rewind doesn't apply and before/in lieu of model escalation. When an
// action returns Recovered=true the scheduler marks the stage recovered and
// continues; on Recovered=false (matched but declined) or no match the
// scheduler falls through to today's existing failure path.
//
// First-match-wins ordering. Action authors order overlap-prone predicates
// from most-specific to most-generic in Default(). Per-run cap bounds the
// total across all stages.
package recovery

import (
	"context"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// StageFailure is the registry's input. The scheduler constructs one of these
// from the gate result, the stage error, and the runtime context before
// calling Registry.TryRecover.
type StageFailure struct {
	Stage    state.PipelineStage
	GateName string     // empty when failure preceded the gate
	GateKind gates.Kind // KindOK / KindNoOp / KindFail; KindOK for non-gate failures

	Reason   string
	Evidence []string

	StageError   string // raw stage error text (post-classifier input)
	TerminalKind string // result of ClassifyTerminalKind, or ""

	PRNumber       int // when known (loaded from pr-{N}.json)
	IssueNumber    int
	Repo           string
	Workspace      string
	AttemptOrdinal int // 1-based — increases per attempt within a run
}

// RecoveryResult is the outcome of one RecoveryAction.Execute call.
type RecoveryResult struct {
	Recovered bool
	Action    string // canonical id (matches RecoveryAction.Name())
	Reason    string
	Evidence  []string
	CostUSD   float64 // ~0 for deterministic actions
	FollowUp   string // "stage can resume" | "issue requires human triage" | "no action"
	DurationMs int64
}

// FollowUp constants — free-form strings are also allowed; these name the
// canonical buckets so telemetry queries can group consistently.
const (
	FollowUpStageCanResume      = "stage can resume"
	FollowUpHumanTriageRequired = "issue requires human triage"
	FollowUpNoAction            = "no action"
)

// RecoveryAction is the contract every recovery implements.
//
// Matches MUST be pure (no IO, no side effects) so the registry can quickly
// short-circuit non-matches without burning shell-out latency. Execute
// performs the deterministic recovery and is only invoked after Matches
// returns true.
type RecoveryAction interface {
	Name() string                                                     // canonical id
	Description() string                                              // for docs/AUTO_TRIAGE.md
	Matches(failure StageFailure) bool                                // pure
	Execute(ctx context.Context, failure StageFailure) RecoveryResult // performs recovery
}

// CapExempt is an optional marker an action implements when it maintains its OWN
// independent attempt bound and therefore must neither be gated by nor counted
// against the registry's global per-run cap. conflict-recovery is the canonical
// case: it is bounded per-edge by max_dev_redispatch, so drawing from the shared
// max_attempts_per_run pool would let an unrelated earlier recovery silently
// pre-empt the configured conflict bound (#4072 review). The scheduler also reads
// this (via Registry.IsCapExempt) to skip incrementing the global counter for
// such actions.
type CapExempt interface {
	CapExempt() bool
}

// isCapExempt reports whether an action opts out of the global per-run cap.
func isCapExempt(a RecoveryAction) bool {
	ce, ok := a.(CapExempt)
	return ok && ce.CapExempt()
}

// Registry is the ordered list of registered actions plus the per-run attempt
// cap. The scheduler iterates in registration order and executes the first
// match. Order matters when two actions' Matches predicates overlap — the
// more specific action MUST be registered first.
type Registry struct {
	actions []RecoveryAction
	cap     int // per-run cap; 0 = unlimited (test-only)
}

// New builds a registry with the given cap and ordered actions. cap <= 0 is
// interpreted as unlimited — useful for tests that want to exercise the
// fall-through path without bumping into the cap.
func New(maxAttempts int, actions ...RecoveryAction) *Registry {
	return &Registry{
		actions: append([]RecoveryAction(nil), actions...),
		cap:     maxAttempts,
	}
}

// Actions returns a copy of the registered actions in registration order.
// Used by docs/AUTO_TRIAGE.md generation and registry-level tests.
func (r *Registry) Actions() []RecoveryAction {
	if r == nil {
		return nil
	}
	out := make([]RecoveryAction, len(r.actions))
	copy(out, r.actions)
	return out
}

// MaxAttemptsPerRun returns the per-run cap. 0 means unlimited.
func (r *Registry) MaxAttemptsPerRun() int {
	if r == nil {
		return 0
	}
	return r.cap
}

// TryRecover walks the action list in registration order. Returns
// (result, true) on the first match — Recovered=true means the action
// performed the recovery, Recovered=false means it matched and declined.
// Returns (zero, false) when no action matched. Caller is responsible for
// recording the result on the runtime state and incrementing its attempt
// counter — the registry is stateless across calls.
//
// attemptsSoFar is the count of recovery attempts already taken in the
// current pipeline run (passed in by the scheduler). When the cap is
// reached, TryRecover returns (zero, false) immediately so the caller falls
// through to the terminal failure path.
func (r *Registry) TryRecover(ctx context.Context, failure StageFailure, attemptsSoFar int) (RecoveryResult, bool) {
	if r == nil || len(r.actions) == 0 {
		return RecoveryResult{}, false
	}
	for _, action := range r.actions {
		if !action.Matches(failure) {
			continue
		}
		// The global per-run cap gates only non-self-bounded actions. A
		// cap-exempt action (conflict-recovery) carries its own per-edge bound,
		// so it must still fire past the cap — `continue` rather than `return`
		// so a cap-exempt action later in the list isn't shadowed by an earlier
		// over-cap match (#4072 review).
		if !isCapExempt(action) && r.cap > 0 && attemptsSoFar >= r.cap {
			continue
		}
		start := time.Now()
		res := action.Execute(ctx, failure)
		if res.Action == "" {
			res.Action = action.Name()
		}
		if res.DurationMs == 0 {
			res.DurationMs = time.Since(start).Milliseconds()
		}
		return res, true
	}
	return RecoveryResult{}, false
}

// IsCapExempt reports whether the named registered action opts out of the global
// per-run cap. The scheduler uses this to avoid incrementing the shared attempt
// counter for a self-bounded action's result (#4072).
func (r *Registry) IsCapExempt(name string) bool {
	if r == nil {
		return false
	}
	for _, a := range r.actions {
		if a.Name() == name {
			return isCapExempt(a)
		}
	}
	return false
}

// ToStateRecoveryAttempt copies an in-process RecoveryResult into the
// persisted state.RecoveryAttempt shape. Includes a default `at` timestamp
// when the caller does not set one.
func ToStateRecoveryAttempt(r RecoveryResult) state.RecoveryAttempt {
	return state.RecoveryAttempt{
		Action:     r.Action,
		Recovered:  r.Recovered,
		Reason:     r.Reason,
		Evidence:   append([]string(nil), r.Evidence...),
		FollowUp:   r.FollowUp,
		CostUSD:    r.CostUSD,
		DurationMs: r.DurationMs,
		At:         time.Now().UTC().Format(time.RFC3339),
	}
}
