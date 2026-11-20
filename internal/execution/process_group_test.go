package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// Killing a stage must reap what the stage SPAWNED, not just the stage (#1253).
//
// Every kill path used to signal a single pid. On SIGTERM a shell child may
// propagate through its own trap; on SIGKILL — the path CancelWithGrace takes
// once the grace period expires — no trap runs at all, so the harder the kill
// the more certain the leak. The orphans reparent to PID 1 with nothing tying
// them back to the run that made them, which is how leaked Android emulators
// outlived their CI jobs on a shared host.
//
// This exercises real processes deliberately. A mock that records "Kill was
// called" cannot tell a pid kill from a group kill, which is the entire
// distinction under test.

// spawnTreeLeader starts `sh` in its own process group, which in turn spawns a
// long-lived grandchild. Returns the leader process and the grandchild's pid.
func spawnTreeLeader(t *testing.T) (*exec.Cmd, int) {
	t.Helper()

	pidFile := t.TempDir() + "/grandchild.pid"
	// The grandchild outlives its parent shell on purpose: the shell exits
	// immediately after recording the pid, so the grandchild is orphaned to
	// PID 1 exactly like a booted emulator. Only a GROUP signal can still
	// reach it — it stays in the leader's process group after reparenting.
	script := "sleep 300 & echo $! > " + pidFile + "; sleep 300"

	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	t.Cleanup(func() {
		// Belt and braces: never leak from the test that exists to stop leaks.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	var grandchild int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				grandchild = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("grandchild never recorded its pid")
	}
	return cmd, grandchild
}

// alive reports whether pid still exists. EPERM counts as alive: a process we
// may not signal is emphatically still running.
//
// NOTE for the caller: a killed-but-unreaped child is a ZOMBIE, and
// kill(pid, 0) succeeds on a zombie — its table entry survives until the
// parent waits. So a test that kills its own child must Wait() before asking
// this, or it will read a corpse as alive. (An ORPHAN needs no such care: it
// reparents to PID 1, which reaps it immediately.)
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !alive(pid)
}

func TestSignalProcessTree_ReachesGrandchildren(t *testing.T) {
	leader, grandchild := spawnTreeLeader(t)

	if !alive(grandchild) {
		t.Fatal("grandchild is not alive at the start — fixture is broken")
	}

	if !signalProcessTree(leader.Process, syscall.SIGKILL) {
		t.Fatal("signalProcessTree reported it signalled nothing")
	}

	// Reap the leader so it is not a zombie when we ask about it. The
	// grandchild needs no equivalent: it is orphaned to PID 1, which reaps it.
	_, _ = leader.Process.Wait()

	// THE ASSERTION THIS FILE EXISTS FOR.
	if !waitGone(grandchild, 5*time.Second) {
		t.Error("grandchild survived the kill — a stage's descendants must not outlive it (#1253)")
	}
	if !waitGone(leader.Process.Pid, 5*time.Second) {
		t.Error("leader survived the kill")
	}
}

