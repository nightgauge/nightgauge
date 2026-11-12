//go:build !windows

package gaterun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/procstart"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Run(t, dir, "init", "-q")
	return dir
}

// shellGate is a one-step gate running script with sh.
func shellGate(dir, script string) func(context.Context, func(*exec.Cmd) error) error {
	return func(ctx context.Context, runStep func(*exec.Cmd) error) error {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Dir = dir
		return runStep(cmd)
	}
}

func options(root, tree, script string, stderr *syncBuffer) Options {
	return Options{
		Root: root, Tree: tree, Kind: gateledger.KindLocalGate,
		Run: shellGate(root, script),
		Record: func(ev gateledger.Event) error {
			ev.Issue = 7
			return gateledger.Append(root, ev)
		},
		Stderr:    stderr,
		Poll:      50 * time.Millisecond,
		TermGrace: 2 * time.Second, KillGrace: 2 * time.Second,
		// Never fires: tests signal through their own channel.
		Signals: make(chan os.Signal),
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// Two gate-runs started together on one tree run one gate, and both report
// its exit code.
func TestRun_SameTreeRunsOneGate(t *testing.T) {
	root := repo(t)
	counter := filepath.Join(t.TempDir(), "runs")
	script := fmt.Sprintf("echo run >> %q; sleep 1; exit 3", counter)
	var stderr syncBuffer
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, err := Run(context.Background(), options(root, "tree-a", script, &stderr))
			if err != nil {
				t.Error(err)
			}
			codes[i] = code
		}(i)
	}
	wg.Wait()
	if n := countLines(t, counter); n != 1 {
		t.Fatalf("%d gates ran on one tree, want 1\n%s", n, stderr.String())
	}
	if codes[0] != 3 || codes[1] != 3 {
		t.Fatalf("exit codes = %v, want both 3 (the gate's)\n%s", codes, stderr.String())
	}
	if !strings.Contains(stderr.String(), "waited for gate") {
		t.Fatalf("the second gate-run did not say it waited:\n%s", stderr.String())
	}
}

func waitForGroup(t *testing.T, root string) guard {
	t.Helper()
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if g, err := readJSON[guard](p.guard); err == nil && g.PGID > 0 {
			return g
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the holder never recorded its process group")
	return guard{}
}

// A gate-run on a changed tree stops the running gate's whole process group,
// verifies it dead, records it as superseded, and runs its own gate.
func TestRun_ADifferentTreeSupersedesTheRunningGate(t *testing.T) {
	root := repo(t)
	var stderr syncBuffer
	holderDone := make(chan int, 1)
	go func() {
		code, err := Run(context.Background(), options(root, "tree-a", "sleep 30 & wait", &stderr))
		if err != nil {
			t.Error(err)
		}
		holderDone <- code
	}()
	holder := waitForGroup(t, root)

	code, err := Run(context.Background(), options(root, "tree-b", "exit 0", &stderr))
	if err != nil || code != 0 {
		t.Fatalf("superseding gate: code %d, err %v\n%s", code, err, stderr.String())
	}
	select {
	case c := <-holderDone:
		if c != ExitSuperseded {
			t.Fatalf("holder exit = %d, want %d\n%s", c, ExitSuperseded, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the superseded holder never returned")
	}
	if err := syscall.Kill(-holder.PGID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("holder group %d is not dead: kill(-pgid, 0) = %v", holder.PGID, err)
	}
	tally, err := gateledger.RunCount(root, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if tally.Superseded != 1 || tally.Passed != 1 || tally.Full != 2 || tally.Failed+tally.Infra != 0 {
		t.Fatalf("RunCount = %+v, want 1 superseded and 1 passed", tally)
	}
	if ans, _ := gateledger.Latest(root, "tree-a", gateledger.KindLocalGate); ans.Reusable {
		t.Fatal("the superseded tree is reusable")
	}
}

// A guard whose holder is dead is reclaimed.
func TestRun_ReclaimsAStaleGuard(t *testing.T) {
	root := repo(t)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(p.guard, guard{PID: dead.Process.Pid, Tree: "tree-a", RunID: "old"}); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	code, err := Run(context.Background(), options(root, "tree-a", "exit 0", &stderr))
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v\n%s", code, err, stderr.String())
	}
	if _, err := os.Stat(p.guard); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the guard was not released: %v", err)
	}
}

// A holder whose group cannot be stopped is reported with exit 75, naming its
// pid and tree, and no gate runs.
func TestRun_AHolderThatCannotBeStoppedExits75(t *testing.T) {
	root := repo(t)
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(p.guard, guard{PID: os.Getpid(), PGID: 999999, Tree: "tree-a", RunID: "held"}); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "runs")
	var stderr syncBuffer
	o := options(root, "tree-b", fmt.Sprintf("echo run >> %q", counter), &stderr)
	o.TermGrace, o.KillGrace = 100*time.Millisecond, 100*time.Millisecond
	o.Stop = func(int, bool) error { return nil }
	o.GroupAlive = func(int) bool { return true }
	code, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if code != ExitHeld {
		t.Fatalf("code = %d, want %d", code, ExitHeld)
	}
	want := fmt.Sprintf("gate pid %d on tree tree-a is still running", os.Getpid())
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr does not name the holder (%q):\n%s", want, stderr.String())
	}
	if _, err := os.Stat(counter); err == nil {
		t.Fatal("a gate ran while another held the worktree")
	}
	// Nothing replaced the holder, so no marker may void its result.
	if _, err := os.Stat(p.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("supersede marker left behind for a holder that kept running: %v", err)
	}
}

