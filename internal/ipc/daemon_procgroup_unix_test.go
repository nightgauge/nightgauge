//go:build unix

package ipc

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isolateProcessGroup starts the serve daemon as the leader of its own process
// group (#3443). Exit 241 is the gate's shard wrapper reporting that a SIGTERM
// killed go test; the sender is unidentified (#3446). No daemon path that
// signals the test binary's group was found: kill(-pid) reaches only the group
// whose ID is pid, and a live group ID is never reused, so signalProcessTree
// cannot alias the caller's group (see
// TestSignalProcessTree_StaleProcessUnderForkLoadSparesOwnGroup). Isolation is
// defence in depth: any group signal the daemon sends stays in its own group.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stopDaemon interrupts the daemon, waits a bounded time, then SIGKILLs its
// whole group. The daemon no longer shares the test binary's group, so nothing
// else reaps the stage children it may leave behind.
func stopDaemon(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL) // reap stragglers left in the group
}
