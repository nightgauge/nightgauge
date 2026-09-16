//go:build canary

package execution

// The latest-CLI canary's OpenCode leg (#1639). scripts/adapter-canary.sh
// runs
//
//	go test -tags canary ./internal/execution -run TestOpenCodeCanary
//
// after installing opencode at, on a schedule or workflow_dispatch, npm's
// `latest` dist-tag — almost never the manifest's own max_tested (opencode
// 1.18.30, ADR-022 § 20) — or, on a pull_request that proposes a new
// max_tested, that proposed version; either way against the #1618 stub
// provider. realOpenCode's own version pin (see nightgaugeCanaryEnv in
// opencode_isolation_integration_test.go) is relaxed to any resolvable
// version under this build tag for exactly that reason: this leg's whole
// point is to exercise whatever is newest today, not to re-verify the pin.
//
// TestOpenCodeCanaryLiveStream dispatches through the same Manager.RunStage
// path a real stage runs (adapters.NewOpenCodeAdapter), reusing the helpers
// opencode_isolation_integration_test.go and manager_test.go already proved:
// realOpenCode (the CI-must-run rule; the exact-version pin holds only for
// the separate opencode_integration suite), isolateOpenCodeHome,
// openCodeStageOptions, writeOpenCodeMachineConfig and openCodeGitWorktree.
// TestOpenCodeCanaryPermission and TestOpenCodeCanaryBadModel's exit-code
// assertions are about the CLI's OWN contract, which Manager.RunStage's own
// reclassification of a self-rejected permission (ADR-022 § 9,
// opencode_usage.go) does not preserve, so those two spawn the binary
// directly (openCodeCanaryDirectRun) with the #1616 isolation env and a
// hand-built config, the same connector PrepareOpenCodeRun's own endpoint
// blocks use (openCodeEndpointNPM, opencode_config.go).
//
// The stream contract (openCodeKnownEvents, TokenAccumulator's own
// ParseOpenCodeStreamLine and its DriftMarkers) is the #1624 parser: this file
// imports no separate list of event types, so the assertions track that
// single source rather than a hand-copied one. Field-path checks
// (openCodeCanaryFieldPaths) additionally walk the raw JSON, because a struct
// field with `json:"...,omitempty"` silently zero-values a renamed key
// instead of failing to parse it. TestOpenCodeCanaryDisableFlags checks the
// #1616 isolation builder's own output (adapters.OpenCodeIsolationEnv)
// directly, rather than a copy of its variable names.
//
// TestOpenCodeCanaryStreamContract needs no live binary: it runs the same
// contract check (checkOpenCodeCanaryStream) against the real captured
// fixtures already committed for the #1624 parser
// (testdata/opencode_stream_local_capture.jsonl,
// testdata/opencode_auto_reject_stream.jsonl), including the negative case a
// renamed step_finish must fail. TestOpenCodeCanaryLiveStream and
// TestOpenCodeCanaryPermission then run the identical check function against
// a live run's own stdout, so the fixture and live paths cannot silently
// diverge.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// openCodeCanaryModel dispatches to the stub provider through the machine
// tier's lmstudio-shaped endpoint (ADR-022 § 1), the same addressing the flag
// contract and schema contract suites use for a local model
// (flagContractModel, schemaContractConfigs).
const openCodeCanaryModel = "lmstudio/stub/stub-model"

// init registers this file's cleanup into opencode_mcp_forge_test.go's
// single, untagged TestMain (#1639 round 4 low) — canaryStubProviderCleanup
// runs after m.Run() and removes stubProviderCanaryDir if
// stubProviderCanaryBinary ever created one.
func init() {
	canaryStubProviderCleanup = func() {
		if stubProviderCanaryDir != "" {
			os.RemoveAll(stubProviderCanaryDir)
		}
	}
}

// stubProviderCanaryDir is the os.MkdirTemp directory stubProviderCanaryBinary
// builds into, recorded so the TestMain cleanup above can remove it; empty
// until stubProviderCanaryBinary has actually run once.
var stubProviderCanaryDir string

