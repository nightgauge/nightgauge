//go:build windows

package gaterun

import (
	"os"
	"os/exec"
)

// isolate is a no-op on Windows: there are no POSIX process groups, so a step
// is stopped by its own pid.
func isolate(*exec.Cmd) {}

// stopGroup kills the process pgid (on Windows, the step's pid).
func stopGroup(pgid int, _ bool) error {
	p, err := os.FindProcess(pgid)
	if err != nil {
		return nil
	}
	return p.Kill()
}

// groupAlive reports whether the step's process still exists.
func groupAlive(pgid int) bool { return pidAlive(pgid) }

// gateSignals: Windows delivers only os.Interrupt to a console process.
var gateSignals = []os.Signal{os.Interrupt}

// signalGroup kills the step's process: Windows cannot forward a signal.
func signalGroup(pgid int, _ os.Signal) error { return stopGroup(pgid, true) }

// pidAlive reports whether process pid exists.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