// A marker is removed only while it names the given run, so a marker a newer
// tree wrote for the current holder survives that holder's cleanup.
func TestRemoveMarkerFor_KeepsAMarkerForAnotherRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	if err := writeJSON(path, supersedeMarker{RunID: "new-holder", Tree: "tree-c"}); err != nil {
		t.Fatal(err)
	}
	removeMarkerFor(path, "old-holder")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker for another run was removed: %v", err)
	}
	removeMarkerFor(path, "new-holder")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker naming the run was kept: %v", err)
	}
}

// The gate's steps receive the run id, so a self-recording gate shares it.
func TestRun_ExportsTheRunID(t *testing.T) {
	root := repo(t)
	out := filepath.Join(t.TempDir(), "id")
	var stderr syncBuffer
	o := options(root, "tree-a", fmt.Sprintf("printf %%s \"$%s\" > %q", EnvRunID, out), &stderr)
	o.RecordsItself = true
	if code, err := Run(context.Background(), o); err != nil || code != 0 {
		t.Fatalf("code %d, err %v", code, err)
	}
	id, err := os.ReadFile(out)
	if err != nil || len(id) != 16 {
		t.Fatalf("run id %q, err %v", id, err)
	}
	tally, _ := gateledger.RunCount(root, 7, "")
	if tally.Full != 0 {
		t.Fatalf("gate-run recorded a gate that records itself: %+v", tally)
	}
}

// A gate-run waiting on a holder that a newer tree superseded is superseded
// too: it must not loop round and stop the newer tree's gate.
func TestWaitFor_AHolderReplacedByAnotherTreeSupersedesTheWaiter(t *testing.T) {
	root := repo(t)
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(p.guard, guard{PID: os.Getpid(), Tree: "tree-b", RunID: "newer"}); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	o := options(root, "tree-a", "exit 0", &stderr)
	o.defaults()
	code, ok, err := o.waitFor(context.Background(), p, guard{PID: os.Getpid(), Tree: "tree-a", RunID: "older"})
	if err != nil || !ok || code != ExitSuperseded {
		t.Fatalf("waitFor = (%d, %v, %v), want (%d, true, nil)\n%s", code, ok, err, ExitSuperseded, stderr.String())
	}
}

