//go:build unix

package opencodeplugin

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// dirUnwritable reports, with a single read-only access(2) syscall, whether
// dir exists and this process cannot write to it. opencode 1.18.30's own
// Npm.install returns immediately, installing nothing, the moment its own
// directory-writability check fails this way — see OperatorInstallSatisfied
// for why that makes such a directory read satisfied rather than
// unsatisfied.
//
// A directory that does not exist at all reports writable (false) here: an
// absent directory is not the shape opencode's own writability check
// guards against (it runs against a directory opencode's own config
// resolution already created), and OperatorInstallSatisfied's own
// node_modules/package.json checks already read unsatisfied for one holding
// nothing at all.
func dirUnwritable(dir string) bool {
	err := unix.Access(dir, unix.W_OK)
	return err != nil && !errors.Is(err, os.ErrNotExist)
}
