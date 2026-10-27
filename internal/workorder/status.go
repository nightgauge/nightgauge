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
	// Validated counts closed issues that carry the post-merge validation
	// marker (isValidated, #2574).
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
// `nightgauge next` cannot disagree. marked holds the keys (IssueKey) of the
// closed issues whose trusted comments carry the validation marker. Status is
// pure and deterministic.
func Status(f *File, issues []pkgtypes.Issue, ranked *Result, marked map[string]bool) []ProgramStatus {
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
			key := IssueKey(is.Repo, is.Number)
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
			if isValidated(is, marked) {
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

// isValidated is the single definition of a validated program issue: closed,
// and carrying the post-merge validation marker in a trusted comment (#2574).
// The marker is written only when the merge commit verified green, so an
// issue closed without a green merge — closed as not planned, or merged into
// a red main — is closed, never validated.
func isValidated(is pkgtypes.Issue, marked map[string]bool) bool {
	return strings.EqualFold(is.State, "CLOSED") && marked[IssueKey(is.Repo, is.Number)]
}

// IssueKey is the case-folded "owner/repo#N" key Status reads marked by.
func IssueKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}

// Covers reports whether the program applies to repo ("owner/name"): its
// repos list names it, bare or qualified, or the list is empty.
func (p Program) Covers(repo string) bool {
	return inRepos(repo, p.Repos)
}
