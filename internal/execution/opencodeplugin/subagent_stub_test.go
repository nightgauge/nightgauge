//go:build opencode_integration

package opencodeplugin

// The #1805 tests drive the pinned opencode 1.18.30 binary with #1618's stub
// provider's task-then-subagent-bash script (no live model, no network
// egress): a primary session that calls `task`, and a subagent session that
// calls bash once.
//
//	go test -tags opencode_integration ./internal/execution/opencodeplugin/ -run 'TestToolExecuteBeforeFiresInSubagentAgainstRealOpenCode|TestRealPluginDeniesTaskAgainstRealOpenCode' -count=1 -v
//
// TestToolExecuteBeforeFiresInSubagentAgainstRealOpenCode is the
// measurement: with a logging plugin in place of Nightgauge's, the hook
// fires for the subagent's bash call in the child's own sessionID.
// TestRealPluginDeniesTaskAgainstRealOpenCode pins what ships: the real
// plugin refuses the `task` call with [nightgauge-gate:task-denied], so no
// child session starts. ADR-022's 2026-10-05 amendment records both and why
// the denial stays.
import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// subagentLogPlugin records every tool.execute.before call, one JSON line
// each, to the file SUBAGENT_HOOK_LOG names.
const subagentLogPlugin = `import { appendFileSync } from "node:fs";
export const SubagentHookLog = async () => ({
  "tool.execute.before": async (input) => {
    appendFileSync(process.env.SUBAGENT_HOOK_LOG, JSON.stringify({ tool: input.tool, sessionID: input.sessionID }) + "\n");
  },
});
`

// taskToolUse is the stream's tool_use event for a `task` call.
type taskToolUse struct {
	sessionID, status, err, childID, parentID string
}

// runTaskStub runs one `opencode run` against the task-then-subagent-bash
// script with pluginEntry loaded and extraEnv added, and returns the task
// call's tool_use event from the JSON stream.
func runTaskStub(t *testing.T, real, pluginEntry string, sh compactionStubHome, stub stubInstance, extraEnv []string) taskToolUse {
	t.Helper()
	configContent := compactionStubConfig(pluginEntry, stub.baseURL, stub.baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), compactionStubBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, "run", "please do the task",
		"-m", "lmstudio/stub-model", "--agent", "build", "--format", "json",
		"--print-logs", "--log-level", "DEBUG")
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string{
		"HOME=" + sh.home,
		"PATH=/usr/bin:/bin",
		"XDG_CONFIG_HOME=" + sh.xdgConfigHome,
		"XDG_DATA_HOME=" + filepath.Join(sh.home, ".data"),
		"XDG_CACHE_HOME=" + filepath.Join(sh.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(sh.home, ".state"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_CONFIG_CONTENT=" + string(configContent),
		pluginIntegrationNoRegistry,
	}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", err, stderr.String())
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				Tool  string `json:"tool"`
				State struct {
					Status   string `json:"status"`
					Error    string `json:"error"`
					Metadata struct {
						ParentSessionID string `json:"parentSessionId"`
						SessionID       string `json:"sessionId"`
					} `json:"metadata"`
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "tool_use" || ev.Part.Tool != "task" {
			continue
		}
		return taskToolUse{
			sessionID: ev.SessionID, status: ev.Part.State.Status, err: ev.Part.State.Error,
			childID: ev.Part.State.Metadata.SessionID, parentID: ev.Part.State.Metadata.ParentSessionID,
		}
	}
	t.Fatalf("test premise broken: no task tool_use event on stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	return taskToolUse{}
}

func TestToolExecuteBeforeFiresInSubagentAgainstRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	stub := startStubInstance(t, buildStubProviderBin(t), "task-then-subagent-bash")
	sh := newCompactionStubHome(t)
	pluginPath := filepath.Join(t.TempDir(), "hook-log.js")
	if err := os.WriteFile(pluginPath, []byte(subagentLogPlugin), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "hook.log")

	task := runTaskStub(t, real, "file://"+pluginPath, sh, stub, []string{"SUBAGENT_HOOK_LOG=" + logPath})
	if task.status != "completed" || task.childID == "" || task.parentID != task.sessionID {
		t.Fatalf("test premise broken: the task call did not start a child session: %+v", task)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading the hook log: %v", err)
	}
	var calls []struct {
		Tool      string `json:"tool"`
		SessionID string `json:"sessionID"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var c struct {
			Tool      string `json:"tool"`
			SessionID string `json:"sessionID"`
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("hook log line %q: %v", line, err)
		}
		calls = append(calls, c)
	}
	if len(calls) != 2 ||
		calls[0].Tool != "task" || calls[0].SessionID != task.sessionID ||
		calls[1].Tool != "bash" || calls[1].SessionID != task.childID {
		t.Fatalf("tool.execute.before calls = %+v, want task in %s then bash in the child %s", calls, task.sessionID, task.childID)
	}
}

func TestRealPluginDeniesTaskAgainstRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	stub := startStubInstance(t, buildStubProviderBin(t), "task-then-subagent-bash")
	sh := newCompactionStubHome(t)
	runDir := t.TempDir()
	outputFile := filepath.Join(runDir, "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	// Build first: the budget times the run, not a go build (#2347).
	hookBin, waitForHooks := trackedHookBin(t, buildNightgaugeBin(t))
	task := runTaskStub(t, real, sh.pluginEntry, sh, stub, []string{
		EnvNonce + "=" + nonce,
		EnvSentinel + "=" + filepath.Join(runDir, "sentinel.json"),
		EnvPluginPath + "=" + sh.pluginEntry,
		"NIGHTGAUGE_BIN=" + hookBin,
		"NIGHTGAUGE_OUTPUT_FILE=" + outputFile,
		"NIGHTGAUGE_RUN_ID=task-deny-stub-" + strconv.FormatInt(time.Now().UnixNano(), 36),
	})
	waitForHooks()
	if task.status != "error" || !strings.Contains(task.err, "[nightgauge-gate:task-denied]") {
		t.Fatalf("the shipped plugin did not refuse the task call: %+v", task)
	}
	if task.childID != "" {
		t.Errorf("a child session %s started although task was refused", task.childID)
	}
}
