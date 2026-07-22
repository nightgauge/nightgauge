// Package teams implements agent team infrastructure: wave calculation,
// dependency detection, budget splitting, and file conflict detection.
package teams

import (
	"fmt"
	"sort"
	"strings"
)

// commonNonTargetFiles are files so frequently MENTIONED in issue prose that a
// shared mention is almost never a genuine same-file edit conflict. They never
// drive authoring-time wave serialization — a real double-edit of one of these
// is low-conflict and the runtime rebase / conflict-recovery handles it (#4074).
var commonNonTargetFiles = map[string]bool{
	"readme.md": true, "package.json": true, "package-lock.json": true,
	"changelog.md": true, "go.mod": true, "go.sum": true, "tsconfig.json": true,
	"makefile": true, "dockerfile": true, "license": true, ".gitignore": true,
	"yarn.lock": true, "pnpm-lock.yaml": true, "docker-compose.yml": true,
}

// serializableTargetFile reports whether a shared file is specific enough to
// justify auto-serializing two sub-issues that both list it. The file extractor
// is deliberately broad (it also feeds the runtime import-chain heuristic), so
// it surfaces bare prose mentions like "README.md" / "main.go". For the
// consequential authoring-time serialization we act ONLY on files that look like
// a deliberately-targeted source file: directory-qualified (lib/foo.dart) OR a
// compound basename (journal_entry_page.dart) — never a bare common doc/config
// file mentioned in passing. This prevents erasing legitimate parallelism when
// two unrelated sub-issues merely mention the same common file (#4074 review).
func serializableTargetFile(f string) bool {
	// Documentation paths never drive serialization (#79): two sub-issues
	// touching the same doc is not the #143/#144 same-file code collision this
	// guard exists to prevent, and a shared doc CITATION (epic #71: six
	// sub-issues all referencing one spike doc) must never erase an epic's
	// parallelism — that failure silently produced six sequential waves.
	lower := strings.ToLower(f)
	if strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") {
		return false
	}
	base := f
	if i := strings.LastIndexByte(f, '/'); i >= 0 {
		base = f[i+1:]
	}
	if commonNonTargetFiles[strings.ToLower(base)] {
		return false
	}
	if strings.Contains(f, "/") {
		return true // directory-qualified → a deliberately targeted path
	}
	// Bare filename: require a compound basename stem (underscore/hyphen) so a
	// specific source file (journal_entry_page.dart, my-widget.tsx) qualifies but
	// a bare common word (main.go, node.js, fine.css, in.md) does not.
	stem := base
	if i := strings.IndexByte(base, '.'); i >= 0 {
		stem = base[:i]
	}
	return strings.ContainsAny(stem, "_-")
}

// reachable reports whether `to` is reachable from `from` by following deps
// edges (transitive dependency). SerializeFileOverlaps uses it instead of a
// direct-edge check so it never injects an edge that contradicts — or forms a
// cycle with — a pre-existing transitive ordering (#4074 review).
func reachable(deps map[int][]int, from, to int) bool {
	if from == to {
		return true
	}
	seen := map[int]bool{from: true}
	stack := []int{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range deps[n] {
			if d == to {
				return true
			}
			if !seen[d] {
				seen[d] = true
				stack = append(stack, d)
			}
		}
	}
	return false
}

// SubIssue represents a decomposed issue for wave planning.
type SubIssue struct {
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	Files      []string `json:"files,omitempty"`
	Complexity string   `json:"complexity,omitempty"` // "simple", "medium", "complex"
}

// WaveAssignment groups issues into execution waves.
type WaveAssignment struct {
	WaveIndex int        `json:"waveIndex"`
	Issues    []SubIssue `json:"issues"`
}

