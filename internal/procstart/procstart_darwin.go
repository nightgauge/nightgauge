//go:build darwin

package procstart

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Start is pid's start time in Unix microseconds.
func Start(pid int) (int64, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, fmt.Errorf("process %d not found", pid)
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec), nil
}
