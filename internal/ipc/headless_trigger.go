package ipc

import (
	"log"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// TriggerIssueBusy reports whether repo's issue has a live or queued run on
// this daemon: it is in the serve queue, or in the run registry every run
// reports to (#3586). A headless daemon refuses a trigger for such an issue.
func (s *Server) TriggerIssueBusy(repo string, issueNumber int) bool {
	if s.scheduler != nil && s.scheduler.QueueHas(repo, issueNumber) {
		return true
	}
	return s.issueRunningHere(repo, issueNumber)
}

// TriggerIssueRunning reports whether repo's issue is in the serve queue or the
// run registry, deliberately ignoring the registered busy checks: a headless
// trigger that waited for a slot is itself one of those, so it re-checks here
// to learn whether another run started meanwhile (#3673).
func (s *Server) TriggerIssueRunning(repo string, issueNumber int) bool {
	if s.scheduler != nil && s.scheduler.QueueHas(repo, issueNumber) {
		return true
	}
	return s.issueInRunRegistry(repo, issueNumber)
}

// ValidateTriggerPin decides a trigger's requested adapter and model with the
// validation queue.validatePin applies (#1656), and returns the public
// refusal, or "" when this machine can serve the pair (#3586).
func (s *Server) ValidateTriggerPin(repo, adapter, model string) string {
	if err := s.validateRemotePin(adapter, model, s.remotePinDeps(repo)); err != nil {
		log.Printf("trigger: %s requested pin refused (#1656): %v", repo, err)
		return orchestrator.RemotePinPublic(err)
	}
	return ""
}

// AddIssueBusyCheck registers a source of runs that never enter the run
// registry, so TriggerIssueBusy and a cloud queue_add see them as running.
func (s *Server) AddIssueBusyCheck(check func(repo string, issueNumber int) bool) {
	if check == nil {
		return
	}
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	s.busyChecks = append(s.busyChecks, check)
}

func (s *Server) runningElsewhere(repo string, issueNumber int) bool {
	s.busyMu.Lock()
	checks := append([]func(string, int) bool(nil), s.busyChecks...)
	s.busyMu.Unlock()
	for _, check := range checks {
		if check(repo, issueNumber) {
			return true
		}
	}
	return false
}
