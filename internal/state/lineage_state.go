package state

import "time"

// LineageState is the session lineage of the most recent dispatch (#55): the
// key fields the next dispatch compares against, plus the warm prefix that
// stage left behind. Local runtime state only — AccountHash is a salted
// workspace hash and BuildV2Record never projects this struct onto history.
type LineageState struct {
	ID          string    `json:"id"`
	Stage       string    `json:"stage"`
	Adapter     string    `json:"adapter"`
	Model       string    `json:"model"`
	Provider    string    `json:"provider"`
	AccountHash string    `json:"accountHash"`
	ModelFamily string    `json:"modelFamily"`
	EffortClass string    `json:"effortClass"`
	DispatchAt  time.Time `json:"dispatchAt"`
}

// HandoffEvent records a dispatch that started a new lineage: the cache the
// previous stage warmed is cold for this one. Safe for history: it carries no
// account hash and no session id.
type HandoffEvent struct {
	FromStage             string  `json:"from_stage"`
	ToStage               string  `json:"to_stage"`
	Reason                string  `json:"reason"`
	FromLineage           string  `json:"from_lineage"`
	ToLineage             string  `json:"to_lineage"`
	EstimatedColdStartUSD float64 `json:"estimated_cold_start_usd"`
}

// StageLineageRecord is one stage's lineage attribution for history.
type StageLineageRecord struct {
	LineageID       string `json:"lineageId,omitempty"`
	ResumeAttempted bool   `json:"resumeAttempted,omitempty"`
	ResumeAccepted  bool   `json:"resumeAccepted,omitempty"`
}

// LastLineage returns a copy of the most recent dispatch's lineage, or nil.
func (rs *RuntimeState) LastLineage() *LineageState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.LastLineageState == nil {
		return nil
	}
	c := *rs.LastLineageState
	return &c
}

// RecordLineage stores the dispatch's lineage as the new comparison point,
// its per-stage attribution, and the handoff event when one occurred.
func (rs *RuntimeState) RecordLineage(cur LineageState, rec StageLineageRecord, handoff *HandoffEvent) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.LastLineageState = &cur
	if rs.StageLineages == nil {
		rs.StageLineages = make(map[string]StageLineageRecord)
	}
	rs.StageLineages[cur.Stage] = rec
	if handoff != nil {
		rs.HandoffEvents = append(rs.HandoffEvents, *handoff)
	}
}

// StageLineage returns the recorded lineage attribution for a stage.
func (rs *RuntimeState) StageLineage(stage PipelineStage) StageLineageRecord {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.StageLineages[string(stage)]
}

// LastCompletedStage returns a copy of the most recent completed stage result.
func (rs *RuntimeState) LastCompletedStage() (StageResult, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.CompletedStages) == 0 {
		return StageResult{}, false
	}
	return rs.CompletedStages[len(rs.CompletedStages)-1], true
}
