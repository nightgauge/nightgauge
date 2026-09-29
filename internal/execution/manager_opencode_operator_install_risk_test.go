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
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// openCodeMachineConfigInherited is openCodeMachineConfig with
// opencode.inherit_user_config on: the only condition, since #1787, under
// which a dispatch's HOME is left untouched and so can still read the
// operator's real $HOME/.opencode or OPENCODE_CONFIG_DIR — the shape the
// operator-install-risk watchdog exists to bound.
const openCodeMachineConfigInherited = openCodeMachineConfig + "  inherit_user_config: true\n"

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

// operatorInstallWatchdogObservation is one report through
// operatorInstallWatchdogObserver.
type operatorInstallWatchdogObservation struct {
	armed bool
	bound time.Duration
	end   operatorInstallWatchdogEnd
}

// observeOperatorInstallWatchdog records what the operator-install-risk
// watchdog did during the test's RunStage, and calls onEnd (when non-nil)
// from the watchdog's own goroutine the moment it ends (#2269). The tests
// here assert that mechanism, not RunStage's wall time: spawning the fake,
// git, provisioning and teardown all sit inside RunStage, and on a loaded
// machine they alone outgrow any ceiling tight enough to tell "the watchdog
// waited" from "the machine is slow". The returned function yields the
// single report RunStage made; it is only valid once RunStage has returned,
// which it does only after the watchdog has reported.
func observeOperatorInstallWatchdog(t *testing.T, onEnd func(operatorInstallWatchdogEnd)) func() operatorInstallWatchdogObservation {
	t.Helper()
	var mu sync.Mutex
	var seen []operatorInstallWatchdogObservation
	prev := operatorInstallWatchdogObserver
	operatorInstallWatchdogObserver = func(armed bool, bound time.Duration, end operatorInstallWatchdogEnd) {
		mu.Lock()
		seen = append(seen, operatorInstallWatchdogObservation{armed: armed, bound: bound, end: end})
		mu.Unlock()
		if onEnd != nil {
			onEnd(end)
		}
	}
	t.Cleanup(func() { operatorInstallWatchdogObserver = prev })
	return func() operatorInstallWatchdogObservation {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 1 {
			t.Fatalf("RunStage reported the operator-install-risk watchdog %d times, want exactly once: %+v", len(seen), seen)
		}
		return seen[0]
	}
}