// The per-pid kill this replaced is the control: it must leave the grandchild
// running. Without this, the test above could pass for the wrong reason (e.g.
// the shell propagating the signal itself) and would not prove the group kill
// is doing the work.
func TestSignalProcessTree_PerPidKillLeaksGrandchild(t *testing.T) {
	leader, grandchild := spawnTreeLeader(t)

	// The OLD behaviour, verbatim.
	if err := leader.Process.Kill(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	_, _ = leader.Process.Wait()
	if !waitGone(leader.Process.Pid, 5*time.Second) {
		t.Fatal("leader survived its own SIGKILL")
	}

	// 250ms is generous: if the grandchild were going to die with its parent
	// it would already be gone.
	time.Sleep(250 * time.Millisecond)
	if !alive(grandchild) {
		t.Skip("grandchild died with its parent on this platform — the leak this guards cannot be demonstrated here")
	}

	// Documented leak: this is precisely what #1253 fixes.
	if !alive(grandchild) {
		t.Error("expected the per-pid kill to leak the grandchild")
	}
	_ = syscall.Kill(grandchild, syscall.SIGKILL)
}

// A process whose group is gone must still be signalled by pid rather than
// silently skipped — the fallback must never make this reach LESS than before.
func TestSignalProcessTree_FallsBackToPidWhenGroupIsGone(t *testing.T) {
	// Started WITHOUT Setpgid: it shares the test binary's group, so it leads
	// no group, leadsForeignGroup rejects it, and only the pid is signalled.
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	if !signalProcessTree(cmd.Process, syscall.SIGKILL) {
		t.Fatal("signalProcessTree returned false for a live process — the pid fallback did not fire")
	}
	_, _ = cmd.Process.Wait() // reap, so the check below is not reading a zombie
	if !waitGone(cmd.Process.Pid, 5*time.Second) {
		t.Error("process survived — the fallback did not actually signal it")
	}
}

func TestSignalProcessTree_NilProcessIsNotSignalled(t *testing.T) {
	if signalProcessTree(nil, syscall.SIGTERM) {
		t.Error("signalProcessTree(nil) reported it signalled something")
	}
}

// leftBehindAdapter is an agentic adapters.SkillRunner whose stage
// backgrounds a long-lived child with its stdio sent away from the stage's
// pipes, records the child's pid, and exits 0: the shape of an OpenCode 2.x
// run whose private `serve --stdio --port 0` outlives its client (spike
// #3197). The child stays in the stage's process group.
type leftBehindAdapter struct{ pidFile string }

func (leftBehindAdapter) Name() string    { return "left-behind-fake" }
func (leftBehindAdapter) UsesStdin() bool { return false }
func (leftBehindAdapter) Agentic() bool   { return true }
func (a leftBehindAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "sh", []string{"-c", `sleep 300 </dev/null >/dev/null 2>&1 & echo $! > "$0"; exit 0`, a.pidFile}, nil
}

// TestRunStageProcessGroupReapedAfterNormalExit (#3303): a stage that exits 0
// leaving a child in its process group does not leave it running. Before the
// post-exit reap only a cancel, a timeout or a budget stop killed the group,
// so the child of a stage that simply ended survived it.
func TestRunStageProcessGroupReapedAfterNormalExit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-3303"), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	result, err := NewManager(root, leftBehindAdapter{pidFile: pidFile}).RunStage(context.Background(), StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 3303,
		Stage:       "feature-dev",
		Timeout:     stubWaitBound(t),
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if result == nil || result.ExitCode != 0 {
		t.Fatalf("RunStage result = %+v, want exit 0", result)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stage recorded no child pid: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || child <= 0 {
		t.Fatalf("child pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	// The orphan reparents to PID 1, which normally reaps it at once. Where
	// PID 1 reaps nothing (a container whose init is the test binary), the
	// killed child stays a zombie, which runs nothing and holds nothing.
	deadline := time.Now().Add(5 * time.Second)
	for alive(child) && !zombie(child) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(child) && !zombie(child) {
		t.Errorf("pid %d, left in the stage's process group, is still running after RunStage returned", child)
	}
}

// zombie reports whether pid is a zombie, read from Linux's /proc/<pid>/stat
// (state is the field after the parenthesised command). It reports false
// where /proc does not exist; there, launchd reaps an orphan at once.
func zombie(pid int) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	_, rest, ok := strings.Cut(string(raw), ") ")
	return ok && strings.HasPrefix(rest, "Z")
}

// pipeHolderAdapter backgrounds a child that KEEPS the stage's stdout and
// stderr open, then exits 0: a survivor holding the pipes the output readers
// wait on.
type pipeHolderAdapter struct{}

func (pipeHolderAdapter) Name() string    { return "pipe-holder-fake" }
func (pipeHolderAdapter) UsesStdin() bool { return false }
func (pipeHolderAdapter) Agentic() bool   { return true }
func (pipeHolderAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "sh", []string{"-c", `sleep 300 </dev/null & exit 0`}, nil
}

// TestRunStageReturnsWhenASurvivorHoldsThePipes (#3303): the group is killed
// the moment the leader exits, not after the output readers finish, so a
// child holding stdout and stderr open cannot hold the stage until its
// timeout.
func TestRunStageReturnsWhenASurvivorHoldsThePipes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-3303"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := NewManager(root, pipeHolderAdapter{}).RunStage(context.Background(), StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 3303,
		Stage:       "feature-dev",
		Timeout:     2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	if result == nil || result.ExitCode != 0 {
		t.Fatalf("RunStage result = %+v, want exit 0", result)
	}
	if took := time.Since(started); took > 30*time.Second {
		t.Errorf("RunStage took %v: a child holding the pipes held the stage", took)
	}
}

// startGroupLeader starts `sleep 300` as the leader of its own process group.
func startGroupLeader(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _, _ = cmd.Process.Wait() })
	return cmd
}

