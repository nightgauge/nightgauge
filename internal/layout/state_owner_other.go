//go:build !unix

package layout

import "io/fs"

// stateDirOwnedByOther has no portable owner check off Unix; the directory's
// ACL is the operating system's concern there.
func stateDirOwnedByOther(fs.FileInfo) bool { return false }