// CalculateWaves performs topological sort using Kahn's algorithm
// to group issues into parallel execution waves.
// deps maps issue index → list of dependency indices.
func CalculateWaves(issues []SubIssue, deps map[int][]int) ([]WaveAssignment, error) {
	n := len(issues)
	if n == 0 {
		return nil, nil
	}

	// Build in-degree and successors
	inDegree := make([]int, n)
	successors := make([][]int, n)
	for i := range successors {
		successors[i] = []int{}
	}

	for node, depList := range deps {
		if node < 0 || node >= n {
			continue
		}
		for _, dep := range depList {
			if dep < 0 || dep >= n || dep == node {
				continue
			}
			inDegree[node]++
			successors[dep] = append(successors[dep], node)
		}
	}

	// Kahn's algorithm: process by wave level
	waveOf := make([]int, n)
	assigned := make([]bool, n)
	totalAssigned := 0

	// Wave 0: all nodes with in-degree 0
	wave := 0
	for totalAssigned < n {
		var frontier []int
		for i := 0; i < n; i++ {
			if !assigned[i] && inDegree[i] == 0 {
				frontier = append(frontier, i)
			}
		}

		if len(frontier) == 0 {
			// Cycle detected — force-assign highest in-degree unassigned node
			maxDeg := -1
			maxNode := -1
			for i := 0; i < n; i++ {
				if !assigned[i] && inDegree[i] > maxDeg {
					maxDeg = inDegree[i]
					maxNode = i
				}
			}
			if maxNode >= 0 {
				frontier = []int{maxNode}
				inDegree[maxNode] = 0
			} else {
				break
			}
		}

		for _, node := range frontier {
			waveOf[node] = wave
			assigned[node] = true
			totalAssigned++

			for _, succ := range successors[node] {
				inDegree[succ]--
			}
		}

		wave++
	}

	// Group by wave
	waveMap := make(map[int][]SubIssue)
	for i, w := range waveOf {
		waveMap[w] = append(waveMap[w], issues[i])
	}

	// Convert to sorted slice
	var waves []WaveAssignment
	for w := 0; w < wave; w++ {
		if issues, ok := waveMap[w]; ok && len(issues) > 0 {
			waves = append(waves, WaveAssignment{
				WaveIndex: w,
				Issues:    issues,
			})
		}
	}

	return waves, nil
}

// MergeWaves merges waves if total exceeds maxTeammates.
// Greedily merges smallest adjacent waves.
func MergeWaves(waves []WaveAssignment, maxTeammates int) []WaveAssignment {
	if maxTeammates <= 0 || len(waves) <= maxTeammates {
		return waves
	}

	for len(waves) > maxTeammates {
		// Find smallest adjacent pair
		minSize := int(^uint(0) >> 1)
		minIdx := 0
		for i := 0; i < len(waves)-1; i++ {
			size := len(waves[i].Issues) + len(waves[i+1].Issues)
			if size < minSize {
				minSize = size
				minIdx = i
			}
		}

		// Merge waves[minIdx] and waves[minIdx+1]
		merged := WaveAssignment{
			WaveIndex: waves[minIdx].WaveIndex,
			Issues:    append(waves[minIdx].Issues, waves[minIdx+1].Issues...),
		}
		waves = append(waves[:minIdx], append([]WaveAssignment{merged}, waves[minIdx+2:]...)...)
	}

	// Renumber
	for i := range waves {
		waves[i].WaveIndex = i
	}

	return waves
}

// FileConflict represents a potential merge conflict between parallel agents.
type FileConflict struct {
	Path     string `json:"path"`
	Issues   []int  `json:"issues"`
	Severity string `json:"severity"` // "error" (exact file) or "warning" (directory)
}

// DetectFileConflicts identifies files claimed by multiple issues.
func DetectFileConflicts(issues []SubIssue) []FileConflict {
	// Pass 1: exact file conflicts
	fileMap := make(map[string][]int)
	for _, issue := range issues {
		for _, f := range issue.Files {
			fileMap[f] = append(fileMap[f], issue.Number)
		}
	}

	var conflicts []FileConflict
	seen := make(map[string]bool) // pairKey for dedup

	for path, issueNums := range fileMap {
		if len(issueNums) >= 2 {
			sort.Ints(issueNums)
			key := fmt.Sprintf("%v", issueNums)
			if !seen[path+key] {
				seen[path+key] = true
				conflicts = append(conflicts, FileConflict{
					Path:     path,
					Issues:   issueNums,
					Severity: "error",
				})
			}
		}
	}

	// Pass 2: directory conflicts (only if no file-level error for the pair)
	dirMap := make(map[string]map[int]bool)
	for _, issue := range issues {
		for _, f := range issue.Files {
			dir := parentDir(f)
			if dir == "" {
				continue
			}
			if dirMap[dir] == nil {
				dirMap[dir] = make(map[int]bool)
			}
			dirMap[dir][issue.Number] = true
		}
	}

	for dir, issueSet := range dirMap {
		if len(issueSet) < 2 {
			continue
		}
		nums := make([]int, 0, len(issueSet))
		for n := range issueSet {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		key := fmt.Sprintf("%v", nums)
		if !seen[dir+key] {
			seen[dir+key] = true
			conflicts = append(conflicts, FileConflict{
				Path:     dir,
				Issues:   nums,
				Severity: "warning",
			})
		}
	}

	// Sort: errors first, then warnings
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Severity != conflicts[j].Severity {
			return conflicts[i].Severity == "error"
		}
		return conflicts[i].Path < conflicts[j].Path
	})

	return conflicts
}

