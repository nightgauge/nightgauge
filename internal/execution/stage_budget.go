// stage_budget.go stops a stage at its non-USD stage budgets (#1652, ADR-023
// Q8): its turns, its wall clock and its tokens. Every USD guard is inert for
// a model priced at $0, so these are what bound a stage on a local model; they
// bind every other stage too, whether or not a USD cap is set.
//
// The budget is checked while the stream runs, on the same stdout reader and
// the same TokenAccumulator.ParseLine call the cost watchdog uses, never after
// the stage has ended. A breach sends SIGTERM to the stage's process group,
// SIGKILL once the grace period has passed, and, once the stage is reaped,
// checks that no member of the group is left.
package execution

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/models"
)

// StageBudgetMarker prefixes the stderr line of a stage stopped at a stage
// budget. The line carries "stage_budget_exceeded:<dimension>", which the
// terminal-kind table's budget-enforcer rule classifies as budget_exceeded.
const StageBudgetMarker = "[stage-budget]"

// StageBudgetExceeded is the reason a stage stopped at a stage budget stamps,
// followed by ":" and the dimension.
const StageBudgetExceeded = "stage_budget_exceeded"

// The three stage budget dimensions.
const (
	StageBudgetTurns     = "turns"
	StageBudgetWallClock = "wall_clock"
	StageBudgetTokens    = "tokens"
)

// stageBudgetKillGrace is how long a stage stopped at a stage budget has to
// exit on SIGTERM before its process group is sent SIGKILL. A variable so a
// test can shorten it.
var stageBudgetKillGrace = 10 * time.Second

// stageBudgetReapWindow bounds the check, once the stage is reaped, that no
// member of its process group is left. A variable so a test can shorten it.
var stageBudgetReapWindow = 2 * time.Second

// stageCost says what the model registry makes of a stage dispatched on
// adapter with model in worktreeDir (#1652). Zero-cost is a provider no USD
// cap can bind: a model server the operator runs, or a model the registry
// prices at $0. For opencode that includes an endpoint the machine-tier
// `opencode:` block declares that is local (models.IsLocalEndpoint: kind
// lm-studio or ollama, or a loopback or private-network base URL, #2128),
// whose id models.ProviderFor reads as "other", unless the entry declares
// self_hosted: false (#1679). An Ollama cloud model is never zero-cost. A hosted model the registry cannot
// price is unpriced, not zero-cost: that is a registry gap, not a free model.
func stageCost(adapter, model, worktreeDir string) config.StageCost {
	if adapter == "opencode" {
		if local, declared := openCodeDeclaredEndpointLocality(model, worktreeDir); declared {
			if local {
				return config.StageZeroCost
			}
			// A declared endpoint that forwards to a hosted service
			// (self_hosted: false, #1679) is priced from the registry when
			// it can be, and unpriced otherwise: never a $0 model.
			return pricedStageCost(adapter, model)
		}
	}
	// IsLocalModel, not IsLocalProvider alone: an Ollama cloud model on the
	// ollama key runs on Ollama's hosted service, not the endpoint (#1679).
	if models.IsLocalModel(adapter, model, nil) {
		return config.StageZeroCost
	}
	if models.IsLocalProvider(models.ProviderFor(adapter, model)) {
		return config.StageUnpriced
	}
	return pricedStageCost(adapter, model)
}

// ResolveDispatchStageBudget is the stage budget a dispatch of stage on
// adapter with model in worktreeDir runs under, from the pipeline.stage_budgets
// entries in budgets, and what the model registry makes of its price. It is
// the resolution the manager runs before it spawns a stage, exported so the
// IPC server answers pipeline.resolveStageBudgets (#1668) for a stage the
// editor launches with exactly the ceilings this package would enforce.
func ResolveDispatchStageBudget(budgets map[string]config.StageBudget, stage, adapter, model, worktreeDir string) (config.ResolvedStageBudget, config.StageCost) {
	cost := stageCost(adapter, model, worktreeDir)
	return config.ResolveStageBudget(budgets, stage, cost), cost
}

