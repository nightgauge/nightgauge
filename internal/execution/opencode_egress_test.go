//go:build canary

package execution

// The #1644 egress fixture. scripts/opencode-egress-check.sh runs
//
//	go test -tags canary ./internal/execution -run TestOpenCodeEgressCheck -count=1 -v
//
// inside a Linux network namespace whose only interface is loopback
// (unshare --user --map-root-user --net), with the whole process tree under
// strace. This file is the Go-level half of that evidence: it dispatches a
// real feature-dev stage through the OpenCode adapter (Manager.RunStage,
// exactly the production path — see PLAN.md's "Premise Correction": the TS
// SDK path refuses OpenCode dispatch pending #1648) against the #1618 stub
// provider, so a broken stub/dispatch fails here as a clear Go failure
// before the strace wrapper ever runs, rather than as an opaque trace-parser
// failure with no attributable cause.
//
// The fixture extends openCodeGitWorktree with a local bare remote (so any
// git push/fetch OpenCode's tools attempt stays on local disk and never
// reaches a forge) and a pre-seeded context file at
// issue-9999.json in the worktree's pipeline state directory, under its git
// directory (ADR-024 § 7; docs/CONTEXT_ARCHITECTURE.md's issue-{N}.json schema), so the stage needs no `gh` call and no GitHub
// credentials to have a context to read.
//
// AC3 (fake cloud keys produce no cloud-provider connection attempt) is
// covered by construction, not by a separate assertion here: ADR-022 § 8
// strips every inherited OPENCODE_* variable and every catalog-bound
// credential variable (OPENAI_API_KEY, ANTHROPIC_API_KEY, XAI_API_KEY among
// them) before spawn, so a fake key in the parent env never reaches the
// child at all — the CI harness (scripts/opencode-egress-check.sh) exports
// them into the process env the same way the technical notes describe, and
// the trace itself is the evidence there was nothing for them to reach.
//
// AC4 (the Nightgauge plugin loaded from the per-run dir, no npm/Bun
// registry lookup) is likewise covered by construction: Manager.RunStage's
// own handshake verification (opencodeplugin.VerifyLoaded) already fails the
// run closed if the plugin did not load, and the pre-seeded dependency
// archive (ADR-022's "Narrowed AC1" amendment) is what stops the npm install
// this test would otherwise wait on — nothing new is added here to prove
// either half a second time.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// openCodeEgressContextFixture is a minimal, schema-valid issue-9999.json
// (docs/CONTEXT_ARCHITECTURE.md's issue-{N}.json, schema_version 1.4) — the
// stage needs no `gh` call to have a context to read, and the stub's canned
// script never inspects its content, so this is deliberately the smallest
// fixture the schema allows.
const openCodeEgressContextFixture = `{
  "schema_version": "1.4",
  "issue_number": 9999,
  "title": "egress fixture",
  "type": "feature",
  "branch": "feat/9999-egress-fixture",
  "base_branch": "main",
  "requirements": {
    "summary": "egress fixture: not a real issue"
  },
  "labels": []
}`

// openCodeEgressWorktree builds the fixture the #1644 egress check dispatches
// against: an issue-9999 worktree cloned from a local bare remote (so any
// git push/fetch the stage's tools attempt stays on local disk), with the
// stub script's edit target (calc.py) committed and a pre-seeded context
// file written into the worktree's own pipeline state directory. It
// returns the workspace root RunStage needs and the worktree's own path (for
// assertions, if any are ever added).
func openCodeEgressWorktree(t *testing.T) (workspace, worktree string) {
	t.Helper()
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	calc := filepath.Join(seed, "calc.py")
	if err := os.WriteFile(calc, []byte("def add(a, b):\n    return a + b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "base")

	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)

	workspace = t.TempDir()
	parent := filepath.Join(workspace, ".nightgauge", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, parent, "clone", "-q", origin, "nightgauge-issue-9999")
	worktree = filepath.Join(parent, "nightgauge-issue-9999")

	pipelineDir := layouttest.MkPipelineDir(t, worktree)
	contextPath := filepath.Join(pipelineDir, "issue-9999.json")
	if err := os.WriteFile(contextPath, []byte(openCodeEgressContextFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(openCodeEgressContextFixture), &probe); err != nil {
		t.Fatalf("the pre-seeded context fixture is not valid JSON: %v", err)
	}
	return workspace, worktree
}

// TestOpenCodeEgressCheck is the #1644 egress fixture's Go-level leg (AC1,
// AC3, AC4, AC5): a real feature-dev dispatch through Manager.RunStage
// against the #1618 stub provider, run inside the CI harness's network
// namespace under strace. Assertions here are the same clean-run assertions
// TestOpenCodeCanaryLiveStream already makes (err == nil, exit 0, a non-zero
// token count) — this fixture's own contribution is the local bare remote
// and the pre-seeded context file, not a new assertion shape.
func TestOpenCodeEgressCheck(t *testing.T) {
	realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	base, _ := startOpenCodeCanaryStubWithPID(t, "tool-edit-stop")
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "http://127.0.0.1:1234/v1", base, 1))

	workspace, worktree := openCodeEgressWorktree(t)
	contextFile := filepath.Join(layouttest.PipelineDir(t, worktree), "issue-9999.json")

	opts := openCodeStageOptions(openCodeCanaryModel, nil)
	opts.IssueNumber = 9999
	opts.ContextFile = contextFile
	opts.Timeout = 120 * time.Second

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
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
		t.Errorf("the egress fixture's own stream parsed to zero tokens:\n%s", result.Stdout)
	}

	// The run's private server is gone at run end (#3303). Every 2.x run
	// spawns `serve --stdio --port 0`, holding a loopback listener (spike
	// #3197 § 1); the strace parser asserts it binds loopback only, and this
	// asserts nothing the run started, that server included, survives
	// RunStage, and so neither does its listener.
	if survivors := openCodeRunSurvivors(t, openCodeEgressRunsDir(t), 5*time.Second); len(survivors) > 0 {
		t.Errorf("processes of the run outlived RunStage:\n%s", strings.Join(survivors, "\n"))
	}

	// AC5's Go-level half is startOpenCodeCanaryStubWithPID's own t.Cleanup
	// (registered above, run unconditionally on this test's return): it
	// signals the stub PID and asserts kill(pid, 0) fails afterward, matching
	// TestOpenCodeCanaryStubIsKilledAndConfirmedDead's pattern. The shell
	// harness re-verifies at the process-tree level for its own spawns (the
	// stub it starts directly, and the `go test` process itself) — nothing
	// further is asserted here.
}

