//go:build opencode_integration

package opencodeplugin

// TestSubagentToolCallGatedAgainstRealOpenCode is #1805's verification: the
// pinned opencode 1.18.30 binary, driven by #1618's stub provider's
// task-then-subagent-bash script (no live model, no network egress), runs a
// primary session that calls `task` and a subagent session that calls bash
// once. With the real Nightgauge plugin loaded and subagents allowed
// (EnvSubagents=allow), the subagent's bash call meets the same gates a
// top-level call does: in an analysis stage the stage gate refuses its
// `git commit` with [nightgauge-gate:stage], and in a stage that may commit
// no gate refuses it. ADR-022's 2026-10-05 amendment records the measurement
// this pins.
//
//	go test -tags opencode_integration ./internal/execution/opencodeplugin/ -run TestSubagentToolCallGatedAgainstRealOpenCode -count=1 -v
//
// Deleting gates.js's tool.execute.before registration, or the stage-gate
// call in its bash branch, turns the analysis-stage case red: the
// subagent's commit then runs ungated.
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

// subagentRun is one `opencode run` against the task-then-subagent-bash
// script: the primary and child session ids, and the child's bash part.
type subagentRun struct {
	primaryID, childID string
	bashStatus         string
	bashError          string
	stdout, stderr     string
}

func runSubagentStub(t *testing.T, real string, stub stubInstance, stage string) subagentRun {
	t.Helper()
	sh := newCompactionStubHome(t)
	projectDir := t.TempDir()
	runDir := t.TempDir()
	outputFile := filepath.Join(runDir, "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	configContent := compactionStubConfig(sh.pluginEntry, stub.baseURL, stub.baseURL)

	// Build first: the budget times the run, not a go build (#2347).
	hookBin, waitForHooks := trackedHookBin(t, buildNightgaugeBin(t))
	ctx, cancel := context.WithTimeout(context.Background(), compactionStubBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, "run", "please do the task",
		"-m", "lmstudio/stub-model", "--agent", "build", "--format", "json",
		"--print-logs", "--log-level", "DEBUG")
	cmd.Dir = projectDir
	cmd.Env = []string{
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
		EnvNonce + "=" + nonce,
		EnvSentinel + "=" + filepath.Join(runDir, "sentinel.json"),
		EnvPluginPath + "=" + sh.pluginEntry,
		EnvSubagents + "=allow",
		"NIGHTGAUGE_BIN=" + hookBin,
		"NIGHTGAUGE_OUTPUT_FILE=" + outputFile,
		"NIGHTGAUGE_RUN_ID=subagent-stub-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"NIGHTGAUGE_STAGE=" + stage,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	waitForHooks()
	if runErr != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", runErr, stderr.String())
	}

	out := subagentRun{stdout: stdout.String(), stderr: stderr.String()}
	for _, line := range strings.Split(out.stdout, "\n") {
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
		if ev.Part.State.Status != "completed" {
			t.Fatalf("the task call did not complete (status %q, error %q): the stage's subagent never ran\nstderr:\n%s",
				ev.Part.State.Status, ev.Part.State.Error, out.stderr)
		}
		out.primaryID = ev.SessionID
		out.childID = ev.Part.State.Metadata.SessionID
		if ev.Part.State.Metadata.ParentSessionID != ev.SessionID {
			t.Fatalf("the task's parentSessionId %q is not the primary session %q", ev.Part.State.Metadata.ParentSessionID, ev.SessionID)
		}
	}
	if out.childID == "" || out.childID == out.primaryID {
		t.Fatalf("test premise broken: no completed task call with a child session on stdout:\n%s\nstderr:\n%s", out.stdout, out.stderr)
	}

	exported := exportSanitized(t, compactionStubResult{sessionID: out.childID, real: real, home: sh})
	var child struct {
		Messages []struct {
			Parts []struct {
				Type  string `json:"type"`
				Tool  string `json:"tool"`
				State struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				} `json:"state"`
			} `json:"parts"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(exported), &child); err != nil {
		t.Fatalf("parsing the child session's export: %v", err)
	}
	for _, m := range child.Messages {
		for _, p := range m.Parts {
			if p.Type == "tool" && p.Tool == "bash" {
				out.bashStatus, out.bashError = p.State.Status, p.State.Error
			}
		}
	}
	if out.bashStatus == "" {
		t.Fatalf("test premise broken: the child session %s has no bash part:\n%s", out.childID, exported)
	}
	return out
}

func TestSubagentToolCallGatedAgainstRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	stubBin := buildStubProviderBin(t)

	t.Run("an analysis stage's gate refuses the subagent's commit", func(t *testing.T) {
		run := runSubagentStub(t, real, startStubInstance(t, stubBin, "task-then-subagent-bash"), "issue-pickup")
		if run.bashStatus != "error" || !strings.Contains(run.bashError, "[nightgauge-gate:stage]") {
			t.Fatalf("the subagent's git commit was not refused by the stage gate: status %q, error %q", run.bashStatus, run.bashError)
		}
	})

	t.Run("a stage that may commit lets the subagent's commit through every gate", func(t *testing.T) {
		run := runSubagentStub(t, real, startStubInstance(t, stubBin, "task-then-subagent-bash"), "feature-dev")
		if strings.Contains(run.bashError, "[nightgauge-gate:") {
			t.Fatalf("a gate refused the subagent's git commit in feature-dev: %q", run.bashError)
		}
	})
}
