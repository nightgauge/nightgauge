package runstate

import (
	"fmt"
	"os"
	"strings"
)

// ResumeKind enumerates the possible orchestrator-start outcomes when a
// run-state.json exists or context files hint at prior state.
type ResumeKind string

const (
	ResumeFresh    ResumeKind = "fresh"
	ResumePaused   ResumeKind = "paused"
	ResumeAborted  ResumeKind = "aborted"
	ResumeRunning  ResumeKind = "running"
	ResumeOrphaned ResumeKind = "orphaned"
)

// ResumeDetection is the structured result of DetectResume. The caller
// (orchestrator/scheduler.go or the user-facing CLI) decides how to surface
// it: skip/log for autonomous, prompt for interactive.
type ResumeDetection struct {
	Kind    ResumeKind
	State   *RunState
	Choices []string // "resume" | "restart" | "discard" | "manual-pickup"
	Reason  string   // populated for ResumeRunning ("concurrent_run" | "stale_writer")
}

// DetectResume inspects run-state.json plus context-file presence and tells
// the caller what to do. `branch` and `hasContextFiles` are caller-provided
// because they require git/IO outside this package's responsibility.
//
// The result.Choices ordering matches the precedence the recovery UX should
// surface to the user.
func DetectResume(baseDir, branch string, hasContextFiles bool) (*ResumeDetection, error) {
	rs, err := Load(baseDir)
	if err != nil {
		return nil, err
	}
	if rs == nil {
		// #3237 fixture: branch present, no context file, no run-state.json
		// OR pre-Gap-1: context files present but no run-state.
		if branch != "" || hasContextFiles {
			return &ResumeDetection{
				Kind:    ResumeOrphaned,
				Choices: []string{"restart", "manual-pickup"},
			}, nil
		}
		return &ResumeDetection{Kind: ResumeFresh}, nil
	}
	switch rs.State {
	case StateRunning:
		last := lastAttempt(rs)
		alive := false
		if last != nil && last.PID != nil {
			alive = processAlive(*last.PID)
		}
		reason := "stale_writer"
		if alive {
			reason = "concurrent_run"
		}
		return &ResumeDetection{Kind: ResumeRunning, State: rs, Reason: reason}, nil
	case StatePaused:
		return &ResumeDetection{
			Kind:    ResumePaused,
			State:   rs,
			Choices: []string{"resume", "restart", "discard"},
		}, nil
	case StateAborted:
		return &ResumeDetection{
			Kind:    ResumeAborted,
			State:   rs,
			Choices: []string{"restart", "discard"},
		}, nil
	case StateCompleted, StateDiscarded:
		return &ResumeDetection{Kind: ResumeFresh, State: rs}, nil
	}
	return &ResumeDetection{Kind: ResumeFresh, State: rs}, nil
}

// HasContextFiles reports whether any pipeline context files for this issue
// still live in baseDir. Used by callers building a DetectResume invocation.
//
// Matches names ending with `-<issueNumber>.json` so it picks up
// issue-NNN.json, planning-NNN.json, dev-NNN.json, etc., but ignores
// run-state.json itself.
func HasContextFiles(baseDir string, issueNumber int) bool {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return false
	}
	suffix := fmt.Sprintf("-%d.json", issueNumber)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == FileName {
			continue
		}
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
