package execution

// Stage budgets (#1652): the turn, wall-clock and token ceilings the manager
// enforces on a stage's stream while it runs. Every stage here runs through
// Manager.RunStage against a fake CLI that replays real captured streams
// (testdata/claude_stream_real_capture.jsonl,
// testdata/opencode_stream_research_sample.jsonl) at a steady pace, so a check
// that waited for the stream to end would let the fake print everything.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// budgetFakeAdapter runs script under /bin/sh. Its name decides the stream
// format the manager parses; it records the turn cap BuildCommand is given,
// the value a real adapter turns into --max-turns.
type budgetFakeAdapter struct {
	name     string
	script   string
	maxTurns atomic.Int64
}

func (a *budgetFakeAdapter) Name() string { return a.name }

func (a *budgetFakeAdapter) BuildCommand(opts adapters.RunOptions) (string, []string, map[string]string) {
	a.maxTurns.Store(int64(opts.MaxTurns))
	return "/bin/sh", []string{"-c", a.script}, nil
}

func (a *budgetFakeAdapter) UsesStdin() bool { return false }
func (a *budgetFakeAdapter) Agentic() bool   { return true }

// budgetStage is what one runBudgetStage dispatch left behind.
type budgetStage struct {
	result  *adapters.RunResult
	logged  string
	elapsed time.Duration
}

// beforeTimeout is the upper bound a budget-stopped stage's elapsed time is
// checked against: the stage timeout less a one-second margin. A stage the
// budget stops ends at about 1s; one it fails to stop runs to the timeout. A
// bound tied to the timeout keeps that distinction while leaving several
// seconds for a loaded machine, such as two gates running at once (#2123).
func beforeTimeout(timeout time.Duration) time.Duration { return timeout - time.Second }

// runBudgetStage dispatches one stage to adapter under budgets and timeout.
func runBudgetStage(t *testing.T, adapter adapters.SkillRunner, budgets map[string]config.StageBudget, timeout time.Duration) budgetStage {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-1652"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root, adapter)
	var out budgetStage
	var err error
	start := time.Now()
	out.logged = captureStderr(t, func() {
		out.result, err = m.RunStage(context.Background(), StageOptions{
			Repo:         "nightgauge/nightgauge",
			IssueNumber:  1652,
			Stage:        "feature-dev",
			Model:        "sonnet",
			Timeout:      timeout,
			StageBudgets: budgets,
		})
	})
	out.elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, out.logged)
	}
	return out
}

