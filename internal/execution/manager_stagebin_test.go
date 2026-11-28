package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// stageBinAdapter is a CLI adapter with a per-run root that names a
// StageBinDir, as the opencode adapter's does, and a shell script for its CLI.
type stageBinAdapter struct {
	root   string
	script string
}

func (a *stageBinAdapter) Name() string     { return "stagebin-fake" }
func (a *stageBinAdapter) UsesStdin() bool  { return false }
func (a *stageBinAdapter) Agentic() bool    { return true }
func (a *stageBinAdapter) binDir() string   { return adapters.OpenCodeStageBinDir(a.root) }
func (a *stageBinAdapter) copyPath() string { return filepath.Join(a.binDir(), "nightgauge") }
func (a *stageBinAdapter) BuildCommand(opts adapters.RunOptions) (string, []string, map[string]string) {
	env := map[string]string{}
	if opts.RunRoot != nil {
		for k, v := range opts.RunRoot.Env {
			env[k] = v
		}
	}
	return "sh", []string{"-c", a.script}, env
}
func (a *stageBinAdapter) PrepareRunRoot(adapters.RunRootRequest) (*adapters.RunRoot, error) {
	return &adapters.RunRoot{Dir: a.root, Env: map[string]string{}, StageBinDir: a.binDir()}, nil
}

func newStageBinAdapter(t *testing.T, script string) *stageBinAdapter {
	t.Helper()
	root := filepath.Join(t.TempDir(), "run-root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return &stageBinAdapter{root: root, script: script}
}

// completionRecorder records whether RunStage delivered a result to its
// streamer, which it does only once it has built one from the stage's output.
type completionRecorder struct{ completed bool }

func (r *completionRecorder) OnOutput(string, []byte)       {}
func (r *completionRecorder) OnComplete(adapters.RunResult) { r.completed = true }

// lastStageBinStreamer is the streamer of the last runStageBinStage call.
var lastStageBinStreamer *completionRecorder

func runStageBinStage(t *testing.T, ctx context.Context, a *stageBinAdapter, timeout time.Duration) (*adapters.RunResult, error) {
	t.Helper()
	m := NewManager(realGitRepo(t), a)
	lastStageBinStreamer = &completionRecorder{}
	return m.RunStage(ctx, StageOptions{
		Repo: "acme/repo", IssueNumber: 3688, Stage: "feature-dev", Timeout: timeout,
		Runtime:  state.NewRuntimeState("acme/repo", 3688, "", "01890a5d-ac96-774b-bcce-b302099a8059"),
		Streamer: lastStageBinStreamer,
	})
}

func wantStageBinRemoved(t *testing.T, a *stageBinAdapter) {
	t.Helper()
	if _, err := os.Lstat(a.binDir()); !os.IsNotExist(err) {
		t.Errorf("the stage's binary directory %s survived the stage: %v", a.binDir(), err)
	}
}

func wantBinTamper(t *testing.T, result *adapters.RunResult, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "[bin-tamper] bin_tamper:") {
		t.Fatalf("RunStage error = %v, want a [bin-tamper] bin_tamper failure", err)
	}
	if result != nil {
		t.Errorf("a tampered stage returned a result (exit %d, stdout %q); its output must not be read", result.ExitCode, result.Stdout)
	}
	if lastStageBinStreamer.completed {
		t.Error("a tampered stage's result was built from its output and delivered to the streamer")
	}
	if got := terminalkind.Classify(err.Error()); got != "bin_tamper" {
		t.Errorf("the tamper error classifies %q, want bin_tamper", got)
	}
}

// A stage that overwrites its NIGHTGAUGE_BIN and exits 0 fails as bin_tamper,
// with no result, and the copy is removed.
func TestRunStageFailsAStageThatRewritesItsBinary(t *testing.T) {
	useStageBinSource(t, "exit 0")
	a := newStageBinAdapter(t, `chmod 700 "$NIGHTGAUGE_BIN" && printf '#!/bin/sh\nexit 0\n# mine\n' > "$NIGHTGAUGE_BIN" && echo '{"type":"result"}'; exit 0`)
	result, err := runStageBinStage(t, context.Background(), a, 30*time.Second)
	wantBinTamper(t, result, err)
	wantStageBinRemoved(t, a)
}