// stubProviderCanaryBinary builds the real cmd/stub-provider binary once for
// the whole test binary run (not per test), the same way
// internal/stubprovider/server_test.go's own buildStubProviderBinary does —
// by Go import path, so it resolves from whatever package directory `go
// test` set as cwd, not a path relative to this file. AC8 names a stub PID
// "captured, killed and confirmed dead", which only a real OS subprocess —
// never the in-process stubprovider.NewServer this file used before round 3
// — has one of.
var stubProviderCanaryBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "nightgauge-canary-stub-provider-")
	if err != nil {
		return "", err
	}
	stubProviderCanaryDir = dir
	bin := filepath.Join(dir, "stub-provider")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/nightgauge/nightgauge/cmd/stub-provider")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building cmd/stub-provider: %w\n%s", err, out)
	}
	return bin, nil
})

// startOpenCodeCanaryStub spawns the real stub-provider BINARY as its own OS
// process, serving script on loopback, bounded the way the shell leg's
// `stub-provider --max-requests 20 --idle-timeout 60s` is (AC8), and returns
// its OpenAI-compatible base URL. Its PID is captured at spawn.
// t.Cleanup — which the testing package runs whether the test passes, fails
// or panics, the same unconditional guarantee an `if: always()` workflow
// step gives a shell-spawned process — sends SIGTERM, waits up to 2s,
// escalates to SIGKILL, and fails the test if the PID is somehow still alive
// after that: the stub is never left running past the test that started it.
func startOpenCodeCanaryStub(t *testing.T, script string) string {
	t.Helper()
	base, _ := startOpenCodeCanaryStubWithPID(t, script)
	return base
}

// startOpenCodeCanaryStubWithPID is startOpenCodeCanaryStub, also returning
// the spawned PID so TestOpenCodeCanaryStubIsKilledAndConfirmedDead can
// verify AC8's contract directly, rather than through this package's own
// unexported t.Cleanup succeeding silently.
func startOpenCodeCanaryStubWithPID(t *testing.T, script string) (baseURL string, pid int) {
	t.Helper()
	bin, err := stubProviderCanaryBinary()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--script", script, "--listen", "127.0.0.1:0",
		"--max-requests", "20", "--idle-timeout", "60s")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stub-provider StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting stub-provider: %v", err)
	}
	pid = cmd.Process.Pid

	// Reap the child as soon as it exits: kill(pid, 0) below would otherwise
	// keep succeeding on a zombie until something calls Wait.
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		wait := func(d time.Duration) bool {
			select {
			case <-waitDone:
				return true
			case <-time.After(d):
				return false
			}
		}
		if !wait(2 * time.Second) {
			_ = cmd.Process.Kill()
			wait(2 * time.Second)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Errorf("stub-provider pid %d is still alive after cleanup", pid)
		}
	})

	lineCh, errCh := make(chan string, 1), make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		lineCh <- line
	}()
	var line string
	select {
	case line = <-lineCh:
	case err := <-errCh:
		t.Fatalf("reading stub-provider's base_url: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("stub-provider did not print its base_url in time")
	}
	var payload struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &payload); err != nil {
		t.Fatalf("stub-provider printed %q, not the base_url JSON line: %v", line, err)
	}
	return payload.BaseURL, pid
}

// openCodeCanaryDirectRunID is a fixed, valid run identity (runstate.IsIdentity),
// the shape EnsureOpenCodeRunRoot requires, for openCodeCanaryDirectRun's own
// per-run root — a literal in the same style as flagContractOptions' RunID.
const openCodeCanaryDirectRunID = "0199a8b3-0000-7000-8000-000000001639"

