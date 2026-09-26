package execution

import (
	"sync"
	"syscall"
	"time"
)

// liveStages is every stage process RunStage has spawned and not yet reaped,
// across every Manager in this process (#2171). A signal to `nightgauge run`
// must reach them all, and the command that owns the signal does not hold the
// Manager the scheduler built.
var liveStages = struct {
	sync.Mutex
	set map[*Execution]struct{}
}{set: map[*Execution]struct{}{}}

func trackLiveStage(ex *Execution) {
	liveStages.Lock()
	liveStages.set[ex] = struct{}{}
	liveStages.Unlock()
}

func untrackLiveStage(ex *Execution) {
	liveStages.Lock()
	delete(liveStages.set, ex)
	liveStages.Unlock()
}

// LiveStageCount reports how many stage processes are running.
func LiveStageCount() int {
	liveStages.Lock()
	defer liveStages.Unlock()
	return len(liveStages.set)
}

// StopAllStages stops every live stage's whole process group and waits for
// each to be reaped, so a process exiting on SIGINT or SIGTERM leaves no
// adapter child behind (#2171). Stages run in their own process group
// (Setpgid), so a terminal's Ctrl-C never reaches them: before this, an
// interrupted `nightgauge run` exited and orphaned its `opencode` child.
//
// Each stage is marked as operator-stopped, sent SIGTERM on its group, given
// grace to exit, then SIGKILLed; the wait after the kill is bounded by grace
// too, so a stuck reap cannot hang the shutdown. Returns how many stages it
// stopped.
func StopAllStages(grace time.Duration) int {
	liveStages.Lock()
	stages := make([]*Execution, 0, len(liveStages.set))
	for ex := range liveStages.set {
		stages = append(stages, ex)
	}
	liveStages.Unlock()

	var wg sync.WaitGroup
	for _, ex := range stages {
		wg.Add(1)
		go func(ex *Execution) {
			defer wg.Done()
			ex.stopRequested.Store(true)
			done := waitForExit(ex)
			signalProcessTree(ex.Process, syscall.SIGTERM)
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				signalProcessTree(ex.Process, syscall.SIGKILL)
				select {
				case <-done:
				case <-time.After(grace):
				}
			}
			if ex.Cancel != nil {
				ex.Cancel()
			}
		}(ex)
	}
	wg.Wait()
	return len(stages)
}