// TestSignalProcessTree_ReapedProcessSendsNoGroupSignal (#3443): a reaped
// stage process whose pid number now leads another group must not reach that
// group. The sentinel stands in for the reused pid: the stale *os.Process is
// rebuilt with the sentinel's pid but marked done by Wait.
func TestSignalProcessTree_ReapedProcessSendsNoGroupSignal(t *testing.T) {
	sentinel := startGroupLeader(t)
	exited := make(chan struct{})
	go func() { _ = sentinel.Wait(); close(exited) }()

	stale := exec.Command("true")
	if err := stale.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	reaped := stale.Process
	reaped.Pid = sentinel.Process.Pid // simulate pid reuse after reap

	if signalProcessTree(reaped, syscall.SIGKILL) {
		t.Error("signalProcessTree reported signalling a reaped process")
	}
	select {
	case <-exited:
		t.Error("a reaped process's group signal killed the group now led by its pid number")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestLeadsForeignGroup(t *testing.T) {
	leader := startGroupLeader(t)
	if !leadsForeignGroup(leader.Process) {
		t.Error("a live group leader was rejected")
	}

	// A child in our group does not lead a group: per-pid only.
	member := exec.Command("sleep", "300")
	if err := member.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = member.Process.Kill(); _, _ = member.Process.Wait() })
	if leadsForeignGroup(member.Process) {
		t.Error("a non-leader was accepted as a group leader")
	}

	init, err := os.FindProcess(1)
	if err != nil {
		t.Fatalf("find 1: %v", err)
	}
	if leadsForeignGroup(init) {
		t.Error("pid 1 was accepted; kill(-1) would broadcast")
	}

	pg := syscall.Getpgrp()
	if got, err := syscall.Getpgid(pg); err != nil || got != pg || pg <= 1 {
		t.Skipf("own group leader %d is not a live leader; own-group arm untestable", pg)
	}
	own, err := os.FindProcess(pg)
	if err != nil {
		t.Fatalf("find own leader: %v", err)
	}
	if leadsForeignGroup(own) {
		t.Error("the caller's own process group was accepted")
	}
}

// TestSignalProcessTree_ReapedLeaderStillReachesSurvivors (#3443 review): once
// the leader is reaped, members left in its group must still be signalled.
func TestSignalProcessTree_ReapedLeaderStillReachesSurvivors(t *testing.T) {
	leader := exec.Command("sh", "-c", "sleep 300 >/dev/null 2>&1 & echo $!")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := leader.Output() // leader exits and is reaped; its child survives
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var child int
	if _, err := fmt.Sscanf(string(out), "%d", &child); err != nil {
		t.Fatalf("parse child pid %q: %v", out, err)
	}
	pgid := leader.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	if !signalProcessTree(leader.Process, syscall.SIGKILL) {
		t.Fatal("signalProcessTree reported nothing signalled with a survivor in the group")
	}
	if !processGroupGone(pgid, 2*time.Second) {
		t.Errorf("survivor %d of reaped leader %d's group was not signalled", child, pgid)
	}
}

// TestSignalProcessTree_StaleProcessUnderForkLoadSparesOwnGroup (#3443) guards
// the #3454 predicates under the fork load of a concurrent gate. It is not a
// reproduction of the exit 241: kill(-pid) reaches only the group whose ID is
// pid, and a live group ID is never reused, so a stale leader cannot alias the
// caller's group. The exit-241 sender is unidentified (#3446): no daemon path
// that signals the test binary's group was found. What it pins is the
// explicit refusal: the caller's own group and pid 1 are never accepted for a
// group signal (removing that check in leadsForeignGroup fails this test), a
// reaped leader with an empty group is not signalled while pids churn, and a
// live foreign leader still is (the positive control, so the guard cannot
// simply disable group signals). SIGTERM is caught so any stray group signal
// is reported instead of killing the binary.
func TestSignalProcessTree_StaleProcessUnderForkLoadSparesOwnGroup(t *testing.T) {
	caught := make(chan os.Signal, 16)
	signal.Notify(caught, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(caught) })

	stop := make(chan struct{})
	churned := make(chan struct{})
	go func() {
		defer close(churned)
		for {
			select {
			case <-stop:
				return
			default:
				_ = exec.Command("true").Run()
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-churned })

	own := syscall.Getpgrp()
	if ownProc, err := os.FindProcess(own); err == nil {
		if leadsForeignGroup(ownProc) || orphanedForeignGroup(ownProc) {
			t.Error("the caller's own process group was accepted for a group signal")
		}
	}
	if init, err := os.FindProcess(1); err == nil {
		if leadsForeignGroup(init) || orphanedForeignGroup(init) {
			t.Error("pid 1 was accepted; kill(-1) would broadcast")
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < 200 && time.Now().Before(deadline); i++ {
		stale := exec.Command("true")
		stale.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := stale.Run(); err != nil {
			t.Fatalf("run: %v", err)
		}
		if signalProcessTree(stale.Process, syscall.SIGTERM) {
			t.Fatalf("iteration %d: a reaped leader with an empty group was signalled", i)
		}
	}

	leader := startGroupLeader(t)
	exited := make(chan struct{})
	go func() { _ = leader.Wait(); close(exited) }()
	if !signalProcessTree(leader.Process, syscall.SIGTERM) {
		t.Fatal("a live foreign group leader was not signalled under fork load")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Error("the live leader survived SIGTERM")
	}

	select {
	case sig := <-caught:
		t.Errorf("%v reached the test's own process group", sig)
	case <-time.After(200 * time.Millisecond):
	}
}
