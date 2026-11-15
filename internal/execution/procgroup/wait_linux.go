//go:build linux

package procgroup

import "golang.org/x/sys/unix"

// waitLeaderExitNoReap blocks until the process pid has exited, leaving it a
// zombie: WNOWAIT keeps it unreaped, so its pid, and the process-group id it
// leads, cannot be handed to another process until cmd.Wait reaps it (#3303).
func waitLeaderExitNoReap(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}
