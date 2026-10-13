//go:build unix

package sessionlog

import (
	"os"
	"syscall"
)

// appendOpenFlags appends to the log without following a symlink at its path
// (O_NOFOLLOW) and without blocking on a FIFO with no reader (O_NONBLOCK).
// The open descriptor's own Stat then refuses anything but a regular file. No
// O_TRUNC: a later run never truncates an earlier one's lines.
const appendOpenFlags = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
