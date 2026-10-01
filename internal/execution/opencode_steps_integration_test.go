//go:build opencode_integration || canary

package execution

// What OpenCode's per-run steps cap does, observed against the real opencode
// binary, pinned to the version the observation was made on (#1811):
//
//	go test -tags opencode_integration ./internal/execution/ -run OpenCodeIntegrationStepsCap -count=1 -v
//
// The stage is dispatched through Manager.RunStage, so the config is the one
// a real stage runs under: its turn cap becomes `steps` on every agent
// (ADR-022 § 7). The model is the #1618 stub provider on 127.0.0.1 serving
// its tools-forever script, a model that never stops calling tools, behind a
// front that records, per request, how many tools it offered and whether it
// carried OpenCode's step-cap nudge. Nothing but those two facts is kept, and
// no request body is written anywhere.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/stubprovider"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// openCodeStepsNudge opens the assistant message opencode 1.18.30 appends to
// a request once its step counter reaches the agent's steps cap
// (session/prompt/max-steps.txt in the bundled source).
const openCodeStepsNudge = "CRITICAL - MAXIMUM STEPS REACHED"

// stepsRequest is what the front keeps of one chat completions request.
type stepsRequest struct {
	tools int
	nudge bool
}

// stepsRecorder is the stub provider serving tools-forever, behind a front
// that records each request's stepsRequest.
type stepsRecorder struct {
	URL      string
	mu       sync.Mutex
	requests []stepsRequest
}

func (r *stepsRecorder) seen() []stepsRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]stepsRequest(nil), r.requests...)
}

func startStepsRecorder(t *testing.T) *stepsRecorder {
	t.Helper()
	zero := time.Duration(0)
	stub, err := stubprovider.NewServer(stubprovider.Config{Script: "tools-forever", DelayOverride: &zero, MaxRequests: 200, IdleTimeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := stubprovider.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- stub.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("the stub provider stopped with %v", err)
		}
	})

	target, err := url.Parse("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	rec := &stepsRecorder{}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if strings.HasSuffix(req.URL.Path, "/chat/completions") {
			var chat struct {
				Tools    []json.RawMessage `json:"tools"`
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(body, &chat) == nil {
				seen := stepsRequest{tools: len(chat.Tools)}
				for _, m := range chat.Messages {
					if m.Role == "assistant" && bytes.Contains(m.Content, []byte(openCodeStepsNudge)) {
						seen.nudge = true
					}
				}
				rec.mu.Lock()
				rec.requests = append(rec.requests, seen)
				rec.mu.Unlock()
			}
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(front.Close)
	rec.URL = front.URL + "/v1"
	return rec
}

// TestOpenCodeIntegrationStepsCapIsNotAHardStop establishes what `steps`
// does on 1.18.30, and that the stage budget, not `steps`, ends the stage.
//
// The stage's turn cap is 4, so the per-run config gives every agent
// `steps: 4`; its turn budget is 12. On 1.18.30 the session loop computes
// `L=_>=oe` with `oe=Y.steps??1/0` and, when L holds, sends
// `messages:[...an,...L?[{role:"assistant",content:<max-steps prompt>}]:[]]`
// with `tools:le` unchanged: the cap appends a nudge saying tools are
// disabled and still offers every tool, and it never ends the loop. A model
// that keeps calling tools, as this stub does, is not stopped by it. The
// manager's turn budget (stage_budget.go, #1652) counts the stream's
// step_finish events and stops the stage at 12: SIGTERM to its process
// group, then SIGKILL, reaped, and stamped stage_budget_exceeded:turns.
//
// Without the stage budget's stop, the session keeps going until the stub
// stops answering: 206 tool-offering requests, every one from the fourth on
// carrying the nudge, in 97 s (measured while writing this test). The
// green run was observed on 1.18.30 and, under the canary, 1.18.32 alike.
func TestOpenCodeIntegrationStepsCapIsNotAHardStop(t *testing.T) {
	realOpenCode(t)
	useRealNightgaugeBinary(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	rec := startStepsRecorder(t)
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "http://127.0.0.1:1234/v1", rec.URL, 1))
	workspace, _ := openCodeGitWorktree(t, map[string]string{"calc.py": "def add(a, b):\n    return a + b\n"})

	const steps, turnBudget = 4, 12
	const timeout = 3 * time.Minute
	var result *adapters.RunResult
	var err error
	start := time.Now()
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/stub/stub-model", nil)
		opts.AllowedTools = []string{"Read"}
		opts.MaxTurns = steps
		opts.StageBudgets = map[string]config.StageBudget{"default": {MaxTurns: turnBudget}}
		opts.Timeout = timeout
		result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, stderr)
	}

	// What the cap did: every request from the one at the cap on carried the
	// nudge, and every one of them still offered tools, which the model used.
	var loop []stepsRequest
	for _, r := range rec.seen() {
		if r.tools > 0 {
			loop = append(loop, r)
		}
	}
	t.Logf("%d loop requests; stage ended after %s", len(loop), elapsed.Round(time.Millisecond))
	if len(loop) <= steps {
		t.Fatalf("the session made %d tool-offering requests, no more than its steps cap of %d, so this run shows nothing about the cap:\n%s", len(loop), steps, stderr)
	}
	for i, r := range loop {
		if want := i+1 >= steps; r.nudge != want {
			t.Errorf("loop request %d carried the nudge = %v, want %v: the cap applies from step %d on", i+1, r.nudge, want, steps)
		}
	}
	if n := strings.Count(result.Stdout, `"type":"tool_use"`); n <= steps {
		t.Errorf("the stream shows %d tool calls, no more than the steps cap of %d: the cap stopped tool use after all", n, steps)
	}

	// What ended the stage: the turn budget, not the cap, the stub's request
	// limit or the timeout. A breach is stamped when it is observed, so the
	// stamp alone would not show the stage was stopped; the requests made
	// after it do. Without the stop, this run makes the stub's 200 and more.
	if len(loop) > turnBudget+1 {
		t.Errorf("the session made %d tool-offering requests against a turn budget of %d: the stage was not stopped at its budget", len(loop), turnBudget)
	}
	b := result.StageBudgetExceeded
	if b == nil || b.Dimension != StageBudgetTurns || b.Observed != turnBudget || b.Limit != turnBudget {
		t.Fatalf("StageBudgetExceeded = %+v, want turns observed %d limit %d\n%s", b, turnBudget, turnBudget, stderr)
	}
	if b.GroupSurvived {
		t.Errorf("a member of the stage's process group survived SIGKILL")
	}
	if kind := terminalkind.Classify(result.Stderr); kind != "budget_exceeded" {
		t.Errorf("the stage's stderr classifies as %q, want budget_exceeded:\n%s", kind, result.Stderr)
	}
	if result.ExitCode == 0 || result.Cancelled {
		t.Errorf("ExitCode %d, Cancelled %v: want a failed stage nobody cancelled", result.ExitCode, result.Cancelled)
	}
	if elapsed >= timeout {
		t.Errorf("the stage ran %s, to its %s timeout: the turn budget did not stop it", elapsed, timeout)
	}
}
