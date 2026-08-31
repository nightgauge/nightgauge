package execution

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	// Started WITHOUT Setpgid: it shares the test binary's group, so
	// kill(-pid) finds no group led by this pid and returns ESRCH.
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
