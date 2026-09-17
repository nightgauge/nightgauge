package gittest

import (
	"errors"
	"io/fs"
)

// scratchOutputDirs lists directory names that a module-root test walk must
// skip because they are scratch output a concurrently-running build step
// creates and deletes, not source the walk is scanning for. Keyed by name,
// not path, because CI_LOCAL_LOG_DIR can relocate scripts/ci-local.sh's own
// entry (see IsScratchOutputDir doc).
var scratchOutputDirs = map[string]bool{
	".ci-local-logs": true,
}

// IsScratchOutputDir reports whether name is a scratch-output directory a
// module-root walk guard must not descend into. Today this is exactly
// scripts/ci-local.sh's LOG_DIR (".ci-local-logs" by default); if
// CI_LOCAL_LOG_DIR relocates it, the walk stops seeing the churn rather than
// silently widening what it skips.
func IsScratchOutputDir(name string) bool {
	return scratchOutputDirs[name]
}

// TolerateConcurrentScratchWrite reports whether a filepath.Walk/WalkDir
// callback's non-nil err is a mid-walk ENOENT — a file or directory another
// process (a concurrently running build step) removed between the walk's
// directory read and its stat/lstat of that entry. Callers skip exactly this
// entry (return nil / continue) and propagate every other error unchanged;
// this must never become a blanket "ignore all walk errors".
func TolerateConcurrentScratchWrite(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
