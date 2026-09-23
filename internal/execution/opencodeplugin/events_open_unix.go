//go:build unix

package opencodeplugin

import (
	"os"
	"syscall"
)

// eventsOpenFlags opens the resolved events file without following a
// symlink swapped in after the containment check (O_NOFOLLOW) and without
// blocking on a FIFO (O_NONBLOCK), whose type the open descriptor's own Stat
// then refuses (#1653).
const eventsOpenFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
