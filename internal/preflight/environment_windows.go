//go:build windows

package preflight

import "os/exec"

// isolateProcessGroup falls back to killing the direct child; WaitDelay
// bounds the wait for any descendants holding the output pipes.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
