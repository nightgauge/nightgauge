//go:build !windows

package preflight

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the check in its own process group and makes
// cancellation kill the whole group, so a child the check spawned (a shell's
// sleep, a curl retry loop) is reaped with it.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
