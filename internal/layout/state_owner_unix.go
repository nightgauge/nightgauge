//go:build unix

package layout

import (
	"io/fs"
	"os"
	"syscall"
)

// stateDirOwnedByOther reports whether info belongs to a user other than the
// one running this process. A state root someone else owns can be read or
// replaced by them, so it is refused (ADR-024 § 17).
func stateDirOwnedByOther(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) != os.Getuid()
}
