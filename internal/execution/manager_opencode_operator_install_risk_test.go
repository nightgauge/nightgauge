package execution

// Manager-side coverage for the operator-install-risk watchdog (#1635/A11
// round 6, ADR-022 amendment 2026-09-15, narrowed AC1): Nightgauge never
// seeds or merges into an operator-owned OpenCode config directory, so
// offline (or against an unreachable registry) OpenCode's own install into
// one can leave the CLI silent forever. A fake `opencode` that emits
// literally nothing (not even the step_start the plugin handshake reacts
// to) and sleeps far longer than the shortened watchdog bound stands in for
// that install — an injected/stubbed install, never a real registry request
// — and proves the wait is bounded independently of the stage's own
// timeout, and the resulting failure is classified adapter_incompatible
// rather than a silent hang.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// installOpenCodeFakeSilent writes a fake `opencode` that answers --version
// (so the post-run version probes never count as the stage's own
// invocation), records its own pid, and then produces NOTHING ELSE — no
// stdout, no stderr, not even a step_start line — before sleeping far
// longer than the shortened watchdog bound any test here sets. This is the
// shape OpenCode's own real npm install takes when it blocks the CLI before
// it has printed a single byte: the stand-in for "an injected or stubbed
// install" the round 6 decision requires, with no registry ever touched.
func installOpenCodeFakeSilent(t *testing.T, sleep time.Duration) (dir string, pidFile string) {
	t.Helper()
	dir = t.TempDir()
	pidFile = filepath.Join(dir, "pid.txt")
	script := fmt.Sprintf(`#!/bin/sh
%s
echo $$ > %q
sleep %d
`, openCodeFakeVersion, pidFile, int(sleep.Seconds()))
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, pidFile
}

// withShortOperatorInstallWaitBound overrides openCodeOperatorInstallWaitBound
// for the duration of a test, so a test proves the bound without waiting out
// the real, production-sized one.
func withShortOperatorInstallWaitBound(t *testing.T, bound time.Duration) {
	t.Helper()
	prev := openCodeOperatorInstallWaitBound
	openCodeOperatorInstallWaitBound = bound
	t.Cleanup(func() { openCodeOperatorInstallWaitBound = prev })
}

// withShortOperatorInstallPollInterval overrides
// openCodeOperatorInstallPollInterval for the duration of a test, so a test
// proves the stand-down-on-satisfaction poll fires promptly rather than
// waiting out the production-sized interval (#1635/A11 round 8).
func withShortOperatorInstallPollInterval(t *testing.T, interval time.Duration) {
	t.Helper()
	prev := openCodeOperatorInstallPollInterval
	openCodeOperatorInstallPollInterval = interval
	t.Cleanup(func() { openCodeOperatorInstallPollInterval = prev })
}