// pricedStageCost is what the registry's rates make of a model that does not
// run on a server the operator runs.
func pricedStageCost(adapter, model string) config.StageCost {
	cost, priced := tokens.CalculateCostFor(adapter, model, tokens.TokenCounts{Input: 1_000_000, Output: 1_000_000})
	switch {
	case priced && cost == 0:
		return config.StageZeroCost
	case priced:
		return config.StagePriced
	}
	return config.StageUnpriced
}

// openCodeDeclaredEndpointLocality reports whether model's provider key is an
// endpoint the machine-tier `opencode:` block declares (declared), and if so
// whether the model runs on it (models.IsLocalModel: the endpoint's
// self_hosted declaration, else its kind or base URL, and never an Ollama
// cloud model). A config that cannot be read declares nothing here; the
// dispatch's own preparation refuses it later.
func openCodeDeclaredEndpointLocality(model, worktreeDir string) (local, declared bool) {
	key, _, ok := strings.Cut(model, "/")
	if !ok || key == "" {
		return false, false
	}
	settings, err := config.LoadOpenCodeConfig(worktreeDir)
	if err != nil {
		return false, false
	}
	endpoints, err := adapters.OpenCodeEndpoints(settings)
	if err != nil {
		return false, false
	}
	for _, ep := range endpoints {
		if ep.ID == key {
			le := models.LocalEndpoint{ID: ep.ID, Provider: ep.Provider, BaseURL: ep.BaseURL, SelfHosted: ep.SelfHosted}
			return models.IsLocalModel("opencode", model, []models.LocalEndpoint{le}), true
		}
	}
	return false, false
}

// stageBudgetEnforcer holds one stage's budget and what the stream has used
// of it. observe runs on the stdout reader; the wall-clock timer runs on its
// own goroutine, so the breach is guarded by mu.
type stageBudgetEnforcer struct {
	limits config.ResolvedStageBudget
	format AdapterStreamFormat
	grace  time.Duration

	// turns is the model turns the stream has shown so far; seen holds the
	// claude message ids already counted, so a turn's blocks count once.
	turns int
	seen  map[string]bool
	// grokUsageSum sums grok's per-turn usage snapshots, which the
	// accumulator does not: it assigns each, so it holds only the latest.
	grokUsageSum int

	started time.Time
	timer   *time.Timer
	proc    *os.Process
	exited  <-chan struct{}

	mu     sync.Mutex
	breach *adapters.StageBudgetBreach
}

// newStageBudgetEnforcer returns the enforcer for a stage whose stream has
// format, under limits. timeout is the stage's own deadline: the wall-clock
// budget only arms when it is the shorter of the two, because the stage
// timeout already ends the stage otherwise.
func newStageBudgetEnforcer(limits config.ResolvedStageBudget, format AdapterStreamFormat, timeout time.Duration) *stageBudgetEnforcer {
	e := &stageBudgetEnforcer{limits: limits, format: format, grace: stageBudgetKillGrace}
	if timeout > 0 && limits.MaxWallClock >= timeout {
		e.limits.MaxWallClock = config.StageBudgetUnlimited
	}
	return e
}

// arm starts the wall clock on the stage's spawned process. exited closes
// once the process has been reaped.
func (e *stageBudgetEnforcer) arm(proc *os.Process, exited <-chan struct{}) {
	e.proc, e.exited, e.started = proc, exited, time.Now()
	if e.limits.MaxWallClock > 0 {
		e.timer = time.AfterFunc(e.limits.MaxWallClock, func() {
			if e.record(StageBudgetWallClock, time.Since(e.started).Milliseconds(), e.limits.MaxWallClock.Milliseconds()) {
				e.stop()
			}
		})
	}
}

// disarm stops the wall clock. The stage has ended.
func (e *stageBudgetEnforcer) disarm() {
	if e.timer != nil {
		e.timer.Stop()
	}
}

