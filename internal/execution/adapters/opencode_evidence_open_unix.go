//go:build unix

package adapters

import (
	"os"
	"syscall"
)

// openCodeToolEvidenceOpenFlags appends to tool-calls.jsonl without following
// a symbolic link at its path (O_NOFOLLOW) or blocking on a FIFO with no
// reader (O_NONBLOCK). The open descriptor's own Stat then refuses anything
// but a regular file.
const openCodeToolEvidenceOpenFlags = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
