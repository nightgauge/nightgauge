package workorder

import (
	"sort"
	"strconv"
	"strings"

	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// ProgramStatus is one program's progress: how many of its issues are open,
// closed and validated, and which are dispatchable now. Its JSON shape is
// documented in docs/GO_BINARY.md § Handoff and Work-Order Operations.
type ProgramStatus struct {
	ID       string   `json:"id"`
	Rank     *int     `json:"rank,omitempty"`
	Title    string   `json:"title,omitempty"`
	Selector string   `json:"selector"`
	Repos    []string `json:"repos"`
	// Total is every issue the selector labels in the program's repos.
	Total  int `json:"total"`
	Open   int `json:"open"`
	Closed int `json:"closed"`
	// Validated counts closed issues that pass isValidated.
	Validated int `json:"validated"`
	// Dispatchable counts open issues that are Ready on their board and
	// unblocked under the scheduler's rule — Rank's Ready list.
	Dispatchable     int      `json:"dispatchable"`
	DispatchableRefs []string `json:"dispatchable_refs"`
	// Blocked is the program's Ready issues that are blocked, with why.
	Blocked []Item `json:"blocked"`
}

// Status reports each program's progress, in rank order. issues is every
// issue the programs' selectors label, in any state; ranked is Rank over the
// same programs file, which supplies the dispatchable frontier so status and
// `nightgauge next` cannot disagree. Status is pure and deterministic.
func Status(f *File, issues []pkgtypes.Issue, ranked *Result) []ProgramStatus {
	byID := map[string]ProgramResult{}
	if ranked != nil {
		for _, p := range ranked.Programs {
			byID[p.ID] = p
		}
	}

	out := []ProgramStatus{}
	for _, p := range f.Ordered() {
		sel := f.Selector(p)
		st := ProgramStatus{
			ID:               p.ID,
			Rank:             p.Rank,
			Title:            p.Title,
			Selector:         sel,
			Repos:            append([]string{}, p.Repos...),
			DispatchableRefs: []string{},
			Blocked:          []Item{},
		}
		seen := map[string]bool{}
		for _, is := range issues {
			key := strings.ToLower(is.Repo) + "#" + strconv.Itoa(is.Number)
			if seen[key] || !hasLabel(is.Labels, sel) || !inRepos(is.Repo, p.Repos) {
				continue
			}
			seen[key] = true
			st.Total++
			if strings.EqualFold(is.State, "OPEN") {
				st.Open++
				continue
			}
			st.Closed++
			if isValidated(is) {
				st.Validated++
			}
		}
		if pr, ok := byID[p.ID]; ok {
			for _, it := range pr.Ready {
				st.DispatchableRefs = append(st.DispatchableRefs, it.Ref)
			}
			sort.Strings(st.DispatchableRefs)
			st.Dispatchable = len(st.DispatchableRefs)
			st.Blocked = append(st.Blocked, pr.Blocked...)
		}
		out = append(out, st)
	}
	return out
}

// isValidated is the single definition of a validated program issue. Until
// the post-merge validation marker exists (#2574), it is an issue closed as
// completed; #2574 replaces this body with the marker read and nothing else
// changes. An issue closed as not planned is closed, never validated.
func isValidated(is pkgtypes.Issue) bool {
	return strings.EqualFold(is.State, "CLOSED") && strings.EqualFold(is.StateReason, "COMPLETED")
}

// Covers reports whether the program applies to repo ("owner/name"): its
// repos list names it, bare or qualified, or the list is empty.
func (p Program) Covers(repo string) bool {
	return inRepos(repo, p.Repos)
}