// observe checks one parsed stdout event against the turn budget and, when
// tokenUpdated says the event carried usage, acc's running total against the
// token budget. It reports whether this event breached a budget, once; the
// caller then stops the stage.
func (e *stageBudgetEnforcer) observe(event *StreamEvent, tokenUpdated bool, acc *TokenAccumulator) bool {
	if e.fired() {
		return false
	}
	if tokenUpdated && e.limits.MaxTokens > 0 {
		used := stageBudgetTokensUsed(acc)
		if e.format == StreamFormatGrok && event != nil && event.Type == "usage" && event.Usage != nil {
			// Grok's usage events are each its own turn's snapshot, and the
			// accumulator keeps the latest (the end event carries the
			// session total), so the running total is their sum.
			e.grokUsageSum += event.Usage.InputTokens + event.Usage.OutputTokens + event.Usage.CacheCreationInput
		}
		used = max(used, e.grokUsageSum)
		if used > e.limits.MaxTokens {
			return e.record(StageBudgetTokens, int64(used), int64(e.limits.MaxTokens))
		}
	}
	if event == nil || e.limits.MaxTurns <= 0 {
		return false
	}
	asksAnother := e.countTurn(event)
	if e.turns > e.limits.MaxTurns || (e.turns == e.limits.MaxTurns && asksAnother) {
		return e.record(StageBudgetTurns, int64(e.turns), int64(e.limits.MaxTurns))
	}
	return false
}

// countTurn counts the model turn event shows, if any, and reports whether
// the stage's latest turn asks for another one. A stage reaches its turn
// budget when its last allowed turn asks for another, which is where the
// claude CLI's own --max-turns stops it too.
//
//   - claude stream: a turn is a main-thread assistant message, counted once
//     by its id however many content blocks carry it; it asks for another
//     when one of its blocks is a tool_use. A subagent's messages are not the
//     stage's turns, as for --max-turns; its tokens still count.
//   - opencode: a turn is a step_finish; it asks for another unless its
//     reason is "stop". A step_start past the budget is a turn begun.
//   - grok: a turn is a usage event; a tool_call asks for another.
//   - codex: a turn is a completed item that is not the agent's message (a
//     command, a file change, a tool call); each asks for another.
//   - gemini: a turn is a tool_use event; each asks for another.
//
// Copilot prints plain text with no turn boundary, so only its wall-clock
// budget binds it.
func (e *stageBudgetEnforcer) countTurn(event *StreamEvent) bool {
	switch e.format {
	case StreamFormatClaude:
		if event.Type != "assistant" || event.Message == nil || event.ParentToolUseID != "" {
			return false
		}
		id := event.Message.ID
		if e.seen == nil {
			e.seen = map[string]bool{}
		}
		// A message without an id cannot be told apart from the next, so
		// each counts as a turn of its own: over-counting stops a stage
		// early, under-counting would never stop it.
		if id == "" || !e.seen[id] {
			e.seen[id] = true
			e.turns++
		}
		for _, t := range event.Message.ContentTypes {
			if t == "tool_use" {
				return true
			}
		}
	case StreamFormatOpenCode:
		switch event.Type {
		case "step_finish":
			e.turns++
			return event.OpenCodeStepReason != "stop"
		case "step_start":
			return e.turns >= e.limits.MaxTurns
		}
	case StreamFormatGrok:
		switch event.Type {
		case "usage":
			e.turns++
		case "tool_call":
			return true
		}
	case StreamFormatCodex:
		if event.Type == "item.completed" && event.Subtype != "text" && event.Subtype != "reasoning" && event.Subtype != "" {
			e.turns++
			return true
		}
	case StreamFormatGemini:
		if event.Type == "tool_use" {
			e.turns++
			return true
		}
	}
	return false
}

// stageBudgetTokensUsed is the tokens a stage's model processed so far:
// input, output and cache writes. Cache reads are left out: a hosted stage
// re-reads its whole cached context every turn, which its USD cap prices, and
// a local server reports its whole prompt as input anyway.
func stageBudgetTokensUsed(acc *TokenAccumulator) int {
	return acc.InputTokens + acc.OutputTokens + acc.CacheCreated
}

