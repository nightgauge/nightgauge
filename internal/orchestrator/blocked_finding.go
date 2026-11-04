package orchestrator

import (
	"encoding/json"

	"log"
	"os"
)

// blockedFindingRecord is the subset of the extension's BlockedFinding
// (packages/nightgauge/src/utils/blockedFinding.ts) the scheduler needs to
// skip an issue and to recognise the same finding across cycles (#2914).
type blockedFindingRecord struct {
	SchemaVersion string `json:"schema_version"`
	IssueNumber   int    `json:"issue_number"`
	SignalType    string `json:"signal_type"`
	Reason        string `json:"reason"`
	RecordedAt    string `json:"recorded_at"`
}

// identity distinguishes one recorded finding from a later re-recording, so
// the skip is logged once per finding rather than once per cycle.
func (r blockedFindingRecord) identity() string {
	return r.SignalType + "|" + r.RecordedAt
}

// readBlockedFinding reads the issue's recorded out-of-scope finding. It
// accepts a file only when it parses and passes the same checks as the
// extension's reader: schema_version "1.0", a non-empty signal_type, and an
// issue_number matching the issue. Anything else reads as no finding, so an
// unreadable file can never wedge an issue out of dispatch.
func readBlockedFinding(repoRoot string, issue int) (blockedFindingRecord, bool) {
	var rec blockedFindingRecord
	path := BlockedFindingPath(repoRoot, issue)
	if path == "" {
		return rec, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, false
	}
	if json.Unmarshal(b, &rec) != nil {
		return blockedFindingRecord{}, false
	}
	if rec.SchemaVersion != "1.0" || rec.SignalType == "" || rec.IssueNumber != issue {
		return blockedFindingRecord{}, false
	}
	return rec, true
}

// blockedFindingFor resolves the root the run itself would use (as
// architectureApprovalFileGranted does) and reads the finding there. The file
// is re-read every cycle, so blocked.clearFinding — which deletes it — makes
// the issue a candidate again on the very next pass.
func (as *AutonomousScheduler) blockedFindingFor(repo string, issue int) (blockedFindingRecord, bool) {
	root := as.workspaceRoot
	if as.scheduler != nil {
		if resolved, err := as.scheduler.resolveRunRoot(repo); err == nil && resolved != "" {
			root = resolved
		}
	}
	if root == "" {
		return blockedFindingRecord{}, false
	}
	return readBlockedFinding(root, issue)
}

// noteBlockedFindingSkip logs the skip only when this finding has not been
// logged before. Caller must NOT hold as.mu.
func (as *AutonomousScheduler) noteBlockedFindingSkip(key string, rec blockedFindingRecord) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.blockedFindingLogged == nil {
		as.blockedFindingLogged = make(map[string]string)
	}
	id := rec.identity()
	if as.blockedFindingLogged[key] == id {
		return
	}
	as.blockedFindingLogged[key] = id
	log.Printf("autonomous: skipping %s — unresolved blocked finding (%s: %s); "+
		"clear it with blocked.clearFinding to re-admit", key, rec.SignalType, rec.Reason)
}

// forgetClearedBlockedFindings drops de-dup entries for issues that had no
// finding this pass, so a finding recorded again later is logged again.
func (as *AutonomousScheduler) forgetClearedBlockedFindings(seen map[string]bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	for key := range as.blockedFindingLogged {
		if !seen[key] {
			delete(as.blockedFindingLogged, key)
		}
	}
}
