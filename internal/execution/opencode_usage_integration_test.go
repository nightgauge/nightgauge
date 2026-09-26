//go:build opencode_integration || canary

package execution

// The usage fold (ADR-022 § 22) observed against the real opencode binary,
// pinned to the version the suite's observations were made on:
//
//	go test -tags opencode_integration ./internal/execution/ -run OpenCodeIntegrationFold -count=1
//
// The stage is dispatched through Manager.RunStage, so the fold runs exactly
// as it does after a pipeline stage. The model is the #1618 stub provider on
// 127.0.0.1, serving its long-session script, so no hosted provider and no
// model server takes part.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/stubprovider"
)

// openCodeFoldRunID is the run identity of the long stage. With one, the
// run's root outlives the stage, as it does in a pipeline run until the run
// ends, so the session can still be measured once the stage has returned.
const openCodeFoldRunID = "0199a8b3-0000-7000-8000-000000002165"

// openCodePipeCapacity is more than a pipe takes at once: 64 KiB on Linux
// and macOS, and never more than 128 KiB observed on macOS.
const openCodePipeCapacity = 128 << 10

// TestOpenCodeIntegrationFoldReadsALongSession: the fold reads the served
// model from the export of a session longer than a pipe takes at once
// (#2165). OpenCode prints an export with a single write and then calls
// process.exit(), which drops whatever a pipe has not yet taken, so a fold
// that read the export through a pipe got it cut short after every real
// stage, and recorded the dispatched model with a drift marker instead of
// the one the export names. Observed on 1.18.30 and 1.18.32 alike, on macOS.
// The stub's long-session script reads calc.py 150 times before its reply.
func TestOpenCodeIntegrationFoldReadsALongSession(t *testing.T) {
	real := realOpenCode(t)
	useRealNightgaugeBinary(t)
	home := isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "http://127.0.0.1:1234/v1", startLongSessionStub(t), 1))
	workspace, _ := openCodeGitWorktree(t, map[string]string{"calc.py": "def add(a, b):\n    return a + b\n"})

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		opts := openCodeStageOptions("lmstudio/stub/stub-model", &state.RuntimeState{RunID: openCodeFoldRunID})
		opts.AllowedTools = []string{"Read"}
		opts.Timeout = 4 * time.Minute
		result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("the stage exited %d:\n%s", result.ExitCode, stderr)
	}
	if len(result.DriftMarkers) != 0 {
		t.Errorf("the fold left drift markers: %q", result.DriftMarkers)
	}
	if result.ModelProvider != "lm-studio" || result.ServedModel != "lm-studio/stub/stub-model" {
		t.Errorf("served = %q from %q, want lm-studio/stub/stub-model from lm-studio", result.ServedModel, result.ModelProvider)
	}

	// The session must be longer than a pipe takes at once, or this case
	// proves nothing. It is measured apart from the fold: the same export, in
	// the fold's environment and directory, printed straight into a file.
	session := parseOpenCode(strings.Split(result.Stdout, "\n")).OpenCode().SessionID
	if session == "" {
		t.Fatalf("the stage's stream names no session:\n%s", stderr)
	}
	root, err := adapters.OpenCodeRunRoot(home, openCodeFoldRunID)
	if err != nil {
		t.Fatal(err)
	}
	isolation, err := adapters.OpenCodeIsolationEnv(adapters.OpenCodeIsolation{
		Root: root, Home: home, GOOS: runtime.GOOS,
		Lookup:           func(string) (string, bool) { return "", false },
		MachineConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), openCodeNoRegistry}
	for k, v := range isolation {
		env = append(env, k+"="+v)
	}
	export, err := os.Create(filepath.Join(t.TempDir(), "export.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer export.Close()
	cmd := exec.Command(real, openCodeExportArgs(session)...)
	cmd.Env, cmd.Dir, cmd.Stdout = openCodeHelperEnv(env), root, export
	if err := cmd.Run(); err != nil {
		t.Fatalf("exporting the stage's session: %v", err)
	}
	info, err := export.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= openCodePipeCapacity {
		t.Fatalf("the session's export is %d bytes, no longer than a pipe takes at once, so this case proves nothing", info.Size())
	}
	t.Logf("the session's sanitized export is %d bytes", info.Size())
}

// startLongSessionStub serves the stub provider's long-session script on
// loopback with no first-token delay, and returns its OpenAI-compatible base
// URL. It stops when the test ends.
func startLongSessionStub(t *testing.T) string {
	t.Helper()
	zero := time.Duration(0)
	stub, err := stubprovider.NewServer(stubprovider.Config{Script: "long-session", DelayOverride: &zero, MaxRequests: 400, IdleTimeout: 2 * time.Minute})
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
	return fmt.Sprintf("http://%s/v1", ln.Addr())
}
