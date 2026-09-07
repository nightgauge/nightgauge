package orchestrator

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// capFallbackChain reads pipeline.adapter_fallback_chain — the operator's
// declared provider order — for a usage-cap provider walk (#1545).
//
// This is the FIRST Go-side reader of that field. It has been declared on the
// config struct since the chain shipped, with the walker living entirely in the
// VSCode layer, so an operator who configured a chain got a stage-start prereq
// walk and nothing else. The chain now serves two triggers with two different
// bounds, deliberately kept apart:
//
//	prereq failure   → the extension's stage-start walk. Strictly stage-start,
//	                   before any token spend, because a missing CLI or a
//	                   logged-out adapter is knowable BEFORE the stage runs and
//	                   re-running a stage that already spent tokens to discover
//	                   it would be pure waste.
//	usage-cap        → this walk. Necessarily mid-run: a cap cannot be known
//	                   before dispatch, and the stage is already lost when it
//	                   surfaces. Re-running one stage on another provider costs
//	                   one stage against an hour of idle fleet.
//
// Widening the extension's walker to cover both would have given a mid-stream
// PREREQ failure permission to re-run a stage that had already spent tokens —
// the exact waste its stage-start bound exists to prevent. So the trigger that
// needs a mid-run walk got its own walk, in the layer that owns the recovery
// decision, and the stage-start bound stays exactly as narrow as it was.
func (s *Scheduler) capFallbackChain(workspaceRoot string) []string {
	if s == nil {
		return nil
	}
	cfg, err := config.Load(workspaceRoot)
	if err != nil || cfg == nil || cfg.Pipeline == nil {
		// An unreadable or chainless config is not an error here: it means the
		// operator configured no walk, which collapses the hop into the
		// cooldown exactly as it did before this existed.
		return nil
	}
	return cfg.Pipeline.AdapterFallbackChain
}

// capAdapterUsableFn returns the installed-and-authenticated probe the cap walk
// should use: the test override when one is wired, the real adapter doctor
// otherwise.
func (s *Scheduler) capAdapterUsableFn() func(string) (bool, string) {
	if s != nil && s.capAdapterUsable != nil {
		return s.capAdapterUsable
	}
	return AdapterUsableForCapHop
}

// SetCapAdapterUsable overrides the fallback-chain health probe. Tests use it so
// a cap-recovery decision never shells a vendor CLI; production leaves it unset.
func (s *Scheduler) SetCapAdapterUsable(fn func(string) (bool, string)) {
	if s == nil {
		return
	}
	s.capAdapterUsable = fn
}

// capFallbackProducer is the Action Center producer id for cap-driven routing
// changes. One id for both verdicts (tier descent and provider hop) because
// they are one condition to the operator — "a usage cap changed how your work is
// being run" — and splitting them would put two cards on screen for one event.
const capFallbackProducer = "cap-fallback"

// BuildCapFallback renders the card for a cap-driven routing change (#1545).
//
// Split from the raiser per docs/ATTENTION_PRODUCERS.md § Run-scoped producers:
// one builder per producer, so no second call site can ever render a different
// card for the same condition.
//
// Before this existed, a tier descent and a provider hop were visible only as a
// line in `go-backend.log`. An operator who woke to a fleet running everything
// on haiku, or on codex, had no way to learn why short of log archaeology at
// 3am — and a usage cap is precisely the kind of condition they may want to act
// on (buy more quota, reorder the chain, wait out the window).
//
// SeverityFYI with a no-op default action, because nothing is blocked: by the
// time this is raised the pipeline has already recovered, so the card REPORTS a
// decision the system made rather than asking for one. The idempotency key
// carries the destination, so a stage that descends two rungs raises two
// distinct cards while a re-raise of the SAME move folds into one.
func BuildCapFallback(repo string, issue int, runID string, stage state.PipelineStage, d CapRecoveryDecision) attention.DecisionRequest {
	var title, destination string
	switch d.Verdict {
	case CapRecoveryDescendTier:
		destination = d.Downgrade.NewTier
		title = fmt.Sprintf("Usage cap — %s dropped to %s", stage, destination)
	case CapRecoveryHopProvider:
		destination = d.NextAdapter
		title = fmt.Sprintf("Usage cap — %s moved to %s", stage, destination)
	}
	return attention.DecisionRequest{
		IdempotencyKey: strings.Join([]string{
			capFallbackProducer, repo, fmt.Sprint(issue), string(stage), string(d.Verdict), destination,
		}, ":"),
		Kind:     attention.KindChoose,
		Severity: attention.SeverityFYI,
		Title:    title,
		Body:     d.Why,
		Producer: capFallbackProducer,
		Context: attention.Context{
			Repo:  repo,
			Issue: issue,
			Stage: string(stage),
			RunID: runID,
		},
		Options: []attention.Option{
			noopOption("acknowledge", "Got it"),
		},
		DefaultAction: attention.ExpireNoop,
		// The steer rail (#363): a cap is exactly the condition an operator has
		// context the pipeline does not — "we bought more quota", "prefer grok
		// for this repo" — and the note is recorded in the decision audit.
		Steer:     &attention.Steer{Enabled: true, Hint: "Optional note — e.g. which provider to prefer next time"},
		ExpiresAt: standingExpiry(),
	}
}

// raiseCapFallback is the thin scheduler-side raiser. Only the two verdicts that
// actually changed the routing produce a card: a cool_down has its own,
// long-standing surfacing through the quota-cooldown reason, and raising a
// second card for it would tell the operator the same thing twice.
func (s *Scheduler) raiseCapFallback(item types.BoardItem, runtime *state.RuntimeState, stage state.PipelineStage, d CapRecoveryDecision) {
	if s == nil || s.attention == nil {
		return
	}
	if d.Verdict != CapRecoveryDescendTier && d.Verdict != CapRecoveryHopProvider {
		return
	}
	runID := ""
	if runtime != nil {
		runID = runtime.RunID
	}
	s.raiseAttention(BuildCapFallback(item.Repo, item.Number, runID, stage, d))
}