// A stage that leaves its copy alone passes, ran with the copy as its
// NIGHTGAUGE_BIN, and the copy is removed.
func TestRunStageExportsAndRemovesAnUntouchedBinaryCopy(t *testing.T) {
	src := useStageBinSource(t, "exit 0")
	seen := filepath.Join(t.TempDir(), "seen")
	a := newStageBinAdapter(t, `printf '%s' "$NIGHTGAUGE_BIN" > '`+seen+`' && "$NIGHTGAUGE_BIN" version`)
	result, err := runStageBinStage(t, context.Background(), a, 30*time.Second)
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if result == nil || result.ExitCode != 0 {
		t.Fatalf("result = %+v, want exit 0", result)
	}
	if !lastStageBinStreamer.completed {
		t.Error("a clean stage's result was not delivered to the streamer")
	}
	got, _ := os.ReadFile(seen)
	if string(got) != a.copyPath() {
		t.Errorf("the stage's NIGHTGAUGE_BIN = %q, want its copy %q (not the source %q)", got, a.copyPath(), src)
	}
	wantStageBinRemoved(t, a)
}

// A stage that takes the write bit off its copy's directory cannot keep the
// copy from being removed.
func TestRunStageRemovesACopyWhoseDirectoryWasLocked(t *testing.T) {
	useStageBinSource(t, "exit 0")
	a := newStageBinAdapter(t, `chmod 500 "$(dirname "$NIGHTGAUGE_BIN")"`)
	result, err := runStageBinStage(t, context.Background(), a, 30*time.Second)
	wantBinTamper(t, result, err)
	wantStageBinRemoved(t, a)
}

// A stage stopped at its timeout after tampering still fails as bin_tamper.
func TestRunStageReportsTamperOnATimedOutStage(t *testing.T) {
	useStageBinSource(t, "exit 0")
	a := newStageBinAdapter(t, `chmod 700 "$NIGHTGAUGE_BIN"; sleep 30`)
	result, err := runStageBinStage(t, context.Background(), a, 2*time.Second)
	wantBinTamper(t, result, err)
	wantStageBinRemoved(t, a)
}

// A stage cancelled after tampering still fails as bin_tamper.
func TestRunStageReportsTamperOnACancelledStage(t *testing.T) {
	useStageBinSource(t, "exit 0")
	ready := filepath.Join(t.TempDir(), "ready")
	a := newStageBinAdapter(t, `touch "$(dirname "$NIGHTGAUGE_BIN")/extra"; touch '`+ready+`'; sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for range 300 {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	result, err := runStageBinStage(t, ctx, a, 30*time.Second)
	wantBinTamper(t, result, err)
	wantStageBinRemoved(t, a)
}

// Replacing the operator's installed binary during a stage does not fail it.
func TestRunStageIgnoresTheOperatorBinaryBeingReplaced(t *testing.T) {
	src := useStageBinSource(t, "exit 0")
	a := newStageBinAdapter(t, `rm -f '`+src+`' && printf '#!/bin/sh\necho new\n' > '`+src+`' && chmod 755 '`+src+`'`)
	result, err := runStageBinStage(t, context.Background(), a, 30*time.Second)
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if result == nil || result.ExitCode != 0 {
		t.Fatalf("result = %+v, want exit 0", result)
	}
	wantStageBinRemoved(t, a)
}

// A copy that cannot be prepared refuses the dispatch before anything runs.
func TestRunStageRefusesDispatchWhenTheCopyCannotBeMade(t *testing.T) {
	useStageBinSource(t, "exit 0")
	ran := filepath.Join(t.TempDir(), "ran")
	a := newStageBinAdapter(t, `touch '`+ran+`'`)
	if err := os.MkdirAll(a.binDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.binDir(), "planted"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := runStageBinStage(t, context.Background(), a, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), `dispatch refused for adapter "stagebin-fake": stage binary:`) {
		t.Fatalf("RunStage = %v, want a refused dispatch", err)
	}
	if result != nil {
		t.Errorf("a refused dispatch returned a result: %+v", result)
	}
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the stage ran although its binary copy could not be made")
	}
}

// An adapter's own NIGHTGAUGE_BIN export outranks the host path; without one
// the host path is exported as before.
func TestComposeStageEnv_KeepsTheAdaptersNightgaugeBin(t *testing.T) {
	src := useStageBinSource(t, "exit 0")
	env := composeStageEnv([]string{"NIGHTGAUGE_BIN=/stale"}, nil, map[string]string{"NIGHTGAUGE_BIN": "/root/bin/nightgauge"}, "", "")
	if got := envValues(env, "NIGHTGAUGE_BIN"); len(got) != 1 || got[0] != "/root/bin/nightgauge" {
		t.Errorf("NIGHTGAUGE_BIN = %v, want the adapter's export alone", got)
	}
	env = composeStageEnv([]string{"NIGHTGAUGE_BIN=/stale"}, nil, nil, "", "")
	if got := envValues(env, "NIGHTGAUGE_BIN"); len(got) != 1 || got[0] != src {
		t.Errorf("NIGHTGAUGE_BIN = %v, want the host binary %s", got, src)
	}
}

func envValues(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			out = append(out, v)
		}
	}
	return out
}
