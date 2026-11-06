package orchestrator

import "sort"

// HaltedRepos returns the repository-scoped halts (#1148) in st, sorted by
// repo. It is never nil, so a caller rendering JSON gets [] for "none".
// `autonomous status` reads it so a repo halt is not hidden behind a
// "Running" headline (#2748).
func HaltedRepos(st *AutonomousState) []RepoPauseRecord {
	if out := pausedReposSnapshot(st); out != nil {
		return out
	}
	return []RepoPauseRecord{}
}

// ArchitectureApprovalParked returns the Failed entries held for
// architecture approval, sorted by repo then number. ReleasePending entries
// are kept: they stay parked until every board reads Ready (#2717). Never nil.
func ArchitectureApprovalParked(st *AutonomousState) []FailedItem {
	out := []FailedItem{}
	if st == nil {
		return out
	}
	for _, f := range st.Failed {
		if f.hold() == HoldArchitectureApproval {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Number < out[j].Number
	})
	return out
}
