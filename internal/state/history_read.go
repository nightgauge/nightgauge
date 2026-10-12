package state

import (
	"fmt"
)

// recentRunDays is how many daily history files RecentRuns reads.
const recentRunDays = 30

// RecentRuns returns at most limit of repoRoot's finished runs from its
// execution history, most recent first, for a reader that only reports on
// them (a conversation's pipeline status, #1587). It reads the daily records
// rather than the index because a record carries what a reader must filter
// on and the index does not: the run's repository and its visibility
// (#2400). A repository that never recorded a run is an empty answer. It
// writes nothing, and an unresolvable root is an error, never a read
// relative to the working directory.
func RecentRuns(repoRoot string, limit int) ([]V2RunRecord, error) {
	hw := NewHistoryWriter(repoRoot)
	if hw.dir == "" {
		return nil, fmt.Errorf("recent runs: the repository has no pipeline state directory")
	}
	if limit <= 0 {
		return nil, nil
	}
	records, err := hw.ReadRecentV2(limit, recentRunDays)
	if err != nil {
		return nil, err
	}
	out := make([]V2RunRecord, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		out = append(out, records[i])
	}
	return out, nil
}
