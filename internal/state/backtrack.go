package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// BacktrackEvent is one backward stage transition a run took (#3081). It
// lives on RuntimeState and is copied to V2RunRecord.Backtracks, so retry
// analysis reads it from durable history instead of the extension's
// PipelineState, which is rebuilt from Go snapshots and dropped it.
type BacktrackEvent struct {
	FromStage  string `json:"from_stage"`
	ToStage    string `json:"to_stage"`
	SignalType string `json:"signal_type,omitempty"`
	Reason     string `json:"reason,omitempty"`
	At         string `json:"at"`
}

// Bounds on what a run's backtrack list may hold (#3081). The list reaches
// the durable run record, and pipeline.recordBacktrack lets a local process
// append to it, so it is capped: a run past MaxBacktracks has looped far
// beyond any backtrack limit, and the first entries already say so.
const (
	MaxBacktracks          = 64
	maxBacktrackSignalLen  = 128
	maxBacktrackReasonRune = 1024
)

// RecordBacktrack appends one backtrack to the run. A blank from or to stage
// records nothing: a backtrack names both ends. The signal type and reason are
// clipped, and nothing is appended past MaxBacktracks.
func (rs *RuntimeState) RecordBacktrack(from, to, signalType, reason string, at time.Time) {
	if from == "" || to == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.Backtracks) >= MaxBacktracks {
		return
	}
	rs.Backtracks = append(rs.Backtracks, BacktrackEvent{
		FromStage:  from,
		ToStage:    to,
		SignalType: clipHistoryRunes(signalType, maxBacktrackSignalLen),
		Reason:     clipHistoryRunes(reason, maxBacktrackReasonRune),
		At:         at.UTC().Format(time.RFC3339),
	})
}

// IsPipelineStage reports whether s names one of AllPipelineStages.
func IsPipelineStage(s string) bool {
	for _, st := range AllPipelineStages {
		if string(st) == s {
			return true
		}
	}
	return false
}

// readPlannedFiles returns planning-<N>.json's files_to_create and
// files_to_modify, in that order, or nil when the file is absent, unreadable
// or names neither list (#3081). The run record carries the list, because the
// planning context is pruned per clone and plan fidelity was lost with it.
func readPlannedFiles(repoRoot string, issue int) []string {
	if repoRoot == "" || issue <= 0 {
		return nil
	}
	dir, err := PipelineStateDir(repoRoot)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "planning-"+strconv.Itoa(issue)+".json"))
	if err != nil {
		return nil
	}
	var ctx struct {
		FilesToCreate []string `json:"files_to_create"`
		FilesToModify []string `json:"files_to_modify"`
	}
	if json.Unmarshal(data, &ctx) != nil {
		return nil
	}
	files := make([]string, 0, len(ctx.FilesToCreate)+len(ctx.FilesToModify))
	files = append(files, ctx.FilesToCreate...)
	files = append(files, ctx.FilesToModify...)
	if len(files) == 0 {
		return nil
	}
	return files
}
