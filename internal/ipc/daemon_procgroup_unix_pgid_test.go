//go:build unix

package ipc

import (
	"syscall"
	"testing"
)

// TestNewCmd_DaemonLeadsItsOwnProcessGroup pins the isolation from #3443: the
// daemon leads its own process group, outside go test's. It is defence in
// depth, not the fix for a known cause: the exit-241 sender is unidentified
// (#3446).
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
		t.Fatalf("daemon pgid %d is the test process's group; want it outside the test process group", pgid)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("daemon pgid %d, want it to lead its own group (%d)", pgid, cmd.Process.Pid)
	}
}