// SerializeFileOverlaps deterministically serializes same-wave sub-issues that
// share a top-level EXACT target file. For each unordered pair (i, j) whose
// Files sets intersect on an exact path, if neither already depends on the
// other in deps, an edge is injected so the later-numbered issue depends on the
// earlier-numbered one (the same tie-break DetectDependencies' depKey uses).
// This forces the pair into adjacent waves and prevents the guaranteed merge
// conflict two parallel PRs owning the same file would produce once the first
// merges (the #143/#144 collision class).
//
// Directory-only overlaps are intentionally NOT serialized — they surface as
// "warning" conflicts via DetectFileConflicts and preserve legitimate
// parallelism. Only exact-file overlaps inject an edge here.
//
// Returns the augmented deps map (a copy; the input is not mutated) and the
// list of injected FileConflicts (Severity "error"), one per shared exact file
// that caused a new serialization edge.
func SerializeFileOverlaps(issues []SubIssue, deps map[int][]int) (map[int][]int, []FileConflict) {
	n := len(issues)

	// Copy deps so callers' maps are not mutated.
	augmented := make(map[int][]int, len(deps))
	for k, v := range deps {
		cp := make([]int, len(v))
		copy(cp, v)
		augmented[k] = cp
	}

	if n < 2 {
		return augmented, nil
	}

	// Index issues by their owned exact files (mirrors DetectFileConflicts'
	// exact-file pass, but resolved to slice indices for edge injection).
	fileToIdx := make(map[string][]int)
	for idx, issue := range issues {
		for _, f := range issue.Files {
			fileToIdx[f] = append(fileToIdx[f], idx)
		}
	}

	// Deterministic iteration over shared files.
	sharedFiles := make([]string, 0, len(fileToIdx))
	for f, idxs := range fileToIdx {
		// Only serialize on a file shared by ≥2 issues that is a deliberate
		// source target — never a bare common doc/config mentioned in prose.
		if len(idxs) >= 2 && serializableTargetFile(f) {
			sharedFiles = append(sharedFiles, f)
		}
	}
	sort.Strings(sharedFiles)

	var conflicts []FileConflict
	for _, f := range sharedFiles {
		idxs := fileToIdx[f]
		// Inject an edge for every unordered pair sharing this file.
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				i, j := idxs[a], idxs[b]
				// Deterministic direction: later issue number depends on the
				// earlier one (same convention as depKey: dependent depends on
				// dependency).
				dependent, dependency := i, j
				if issues[i].Number < issues[j].Number {
					dependent, dependency = j, i
				}

				if reachable(augmented, dependent, dependency) ||
					reachable(augmented, dependency, dependent) {
					// Pair already ordered (in either direction, transitively) —
					// respect the existing ordering; never duplicate or cycle.
					continue
				}

				augmented[dependent] = append(augmented[dependent], dependency)
				sort.Ints(augmented[dependent])

				nums := []int{issues[dependent].Number, issues[dependency].Number}
				sort.Ints(nums)
				conflicts = append(conflicts, FileConflict{
					Path:     f,
					Issues:   nums,
					Severity: "error",
				})
			}
		}
	}

	// Stable order: by path, then by issue numbers.
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Path != conflicts[j].Path {
			return conflicts[i].Path < conflicts[j].Path
		}
		return fmt.Sprintf("%v", conflicts[i].Issues) < fmt.Sprintf("%v", conflicts[j].Issues)
	})

	return augmented, conflicts
}

// parentDir returns the directory portion of a path.
func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