// claudeTurnsFixture writes a claude stream of n tool-using turns, each the
// real capture's first turn (its thinking and two tool_use blocks, then the
// two tool results) under its own message id, then the capture's result
// event. It returns the directory: turn01 .. turnNN, then result.
func claudeTurnsFixture(t *testing.T, n int) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(readTestdata(t, "claude_stream_real_capture.jsonl")), "\n")
	const firstTurn = "msg_011CdkixpD3Jjqq1eV8pN4zj"
	var turn []string
	var result string
	for _, line := range lines {
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("the capture holds a line that is not JSON: %v", err)
		}
		switch {
		case ev.Type == "assistant" && strings.Contains(line, firstTurn), ev.Type == "user":
			turn = append(turn, line)
		case ev.Type == "result":
			result = line
		}
	}
	if len(turn) != 5 || result == "" {
		t.Fatalf("the capture's first turn has %d lines and result %q; the fixture expects 3 assistant blocks and 2 tool results", len(turn), result)
	}
	dir := t.TempDir()
	for i := 1; i <= n; i++ {
		body := strings.ReplaceAll(strings.Join(turn, "\n"), firstTurn, fmt.Sprintf("msg_011CdkixpD3Jjqq1eV8pN%03d", i))
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("turn%02d", i)), []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "result"), []byte(result+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// countDistinct counts the distinct matches of re in s.
func countDistinct(re *regexp.Regexp, s string) int {
	seen := map[string]bool{}
	for _, m := range re.FindAllString(s, -1) {
		seen[m] = true
	}
	return len(seen)
}

// TestStageBudgetClaudeTurnsStopAtTheLimit: a claude stream of 12 tool-using
// turns against max_turns 5 is stopped at turn 5, as it streams, not after
// the stream has ended; the turn cap also reaches the adapter as its native
// --max-turns value. The stage fails with the stamped reason, which
// classifies as budget_exceeded.
func TestStageBudgetClaudeTurnsStopAtTheLimit(t *testing.T) {
	dir := claudeTurnsFixture(t, 12)
	adapter := &budgetFakeAdapter{
		name:   "claude-budget-fake",
		script: fmt.Sprintf(`for f in %q/turn*; do cat "$f"; sleep 0.15; done; cat %q/result`, dir, dir),
	}
	out := runBudgetStage(t, adapter, map[string]config.StageBudget{"feature-dev": {MaxTurns: 5}}, 30*time.Second)

	if got := adapter.maxTurns.Load(); got != 5 {
		t.Errorf("the adapter was built with MaxTurns %d, want 5: the turn budget is its native --max-turns", got)
	}
	b := out.result.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetTurns || b.Observed != 5 || b.Limit != 5 {
		t.Fatalf("StageBudgetExceeded = %+v, want turns observed 5 limit 5\nstderr:\n%s", b, out.result.Stderr)
	}
	turns := countDistinct(regexp.MustCompile(`msg_011CdkixpD3Jjqq1eV8pN\d{3}`), out.result.Stdout)
	if turns > 6 {
		t.Errorf("the stage printed %d of its 12 turns: it was not stopped at turn 5 as the stream arrived", turns)
	}
	if out.result.ExitCode == 0 {
		t.Error("ExitCode = 0, want the stage failed")
	}
	if out.result.Cancelled {
		t.Error("Cancelled = true: a budget stop is not an operator's stop")
	}
	if kind := terminalkind.Classify(out.result.Stderr); kind != "budget_exceeded" {
		t.Errorf("the stage's stderr classifies as %q, want budget_exceeded:\n%s", kind, out.result.Stderr)
	}
}

// openCodeStepsFixture writes the research capture's tool-using step
// (step_start, tool_use, a step_finish whose reason is tool-calls), which a
// test's fake replays once per step. When
// input is non-zero every step_finish reports that many input tokens, 44
// output and 10 reasoning, and no cache.
func openCodeStepsFixture(t *testing.T, input int) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(readTestdata(t, "opencode_stream_research_sample.jsonl")), "\n")
	step := lines[:3]
	if !strings.Contains(step[2], `"type":"step_finish"`) || !strings.Contains(step[2], `"reason":"tool-calls"`) {
		t.Fatalf("the research capture's third line is not a tool-calls step_finish: %s", step[2])
	}
	if input > 0 {
		var ev map[string]any
		if err := json.Unmarshal([]byte(step[2]), &ev); err != nil {
			t.Fatal(err)
		}
		ev["part"].(map[string]any)["tokens"] = map[string]any{
			"total": input + 54, "input": input, "output": 44, "reasoning": 10,
			"cache": map[string]any{"read": 0, "write": 0},
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		step = []string{step[0], step[1], string(raw)}
	}
	path := filepath.Join(t.TempDir(), "step.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(step, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStageBudgetOpenCodeStepsStopAtTheLimit: an OpenCode stage of 8
// tool-using steps against max_turns 5 is stopped at the fifth step_finish,
// as it streams. The same limit reaches OpenCode as its agents' steps cap,
// which #1811 found is not a hard stop, so the stream count is what ends
// the stage.
func TestStageBudgetOpenCodeStepsStopAtTheLimit(t *testing.T) {
	step := openCodeStepsFixture(t, 0)
	cfg := filepath.Join(t.TempDir(), "config.json")
	out := runCostStage(t, costStage{
		model:        "lmstudio/qwen/qwen3.8-27b",
		stageBudgets: map[string]config.StageBudget{"default": {MaxTurns: 5}},
		run: fmt.Sprintf(`printf '%%s' "$OPENCODE_CONFIG_CONTENT" > %q
for i in 1 2 3 4 5 6 7 8; do cat %q; sleep 0.2; done`, cfg, step),
	})
	res := out.result
	b := res.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetTurns || b.Observed != 5 || b.Limit != 5 {
		t.Fatalf("StageBudgetExceeded = %+v, want turns observed 5 limit 5\nstderr:\n%s", b, res.Stderr)
	}
	if n := strings.Count(res.Stdout, `"type":"step_finish"`); n > 6 {
		t.Errorf("the stage printed %d of its 8 steps: it was not stopped at the fifth as the stream arrived", n)
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("the fake did not record its config: %v", err)
	}
	if !strings.Contains(string(raw), `"steps":5`) {
		t.Errorf("OpenCode's config does not carry the turn budget as its steps cap:\n%s", raw)
	}
	if kind := terminalkind.Classify(res.Stderr); kind != "budget_exceeded" {
		t.Errorf("the stage's stderr classifies as %q, want budget_exceeded:\n%s", kind, res.Stderr)
	}
	if res.ExitCode == 0 || res.Cancelled {
		t.Errorf("ExitCode %d, Cancelled %v: want a failed stage nobody cancelled", res.ExitCode, res.Cancelled)
	}
}

// TestStageBudgetOpenCodeTokensStopAfterTheCrossingEvent: an OpenCode stage
// whose running token total crosses max_tokens 50000 at its seventh
// step_finish (7 x 7154 = 50078; six steps are 42924) is stopped right
// after that event, not once its ten steps have all been printed.
func TestStageBudgetOpenCodeTokensStopAfterTheCrossingEvent(t *testing.T) {
	step := openCodeStepsFixture(t, 7100)
	out := runCostStage(t, costStage{
		model:        "lmstudio/qwen/qwen3.8-27b",
		stageBudgets: map[string]config.StageBudget{"default": {MaxTokens: 50_000}},
		run:          fmt.Sprintf(`for i in 1 2 3 4 5 6 7 8 9 10; do cat %q; sleep 0.2; done`, step),
	})
	res := out.result
	b := res.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetTokens || b.Observed != 50_078 || b.Limit != 50_000 {
		t.Fatalf("StageBudgetExceeded = %+v, want tokens observed 50078 limit 50000\nstderr:\n%s", b, res.Stderr)
	}
	if n := strings.Count(res.Stdout, `"type":"step_finish"`); n > 8 {
		t.Errorf("the stage printed %d of its 10 steps: it was not stopped after the seventh as the stream arrived", n)
	}
	if !strings.Contains(res.Stderr, "stage_budget_exceeded:tokens observed=50078 limit=50000") {
		t.Errorf("stderr does not stamp the observed total and the limit:\n%s", res.Stderr)
	}
}

// TestStageBudgetGrokTokensSumThePerTurnSnapshots: grok's usage events are
// each one turn's own snapshot (testdata/README.md), which the accumulator
// assigns rather than sums. A grok stage repeating the real capture's first
// usage event (3593 in, 63 out, 28 reasoning: 3684 a turn) against
// max_tokens 20000 is stopped at the sixth (22104), as it streams, not at
// its end event.
func TestStageBudgetGrokTokensSumThePerTurnSnapshots(t *testing.T) {
	var usage string
	for _, line := range strings.Split(readTestdata(t, "grok_stream_real_capture.jsonl"), "\n") {
		if strings.HasPrefix(line, `{"type":"usage"`) {
			usage = line
			break
		}
	}
	if !strings.Contains(usage, `"input_tokens":3593`) {
		t.Fatalf("the capture's first usage event is not the one this test counts: %s", usage)
	}
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, []byte(usage+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &budgetFakeAdapter{
		name:   "grok-budget-fake",
		script: fmt.Sprintf(`for i in 1 2 3 4 5 6 7 8 9 10; do cat %q; sleep 0.15; done`, path),
	}
	out := runBudgetStage(t, adapter, map[string]config.StageBudget{"default": {MaxTokens: 20_000}}, 30*time.Second)
	b := out.result.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetTokens || b.Observed != 22_104 || b.Limit != 20_000 {
		t.Fatalf("StageBudgetExceeded = %+v, want tokens observed 22104 limit 20000\nstderr:\n%s", b, out.result.Stderr)
	}
	if n := strings.Count(out.result.Stdout, `"type":"usage"`); n > 7 {
		t.Errorf("the stage printed %d of its 10 turns: it was not stopped at the sixth as the stream arrived", n)
	}
}

// TestStageBudgetClaudeMessagesWithoutAnIdEachCountAsATurn: assistant events
// that carry no message id cannot be told apart, so each counts as a turn;
// folded into one, a stage of them would never reach its turn budget.
func TestStageBudgetClaudeMessagesWithoutAnIdEachCountAsATurn(t *testing.T) {
	e := newStageBudgetEnforcer(config.ResolvedStageBudget{MaxTurns: 3}, StreamFormatClaude, time.Minute)
	acc := &TokenAccumulator{}
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}`
	for i := 1; i <= 5; i++ {
		event, updated := acc.ParseLine(StreamFormatClaude, line)
		if e.observe(event, updated, acc) {
			if i != 3 {
				t.Fatalf("the turn budget fired at id-less message %d, want 3", i)
			}
			return
		}
	}
	t.Fatal("five id-less tool-using messages never reached max_turns 3")
}

// TestStageBudgetWallClockBindsAChildThatClosedItsOutput: a stage that closes
// stdout and stderr and keeps running is still stopped at its wall clock,
// stamped, and its group checked: the wall clock runs until the stage is
// reaped, not until its output closes.
func TestStageBudgetWallClockBindsAChildThatClosedItsOutput(t *testing.T) {
	adapter := &budgetFakeAdapter{
		name:   "claude-budget-fake",
		script: `exec >/dev/null 2>&1; sleep 30`,
	}
	out := runBudgetStage(t, adapter, map[string]config.StageBudget{"default": {MaxWallClock: config.StageBudgetDuration(time.Second)}}, 15*time.Second)
	b := out.result.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetWallClock {
		t.Fatalf("StageBudgetExceeded = %+v, want a wall_clock breach (ran %s)", b, out.elapsed)
	}
	if out.elapsed > beforeTimeout(15*time.Second) {
		t.Errorf("the stage ran %s: want it stopped at about 1s, well before its 15s timeout", out.elapsed)
	}
}

// TestStageBudgetWallClockStopsAContinuouslyStreamingStage: a stage that
// prints a line every 100 ms, so it is never idle, under max_wall_clock 1s
// and a 10s stage timeout is stopped at about 1s by the wall-clock budget,
// well before the timeout. Only a lower bound and an upper bound of the
// timeout less a margin are asserted, so a loaded machine cannot make it
// flaky but a stage the budget failed to stop still fails.
func TestStageBudgetWallClockStopsAContinuouslyStreamingStage(t *testing.T) {
	adapter := &budgetFakeAdapter{
		name:   "claude-budget-fake",
		script: `while :; do echo '{"type":"system","subtype":"tick"}'; sleep 0.1; done`,
	}
	out := runBudgetStage(t, adapter, map[string]config.StageBudget{"feature-dev": {MaxWallClock: config.StageBudgetDuration(time.Second)}}, 10*time.Second)
	b := out.result.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetWallClock || b.Limit != 1000 || b.Observed < 1000 {
		t.Fatalf("StageBudgetExceeded = %+v, want wall_clock with limit 1000ms and observed at least that\nstderr:\n%s", b, out.result.Stderr)
	}
	if out.elapsed < 900*time.Millisecond || out.elapsed > beforeTimeout(10*time.Second) {
		t.Errorf("the stage ran %s: want it stopped at about 1s, well before its 10s timeout", out.elapsed)
	}
	if kind := terminalkind.Classify(out.result.Stderr); kind != "budget_exceeded" {
		t.Errorf("the stage's stderr classifies as %q, want budget_exceeded:\n%s", kind, out.result.Stderr)
	}
}

// TestStageBudgetBreachKillsTheWholeProcessGroup: a stage that traps SIGTERM
// and has spawned a grandchild that ignores it too is stopped at its wall
// clock. Both pids end dead (kill -0 fails), SIGKILL having followed the
// grace period; the manager's own check finds no survivor; and the stamped
// reason classifies as budget_exceeded.
func TestStageBudgetBreachKillsTheWholeProcessGroup(t *testing.T) {
	prevGrace := stageBudgetKillGrace
	stageBudgetKillGrace = 500 * time.Millisecond
	t.Cleanup(func() { stageBudgetKillGrace = prevGrace })
	dir := t.TempDir()
	childPID, grandchildPID := filepath.Join(dir, "child.pid"), filepath.Join(dir, "grandchild.pid")
	adapter := &budgetFakeAdapter{
		name: "claude-budget-fake",
		script: fmt.Sprintf(`trap '' TERM
echo $$ > %q
/bin/sh -c 'trap "" TERM; while :; do sleep 0.1; done' &
echo $! > %q
while :; do echo '{"type":"system","subtype":"tick"}'; sleep 0.1; done`, childPID, grandchildPID),
	}
	t.Cleanup(func() {
		for _, f := range []string{childPID, grandchildPID} {
			if pid := readPID(t, f); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	out := runBudgetStage(t, adapter, map[string]config.StageBudget{"default": {MaxWallClock: config.StageBudgetDuration(time.Second)}}, 20*time.Second)

	b := out.result.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetWallClock {
		t.Fatalf("StageBudgetExceeded = %+v, want a wall_clock breach\nstderr:\n%s", b, out.result.Stderr)
	}
	if b.GroupSurvived {
		t.Error("GroupSurvived = true: the manager's check found a member of the group after SIGKILL")
	}
	if out.elapsed > beforeTimeout(20*time.Second) {
		t.Errorf("the stage ran %s: its own SIGKILL after the grace should have ended it well before the 20s stage timeout", out.elapsed)
	}
	for name, file := range map[string]string{"child": childPID, "grandchild": grandchildPID} {
		pid := readPID(t, file)
		if pid <= 0 {
			t.Fatalf("the %s never wrote its pid", name)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			if time.Now().After(deadline) {
				t.Fatalf("the %s (pid %d) is alive 5s after the stage returned: kill -0 still succeeds", name, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if kind := terminalkind.Classify(out.result.Stderr); kind != "budget_exceeded" {
		t.Errorf("the stamped reason classifies as %q, want budget_exceeded:\n%s", kind, out.result.Stderr)
	}
	if !strings.Contains(out.result.Stderr, StageBudgetExceeded+":"+StageBudgetWallClock) {
		t.Errorf("stderr does not stamp %s:%s:\n%s", StageBudgetExceeded, StageBudgetWallClock, out.result.Stderr)
	}
}

// TestStageBudgetUnlimitedWarnsOnEveryDispatch: an explicit -1 lifts a limit
// on a priced stage, and says so each time the stage is dispatched, not once.
func TestStageBudgetUnlimitedWarnsOnEveryDispatch(t *testing.T) {
	adapter := &budgetFakeAdapter{name: "claude-budget-fake", script: `exit 0`}
	budgets := map[string]config.StageBudget{"default": {MaxTurns: config.StageBudgetUnlimited}}
	for i := 1; i <= 2; i++ {
		out := runBudgetStage(t, adapter, budgets, 10*time.Second)
		if !strings.Contains(out.logged, StageBudgetMarker) || !strings.Contains(out.logged, "no turn budget") {
			t.Errorf("dispatch %d logged no unlimited-turns warning:\n%s", i, out.logged)
		}
		if got := adapter.maxTurns.Load(); got != 0 {
			t.Errorf("dispatch %d built the adapter with MaxTurns %d, want none", i, got)
		}
	}
}

// TestStageBudgetStampsClassifyAsBudgetExceeded: every dimension's stamped
// line classifies as budget_exceeded through the terminal-kind table, which
// has no rule of its own for it: the budget-enforcer rule claims
// stage_budget_exceeded. None of the lines may be claimed first by a rule
// that recovers differently, such as the stall rule's "hard cap".
func TestStageBudgetStampsClassifyAsBudgetExceeded(t *testing.T) {
	for _, b := range []adapters.StageBudgetBreach{
		{Dimension: StageBudgetTurns, Observed: 5, Limit: 5},
		{Dimension: StageBudgetWallClock, Observed: 1003, Limit: 1000},
		{Dimension: StageBudgetTokens, Observed: 50_078, Limit: 50_000},
		{Dimension: StageBudgetTokens, Observed: 50_078, Limit: 50_000, GroupSurvived: true},
	} {
		line := StageBudgetNotice(b)
		if kind := terminalkind.Classify("exit 1: " + line); kind != "budget_exceeded" {
			t.Errorf("%q classifies as %q, want budget_exceeded", line, kind)
		}
	}
}

// TestStageCost pins which stages the zero-cost rule covers: a model server
// the operator runs, including an endpoint the machine-tier opencode: block
// declares as lm-studio or ollama under its own id, and a model the registry
// prices at $0. A hosted model the registry cannot price is unpriced, not
// zero-cost.
func TestStageCost(t *testing.T) {
	isolateOpenCodeHome(t)
	writeOpenCodeMachineConfig(t, `opencode:
  endpoints:
    - id: gpubox
      provider: lm-studio
      base_url: http://127.0.0.1:1234/v1
      limit:
        context: 131072
        output: 8192
    - id: omlx
      provider: openai-compatible
      base_url: http://127.0.0.1:4000/v1
      limit:
        context: 131072
        output: 8192
    - id: litellm
      provider: openai-compatible
      base_url: http://127.0.0.1:4001/v1
      self_hosted: false
      limit:
        context: 131072
        output: 8192
`)
	for _, tc := range []struct {
		adapter, model string
		want           config.StageCost
	}{
		{"opencode", "lmstudio/qwen/qwen3.8-27b", config.StageZeroCost},
		{"opencode", "ollama/llama3", config.StageZeroCost},
		{"opencode", "gpubox/qwen3-coder", config.StageZeroCost},
		// Locality follows the declared endpoint, not the key's brand (#2128):
		// an openai-compatible endpoint on loopback is a server the operator
		// runs, and an undeclared unknown key is still unpriced.
		{"opencode", "omlx/qwen3-coder-30b", config.StageZeroCost},
		{"opencode", "mystery/some-model", config.StageUnpriced},
		// A loopback proxy to a hosted API, marked self_hosted: false, is
		// never a $0 model a USD cap cannot bind (#1679).
		{"opencode", "litellm/claude-sonnet-5", config.StageUnpriced},
		// An Ollama cloud model runs on Ollama's hosted service (#1679).
		{"opencode", "ollama/gpt-oss:120b-cloud", config.StageUnpriced},
		// A local serving provider (a RunResult's ModelProvider) is zero.
		{"lm-studio", "qwen", config.StageZeroCost},
		{"ollama", "llama3", config.StageZeroCost},
		{"copilot", "sonnet", config.StageZeroCost},
		{"claude", "sonnet", config.StagePriced},
		{"claude", "fable", config.StagePriced},
		{"opencode", "anthropic/claude-sonnet-5", config.StagePriced},
		{"claude", "claude-unlisted-model", config.StageUnpriced},
	} {
		if got := stageCost(tc.adapter, tc.model, ""); got != tc.want {
			t.Errorf("stageCost(%q, %q) = %v, want %v", tc.adapter, tc.model, got, tc.want)
		}
	}
}
