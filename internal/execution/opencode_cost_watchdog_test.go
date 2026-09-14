package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// costStage is one opencode stage runCostStage dispatches to a fake opencode.
type costStage struct {
	// model is the dispatched model, and the export names it as the model
	// that served the stage.
	model string
	// budget is the stage's CostBudget in USD.
	budget float64
	// run is the shell the stage runs once it has read its prompt. $STEPS is
	// the captured research stream.
	run string
}

// costStageOutcome is what one stage left behind.
type costStageOutcome struct {
	result *adapters.RunResult
	// logged is what the manager wrote to its own stderr.
	logged string
	// pid is the stage's own process, the leader of its process group.
	pid int
	// elapsed is how long RunStage took.
	elapsed time.Duration
}

// runCostStage dispatches stage through Manager.RunStage. The fake answers
// the version probe and the usage fold's --version, db and export, and its
// run replays the real opencode 1.18.30 capture in
// testdata/opencode_stream_research_sample.jsonl as stage.run decides.
func runCostStage(t *testing.T, stage costStage) costStageOutcome {
	t.Helper()
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic-key-for-the-cost-watchdog-test")
	dir := t.TempDir()
	steps := filepath.Join(dir, "steps.jsonl")
	if err := os.WriteFile(steps, []byte(readTestdata(t, "opencode_stream_research_sample.jsonl")), 0o600); err != nil {
		t.Fatal(err)
	}
	key, id, _ := strings.Cut(stage.model, "/")
	export := filepath.Join(dir, "export.json")
	if err := os.WriteFile(export, []byte(sessionExport(0, 0, 0, 0, 0, 0, key, id)), 0o600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "stage.pid")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
--version) echo 1.18.30; exit 0 ;;
db) echo '[]'; exit 0 ;;
export) cat %q; exit 0 ;;
esac
echo $$ > %q
cat > /dev/null
STEPS=%q
%s
`, export, pidFile, steps, stage.run)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	opts := openCodeStageOptions(stage.model, nil)
	opts.CostBudget = stage.budget
	opts.Timeout = 20 * time.Second
	manager := NewManager(openCodeWorkspace(t), adapters.NewOpenCodeAdapter())
	var result *adapters.RunResult
	var err error
	start := time.Now()
	logged := captureStderr(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err = manager.RunStage(ctx, opts)
		}()
		select {
		case <-done:
		case <-time.After(45 * time.Second):
			if pid := readPID(t, pidFile); pid > 0 {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
			<-done
		}
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, logged)
	}
	return costStageOutcome{result: result, logged: logged, pid: readPID(t, pidFile), elapsed: elapsed}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}

// capturedSteps is the usage of each step_finish of the research capture,
// read through the stream parser, in order.
func capturedSteps(t *testing.T) []tokens.TokenCounts {
	t.Helper()
	var steps []tokens.TokenCounts
	acc := &TokenAccumulator{}
	var prev tokens.TokenCounts
	for _, line := range strings.Split(readTestdata(t, "opencode_stream_research_sample.jsonl"), "\n") {
		if _, added := acc.ParseOpenCodeStreamLine(line); !added {
			continue
		}
		now := tokens.TokenCounts{Input: acc.InputTokens, Output: acc.OutputTokens, CacheRead: acc.CacheRead, CacheCreation5m: acc.CacheCreated}
		steps = append(steps, tokens.TokenCounts{
			Input: now.Input - prev.Input, Output: now.Output - prev.Output,
			CacheRead: now.CacheRead - prev.CacheRead, CacheCreation5m: now.CacheCreation5m - prev.CacheCreation5m,
		})
		prev = now
	}
	if len(steps) == 0 {
		t.Fatal("the capture has no step_finish event")
	}
	return steps
}

// processGone reports whether pid and its process group no longer exist.
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) && errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH)
}

// TestOpenCodeCostWatchdog: a stage on a registry-priced model whose stream
// never ends is stopped at the step_finish that takes its registry-priced
// cost past CostBudget. The stage's stderr ends with [cost-cap-exceeded],
// which classifies as budget_exceeded; the stage failed but was not
// cancelled, because nobody stopped it; and no process of its group survives.
func TestOpenCodeCostWatchdog(t *testing.T) {
	const model, budget = "anthropic/claude-sonnet-5", 0.01
	// The step that crosses the budget, from the capture's own numbers.
	steps := capturedSteps(t)
	var running tokens.TokenCounts
	crossing, inputAtCrossing := 0, 0
	for n := 1; crossing == 0; n++ {
		s := steps[(n-1)%len(steps)]
		running.Input += s.Input
		running.Output += s.Output
		running.CacheRead += s.CacheRead
		running.CacheCreation5m += s.CacheCreation5m
		if cost, _ := tokens.CalculateCostFor("opencode", model, running); cost > budget {
			crossing, inputAtCrossing = n, running.Input
		}
	}
	nextStep := steps[crossing%len(steps)].Input

	out := runCostStage(t, costStage{
		model:  model,
		budget: budget,
		run:    `while :; do cat "$STEPS"; sleep 0.2; done`,
	})
	res := out.result

	if res.Cancelled {
		t.Error("RunResult.Cancelled = true: a budget stop is not an operator's stop")
	}
	if res.ExitCode == 0 {
		t.Error("the stopped stage exited 0; a stage stopped at its budget failed")
	}
	if res.InputTokens < inputAtCrossing || res.InputTokens > inputAtCrossing+nextStep {
		t.Errorf("input tokens = %d; the budget is crossed at step %d (%d input), so the stage must stop there or one step later",
			res.InputTokens, crossing, inputAtCrossing)
	}
	lines := strings.Split(strings.TrimRight(res.Stderr, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, CostCapExceededMarker+" ") {
		t.Errorf("the stage's stderr does not end with %s:\n%s", CostCapExceededMarker, res.Stderr)
	}
	if kind := terminalkind.Classify(fmt.Sprintf("exit %d: %s", res.ExitCode, res.Stderr)); kind != "budget_exceeded" {
		t.Errorf("the stage classifies as %q, want budget_exceeded:\n%s", kind, res.Stderr)
	}
	if n := strings.Count(out.logged, CostCapExceededMarker); n != 1 {
		t.Errorf("the manager logged %s %d times, want once:\n%s", CostCapExceededMarker, n, out.logged)
	}
	if out.pid == 0 {
		t.Fatal("the fake never recorded its pid")
	}
	if !processGone(out.pid) {
		_ = syscall.Kill(-out.pid, syscall.SIGKILL)
		t.Errorf("process %d or its group survived the budget stop", out.pid)
	}
	if out.elapsed > 8*time.Second {
		t.Errorf("RunStage took %s: the stage ignored no SIGTERM, so it should end at once", out.elapsed)
	}
}

// TestOpenCodeCostWatchdogInert: a watchdog that cannot price the stage
// enforces nothing. A hosted model the registry does not list logs exactly one
// warning line and runs to its end; a local model is priced at zero, so its
// budget is never crossed and nothing is logged.
func TestOpenCodeCostWatchdogInert(t *testing.T) {
	wantInput := 0
	for _, s := range capturedSteps(t) {
		wantInput += s.Input
	}
	wantInput *= 5
	for _, tc := range []struct {
		model    string
		warnings int
	}{
		{"openai/gpt-9-preview", 1},
		{"lmstudio/qwen/qwen3.8-27b", 0},
	} {
		t.Run(tc.model, func(t *testing.T) {
			out := runCostStage(t, costStage{
				model:  tc.model,
				budget: 0.01,
				run:    `for i in 1 2 3 4 5; do cat "$STEPS"; done`,
			})
			res := out.result
			if res.ExitCode != 0 || strings.Contains(res.Stderr, CostCapExceededMarker) {
				t.Errorf("exit %d, stderr:\n%s\nwant the stage to run to its end", res.ExitCode, res.Stderr)
			}
			if res.InputTokens != wantInput {
				t.Errorf("input tokens = %d, want every step's %d", res.InputTokens, wantInput)
			}
			var warnings []string
			for _, line := range strings.Split(out.logged, "\n") {
				if strings.HasPrefix(line, OpenCodeCostWarning+" ") {
					warnings = append(warnings, line)
				}
			}
			if len(warnings) != tc.warnings {
				t.Errorf("%d %s lines, want %d:\n%s", len(warnings), OpenCodeCostWarning, tc.warnings, out.logged)
			}
			if strings.Contains(out.logged, CostCapExceededMarker) {
				t.Errorf("the manager logged a budget stop:\n%s", out.logged)
			}
		})
	}
}

// TestOpenCodeCostWatchdogKillsTheGroupAfterGrace: a stage that ignores the
// SIGTERM is sent SIGKILL, as a group, once the grace period has passed, and
// not before.
func TestOpenCodeCostWatchdogKillsTheGroupAfterGrace(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `trap '' TERM; sleep 30 & while :; do sleep 0.05; done`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	exited := make(chan struct{})
	var state *os.ProcessState
	go func() {
		_ = cmd.Wait()
		state = cmd.ProcessState
		close(exited)
	}()
	// Let the shell install its trap before the signal.
	time.Sleep(200 * time.Millisecond)

	w := &openCodeCostWatchdog{grace: 500 * time.Millisecond}
	start := time.Now()
	w.stop(cmd.Process, exited)
	select {
	case <-exited:
		t.Fatalf("the stage exited %s after SIGTERM, before the grace: it did not ignore the signal", time.Since(start))
	case <-time.After(300 * time.Millisecond):
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the stage outlived the grace period by 5s: no SIGKILL reached it")
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("the stage ended with %v, want SIGKILL", state)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d survived the SIGKILL", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOpenCodeStageCostRecorded: an opencode stage's cost reaches its record
// through the same call the scheduler makes, RuntimeState.CompleteStage with
// the stage's served model and the adapter. The captured stream's
// step_finish events report cost 0, because OpenCode holds no price for the
// provider. A local stage records a stamped zero; a hosted model the registry
// does not list stays unstamped however OpenCode priced it; a registry model
// is priced at its registry rates, over the stream's usage.
func TestOpenCodeStageCostRecorded(t *testing.T) {
	stream := readTestdata(t, "opencode_stream_research_sample.jsonl")
	if n := strings.Count(stream, `"type":"step_finish"`); n == 0 || strings.Count(stream, `"cost":0}`) != n {
		t.Fatalf("the capture's %d step_finish events do not all report cost 0; this test depends on it", n)
	}
	for _, tc := range []struct {
		model, served string
		stamped       bool
		cost          float64
	}{
		{"lmstudio/qwen/qwen3.8-27b", "lm-studio/qwen/qwen3.8-27b", true, 0},
		{"openai/gpt-9-preview", "openai/gpt-9-preview", false, 0},
		{"anthropic/claude-sonnet-5", "claude-sonnet-5", true, (3089*3.0 + 14*15.0) / 1e6},
	} {
		t.Run(tc.model, func(t *testing.T) {
			res := runCostStage(t, costStage{model: tc.model, run: `cat "$STEPS"`}).result
			if res.ServedModel != tc.served {
				t.Fatalf("served model = %q, want %q", res.ServedModel, tc.served)
			}
			if res.AdapterReportedCostUSD != 0 {
				t.Fatalf("OpenCode reported cost %v, want the capture's 0", res.AdapterReportedCostUSD)
			}
			rs := state.NewRuntimeState("nightgauge/nightgauge", 1630, "item-1630", "")
			rs.BeginStage(state.StageFeatureDev)
			rs.CompleteStage(res.ExitCode, tokens.TokenCounts{
				Input: res.InputTokens, Output: res.OutputTokens, CacheRead: res.CacheReadTokens,
				CacheCreation5m: res.CacheCreation5mTokens, CacheCreation1h: res.CacheCreation1hTokens,
			}, res.ServedModel, "opencode")
			got := rs.CompletedStages[0]
			if got.CostUnstamped == tc.stamped {
				t.Errorf("cost unstamped = %v, want %v", got.CostUnstamped, !tc.stamped)
			}
			if diff := got.CostUSD - tc.cost; diff > 1e-12 || diff < -1e-12 {
				t.Errorf("cost = %v, want %v", got.CostUSD, tc.cost)
			}
		})
	}
}