// openCodeCanaryDirectRun spawns the real, installed opencode binary
// directly — never through Manager.RunStage — with the #1616 isolation env
// and a config declaring a "stub" provider pointed at stubBase, the OpenAI-
// compatible connector PrepareOpenCodeRun's own lm-studio/ollama endpoint
// blocks use (openCodeEndpointNPM, opencode_config.go). The fixed argv ADR-022
// § 1 pins:
//
//	opencode run --format json --print-logs --log-level ERROR -m <model> --dir <worktree>
//
// with prompt on stdin. Bypassing Manager.RunStage keeps this canary a check
// of the CLI's own contract, decoupled from Manager.RunStage's own
// reclassification of a self-rejected permission (ADR-022 § 9,
// opencode_usage.go): exitCode here is the CHILD PROCESS's own raw exit, the
// number the acceptance criteria's "the process exits 0" names.
func openCodeCanaryDirectRun(t *testing.T, real, model string, extraConfig map[string]any, stubBase string) (exitCode int, stdout, stderr string) {
	t.Helper()
	home := t.TempDir()
	root, _, err := adapters.EnsureOpenCodeRunRoot(home, openCodeCanaryDirectRunID, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	env, err := adapters.OpenCodeIsolationEnv(adapters.OpenCodeIsolation{
		Root: root, Home: home, GOOS: runtime.GOOS,
		Lookup:           func(string) (string, bool) { return "", false },
		MachineConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, worktree := openCodeGitWorktree(t, map[string]string{"calc.py": "def add(a, b):\n    return a + b\n"})

	config := map[string]any{
		"$schema":    "https://opencode.ai/config.json",
		"share":      "disabled",
		"autoupdate": false,
		"provider": map[string]any{
			"stub": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"options": map[string]any{"baseURL": stubBase},
				"models":  map[string]any{"stub-model": map[string]any{}},
			},
		},
	}
	for k, v := range extraConfig {
		config[k] = v
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}

	envSlice := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "OPENCODE_CONFIG_CONTENT=" + string(raw)}
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, "run", "--format", "json", "--print-logs", "--log-level", "ERROR",
		"-m", model, "--dir", worktree)
	cmd.Env = envSlice
	cmd.Stdin = strings.NewReader("implement the issue")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	switch e := runErr.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = e.ExitCode()
	default:
		t.Fatalf("running opencode directly: %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout, stderr)
	}
	return exitCode, stdout, stderr
}

// openCodeCanaryProblem is one contract violation, naming the offending raw
// line so a renamed or reshaped event is diagnosable from the failure alone.
type openCodeCanaryProblem struct {
	reason string
	line   string
}

func (p openCodeCanaryProblem) String() string {
	return fmt.Sprintf("%s\nline: %s", p.reason, p.line)
}

// openCodeCanaryFieldPaths walks one raw `opencode run --format json` line as
// generic JSON — independent of the #1624 parser's typed structs, which a
// `json:"...,omitempty"` field would let a renamed key pass silently — and
// checks the field paths the acceptance criteria name for each event kind:
// sessionID always; part.tokens.input and part.tokens.output and part.reason
// on step_finish; part.state.status and part.state.error on a tool_use whose
// state is an error.
func openCodeCanaryFieldPaths(line string) []string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil // not JSON: the caller's own event-type check reports it.
	}
	var problems []string
	if _, ok := raw["sessionID"]; !ok {
		problems = append(problems, "no top-level sessionID")
	}
	part, _ := raw["part"].(map[string]any)
	switch raw["type"] {
	case "step_finish":
		tokens, ok := part["tokens"].(map[string]any)
		if !ok {
			problems = append(problems, "no part.tokens object")
			break
		}
		if _, ok := tokens["input"]; !ok {
			problems = append(problems, "no part.tokens.input")
		}
		if _, ok := tokens["output"]; !ok {
			problems = append(problems, "no part.tokens.output")
		}
		if _, ok := part["reason"]; !ok {
			problems = append(problems, "no part.reason")
		}
	case "tool_use":
		state, _ := part["state"].(map[string]any)
		if status, _ := state["status"].(string); status == "error" {
			if _, ok := state["status"]; !ok {
				problems = append(problems, "no part.state.status")
			}
			if _, ok := state["error"]; !ok {
				problems = append(problems, "no part.state.error")
			}
		}
	}
	return problems
}