// record stamps the first breach and reports whether this call was it.
func (e *stageBudgetEnforcer) record(dimension string, observed, limit int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.breach != nil {
		return false
	}
	e.breach = &adapters.StageBudgetBreach{Dimension: dimension, Observed: observed, Limit: limit}
	return true
}

// fired reports whether the stage has breached a budget.
func (e *stageBudgetEnforcer) fired() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.breach != nil
}

// result is the breach, or nil.
func (e *stageBudgetEnforcer) result() *adapters.StageBudgetBreach {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.breach
}

// stop ends the stage: SIGTERM to its process group now, SIGKILL once the
// grace period has passed unless it has been reaped first. It returns at
// once. A budget stop is not an operator's stop, so RunResult.Cancelled stays
// false.
func (e *stageBudgetEnforcer) stop() {
	terminateProcessTree(e.proc, e.exited, e.grace)
}

// settle runs once the stage has been reaped. For a stopped stage it checks
// that no member of the stage's process group is left, SIGKILLs any that is,
// and records one that still is.
func (e *stageBudgetEnforcer) settle(pgid int) {
	b := e.result()
	if b == nil {
		return
	}
	survived := !processGroupGone(pgid, stageBudgetReapWindow)
	e.mu.Lock()
	b.GroupSurvived = survived
	e.mu.Unlock()
}

// notice is the stderr line of a stage stopped at a stage budget, or "".
func (e *stageBudgetEnforcer) notice() string {
	b := e.result()
	if b == nil {
		return ""
	}
	return StageBudgetNotice(*b)
}

// StageBudgetNotice is the stderr line that ends a stage stopped at a stage
// budget: the stamped reason, stage_budget_exceeded:<dimension>, with the
// observed value and the limit. It must not carry a phrase that a terminal
// rule ordered before budget-enforcer claims, such as "hard cap" or "stall
// kill", or the stop is recovered like a stall and retried.
func StageBudgetNotice(b adapters.StageBudgetBreach) string {
	observed, limit := fmt.Sprintf("%d", b.Observed), fmt.Sprintf("%d", b.Limit)
	if b.Dimension == StageBudgetWallClock {
		observed = (time.Duration(b.Observed) * time.Millisecond).String()
		limit = (time.Duration(b.Limit) * time.Millisecond).String()
	}
	line := fmt.Sprintf("%s %s:%s observed=%s limit=%s: the stage reached its %s budget and was stopped (pipeline.stage_budgets, docs/GUARDRAILS_AND_BUDGETS.md)",
		StageBudgetMarker, StageBudgetExceeded, b.Dimension, observed, limit, stageBudgetNoun(b.Dimension))
	if b.GroupSurvived {
		line += "; a member of its process group was still present after SIGKILL"
	}
	return line
}

// stageBudgetNoun names a dimension in prose.
func stageBudgetNoun(dimension string) string {
	switch dimension {
	case StageBudgetWallClock:
		return "wall-clock"
	case StageBudgetTokens:
		return "token"
	}
	return "turn"
}

// terminateProcessTree sends SIGTERM to proc's process group and, unless
// exited closes first, SIGKILL once grace has passed. It returns at once.
func terminateProcessTree(proc *os.Process, exited <-chan struct{}, grace time.Duration) {
	signalProcessTree(proc, syscall.SIGTERM)
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			signalProcessTree(proc, syscall.SIGKILL)
		}
	}()
}

// processGroupGone reports whether process group pgid has no member left,
// SIGKILLing the group while one is, for up to window. It is only meaningful
// once the group's leader has been reaped: an unreaped leader is a member
// until then. A member that has already exited but is not yet reaped by its
// new parent can still count for a moment, which the window allows for.
func processGroupGone(pgid int, window time.Duration) bool {
	if pgid <= 0 {
		return true
	}
	deadline := time.Now().Add(window)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		time.Sleep(15 * time.Millisecond)
	}
}
