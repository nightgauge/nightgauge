package platform

import (
	"fmt"
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// CanonicalizeResult reports the outcome of a CanonicalizeRuns call so callers
// (the backfill CLI, the IPC sync path) can surface dedup/noise counts.
type CanonicalizeResult struct {
	// Input is the number of records handed to CanonicalizeRuns.
	Input int
	// ParseSkipped is the number of records dropped because StartedAt did not
	// parse as RFC3339 (they cannot be grouped or ingested).
	ParseSkipped int
	// Groups is the number of distinct logical runs after folding duplicates.
	Groups int
	// DroppedNoise is the number of merged runs dropped by the real-runs-only
	// noise filter (zero stages AND zero cost AND zero duration).
	DroppedNoise int
	// Merged is the number of surviving canonical runs returned (Groups minus
	// DroppedNoise).
	Merged int
	// Earliest / Latest bound the StartedAt range of the surviving runs (zero
	// values when no run survives).
	Earliest time.Time
	Latest   time.Time
}

// CanonicalizeRuns folds the duplicate records the local history writes per
// logical run into a single record, then filters pure-synthetic noise. It is
// deterministic: the same input slice always yields the same output regardless
// of map iteration order.
//
// Grouping: records are keyed by (issueNumber, StartedAt truncated to the
// second in UTC). This collapses the local-offset and UTC-synthetic variants of
// the same run — they describe the same absolute instant. Records whose
// StartedAt fails to parse are dropped (counted as ParseSkipped).
//
// Merge: within a group the fields are combined to recover the most complete
// view of the run — terminal outcome from the latest record, max cost / max
// duration, the latest CompletedAt, the stage map with the most entries, and
// the first non-empty branch/labels/execution-mode (preferring a real mode over
// the synthetic "automatic").
//
// Noise filter (real-runs-only): a merged run is dropped only when it has zero
// stages AND zero cost AND zero duration — pure synthetic noise. Cancelled runs
// with real stages/cost/duration are kept: a cancelled run is real history.
func CanonicalizeRuns(records []state.V2RunRecord) ([]state.V2RunRecord, CanonicalizeResult) {
	res := CanonicalizeResult{Input: len(records)}

	type groupKey struct {
		issue  int
		second int64 // UTC unix seconds, truncated
	}

	// Preserve first-seen order of group keys for deterministic output ordering.
	groups := make(map[groupKey][]state.V2RunRecord)
	var order []groupKey

	for _, rec := range records {
		t, err := time.Parse(time.RFC3339, rec.StartedAt)
		if err != nil {
			res.ParseSkipped++
			continue
		}
		key := groupKey{issue: rec.IssueNumber, second: t.UTC().Truncate(time.Second).Unix()}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], rec)
	}

	res.Groups = len(order)

	var out []state.V2RunRecord
	for _, key := range order {
		merged := mergeGroup(groups[key])
		if isNoiseRun(merged) {
			res.DroppedNoise++
			continue
		}
		out = append(out, merged)

		// Track surviving date range (StartedAt already parses — checked above).
		if t, err := time.Parse(time.RFC3339, merged.StartedAt); err == nil {
			tu := t.UTC()
			if res.Earliest.IsZero() || tu.Before(res.Earliest) {
				res.Earliest = tu
			}
			if res.Latest.IsZero() || tu.After(res.Latest) {
				res.Latest = tu
			}
		}
	}

	res.Merged = len(out)
	return out, res
}