// TestOpenCodeOperatorInstallRiskBoundedAndClassified is the round 6 AC's
// manager test: a fake opencode that produces no output at all, dispatched
// with a pre-existing, unsatisfied $HOME/.opencode (the shape the official
// install script leaves before any node_modules ever arrives — a real
// operator-install risk, never seeded or merged into), fails the stage well
// under the shortened watchdog bound, names the risk directory and #1787,
// classifies as adapter_incompatible, and reaps the whole process group.
// Deleting the watchdog (or the killProcessTreeUntilGone call it makes)
// turns this red: the fake sleeps 30s longer than the bound, so a RunStage
// that took anywhere near that long, or whose stderr carries no
// adapter_incompatible marker, means the watchdog did not run.
func TestOpenCodeOperatorInstallRiskBoundedAndClassified(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCode := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCode, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, pidFile := installOpenCodeFakeSilent(t, 30*time.Second)
	withShortOperatorInstallWaitBound(t, 500*time.Millisecond)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	// A run identity, so InstallNightgaugePlugin arms the plugin handshake
	// too: this proves the operator-install-risk watchdog is what ends the
	// stage, not the (never-reached) handshake step_start check.
	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &state.RuntimeState{RunID: runID}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	started := time.Now()
	var result *adapters.RunResult
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime))
	})
	elapsed := time.Since(started)

	if elapsed > 5*time.Second {
		t.Fatalf("RunStage took %s; the 500ms watchdog bound must kill the stage long before the fake's 30s sleep or the stage's own 30s timeout", elapsed)
	}

	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if !strings.Contains(combined, "adapter_incompatible") {
		t.Errorf("stderr/result carries no adapter_incompatible marker:\nerr=%v\nstderr=%s", err, combined)
	}
	if !strings.Contains(combined, operatorOpenCode) {
		t.Errorf("stderr/result does not name the operator-owned directory %s:\nstderr=%s", operatorOpenCode, combined)
	}
	if !strings.Contains(combined, "#1787") {
		t.Errorf("stderr/result does not cite the per-run-HOME follow-up (#1787):\nstderr=%s", combined)
	}
	if result != nil && result.ExitCode == 0 {
		t.Error("a classified operator-install-risk failure must force ExitCode nonzero")
	}

	pid := waitForPID(t, pidFile, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !processGroupDead(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !processGroupDead(pid) {
		t.Errorf("the fake opencode (pid %d) or its process group is still alive after the operator-install-risk timeout", pid)
	}
}

// TestOpenCodeOperatorInstallRiskBoundByRemainingStageContext: the watchdog
// must never wait LONGER than the stage's own remaining context deadline,
// even when openCodeOperatorInstallWaitBound itself is longer — "bounded by
// the stage context" (#1635/A11 round 6 decision), not only by its own
// constant. A short stage timeout with a long watchdog bound must still end
// promptly.
func TestOpenCodeOperatorInstallRiskBoundByRemainingStageContext(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	if err := os.MkdirAll(filepath.Join(home, ".opencode", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, pidFile := installOpenCodeFakeSilent(t, 30*time.Second)
	// A watchdog bound far longer than the stage timeout below: without the
	// execCtx-deadline cap, the watchdog would still be waiting when this
	// test's own assertions run.
	withShortOperatorInstallWaitBound(t, 10*time.Second)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())
	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &state.RuntimeState{RunID: runID}

	opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime)
	opts.Timeout = 1 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	started := time.Now()
	var result *adapters.RunResult
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, opts)
	})
	elapsed := time.Since(started)

	if elapsed > 5*time.Second {
		t.Fatalf("RunStage took %s; a 1s stage timeout must cap the watchdog's 10s bound, not the other way round", elapsed)
	}

	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if !strings.Contains(combined, "adapter_incompatible") {
		t.Errorf("stderr/result carries no adapter_incompatible marker:\nerr=%v\nstderr=%s", err, combined)
	}

	pid := waitForPID(t, pidFile, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !processGroupDead(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !processGroupDead(pid) {
		t.Errorf("the fake opencode (pid %d) or its process group is still alive after the bounded timeout", pid)
	}
}

// TestOpenCodeOperatorInstallRiskDoesNotMisclassifyAFastFailure: a stage
// that fails quickly for an unrelated reason (here, the fake opencode exits
// nonzero immediately after printing a line) must not be misclassified as
// an operator-install-risk timeout just because operatorInstallRisk was
// set — the watchdog must stand down the instant real output arrives. The
// fake never writes the plugin handshake's own sentinel (it is not a real
// opencode loading a real plugin), so the PRE-EXISTING handshake check
// (unrelated to this watchdog) fails it "adapter_incompatible" regardless —
// this test's own assertion is that the watchdog's OWN, more specific
// marker text (naming the operator directory / #1787) is absent, not that
// the stage passes.
func TestOpenCodeOperatorInstallRiskDoesNotMisclassifyAFastFailure(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	if err := os.MkdirAll(filepath.Join(home, ".opencode", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// The real error line is written to stderr, and step_start to stdout,
	// BEFORE step_start — not after — so those bytes are already sitting in
	// the (kernel-buffered) pipe before the plugin handshake check ever
	// observes step_start and kills the process group. Order here only
	// controls whether the bytes were written to the pipe at all before the
	// process dies, not whether the manager's reader goroutine gets to read
	// them — a killed writer does not erase what it already wrote. Emitting
	// them the other way around (as an earlier revision of this fixture
	// did) races the manager's own step_start-triggered kill, which is
	// deliberately fast (TestOpenCodeHandshakeKillPrecedesVersionProbe),
	// against the child's next scheduler slice, and was observed flaky
	// under CI's heavier concurrent load.
	script := fmt.Sprintf(`#!/bin/sh
%s
echo "a real, unrelated failure" >&2
echo '{"type":"step_start","sessionID":"fixture"}'
exit 7
`, openCodeFakeVersion)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	withShortOperatorInstallWaitBound(t, 3*time.Second)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})
	elapsed := time.Since(started)

	if elapsed > 2*time.Second {
		t.Fatalf("RunStage took %s; a process that exits immediately must not wait out any part of the watchdog bound", elapsed)
	}
	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if strings.Contains(combined, "may be waiting on an unreachable registry") || strings.Contains(combined, "#1787") {
		t.Errorf("a fast, unrelated failure was misclassified with the operator-install-risk watchdog's own marker text:\nerr=%v\nstderr=%s", err, combined)
	}
	if !strings.Contains(combined, "a real, unrelated failure") {
		t.Errorf("the fake's own stderr did not reach the result:\nstderr=%s", combined)
	}
}

// installOpenCodeFakeSatisfiesThenSilent writes a fake `opencode` that
// answers --version, then WRITES the set opencodeplugin.OperatorInstallSatisfied
// actually reads into operatorDir — node_modules/, a package.json naming
// @opencode-ai/plugin as a dependency, and a package-lock.json whose root
// ("") package entry lists that same name under "dependencies" (the real npm
// lockfile shape depsdata/opencode-ai-plugin-1.18.30.tar.gz's own
// package-lock.json takes — depsdata/README.md), standing in for OpenCode's
// own real install completing in the background — no registry ever touched
// — then stays silent — no step_start, nothing at all — for sleep,
// comfortably longer than the shortened watchdog bound any test here sets,
// before finally printing one harmless, non-NDJSON line and exiting 0. Never
// emitting a step_start/tool_use-shaped line keeps the PRE-EXISTING plugin
// handshake check out of this test's own assertions: VerifyNotLate is a
// no-op when no tool call was ever observed (firstToolUse.IsZero()), so any
// classified failure recorded here can only be the operator-install-risk
// watchdog's own.
func installOpenCodeFakeSatisfiesThenSilent(t *testing.T, operatorDir string, sleep time.Duration) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
%s
mkdir -p %q
printf '{"dependencies":{"@opencode-ai/plugin":"%s"}}' > %q
printf '{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"%s"}}}}' > %q
sleep %d
echo not-json-output
`,
		openCodeFakeVersion,
		filepath.Join(operatorDir, "node_modules"),
		opencodeplugin.DepsVersion, filepath.Join(operatorDir, "package.json"),
		opencodeplugin.DepsVersion, filepath.Join(operatorDir, "package-lock.json"),
		int(sleep.Seconds()),
	)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// installOpenCodeFakeSlowButHarmless is installOpenCodeFakeSatisfiesThenSilent
// with no dependency-file writes of its own: for a directory already
// satisfied BEFORE this fake ever spawns, standing in for a slow first model
// token (a local model prefilling a large prompt) rather than an install.
func installOpenCodeFakeSlowButHarmless(t *testing.T, sleep time.Duration) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
%s
sleep %d
echo not-json-output
`, openCodeFakeVersion, int(sleep.Seconds()))
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestOpenCodeOperatorInstallRiskStandsDownWhenDirectoryBecomesSatisfied
// (#1635/A11 round 8, ADR-022 amendment 2026-09-15, correcting round 7): an
// operator directory that exists but does NOT satisfy the pin at spawn time
// arms the watchdog, exactly as round 6/7 already did. What round 8 changes
// is what makes it stand down: the fake here writes the set
// opencodeplugin.OperatorInstallSatisfied actually reads INTO the directory
// (standing in for OpenCode's own real install completing
// in the background — never a registry request) partway through, then stays
// silent for far longer than the shortened bound before ever printing a
// byte. Round 7's code stands down only on first output, so it would kill
// this stage well before the fake's sleep ends — this test is red against
// it. Round 8 also polls, read-only, whether the directory has become
// satisfied, and stands down the instant it has, so the stage here must run
// to completion, never killed by this watchdog.
func TestOpenCodeOperatorInstallRiskStandsDownWhenDirectoryBecomesSatisfied(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCode := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCode, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	sleep := 2 * time.Second
	installOpenCodeFakeSatisfiesThenSilent(t, operatorOpenCode, sleep)
	withShortOperatorInstallWaitBound(t, 500*time.Millisecond)
	withShortOperatorInstallPollInterval(t, 100*time.Millisecond)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	started := time.Now()
	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})
	elapsed := time.Since(started)

	if elapsed < sleep {
		t.Errorf("RunStage took only %s, less than the fake's own %s sleep: it was killed before the directory became satisfied", elapsed, sleep)
	}
	if elapsed > sleep+5*time.Second {
		t.Fatalf("RunStage took %s; well past the fake's own %s sleep plus generous headroom", elapsed, sleep)
	}
	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if strings.Contains(combined, "may be waiting on an unreachable registry") || strings.Contains(combined, "#1787") {
		t.Errorf("the operator-install-risk watchdog fired even though the directory became satisfied mid-run:\nerr=%v\nstderr=%s", err, combined)
	}
	if result != nil && result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0: the fake exits cleanly and must not be killed", result.ExitCode)
	}
}

