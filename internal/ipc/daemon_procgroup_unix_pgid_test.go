//go:build unix

package ipc

import (
	"syscall"
	"testing"
)

// TestNewCmd_DaemonLeadsItsOwnProcessGroup pins #3443: a group signal from the
// daemon side must not be able to reach go test's process group.
func TestNewCmd_DaemonLeadsItsOwnProcessGroup(t *testing.T) {
	cmd := newCmd(t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	defer stopDaemon(cmd)
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid == syscall.Getpgrp() {
		t.Fatalf("daemon pgid %d is the test process's group; a group SIGTERM would kill go test", pgid)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("daemon pgid %d, want it to lead its own group (%d)", pgid, cmd.Process.Pid)
	}
}
