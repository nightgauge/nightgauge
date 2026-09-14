// opencode_cost_watchdog.go stops an opencode stage at its cost budget
// (ADR-022 § 3). OpenCode takes no cost cap of its own, so the manager prices
// the stage from the model registry as its stream arrives and ends the run
// once the registry-priced cost passes RunOptions.CostBudget, and prices it
// again with its subagent sessions once it has ended.
package execution

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// CostCapExceededMarker ends the stderr of a stage the cost watchdog stopped.
// The terminal-kind table's cost-cap-exceeded rule classifies it as
// budget_exceeded.
const CostCapExceededMarker = "[cost-cap-exceeded]"

// OpenCodeCostWarning prefixes the one line the watchdog logs for a stage
// whose cost budget it cannot enforce.
const OpenCodeCostWarning = "[opencode-cost]"

// openCodeCostKillGrace is how long a stage stopped at its cost budget has to
// exit on SIGTERM before its process group is sent SIGKILL.
const openCodeCostKillGrace = 10 * time.Second

// openCodeCostWatchdog prices one opencode stage as its stream arrives. Only
// the stdout reader touches it.
//
// While the stage runs it prices the stage's own session: a subagent's steps
// never reach the stream, so it cannot stop a stage while its subagents
// spend. Their usage is folded in once the stage has ended
// (opencode_usage.go), and settle prices the stage again then, so a stage
// its subagents took past the budget fails instead of being recorded as a
// success. It prices at the rates of the model the stage was dispatched
// with, because no stream event names the model that served a step.
type openCodeCostWatchdog struct {
	// model is the -m value the stage was dispatched with.
	model string
	// budget is the stage's cost budget in USD.
	budget float64
	// grace is the time between SIGTERM and SIGKILL.
	grace time.Duration
	// cost is the registry-priced cost of the stream's usage so far.
	cost float64
	// fired is set once the stage crossed its budget.
	fired bool
	// settled is set when the stage crossed it only once its subagents'
	// usage was folded in, after it had ended.
	settled bool
}

// newOpenCodeCostWatchdog returns the watchdog for a stage dispatched with
// model under a cost budget of budget USD, or nil when there is nothing it can
// enforce. With no budget there is nothing to enforce. A model the registry
// cannot price has no cost to measure the budget against: then one line is
// written to warn, and the stage's other budgets are its only bound.
func newOpenCodeCostWatchdog(model string, budget float64, warn io.Writer, stage string) *openCodeCostWatchdog {
	if budget <= 0 {
		return nil
	}
	if _, priced := tokens.CalculateCostFor("opencode", model, tokens.TokenCounts{}); !priced {
		fmt.Fprintf(warn, "%s %s: the model registry has no rates for %q, so the stage's cost budget of USD %.4f is not enforced\n",
			OpenCodeCostWarning, stage, model, budget)
		return nil
	}
	return &openCodeCostWatchdog{model: model, budget: budget, grace: openCodeCostKillGrace}
}

// observe prices the stream's usage so far, after a step_finish added a step
// to acc, and reports whether this step took the stage past its budget. It
// reports that once. Registry rates are linear, so pricing the running total
// is the sum of every step's price. OpenCode's own part.cost is never read.
func (w *openCodeCostWatchdog) observe(acc *TokenAccumulator) bool {
	if w == nil || w.fired {
		return false
	}
	return w.price(acc)
}

// settle prices the stage once it has ended and its subagent sessions'
// usage has been folded into acc, and reports whether that took it past its
// budget for the first time. The stage is not stopped, since it has already
// ended, but it fails all the same.
func (w *openCodeCostWatchdog) settle(acc *TokenAccumulator) bool {
	if w == nil || w.fired {
		return false
	}
	w.settled = w.price(acc)
	return w.settled
}

// price prices acc's usage at the model's registry rates and marks the
// watchdog fired when that is past the budget.
func (w *openCodeCostWatchdog) price(acc *TokenAccumulator) bool {
	cache5m, cache1h := acc.CacheCreationByTTL()
	w.cost, _ = tokens.CalculateCostFor("opencode", w.model, tokens.TokenCounts{
		Input:           acc.InputTokens,
		Output:          acc.OutputTokens,
		CacheRead:       acc.CacheRead,
		CacheCreation5m: cache5m,
		CacheCreation1h: cache1h,
	})
	if w.cost <= w.budget {
		return false
	}
	w.fired = true
	return true
}

// notice is the line that ends the stderr of a stage past its budget.
func (w *openCodeCostWatchdog) notice() string {
	if w.settled {
		return fmt.Sprintf("%s the stage's registry-priced cost of USD %.4f, its subagent sessions included, passed its cost budget of USD %.4f, so it failed; a subagent's usage is read only once the stage has ended, so it was not stopped",
			CostCapExceededMarker, w.cost, w.budget)
	}
	return fmt.Sprintf("%s the stage's registry-priced cost of USD %.4f passed its cost budget of USD %.4f, so it was stopped",
		CostCapExceededMarker, w.cost, w.budget)
}

// stop sends SIGTERM to the stage's process group and, unless exited closes
// first, SIGKILL once the grace period has passed. It returns at once. A
// budget stop is not an operator's stop, so it never marks the execution
// stopped, and RunResult.Cancelled stays false.
func (w *openCodeCostWatchdog) stop(proc *os.Process, exited <-chan struct{}) {
	signalProcessTree(proc, syscall.SIGTERM)
	go func() {
		timer := time.NewTimer(w.grace)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			signalProcessTree(proc, syscall.SIGKILL)
		}
	}()
}