// runWithin runs Run and fails the test when it does not return within d.
func runWithin(t *testing.T, d time.Duration, o Options) int {
	t.Helper()
	type res struct {
		code int
		err  error
	}
	done := make(chan res, 1)
	go func() {
		code, err := Run(context.Background(), o)
		done <- res{code, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.code
	case <-time.After(d):
		t.Fatalf("gate-run did not return within %s", d)
		return 0
	}
}

// sleeper starts a process leading its own process group, and reaps it when
// the test ends.
func sleeper(t *testing.T) (pid int, start int64) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
	})
	start, err := procstart.Start(cmd.Process.Pid)
	if err != nil {
		t.Skipf("process start time unavailable: %v", err)
	}
	return cmd.Process.Pid, start
}

func selfStart(t *testing.T) int64 {
	t.Helper()
	s, err := procstart.Start(os.Getpid())
	if err != nil {
		t.Skipf("process start time unavailable: %v", err)
	}
	return s
}

// A guard naming a live pid whose start time differs (the holder died and its
// pid was reused) is stale: it is reclaimed, and neither that process nor the
// group under its pgid is signalled.
func TestRun_AReusedPIDIsReclaimedWithoutSignallingIt(t *testing.T) {
	root := repo(t)
	pid, start := sleeper(t)
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	stale := guard{
		PID: pid, PIDStart: start + 1, Host: hostname(), PGID: pid, PGIDStart: start + 1,
		Tree: "tree-a", RunID: "old", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSON(p.guard, stale); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	if code := runWithin(t, 10*time.Second, options(root, "tree-b", "exit 0", &stderr)); code != 0 {
		t.Fatalf("code = %d, want 0\n%s", code, stderr.String())
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the process that reused the pid was signalled: kill(%d, 0) = %v\n%s", pid, err, stderr.String())
	}
	if strings.Contains(stderr.String(), "stopping gate old") {
		t.Fatalf("a reused pid was superseded as a live holder:\n%s", stderr.String())
	}
}

// A guard older than the gate timeout is stale even while its holder lives:
// its step group is stopped (its leader's start time matches) and the gate
// runs.
func TestRun_AnOverAgeGuardIsReclaimed(t *testing.T) {
	root := repo(t)
	pgid, pgidStart := sleeper(t)
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	old := guard{
		PID: os.Getpid(), PIDStart: selfStart(t), Host: hostname(), PGID: pgid, PGIDStart: pgidStart,
		Tree: "tree-a", RunID: "old", StartedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSON(p.guard, old); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	o := options(root, "tree-a", "exit 0", &stderr)
	o.Timeout = time.Hour
	if code := runWithin(t, 10*time.Second, o); code != 0 {
		t.Fatalf("code = %d, want 0\n%s", code, stderr.String())
	}
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the over-age holder's group %d is not dead: %v", pgid, err)
	}
}

// A same-tree wait on a live holder that never finishes gives up after the
// gate timeout with exit 75, naming the guard file.
func TestRun_ASameTreeWaitIsBoundedByTheTimeout(t *testing.T) {
	root := repo(t)
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	live := guard{
		PID: os.Getpid(), PIDStart: selfStart(t), Host: hostname(),
		Tree: "tree-a", RunID: "live", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSON(p.guard, live); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "runs")
	var stderr syncBuffer
	o := options(root, "tree-a", fmt.Sprintf("echo run >> %q", counter), &stderr)
	o.Timeout = 500 * time.Millisecond
	if code := runWithin(t, 10*time.Second, o); code != ExitHeld {
		t.Fatalf("code = %d, want %d\n%s", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "gave up") || !strings.Contains(stderr.String(), p.guard) {
		t.Fatalf("stderr does not name the guard file %s:\n%s", p.guard, stderr.String())
	}
	if _, err := os.Stat(counter); err == nil {
		t.Fatal("a gate ran while a live holder kept the worktree")
	}
}

// A signal while holding stops the step's group, even one ignoring the
// signal (SIGKILL after the grace), refuses further steps, records infra,
// writes no result for a waiter to adopt, releases the guard and exits
// 128+signo.
func TestRun_ASignalStopsTheStepGroupAndReleasesTheGuard(t *testing.T) {
	root := repo(t)
	tmp := t.TempDir()
	childFile, counter := filepath.Join(tmp, "child"), filepath.Join(tmp, "runs")
	var stderr syncBuffer
	sigs := make(chan os.Signal, 1)
	o := options(root, "tree-a", "", &stderr)
	o.Signals = sigs
	o.TermGrace = 300 * time.Millisecond
	o.Run = func(ctx context.Context, runStep func(*exec.Cmd) error) error {
		first := exec.CommandContext(ctx, "sh", "-c",
			fmt.Sprintf(`trap "" INT; sleep 300 & echo $! > %q; wait`, childFile))
		if err := runStep(first); err != nil {
			return err
		}
		return runStep(exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("echo run >> %q", counter)))
	}
	done := make(chan int, 1)
	go func() {
		code, err := Run(context.Background(), o)
		if err != nil {
			t.Error(err)
		}
		done <- code
	}()
	held := waitForGroup(t, root)
	child := waitForPIDFile(t, childFile)
	sigs <- syscall.SIGINT
	var code int
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("gate-run did not stop on SIGINT\n%s", stderr.String())
	}
	if code != 130 {
		t.Fatalf("code = %d, want 130\n%s", code, stderr.String())
	}
	assertStopped(t, root, held.PGID, child)
	if _, err := os.Stat(counter); err == nil {
		t.Fatal("a step ran after the signal")
	}
	p, _ := pathsFor(root)
	if _, err := os.Stat(p.result); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an interrupted gate left a result for waiters: %v", err)
	}
	tally, err := gateledger.RunCount(root, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if tally.Infra != 1 || tally.Passed+tally.Failed != 0 {
		t.Fatalf("RunCount = %+v, want 1 infra", tally)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pid int
		if data, err := os.ReadFile(path); err == nil {
			if _, err := fmt.Sscan(string(data), &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never named a pid", path)
	return 0
}

// assertStopped checks that no process of the step group survives, the
// backgrounded child is gone, and the guard was released.
func assertStopped(t *testing.T, root string, pgid, child int) {
	t.Helper()
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("step group %d survived: kill(-pgid, 0) = %v", pgid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("the step's background child %d survived", child)
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, err := pathsFor(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.guard); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the guard was not released: %v", err)
	}
}

const helperEnv = "GATERUN_TEST_HELPER_ROOT"

// TestMain lets a test re-exec this binary as a real gate-run process, so a
// real signal reaches gate-run's own handler.
func TestMain(m *testing.M) {
	if root := os.Getenv(helperEnv); root != "" {
		code, err := Run(context.Background(), Options{
			Root: root, Tree: "tree-a", Kind: gateledger.KindLocalGate,
			Run:    shellGate(root, os.Getenv(helperEnv+"_SCRIPT")),
			Stderr: os.Stderr, Poll: 50 * time.Millisecond,
			TermGrace: 300 * time.Millisecond, KillGrace: 2 * time.Second,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// Killing gate-run itself with SIGTERM stops the running step's group, even a
// step that ignores SIGTERM, releases the guard and exits 143.
func TestRun_SIGTERMToGateRunStopsTheStepGroup(t *testing.T) {
	root := repo(t)
	childFile := filepath.Join(t.TempDir(), "child")
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), helperEnv+"="+root,
		helperEnv+"_SCRIPT="+fmt.Sprintf(`trap "" TERM; sleep 300 & echo $! > %q; wait`, childFile))
	var out syncBuffer
	helper.Stdout, helper.Stderr = &out, &out
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- helper.Wait() }()
	held := waitForGroup(t, root)
	child := waitForPIDFile(t, childFile)
	t.Cleanup(func() { _ = syscall.Kill(-held.PGID, syscall.SIGKILL) })
	if err := helper.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = helper.Process.Kill()
		t.Fatalf("gate-run did not exit on SIGTERM\n%s", out.String())
	}
	if code := helper.ProcessState.ExitCode(); code != 143 {
		t.Fatalf("gate-run exit = %d, want 143\n%s", code, out.String())
	}
	assertStopped(t, root, held.PGID, child)
}