// TestOpenCodeOperatorInstallRiskBoundedAndClassified is the round 6 AC's
// manager test: a fake opencode that produces no output at all, dispatched
// with a pre-existing, unsatisfied $HOME/.opencode (the shape the official
// install script leaves before any node_modules ever arrives — a real
// operator-install risk, never seeded or merged into), fails the stage well
// under the shortened watchdog bound, names the risk directory and #1787,
// classifies as adapter_incompatible, and reaps the whole process group.
// Deleting the watchdog (or the killProcessTreeUntilGone call it makes)
// turns this red: the watchdog must report that its own 500ms bound fired,
// the stderr must carry the adapter_incompatible marker, and the fake's
// process group must be gone although the fake sleeps 30s.
//
// #1787 gave a non-inheriting run its own per-run HOME, so a dispatch never
// reads the operator's real $HOME/.opencode at all unless
// opencode.inherit_user_config is on — the only condition under which this
// risk can still be real, so this fixture opts in to keep exercising it.
func TestOpenCodeOperatorInstallRiskBoundedAndClassified(t *testing.T) {
	home := isolateOpenCodeHome(t)
	writeOpenCodeMachineConfig(t, openCodeMachineConfigInherited)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCode := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCode, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, pidFile := installOpenCodeFakeSilent(t, 30*time.Second)
	withShortOperatorInstallWaitBound(t, 500*time.Millisecond)

	watchdog := observeOperatorInstallWatchdog(t, nil)

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

	var result *adapters.RunResult
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime))
	})

	// The watchdog's own report, not RunStage's wall time, separates "the
	// watchdog ended it" from "the fake's 30s sleep or the 30s stage timeout
	// ended it" (#2269): wall time includes provisioning and teardown, which
	// grow with host load.
	if got := watchdog(); !got.armed || got.bound != 500*time.Millisecond || got.end != operatorInstallWatchdogEndTimedOut {
		t.Fatalf("watchdog armed=%t bound=%s end=%s; want armed with the 500ms bound and ended by it timing out", got.armed, got.bound, got.end)
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
//
// opencode.inherit_user_config is on here for the same reason as
// TestOpenCodeOperatorInstallRiskBoundedAndClassified: since #1787, a
// non-inheriting run's own per-run HOME keeps $HOME/.opencode out of reach.
func TestOpenCodeOperatorInstallRiskBoundByRemainingStageContext(t *testing.T) {
	home := isolateOpenCodeHome(t)
	writeOpenCodeMachineConfig(t, openCodeMachineConfigInherited)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	if err := os.MkdirAll(filepath.Join(home, ".opencode", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, pidFile := installOpenCodeFakeSilent(t, 120*time.Second)
	// A watchdog bound far longer than the stage timeout below: without the
	// execCtx-deadline cap, the watchdog would arm with the full 60s.
	withShortOperatorInstallWaitBound(t, 60*time.Second)
	watchdog := observeOperatorInstallWatchdog(t, nil)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())
	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &state.RuntimeState{RunID: runID}

	opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime)
	// The stage deadline starts before the pre-dispatch probes and
	// provisioning, so it has to outlast them on a loaded machine, or the
	// fake never spawns and the watchdog never arms. The 3s this replaced
	// left no room for that (#2269). 10s still sits far below the 60s bound
	// the capped bound below is compared against.
	opts.Timeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	var result *adapters.RunResult
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, opts)
	})

	// The mechanism, not the wall clock (#2269): the watchdog armed with a
	// bound no longer than the stage's own timeout, never the 60s it was
	// configured with. Either the watchdog's capped bound or the stage
	// deadline's own kill may end the stall first (#1954); both classify.
	got := watchdog()
	if !got.armed || got.bound > opts.Timeout {
		t.Fatalf("watchdog armed=%t bound=%s; want armed with a bound capped at the %s stage timeout, not its configured 60s", got.armed, got.bound, opts.Timeout)
	}
	if got.end != operatorInstallWatchdogEndTimedOut && got.end != operatorInstallWatchdogEndStopped {
		t.Errorf("watchdog ended by %s; a silent fake can only end at the capped bound or the stage deadline", got.end)
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

func TestOperatorInstallWaitBound(t *testing.T) {
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		configured  time.Duration
		deadline    time.Time
		hasDeadline bool
		want        time.Duration
	}{
		{name: "no deadline", configured: 10 * time.Second, want: 10 * time.Second},
		{
			name:        "later deadline",
			configured:  10 * time.Second,
			deadline:    now.Add(time.Minute),
			hasDeadline: true,
			want:        10 * time.Second,
		},
		{
			name:        "earlier deadline",
			configured:  10 * time.Second,
			deadline:    now.Add(3 * time.Second),
			hasDeadline: true,
			want:        3 * time.Second,
		},
		{
			name:        "expired deadline",
			configured:  10 * time.Second,
			deadline:    now.Add(-time.Second),
			hasDeadline: true,
			want:        0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := operatorInstallWaitBound(tc.configured, tc.deadline, tc.hasDeadline, now)
			if got != tc.want {
				t.Fatalf("operatorInstallWaitBound() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestOperatorInstallStallClassified covers the classification rule and
// every guard deterministically (#1954): the watchdog's own timeout, and the
// race it can lose to the stage deadline's own kill, both classify; output,
// a handshake failure, an operator Stop, a caller's cancel, a satisfied
// directory and an unarmed stage never do.
func TestOperatorInstallStallClassified(t *testing.T) {
	unsatisfied := func() bool { return false }
	satisfied := func() bool { return true }
	// deadlineWon is the #1954 shape: the stage deadline killed a silent
	// child before the watchdog recorded its own timeout.
	deadlineWon := operatorInstallStallEvidence{
		armed:     true,
		execErr:   context.DeadlineExceeded,
		satisfied: unsatisfied,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*operatorInstallStallEvidence)
		want   bool
	}{
		{name: "stage deadline won the race", mutate: func(*operatorInstallStallEvidence) {}, want: true},
		{name: "wrapped deadline error", mutate: func(e *operatorInstallStallEvidence) {
			e.execErr = fmt.Errorf("stage: %w", context.DeadlineExceeded)
		}, want: true},
		{name: "watchdog timed out", mutate: func(e *operatorInstallStallEvidence) {
			e.watchdogTimedOut = true
			e.execErr = nil
		}, want: true},
		{name: "not armed", mutate: func(e *operatorInstallStallEvidence) {
			e.armed = false
			e.watchdogTimedOut = true
		}, want: false},
		{name: "handshake failure wins over watchdog", mutate: func(e *operatorInstallStallEvidence) {
			e.handshakeFailed = true
			e.watchdogTimedOut = true
		}, want: false},
		{name: "handshake failure wins over deadline", mutate: func(e *operatorInstallStallEvidence) {
			e.handshakeFailed = true
		}, want: false},
		{name: "operator stop with watchdog", mutate: func(e *operatorInstallStallEvidence) {
			e.stopRequested = true
			e.watchdogTimedOut = true
		}, want: false},
		{name: "operator stop at deadline", mutate: func(e *operatorInstallStallEvidence) {
			e.stopRequested = true
		}, want: false},
		{name: "child produced output", mutate: func(e *operatorInstallStallEvidence) {
			e.sawOutput = true
		}, want: false},
		{name: "fast failure, context still live", mutate: func(e *operatorInstallStallEvidence) {
			e.execErr = nil
		}, want: false},
		{name: "caller cancelled", mutate: func(e *operatorInstallStallEvidence) {
			e.execErr = context.Canceled
		}, want: false},
		{name: "directory became satisfied", mutate: func(e *operatorInstallStallEvidence) {
			e.satisfied = satisfied
		}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := deadlineWon
			tc.mutate(&e)
			if got := operatorInstallStallClassified(e); got != tc.want {
				t.Fatalf("operatorInstallStallClassified(%+v) = %t, want %t", e, got, tc.want)
			}
		})
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
//
// opencode.inherit_user_config is on so the watchdog really arms: without
// it, #1787's per-run HOME keeps $HOME/.opencode out of reach and this test
// would pass without the watchdog ever running (#2269).
func TestOpenCodeOperatorInstallRiskDoesNotMisclassifyAFastFailure(t *testing.T) {
	home := isolateOpenCodeHome(t)
	writeOpenCodeMachineConfig(t, openCodeMachineConfigInherited)
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
	// A bound longer than the stage timeout, so it is capped at the stage
	// deadline: the healthy path never comes near it however loaded the
	// machine is, and a watchdog that failed to stand down could only end by
	// timing out, which the observation below reports. A RunStage wall-time
	// ceiling cannot make that distinction: spawning the fake and tearing
	// its group down alone has taken 3s to 9s under load (#1836, #2269).
	withShortOperatorInstallWaitBound(t, 60*time.Second)
	watchdog := observeOperatorInstallWatchdog(t, nil)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})

	// The watchdog armed and stood down without waiting out any part of its
	// bound: on the fake's first line, or when RunStage stopped it after the
	// fake exited. Both are closed within moments of each other here, so
	// which one its select saw first is the scheduler's choice, not a
	// property of the watchdog.
	got := watchdog()
	if !got.armed {
		t.Fatal("the operator-install-risk watchdog never armed, so this test proved nothing about it")
	}
	if got.end != operatorInstallWatchdogEndOutput && got.end != operatorInstallWatchdogEndStopped {
		t.Fatalf("watchdog ended by %s; a process that exits immediately must stand it down, not wait out its bound", got.end)
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
// — then stays silent — no step_start, nothing at all — until release
// exists (or about 20s pass), before finally printing one harmless,
// non-NDJSON line and exiting 0. Waiting on release rather than a fixed
// sleep lets a test hold the child silent exactly until the watchdog has
// stood down, whatever the machine's load (#2269). Never
// emitting a step_start/tool_use-shaped line keeps the PRE-EXISTING plugin
// handshake check out of this test's own assertions: VerifyNotLate is a
// no-op when no tool call was ever observed (firstToolUse.IsZero()), so any
// classified failure recorded here can only be the operator-install-risk
// watchdog's own.
func installOpenCodeFakeSatisfiesThenSilent(t *testing.T, operatorDir, release string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
%s
mkdir -p %q
printf '{"dependencies":{"@opencode-ai/plugin":"%s"}}' > %q
printf '{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"%s"}}}}' > %q
i=0
while [ ! -e %q ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done
echo not-json-output
`,
		openCodeFakeVersion,
		filepath.Join(operatorDir, "node_modules"),
		opencodeplugin.DepsVersion, filepath.Join(operatorDir, "package.json"),
		opencodeplugin.DepsVersion, filepath.Join(operatorDir, "package-lock.json"),
		release,
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
// silent until the watchdog has stood down. Round 7's code stands down only
// on first output, so its watchdog could never report standing down on
// satisfaction, and the silent fake would only speak after its own ~20s
// wait — this test is red against it. Round 8 also polls, read-only,
// whether the directory has become satisfied, and stands down the instant
// it has, so the stage here must run to completion, never killed by this
// watchdog.
//
// The fake is held silent until the watchdog reports, not for a fixed
// sleep against a short bound: on a loaded machine the fake's own file
// writes could outlast a 500ms bound, and RunStage's wall time says nothing
// about which of the watchdog's exits fired (#2269). The bound is capped at
// the stage deadline, so only an unsatisfied, silent directory could ever
// reach it.
//
// opencode.inherit_user_config is on so the watchdog really arms: without
// it, #1787's per-run HOME keeps $HOME/.opencode out of reach.
func TestOpenCodeOperatorInstallRiskStandsDownWhenDirectoryBecomesSatisfied(t *testing.T) {
	home := isolateOpenCodeHome(t)
	writeOpenCodeMachineConfig(t, openCodeMachineConfigInherited)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCode := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(filepath.Join(operatorOpenCode, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(t.TempDir(), "release")
	installOpenCodeFakeSatisfiesThenSilent(t, operatorOpenCode, release)
	withShortOperatorInstallWaitBound(t, 60*time.Second)
	withShortOperatorInstallPollInterval(t, 100*time.Millisecond)
	watchdog := observeOperatorInstallWatchdog(t, func(operatorInstallWatchdogEnd) {
		// Whatever ended the watchdog, let the fake finish; the assertion
		// below says whether it was the satisfaction poll.
		_ = os.WriteFile(release, nil, 0o600)
	})

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})

	if got := watchdog(); !got.armed || got.end != operatorInstallWatchdogEndSatisfied {
		t.Fatalf("watchdog armed=%t end=%s; want armed for the unsatisfied directory and stood down by it becoming satisfied", got.armed, got.end)
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
// 6's exemption entirely), so its watchdog reports arming — this test is
// red against it.
//
// opencode.inherit_user_config is on, so it is the directory's being
// satisfied that keeps the watchdog unarmed: without the setting, #1787's
// per-run HOME keeps $HOME/.opencode out of reach whatever it holds, and
// this test would pass however the satisfied check behaved (#2269).
func TestOpenCodeOperatorInstallRiskNeverArmsForAnAlreadySatisfiedDirectory(t *testing.T) {
	home := isolateOpenCodeHome(t)
	writeOpenCodeMachineConfig(t, openCodeMachineConfigInherited)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	operatorOpenCode := filepath.Join(home, ".opencode")
	// The full four-file archive (a superset of what OperatorInstallSatisfied
	// itself reads), written BEFORE the dispatch, standing in for "the
	// operator already ran opencode themselves" — never written by
	// production code for an operator-owned directory (opencode_plugin_deps.go's
	// operatorInstallRisk doc comment).
	//
	// With the inherit setting the run also points OPENCODE_CONFIG_DIR at the
	// operator's own XDG OpenCode directory, which operatorInstallRisk checks
	// the same way, so an operator who already ran opencode has both.
	for _, dir := range []string{operatorOpenCode, filepath.Join(home, ".config", "opencode")} {
		if err := opencodeplugin.WriteDependencies(dir); err != nil {
			t.Fatal(err)
		}
	}
	installOpenCodeFakeSlowButHarmless(t, time.Second)
	withShortOperatorInstallWaitBound(t, 500*time.Millisecond)
	withShortOperatorInstallPollInterval(t, 100*time.Millisecond)
	watchdog := observeOperatorInstallWatchdog(t, nil)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})

	// The watchdog's own report, not RunStage's wall time (#2269): a
	// satisfied directory must never arm it.
	if got := watchdog(); got.armed {
		t.Fatalf("watchdog armed (bound %s, ended by %s) for an already-satisfied directory; it must never arm at all", got.bound, got.end)
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
