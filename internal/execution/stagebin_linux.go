package execution

import (
	"io/fs"
	"syscall"
	"time"
)

// statIdentity is fi's device, inode and status-change time.
func statIdentity(fi fs.FileInfo) (dev, ino uint64, ctime time.Time, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, time.Time{}, false
	}
	return uint64(st.Dev), st.Ino, time.Unix(st.Ctim.Unix()), true
}