// checkOpenCodeCanaryStream runs every allow-list, field-path and token
// assertion the acceptance criteria name against one run's raw
// `opencode run --format json` stdout, using the production parser
// (TokenAccumulator.ParseOpenCodeStreamLine, stream.go) for the event-type
// allow-list and the drift/token accounting, and openCodeCanaryFieldPaths for
// the raw shape. It returns every problem found, and the accumulator so a
// caller can inspect rejection counts or session id.
func checkOpenCodeCanaryStream(stdout string) ([]openCodeCanaryProblem, *TokenAccumulator) {
	acc := &TokenAccumulator{}
	var problems []openCodeCanaryProblem
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			problems = append(problems, openCodeCanaryProblem{reason: "not a JSON event", line: line})
			continue
		}
		if !openCodeKnownEvents[probe.Type] {
			problems = append(problems, openCodeCanaryProblem{
				reason: fmt.Sprintf("event type %q is not in the allow-list step_start/step_finish/tool_use/text/reasoning/error", probe.Type),
				line:   line,
			})
		}
		for _, reason := range openCodeCanaryFieldPaths(line) {
			problems = append(problems, openCodeCanaryProblem{reason: reason, line: line})
		}
		acc.ParseOpenCodeStreamLine(line)
	}
	for _, marker := range acc.OpenCode().DriftMarkers() {
		problems = append(problems, openCodeCanaryProblem{reason: marker, line: "(see the run's own stdout; the parser does not repeat the line)"})
	}
	return problems, acc
}

// openCodeCanaryFixture reads a committed real capture of `opencode run
// --format json` stdout (testdata/README.md documents each one's provenance).
func openCodeCanaryFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestOpenCodeCanaryStreamContract runs checkOpenCodeCanaryStream against the
// committed real captures — no live binary needed — so the contract's own
// logic is provably red on a reshaped stream before any live case runs it.
func TestOpenCodeCanaryStreamContract(t *testing.T) {
	t.Run("clean run", func(t *testing.T) {
		stdout := openCodeCanaryFixture(t, "opencode_stream_local_capture.jsonl")
		problems, acc := checkOpenCodeCanaryStream(stdout)
		for _, p := range problems {
			t.Errorf("%s", p)
		}
		if acc.Total() == 0 {
			t.Error("the parser accumulated zero tokens over a real capture")
		}
	})

	t.Run("permission reject", func(t *testing.T) {
		stdout := openCodeCanaryFixture(t, "opencode_auto_reject_stream.jsonl")
		problems, acc := checkOpenCodeCanaryStream(stdout)
		for _, p := range problems {
			t.Errorf("%s", p)
		}
		if acc.OpenCode().RejectedToolCalls == 0 {
			t.Error("the parser did not raise its permission-reject count over a real rejected-tool capture")
		}
	})

	// The negative control the verification section asks for: renaming
	// step_finish to step_done must fail, and the failure must name the
	// offending line.
	t.Run("renamed event type fails, naming the line", func(t *testing.T) {
		stdout := openCodeCanaryFixture(t, "opencode_stream_local_capture.jsonl")
		var renamedLine string
		for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
			if strings.Contains(line, `"type":"step_finish"`) {
				renamedLine = strings.Replace(line, `"type":"step_finish"`, `"type":"step_done"`, 1)
				stdout = strings.Replace(stdout, line, renamedLine, 1)
				break
			}
		}
		if renamedLine == "" {
			t.Fatal("the fixture has no step_finish line to rename; pick a different fixture")
		}
		problems, _ := checkOpenCodeCanaryStream(stdout)
		var found bool
		for _, p := range problems {
			if strings.Contains(p.reason, `"step_done"`) && p.line == renamedLine {
				found = true
			}
		}
		if !found {
			t.Errorf("renaming step_finish to step_done did not fail naming the line; problems: %v", problems)
		}
	})
}

