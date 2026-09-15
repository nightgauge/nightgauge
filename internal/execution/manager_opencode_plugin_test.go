package execution

// Manager-side coverage for the Nightgauge OpenCode plugin handshake (#1635):
// a fake `opencode` that never writes the handshake sentinel proves the
// manager's stream loop fails the stage closed — kills the whole process
// group, not merely the top process — the instant it observes the run's
// first step_start event, rather than trusting the plugin ran at all.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// installOpenCodeFakeNoSentinel writes a fake `opencode` that answers
// --version (so the post-run version probes never count as the stage's own
// invocation), records its own pid, emits a single step_start event on
// stdout — the earliest point a real run's plugin would already have
// written its sentinel — and then sleeps far longer than a killed process
// should survive. It writes NO sentinel at all: the handshake the manager
// looks for never appears, exactly as if the plugin had failed to load.
func installOpenCodeFakeNoSentinel(t *testing.T) (dir string, pidFile string) {
	t.Helper()
	dir = t.TempDir()
	pidFile = filepath.Join(dir, "pid.txt")
	script := fmt.Sprintf(`#!/bin/sh
%s
echo $$ > %q
echo '{"type":"step_start","sessionID":"fixture"}'
sleep 30
`, openCodeFakeVersion, pidFile)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, pidFile
}

// waitForPID polls pidFile until it can read an integer or the deadline
// passes, so the test never races the fake's own first write.
func waitForPID(t *testing.T, pidFile string, deadline time.Duration) int {
	t.Helper()
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the fake opencode never wrote its pid to %s", pidFile)
	return 0
}

// processGroupDead reports whether pid and the process group it led are both
// gone (kill -0 fails with ESRCH for each), the same evidence
// TestKillReapsTheWholeProcessGroup already relies on elsewhere in this
// package.
func processGroupDead(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) && errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH)
}

// TestOpenCodePluginHandshakeFailureKillsTheStage is the AC's manager test: a
// fake opencode replaying a fixture with no handshake sentinel fails the
// stage and kill -0 <pgid> fails afterward. Deleting the manager's
// VerifyLoaded call (or the SIGKILL it triggers) turns this red: the fake
// sleeps 30s, so a RunStage that took anywhere near that long, or whose
// stderr carries no adapter_incompatible marker, means the handshake check
// did not run.
func TestOpenCodePluginHandshakeFailureKillsTheStage(t *testing.T) {
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	_, pidFile := installOpenCodeFakeNoSentinel(t)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	// A run identity is required: without one, InstallNightgaugePlugin mints
	// no handshake nonce or sentinel at all (the SDK config-print path, which
	// never spawns opencode), and there would be nothing for the manager to
	// verify.
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

	if elapsed > 15*time.Second {
		t.Fatalf("RunStage took %s; a killed stage must return long before the fake's 30s sleep would", elapsed)
	}

	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if !strings.Contains(combined, "adapter_incompatible") {
		t.Errorf("stderr/result carries no adapter_incompatible marker:\nerr=%v\nstderr=%s", err, combined)
	}
	if !strings.Contains(combined, "[nightgauge-opencode-plugin]") {
		t.Errorf("stderr/result carries no [nightgauge-opencode-plugin] marker:\nstderr=%s", combined)
	}

	pid := waitForPID(t, pidFile, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !processGroupDead(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !processGroupDead(pid) {
		t.Errorf("the fake opencode (pid %d) or its process group is still alive after the handshake failure", pid)
	}
}

// installOpenCodeFakeNoSentinelTimeout is installOpenCodeFakeNoSentinel with
// a caller-chosen sleep, so a test whose failure mode is "nothing kills the
// stage at all" does not have to wait out a fixed 30s to prove it.
func installOpenCodeFakeNoSentinelTimeout(t *testing.T, sleep time.Duration) (dir string, pidFile string) {
	t.Helper()
	dir = t.TempDir()
	pidFile = filepath.Join(dir, "pid.txt")
	script := fmt.Sprintf(`#!/bin/sh
%s
echo $$ > %q
echo '{"type":"step_start","sessionID":"fixture"}'
sleep %d
`, openCodeFakeVersion, pidFile, int(sleep.Seconds()))
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, pidFile
}

// TestOpenCodePluginHandshakeRunsWithoutRunIdentity is the #1635 fix round's
// finding 1/5 regression: the autonomous issue-refine dispatch has a nil
// Runtime by construction (buildRunOptions), so runOpts.RunID (and so
// req.Run.RunID, RunRootRequest's echo of it) is empty. The manager still
// mints an identity for that dispatch's own per-run root — req.ID is always
// an identity (RunRootRequest.ID's contract) — but InstallNightgaugePlugin
// used to be keyed on req.Run.RunID, not req.ID, so this dispatch shape got
// the plugin with no handshake nonce or sentinel at all: a plugin that fails
// to load here goes unnoticed. Red before the fix: with no handshake armed,
// nothing kills the fake's process group, so RunStage waits out the fake's
// sleep and returns no adapter_incompatible marker.
func TestOpenCodePluginHandshakeRunsWithoutRunIdentity(t *testing.T) {
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	_, pidFile := installOpenCodeFakeNoSentinelTimeout(t, 12*time.Second)

	workspace := openCodeWorkspace(t)
	m := NewManager(workspace, adapters.NewOpenCodeAdapter())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	started := time.Now()
	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		// No Runtime: the autonomous issue-refine dispatch shape this
		// finding is about.
		result, err = m.RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})
	elapsed := time.Since(started)

	if elapsed > 6*time.Second {
		t.Fatalf("RunStage took %s; a dispatch with no run identity must still fail its handshake quickly, not wait out the fake's sleep", elapsed)
	}

	combined := stderr
	if result != nil {
		combined += result.Stderr
	}
	if !strings.Contains(combined, "adapter_incompatible") {
		t.Errorf("stderr/result carries no adapter_incompatible marker for a dispatch with no run identity:\nerr=%v\nstderr=%s", err, combined)
	}

	pid := waitForPID(t, pidFile, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !processGroupDead(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !processGroupDead(pid) {
		t.Errorf("the fake opencode (pid %d) or its process group is still alive after the handshake failure", pid)
	}
}