// mergeGroup folds a group of records (all the same logical run) into one. The
// group is sorted by RecordedAt (then a stable tiebreak) so the "terminal"
// record — the one with the greatest recorded_at — drives the final outcome.
func mergeGroup(group []state.V2RunRecord) state.V2RunRecord {
	// Sort ascending by recorded_at; tiebreak on completed_at then started_at so
	// the ordering is fully deterministic and independent of input order.
	sorted := make([]state.V2RunRecord, len(group))
	copy(sorted, group)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := recordedAtKey(sorted[i]), recordedAtKey(sorted[j])
		if ri != rj {
			return ri < rj
		}
		ci, cj := sorted[i].CompletedAt, sorted[j].CompletedAt
		if ci != cj {
			return ci < cj
		}
		return sorted[i].StartedAt < sorted[j].StartedAt
	})

	// Seed from the earliest record (carries the run's identity/start).
	merged := sorted[0]

	// CompletedAt: latest non-empty across the group.
	// Outcome/OutcomeType: from the terminal record (greatest recorded_at) that
	// carries a non-empty outcome — prefer a real terminal outcome over an
	// interim one.
	// Cost/Duration: max across the group.
	// Branch/Labels: first non-empty.
	// ExecutionMode: first non-empty that is not "automatic", else any non-empty.
	// Stages: the member with the most stage entries.

	var latestCompleted string
	var terminalOutcome, terminalOutcomeType string
	var maxCost float64
	var maxDuration int64
	var firstBranch string
	var firstLabels []string
	var realMode, anyMode string
	bestStages := merged.Stages
	bestStageCount := len(merged.Stages)

	for _, rec := range sorted {
		if rec.CompletedAt > latestCompleted {
			latestCompleted = rec.CompletedAt
		}
		// Walk in ascending recorded_at order, so the last non-empty outcome we
		// see is the terminal one.
		if rec.Outcome != "" {
			terminalOutcome = rec.Outcome
			terminalOutcomeType = rec.OutcomeType
		}
		if rec.Tokens.EstimatedCostUSD > maxCost {
			maxCost = rec.Tokens.EstimatedCostUSD
		}
		if rec.TotalDuration > maxDuration {
			maxDuration = rec.TotalDuration
		}
		if firstBranch == "" && rec.Branch != "" {
			firstBranch = rec.Branch
		}
		if firstLabels == nil && len(rec.Labels) > 0 {
			firstLabels = rec.Labels
		}
		if rec.ExecutionMode != "" {
			if anyMode == "" {
				anyMode = rec.ExecutionMode
			}
			if realMode == "" && rec.ExecutionMode != "automatic" {
				realMode = rec.ExecutionMode
			}
		}
		if len(rec.Stages) > bestStageCount {
			bestStages = rec.Stages
			bestStageCount = len(rec.Stages)
		}
	}

	merged.CompletedAt = latestCompleted
	if terminalOutcome != "" {
		merged.Outcome = terminalOutcome
		merged.OutcomeType = terminalOutcomeType
	}
	merged.Tokens.EstimatedCostUSD = maxCost
	merged.TotalDuration = maxDuration
	merged.Branch = firstBranch
	merged.Labels = firstLabels
	if realMode != "" {
		merged.ExecutionMode = realMode
	} else {
		merged.ExecutionMode = anyMode
	}
	merged.Stages = bestStages

	return merged
}

// recordedAtKey returns a sortable key for a record's recorded_at. Records with
// an unparseable/empty recorded_at sort first (empty string), which keeps the
// ordering deterministic without panicking.
func recordedAtKey(rec state.V2RunRecord) string {
	if rec.RecordedAt != "" {
		if t, err := time.Parse(time.RFC3339, rec.RecordedAt); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}

// isNoiseRun reports whether a merged run is pure synthetic noise: zero stages
// AND zero cost AND zero duration. Such records carry no real history and would
// only pollute the dashboard.
func isNoiseRun(rec state.V2RunRecord) bool {
	return len(rec.Stages) == 0 &&
		rec.Tokens.EstimatedCostUSD == 0 &&
		rec.TotalDuration == 0
}

// DateRange formats the surviving run StartedAt range for human-readable
// reporting. Returns "(none)" when no run survived.
func (r CanonicalizeResult) DateRange() string {
	if r.Earliest.IsZero() || r.Latest.IsZero() {
		return "(none)"
	}
	return fmt.Sprintf("%s .. %s",
		r.Earliest.Format("2006-01-02"),
		r.Latest.Format("2006-01-02"))
}
