package execution

// Fix round for #1635/A11: three gaps in the Nightgauge OpenCode plugin
// handshake (opencodeplugin, manager.go), none needing the real opencode
// binary — a fake `opencode` first on PATH stands in, scripted to reproduce
// the exact event ordering observed on 1.18.30 (ADR-022).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
)

// installHandshakeFake writes a fake `opencode` first on PATH running body,
// a shell fragment invoked for the stage's own `run`. versionDelay is the
// pause before `opencode --version` (the manager's own post-failure probe,
// openCodePluginHandshakeMarker) answers — 0 for instant, as the real binary
// normally is.
func installHandshakeFake(t *testing.T, versionDelay time.Duration, body string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then
  sleep %.3f
  echo 1.18.30
  exit 0
fi
%s
cat > /dev/null
exit 0
`, versionDelay.Seconds(), body)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestOpenCodeHandshakeKillPrecedesVersionProbe (#1635 fix round finding 3):
// when VerifyLoaded fails at step_start, the stage's process group must be
// signalled before the manager spends any time naming the binary in the
// failure marker. The fake never writes a valid sentinel (simulating the
// plugin failing to load), so VerifyLoaded fails the instant step_start is
// read; a slow `--version` stands in for the observed ~0.3s a real probe
// took. The fake's own shell process sleeps 300ms after step_start and then
// writes a marker file — representing a tool call already running — so the
// marker's presence tells "the kill arrived after that window" apart from
// "before it": on unfixed code, the marker exists (the probe delays the
// kill well past 300ms); fixed, it must not.
func TestOpenCodeHandshakeKillPrecedesVersionProbe(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	workspace := openCodeWorkspace(t)
	const runID = "01890a5d-ac96-774b-bcce-b30209a81701"
	marker := filepath.Join(t.TempDir(), "ran-after-kill-window.marker")

	installHandshakeFake(t, 800*time.Millisecond, fmt.Sprintf(`
echo '{"type":"step_start","sessionID":"s1"}'
sleep 0.3
echo ran > %q
`, marker))

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 1612, "item-1612", runID)
	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime)
		_, _ = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	_ = home

	if _, err := os.Stat(marker); err == nil {
		t.Error("the stage's shell kept running (and wrote the post-window marker) past its 300ms mark: the SIGKILL was delayed behind the --version probe")
	}
}

// TestOpenCodeLateHandshakeFailsTheStage (#1635 fix round finding 4a): a
// sentinel rewritten AFTER the run's own first tool_use was observed must
// fail the stage (VerifyNotLate, checked at exit). The fake writes a valid
// sentinel before step_start (VerifyLoaded passes), emits tool_use, then
// rewrites the sentinel — simulating a plugin that reloaded, or loaded a
// second time, after a tool had already run.
func TestOpenCodeLateHandshakeFailsTheStage(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	workspace := openCodeWorkspace(t)
	const runID = "01890a5d-ac96-774b-bcce-b30209a81702"

	installHandshakeFake(t, 0, `
sentinel_body="{\"nonce\":\"$NIGHTGAUGE_OPENCODE_PLUGIN_NONCE\",\"plugin_version\":\"1\",\"hooks\":[]}"
printf '%s' "$sentinel_body" > "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
echo '{"type":"step_start","sessionID":"s1"}'
echo '{"type":"tool_use","sessionID":"s1","part":{"tool":"bash","state":{"status":"completed"}}}'
sleep 0.3
printf '%s' "$sentinel_body" > "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
`)

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 1612, "item-1612", runID)
	var result *adapters.RunResult
	var err error
	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime)
		result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	_ = home
	// keepStderr appends the handshake failure marker to the stage's OWN
	// captured stderr (result.Stderr), not the manager process's live
	// os.Stderr — that's the RunResult a caller (and the pipeline) actually
	// reads.
	if !strings.Contains(result.Stderr, "[nightgauge-opencode-plugin]") {
		t.Fatalf("result.Stderr does not carry the handshake failure marker, so this case is not exercising the late-sentinel path:\n%s", result.Stderr)
	}
	if result.ExitCode == 0 {
		t.Error("a late-rewritten handshake sentinel must fail the stage (ExitCode != 0), but the CLI's own clean exit was left as the stage's result")
	}
}

// TestOpenCodeHandshakeUsesToolStartTime (#1635 fix round finding 4b): the
// late-sentinel check must compare the sentinel's mtime against the tool's
// OWN reported start time (part.state.time.start, epoch ms) rather than the
// moment the manager's reader observed the tool_use line. The fake writes
// the sentinel only after a short pause (so its mtime is "now"), then emits
// a tool_use claiming its state.time.start was an hour ago — well before the
// sentinel ever existed. A check keyed on state.time.start must fail this
// stage; one keyed on wall-clock observation time (after the sentinel write)
// would wrongly pass it.
func TestOpenCodeHandshakeUsesToolStartTime(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	workspace := openCodeWorkspace(t)
	const runID = "01890a5d-ac96-774b-bcce-b30209a81703"

	longAgoMillis := time.Now().Add(-1 * time.Hour).UnixMilli()

	installHandshakeFake(t, 0, fmt.Sprintf(`
sleep 0.3
sentinel_body="{\"nonce\":\"$NIGHTGAUGE_OPENCODE_PLUGIN_NONCE\",\"plugin_version\":\"1\",\"hooks\":[]}"
printf '%%s' "$sentinel_body" > "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
echo '{"type":"step_start","sessionID":"s1"}'
echo '{"type":"tool_use","sessionID":"s1","part":{"tool":"bash","state":{"status":"completed","time":{"start":%d}}}}'
`, longAgoMillis))

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 1612, "item-1612", runID)
	var result *adapters.RunResult
	var err error
	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime)
		result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	_ = home
	if !strings.Contains(result.Stderr, "[nightgauge-opencode-plugin]") {
		t.Fatalf("result.Stderr does not carry the handshake failure marker: a tool reported starting an hour before the sentinel existed must fail late, but did not:\n%s", result.Stderr)
	}
	if result.ExitCode == 0 {
		t.Error("a tool_use reporting state.time.start before the sentinel's own write must fail the stage")
	}
}