// TestOpenCodeCanaryDisableFlags: the four OPENCODE_DISABLE_* variables the
// acceptance criteria name are set to "1" by the #1616 isolation builder
// itself (adapters.OpenCodeIsolationEnv, the single source
// opencode_isolation.go's openCodeDisableFlags feeds) — no separate literal
// list is kept here, so removing one from the builder fails this directly.
func TestOpenCodeCanaryDisableFlags(t *testing.T) {
	env, err := adapters.OpenCodeIsolationEnv(adapters.OpenCodeIsolation{
		Root: "/r", Home: "/h", GOOS: "linux",
		Lookup:           func(string) (string, bool) { return "", false },
		MachineConfigDir: "/m",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"OPENCODE_DISABLE_MODELS_FETCH",
		"OPENCODE_DISABLE_AUTOUPDATE",
		"OPENCODE_DISABLE_SHARE",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS",
	} {
		if env[want] != "1" {
			t.Errorf("the run isolation env sets %s=%q, want \"1\": a spawned opencode would not have this switch on", want, env[want])
		}
	}
}

// TestOpenCodeCanaryLiveStream drives the real, installed opencode through
// Manager.RunStage against the #1618 stub provider's tool-edit-stop script,
// and runs the same contract check TestOpenCodeCanaryStreamContract proved
// against the committed fixture.
func TestOpenCodeCanaryLiveStream(t *testing.T) {
	realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	base := startOpenCodeCanaryStub(t, "tool-edit-stop")
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "http://127.0.0.1:1234/v1", base, 1))
	workspace, _ := openCodeGitWorktree(t, map[string]string{"calc.py": "def add(a, b):\n    return a + b\n"})

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		opts := openCodeStageOptions(openCodeCanaryModel, nil)
		opts.Timeout = 120 * time.Second
		result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("the stage exited %d, want 0:\n%s", result.ExitCode, stderr)
	}
	if len(result.DriftMarkers) > 0 {
		t.Errorf("RunResult carries drift markers from a clean run: %v", result.DriftMarkers)
	}
	problems, acc := checkOpenCodeCanaryStream(result.Stdout)
	for _, p := range problems {
		t.Errorf("%s", p)
	}
	if acc.Total() == 0 {
		t.Errorf("the live run's own stream parsed to zero tokens:\n%s", result.Stdout)
	}
}

// TestOpenCodeCanaryPermission drives the real, installed opencode directly
// (openCodeCanaryDirectRun) against the stub's bash-then-stop script, with
// permission.bash "ask" and no interactive approval, so OpenCode auto-rejects
// the bash call by itself. The child's own raw exit code is 0 even though the
// tool call ended in error (ADR-022 § 9; Manager.RunStage's own
// reclassification of this outcome to 1 is a separate, already-tested
// Nightgauge decision this canary does not re-litigate — opencode_usage.go).
func TestOpenCodeCanaryPermission(t *testing.T) {
	real := realOpenCode(t)
	base := startOpenCodeCanaryStub(t, "bash-then-stop")
	exitCode, stdout, stderr := openCodeCanaryDirectRun(t, real, "stub/stub-model",
		map[string]any{"permission": map[string]any{"bash": "ask"}}, base)

	if !strings.Contains(stderr, "; auto-rejecting") {
		t.Errorf("stderr does not contain the auto-reject notice:\n%s", stderr)
	}
	if exitCode != 0 {
		t.Errorf("the process exited %d, want 0: a permission OpenCode rejects on its own does not fail the CLI's own exit status (ADR-022 § 9)\n%s", exitCode, stderr)
	}
	problems, acc := checkOpenCodeCanaryStream(stdout)
	for _, p := range problems {
		t.Errorf("%s", p)
	}
	if acc.OpenCode().RejectedToolCalls == 0 {
		t.Errorf("the #1624 parser's permission-reject count did not rise over the live run:\n%s", stdout)
	}
	var sawErrorState bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				State struct {
					Status string `json:"status"`
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "tool_use" && ev.Part.State.Status == "error" {
			sawErrorState = true
		}
	}
	if !sawErrorState {
		t.Errorf("no tool_use event carries part.state.status \"error\":\n%s", stdout)
	}
}

