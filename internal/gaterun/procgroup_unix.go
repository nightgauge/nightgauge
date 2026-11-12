//go:build !windows

package gaterun

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolate puts a gate step in its own process group, so stopping the group
// stops everything the step spawned, and makes cancellation kill the group.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// stopGroup sends SIGTERM to the process group pgid, or SIGKILL when force.
func stopGroup(pgid int, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-pgid, sig)
}

// groupAlive reports whether any process of group pgid exists. Only ESRCH
// proves the group is gone; EPERM means a member exists that we may not signal.
func groupAlive(pgid int) bool {
	return !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// gateSignals are the signals gate-run handles itself, so killing it stops
// the running step's group instead of orphaning it.
var gateSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// signalGroup sends sig to the process group pgid.
func signalGroup(pgid int, sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		s = syscall.SIGTERM
	}
	return syscall.Kill(-pgid, s)
}

// pidAlive reports whether process pid exists. EPERM means it exists but
// belongs to someone else; callers confirm identity by its start time.
func pidAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