// TestOpenCodeOperatorInstallRiskNeverArmsForAnAlreadySatisfiedDirectory
// (#1635/A11 round 8, ADR-022 amendment 2026-09-15, correcting round 7): an
// operator directory that ALREADY satisfies opencode's own install check
// BEFORE this dispatch ever spawns opencode never arms the watchdog at
// all, so a silent stretch far longer than the shortened bound (standing in
// for a slow first model token, e.g. a local model prefilling a large
// prompt) is never mistaken for a hung install and never killed. Round 7's
// code armed the watchdog for a satisfied directory too (it dropped round
// 6's exemption entirely) and stood down only on first output, so it would
// kill this stage well before the fake's sleep ends — this test is red
// against it.
func TestOpenCodeOperatorInstallRiskNeverArmsForAnAlreadySatisfiedDirectory(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCode := filepath.Join(home, ".opencode")
	// The full four-file archive (a superset of what OperatorInstallSatisfied
	// itself reads), written BEFORE the dispatch, standing in for "the
	// operator already ran opencode themselves" — never written by
	// production code for an operator-owned directory (opencode_plugin_deps.go's
	// operatorInstallRisk doc comment).
	if err := opencodeplugin.WriteDependencies(operatorOpenCode); err != nil {
		t.Fatal(err)
	}
	sleep := 2 * time.Second
	installOpenCodeFakeSlowButHarmless(t, sleep)
	withShortOperatorInstallWaitBound(t, 500*time.Millisecond)
	withShortOperatorInstallPollInterval(t, 100*time.Millisecond)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	started := time.Now()
	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})
	elapsed := time.Since(started)

	if elapsed < sleep {
		t.Errorf("RunStage took only %s, less than the fake's own %s sleep: a satisfied directory must never arm the watchdog at all", elapsed, sleep)
	}
	if elapsed > sleep+5*time.Second {
		t.Fatalf("RunStage took %s; well past the fake's own %s sleep plus generous headroom", elapsed, sleep)
	}
	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if strings.Contains(combined, "may be waiting on an unreachable registry") || strings.Contains(combined, "#1787") {
		t.Errorf("the operator-install-risk watchdog fired for an already-satisfied directory:\nerr=%v\nstderr=%s", err, combined)
	}
	if result != nil && result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0: the fake exits cleanly and must not be killed", result.ExitCode)
	}
}