// TestOpenCodeCanaryBadModel dispatches openCodeIntegrationModel — a model
// OpenCode's bundled catalog does not list under a hosted provider it knows,
// the same fixture TestOpenCodeIntegrationIsolatesTheRun already proves fails
// before any request leaves the machine — and checks the acceptance
// criteria's two assertions the isolation suite does not make: the run's own
// stdout carries a type:"error" event, on top of the exit code the isolation
// suite already covers.
func TestOpenCodeCanaryBadModel(t *testing.T) {
	realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	result, stderr, err := runOpenCodeIntegrationStage(t)
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, stderr)
	}
	if result.ExitCode != 1 {
		t.Errorf("the stage exited %d, want 1: a model the catalog does not list should exit 1\n%s", result.ExitCode, stderr)
	}
	var sawError bool
	for _, line := range strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n") {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &probe) == nil && probe.Type == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("the bad-model run's stdout carries no type:\"error\" event:\n%s\nstderr:\n%s", result.Stdout, stderr)
	}
}

// TestOpenCodeCanaryStubIsKilledAndConfirmedDead is AC8's own regression test
// for startOpenCodeCanaryStub: it needs no live opencode binary, only the
// stub-provider binary this package now builds and spawns as a real
// subprocess. The stub is started inside a t.Run subtest, so its own
// t.Cleanup — the unconditional guarantee this file's comments claim — has
// already run by the time this test observes the PID afterward: if the
// process were still alive, kill(pid, 0) below would say so.
func TestOpenCodeCanaryStubIsKilledAndConfirmedDead(t *testing.T) {
	var pid int
	t.Run("spawn and use the stub", func(t *testing.T) {
		var base string
		base, pid = startOpenCodeCanaryStubWithPID(t, "tool-edit-stop")
		if base == "" {
			t.Fatal("startOpenCodeCanaryStubWithPID returned no base_url")
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("the stub-provider pid %d is not alive right after spawn: %v", pid, err)
		}
	})
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("stub-provider pid %d is still alive after its own t.Cleanup ran: AC8's kill-and-confirm-dead contract did not hold", pid)
	}
}

// TestOpenCodeCanaryStubProviderCleanupRegistered is the #1639 round 4 low's
// own regression test: opencode_mcp_forge_test.go's single, untagged
// TestMain only removes stubProviderCanaryBinary's os.MkdirTemp directory
// when this file's init has wired canaryStubProviderCleanup, so a change
// that drops that wiring goes back to leaking one
// nightgauge-canary-stub-provider-* directory under $TMPDIR per test-binary
// run without failing anything — this test is what fails instead. It does
// not call the hook itself: canaryStubProviderCleanup removes the directory
// stubProviderCanaryBinary's sync.OnceValues cached bin path still points
// into, which every other test in this same process that spawns the stub
// (TestOpenCodeCanaryStubIsKilledAndConfirmedDead and the live-run tests
// above) still needs. The removal itself is proven by hand, once, outside
// this process: `go test -tags canary ./internal/execution -run
// TestOpenCodeCanaryStubIsKilledAndConfirmedDead` in a fresh process, then
// confirming with `find "$TMPDIR" -maxdepth 1 -iname
// 'nightgauge-canary-stub-provider-*'` that nothing remains.
func TestOpenCodeCanaryStubProviderCleanupRegistered(t *testing.T) {
	if canaryStubProviderCleanup == nil {
		t.Fatal("canaryStubProviderCleanup is nil: this file's init no longer registers it, so TestMain will not remove stubProviderCanaryBinary's os.MkdirTemp directory")
	}
}
