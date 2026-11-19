//go:build !unix

package ipc

import (
	"os"
	"os/exec"
)

func isolateProcessGroup(*exec.Cmd) {}

func stopDaemon(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}
}
