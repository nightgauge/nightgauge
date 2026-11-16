//go:build darwin

package procgroup

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinPPID is idtype_t P_PID from <sys/wait.h>; x/sys/unix does not export
// it, nor a Waitid wrapper, for darwin.
const darwinIDTypePID = 1

// waitLeaderExitNoReap blocks until the process pid has exited, leaving it a
// zombie: WNOWAIT keeps it unreaped, so its pid, and the process-group id it
// leads, cannot be handed to another process until cmd.Wait reaps it (#3303).
func waitLeaderExitNoReap(pid int) error {
	var info [128]byte // siginfo_t is 104 bytes on darwin
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, darwinIDTypePID, uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])), uintptr(unix.WEXITED|unix.WNOWAIT), 0, 0)
		if errno != syscall.EINTR {
			if errno != 0 {
				return errno
			}
			return nil
		}
	}
}
