//go:build !darwin && !linux

package execution

import (
	"io/fs"
	"time"
)

// statIdentity reports nothing on a platform Nightgauge does not release for,
// so prepareStageBinary refuses the dispatch instead of running it unverified.
func statIdentity(fs.FileInfo) (dev, ino uint64, ctime time.Time, ok bool) {
	return 0, 0, time.Time{}, false
}
