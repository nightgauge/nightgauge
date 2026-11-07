package orchestrator

import (
	"os"
	"sync"
)

// Scheduler harnesses run on plain temp directories with no git checkout
// state; give their run snapshots a per-process home (#2813).
func init() {
	var once sync.Once
	var dir string
	var err error
	skillSnapshotFallbackBase = func() (string, error) {
		once.Do(func() { dir, err = os.MkdirTemp("", "ng-skillsnap-test-") })
		return dir, err
	}
}
