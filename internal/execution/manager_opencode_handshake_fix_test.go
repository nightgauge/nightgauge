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
// a shell fragment invoked for the stage's own `run`. onVersion is a shell
// fragment run when `opencode --version` (the manager's own post-failure
// probe, openCodePluginHandshakeMarker) starts, before it answers — "" for
// an instant answer, as the real binary normally gives.
func installHandshakeFake(t *testing.T, onVersion string, body string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then
%s
  echo 1.18.30
  exit 0
fi
%s
cat > /dev/null
exit 0
`, onVersion, body)
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
// read. The test orders on events, not a clock (#2621): the `--version`
// branch touches a probe-started file, and the stage's shell — representing
// a tool call already running — writes the marker only once it sees that
// file. So the marker exists only if the group was still alive when the
// probe started. The probe then holds its answer (bounded at 3s, below the
// manager's 5s probe timeout) until the marker appears, so on unfixed code
// the kill cannot land before the marker is written, however loaded the
// host. Fixed, the group is gone before the probe starts and the marker
// never appears.
func TestOpenCodeHandshakeKillPrecedesVersionProbe(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	workspace := openCodeWorkspace(t)
	const runID = "01890a5d-ac96-774b-bcce-b30209a81701"
	marker := filepath.Join(t.TempDir(), "ran-after-probe-start.marker")
	probe := filepath.Join(t.TempDir(), "probe-started")
	// The manager also runs `--version` before the stage starts; only a probe
	// that begins after the stage's step_start counts as the handshake one.
	stepped := filepath.Join(t.TempDir(), "step-started")

	installHandshakeFake(t, fmt.Sprintf(`
  if [ -e %q ]; then
    : > %q
    i=0
    while [ ! -e %q ] && [ $i -lt 150 ]; do sleep 0.02; i=$((i+1)); done
  fi
`, stepped, probe, marker), fmt.Sprintf(`
: > %q
echo '{"type":"step_start","sessionID":"s1"}'
i=0
while [ $i -lt 1000 ]; do
  if [ -e %q ]; then echo ran > %q; break; fi
  sleep 0.02
  i=$((i+1))
done
`, stepped, probe, marker))

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 1612, "item-1612", runID)
	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime)
		_, _ = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	_ = home

	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("the --version probe never started, so this case is not exercising the kill/probe ordering: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the stage's shell observed the --version probe start (and wrote the marker), so the group was still alive when the probe ran: the SIGKILL was delayed behind the probe")
	}
}

// TestOpenCodeLateHandshakeFailsTheStage (#1635 fix round finding 4a): a
// sentinel rewritten AFTER the run's own first tool_use was observed must
// fail the stage (VerifyNotLate, checked at exit). The fake writes a valid
// sentinel before step_start (VerifyLoaded passes), emits tool_use, then
// rewrites the sentinel — simulating a plugin that reloaded, or loaded a
// second time, after a tool had already run. The rewrite's mtime is set an
// hour ahead (#2621), so it falls after the observed tool_use time however
// long the manager's reader takes to see the line; the tool_use carries no
// state.time.start, so this still exercises the observed-time path.
func TestOpenCodeLateHandshakeFailsTheStage(t *testing.T) {
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	workspace := openCodeWorkspace(t)
	const runID = "01890a5d-ac96-774b-bcce-b30209a81702"

	future := time.Now().Add(time.Hour).Format("200601021504.05")
	installHandshakeFake(t, "", fmt.Sprintf(`
sentinel_body="{\"nonce\":\"$NIGHTGAUGE_OPENCODE_PLUGIN_NONCE\",\"plugin_version\":\"1\",\"hooks\":[]}"
printf '%%s' "$sentinel_body" > "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
echo '{"type":"step_start","sessionID":"s1"}'
echo '{"type":"tool_use","sessionID":"s1","part":{"tool":"bash","state":{"status":"completed"}}}'
printf '%%s' "$sentinel_body" > "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
touch -t %s "$NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL"
`, future))

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

	installHandshakeFake(t, "", fmt.Sprintf(`
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
