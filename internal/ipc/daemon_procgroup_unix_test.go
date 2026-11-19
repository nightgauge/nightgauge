//go:build unix

package ipc

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isolateProcessGroup starts the serve daemon as the leader of its own process
// group (#3443). In the test binary's group, which is also go test's and the
// gate's shard wrapper's, any group signal resolved on the daemon side would
// SIGTERM the whole shard: exit 241 under concurrent gate load.
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