// openCodeEgressRunsDir is the directory isolateOpenCodeHome's state root
// keeps every OpenCode run root under. Each process a run starts, the
// dispatched `opencode run` and the `serve --stdio` it spawns among them,
// holds an environment pointing into its run root (PrepareRunRoot); the test
// process and the stub provider hold none.
func openCodeEgressRunsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("NIGHTGAUGE_STATE_HOME"), "opencode", "runs") + string(filepath.Separator)
}

// openCodeRunSurvivors returns one line per live process whose environment
// names a path under runsDir, with the TCP sockets it holds in LISTEN state,
// waiting up to d for them to go. It reads Linux's /proc, the egress
// harness's platform, and skips the test elsewhere. A process whose
// environment cannot be read (a zombie, or one gone mid-scan) is not counted:
// a zombie holds no socket and runs nothing.
func openCodeRunSurvivors(t *testing.T, runsDir string, d time.Duration) []string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the run-end survivor scan reads /proc")
	}
	deadline := time.Now().Add(d)
	for {
		survivors, err := scanOpenCodeRunProcesses(runsDir)
		if err != nil {
			t.Fatalf("the run-end survivor scan could not read /proc, so it checked nothing: %v", err)
		}
		if len(survivors) == 0 || time.Now().After(deadline) {
			return survivors
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// scanOpenCodeRunProcesses is one pass of openCodeRunSurvivors: a line per
// process whose environment names a path under runsDir, or the error that
// kept /proc from being listed.
func scanOpenCodeRunProcesses(runsDir string) ([]string, error) {
	listening := procListeningInodes()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil || !strings.Contains(string(environ), runsDir) {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		var listeners []string
		fds, _ := os.ReadDir(filepath.Join("/proc", e.Name(), "fd"))
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", e.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			if addr, ok := listening[link]; ok {
				listeners = append(listeners, addr)
			}
		}
		out = append(out, fmt.Sprintf("pid %d %q listening on %v", pid, strings.ReplaceAll(strings.TrimRight(string(cmdline), "\x00"), "\x00", " "), listeners))
	}
	return out, nil
}

// procListeningInodes maps "socket:[inode]", the fd link of a TCP socket, to
// the hex local address of each socket /proc/net/tcp and tcp6 list in LISTEN
// state (st 0A).
func procListeningInodes() map[string]string {
	out := map[string]string{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) >= 10 && f[3] == "0A" {
				out["socket:["+f[9]+"]"] = f[1]
			}
		}
	}
	return out
}

// TestOpenCodeEgressCheckSurvivorScan holds openCodeRunSurvivors to a known
// bad and a known good case before the egress check trusts it: a process
// carrying a run root in its environment and holding a loopback listener is
// reported, with that listener, and once it is killed it is not. Its name
// matches the harness's -run TestOpenCodeEgressCheck, so it runs there.
func TestOpenCodeEgressCheckSurvivorScan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the run-end survivor scan reads /proc")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed to hold a listener")
	}
	runs := filepath.Join(t.TempDir(), "opencode", "runs") + string(filepath.Separator)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(python, "-c", `import socket,sys,time
s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1)
open(sys.argv[1],"w").close(); time.sleep(300)`, ready)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(runs, "run-1", "data"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the listener never became ready")
		}
	}
	survivors := openCodeRunSurvivors(t, runs, 0)
	if len(survivors) != 1 || !strings.Contains(survivors[0], fmt.Sprintf("pid %d ", cmd.Process.Pid)) || strings.Contains(survivors[0], "listening on []") {
		t.Fatalf("the scan reported %q, want pid %d with its listener", survivors, cmd.Process.Pid)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if survivors := openCodeRunSurvivors(t, runs, 5*time.Second); len(survivors) > 0 {
		t.Errorf("the scan reported %q after the process was killed and reaped", survivors)
	}
}
