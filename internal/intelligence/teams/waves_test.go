package teams

import (
	"testing"
)

func TestCalculateWavesNoDeps(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Title: "A"},
		{Number: 2, Title: "B"},
		{Number: 3, Title: "C"},
	}

	waves, err := CalculateWaves(issues, nil)
	if err != nil {
		t.Fatalf("CalculateWaves: %v", err)
	}

	// All independent — should be one wave
	if len(waves) != 1 {
		t.Errorf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0].Issues) != 3 {
		t.Errorf("expected 3 issues in wave 0, got %d", len(waves[0].Issues))
	}
}

func TestCalculateWavesLinearChain(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Title: "A"},
		{Number: 2, Title: "B"},
		{Number: 3, Title: "C"},
	}
	// C depends on B, B depends on A
	deps := map[int][]int{
		1: {0}, // B depends on A
		2: {1}, // C depends on B
	}

	waves, err := CalculateWaves(issues, deps)
	if err != nil {
		t.Fatalf("CalculateWaves: %v", err)
	}

	if len(waves) != 3 {
		t.Fatalf("expected 3 waves for linear chain, got %d", len(waves))
	}

	// Wave 0: A, Wave 1: B, Wave 2: C
	if waves[0].Issues[0].Number != 1 {
		t.Errorf("wave 0 should have issue 1, got %d", waves[0].Issues[0].Number)
	}
	if waves[1].Issues[0].Number != 2 {
		t.Errorf("wave 1 should have issue 2, got %d", waves[1].Issues[0].Number)
	}
	if waves[2].Issues[0].Number != 3 {
		t.Errorf("wave 2 should have issue 3, got %d", waves[2].Issues[0].Number)
	}
}

func TestCalculateWavesDiamond(t *testing.T) {
	// A → B, A → C, B → D, C → D
	issues := []SubIssue{
		{Number: 1, Title: "A"},
		{Number: 2, Title: "B"},
		{Number: 3, Title: "C"},
		{Number: 4, Title: "D"},
	}
	deps := map[int][]int{
		1: {0},    // B depends on A
		2: {0},    // C depends on A
		3: {1, 2}, // D depends on B and C
	}

	waves, err := CalculateWaves(issues, deps)
	if err != nil {
		t.Fatalf("CalculateWaves: %v", err)
	}

	if len(waves) != 3 {
		t.Fatalf("expected 3 waves for diamond, got %d", len(waves))
	}

	// Wave 0: A (1 issue), Wave 1: B+C (2 issues), Wave 2: D (1 issue)
	if len(waves[0].Issues) != 1 {
		t.Errorf("wave 0 should have 1 issue, got %d", len(waves[0].Issues))
	}
	if len(waves[1].Issues) != 2 {
		t.Errorf("wave 1 should have 2 issues, got %d", len(waves[1].Issues))
	}
	if len(waves[2].Issues) != 1 {
		t.Errorf("wave 2 should have 1 issue, got %d", len(waves[2].Issues))
	}
}

func TestCalculateWavesEmpty(t *testing.T) {
	waves, err := CalculateWaves(nil, nil)
	if err != nil {
		t.Fatalf("CalculateWaves: %v", err)
	}
	if waves != nil {
		t.Errorf("expected nil for empty input, got %v", waves)
	}
}

func TestMergeWaves(t *testing.T) {
	waves := []WaveAssignment{
		{WaveIndex: 0, Issues: []SubIssue{{Number: 1}}},
		{WaveIndex: 1, Issues: []SubIssue{{Number: 2}}},
		{WaveIndex: 2, Issues: []SubIssue{{Number: 3}}},
		{WaveIndex: 3, Issues: []SubIssue{{Number: 4}}},
	}

	merged := MergeWaves(waves, 2)
	if len(merged) != 2 {
		t.Errorf("expected 2 waves after merge, got %d", len(merged))
	}
}

func TestMergeWavesNoMergeNeeded(t *testing.T) {
	waves := []WaveAssignment{
		{WaveIndex: 0, Issues: []SubIssue{{Number: 1}}},
		{WaveIndex: 1, Issues: []SubIssue{{Number: 2}}},
	}

	merged := MergeWaves(waves, 5)
	if len(merged) != 2 {
		t.Errorf("expected 2 waves (no merge needed), got %d", len(merged))
	}
}

func TestDetectFileConflictsExact(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Files: []string{"src/main.go", "src/utils.go"}},
		{Number: 2, Files: []string{"src/main.go", "tests/test.go"}},
		{Number: 3, Files: []string{"tests/test.go", "README.md"}},
	}

	conflicts := DetectFileConflicts(issues)
	if len(conflicts) == 0 {
		t.Fatal("expected conflicts")
	}

	// src/main.go should have error severity
	found := false
	for _, c := range conflicts {
		if c.Path == "src/main.go" && c.Severity == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error conflict for src/main.go")
	}
}

func TestDetectFileConflictsNone(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Files: []string{"a.go"}},
		{Number: 2, Files: []string{"b.go"}},
	}

	conflicts := DetectFileConflicts(issues)
	// No exact file conflicts; may have directory warning if same dir
	for _, c := range conflicts {
		if c.Severity == "error" {
			t.Errorf("unexpected error conflict: %s", c.Path)
		}
	}
}

func TestSerializeFileOverlapsSharedFile(t *testing.T) {
	// Two issues sharing a top-level exact file get serialized: the
	// later-numbered issue depends on the earlier-numbered one.
	issues := []SubIssue{
		{Number: 10, Title: "A", Files: []string{"lib/page.dart"}},
		{Number: 20, Title: "B", Files: []string{"lib/page.dart"}},
	}

	deps, conflicts := SerializeFileOverlaps(issues, map[int][]int{})

	// Index 1 (#20) should depend on index 0 (#10).
	if !containsIdx(deps[1], 0) {
		t.Errorf("expected #20 (idx 1) to depend on #10 (idx 0); deps=%v", deps)
	}
	if containsIdx(deps[0], 1) {
		t.Errorf("did not expect #10 to depend on #20; deps=%v", deps)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 injected conflict, got %d: %v", len(conflicts), conflicts)
	}
	if conflicts[0].Severity != "error" || conflicts[0].Path != "lib/page.dart" {
		t.Errorf("unexpected conflict: %+v", conflicts[0])
	}

	// The serialized pair must land in adjacent waves.
	waves, _ := CalculateWaves(issues, deps)
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves after serialization, got %d", len(waves))
	}
	if waves[0].Issues[0].Number != 10 || waves[1].Issues[0].Number != 20 {
		t.Errorf("expected wave0=#10, wave1=#20; got %v / %v",
			waveNumbers(waves[0]), waveNumbers(waves[1]))
	}
}

func TestSerializeFileOverlapsNonOverlapping(t *testing.T) {
	// No shared file → no serialization, stays a single wave.
	issues := []SubIssue{
		{Number: 1, Title: "A", Files: []string{"a.go"}},
		{Number: 2, Title: "B", Files: []string{"b.go"}},
	}

	deps, conflicts := SerializeFileOverlaps(issues, map[int][]int{})
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts, got %v", conflicts)
	}
	if len(deps) != 0 {
		t.Errorf("expected no injected deps, got %v", deps)
	}

	waves, _ := CalculateWaves(issues, deps)
	if len(waves) != 1 || len(waves[0].Issues) != 2 {
		t.Errorf("expected 1 wave of 2 issues, got %v", waves)
	}
}

func TestSerializeFileOverlapsDirectoryOnlyNotSerialized(t *testing.T) {
	// Same directory, different files → NOT an exact-file overlap, so no
	// serialization edge is injected (legitimate parallelism preserved).
	issues := []SubIssue{
		{Number: 1, Title: "A", Files: []string{"lib/pages/a.dart"}},
		{Number: 2, Title: "B", Files: []string{"lib/pages/b.dart"}},
	}

	deps, conflicts := SerializeFileOverlaps(issues, map[int][]int{})
	if len(conflicts) != 0 {
		t.Errorf("directory-only overlap must not inject conflicts, got %v", conflicts)
	}
	if len(deps) != 0 {
		t.Errorf("directory-only overlap must not inject deps, got %v", deps)
	}
}

func TestSerializeFileOverlapsRespectsExistingEdge(t *testing.T) {
	// Pre-existing dependency (#10 → #20 reversed direction) must be respected,
	// not duplicated, even though they share a file.
	issues := []SubIssue{
		{Number: 10, Title: "A", Files: []string{"shared.go"}},
		{Number: 20, Title: "B", Files: []string{"shared.go"}},
	}
	// #10 (idx 0) already depends on #20 (idx 1) — opposite of the default
	// tie-break direction.
	pre := map[int][]int{0: {1}}

	deps, conflicts := SerializeFileOverlaps(issues, pre)
	if len(conflicts) != 0 {
		t.Errorf("expected no new conflict when pair already ordered, got %v", conflicts)
	}
	// The existing edge survives; no reverse edge added.
	if !containsIdx(deps[0], 1) {
		t.Errorf("expected pre-existing edge #10→#20 preserved; deps=%v", deps)
	}
	if containsIdx(deps[1], 0) {
		t.Errorf("did not expect a duplicate reverse edge; deps=%v", deps)
	}
}

func TestSerializeFileOverlaps_143_144_Reproduction(t *testing.T) {
	// The exact epic #142 collision: #143 and #144 both edit
	// journal_entry_page.dart and journal_list_page.dart but were placed in the
	// same wave with no dependency. SerializeFileOverlaps must inject exactly
	// one edge (later #144 depends on earlier #143).
	issues := []SubIssue{
		{Number: 143, Title: "Read-first view", Files: []string{
			"lib/pages/journal_entry_page.dart",
			"lib/pages/journal_list_page.dart",
		}},
		{Number: 144, Title: "Folders", Files: []string{
			"lib/pages/journal_entry_page.dart",
			"lib/pages/journal_list_page.dart",
		}},
	}

	deps, conflicts := SerializeFileOverlaps(issues, map[int][]int{})

	// Exactly one edge: #144 (idx 1) depends on #143 (idx 0).
	if len(deps[1]) != 1 || deps[1][0] != 0 {
		t.Errorf("expected exactly one edge #144→#143; deps=%v", deps)
	}
	if len(deps[0]) != 0 {
		t.Errorf("expected #143 to have no injected deps; deps=%v", deps)
	}

	// Exactly ONE injected conflict: the pair is serialized on the first shared
	// file; the second shared file finds the pair already ordered and is
	// skipped (no redundant edge). One edge, not one-per-file.
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 injected conflict (one edge for the pair), got %d: %v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.Severity != "error" {
		t.Errorf("expected error severity, got %q for %s", c.Severity, c.Path)
	}
	if len(c.Issues) != 2 || c.Issues[0] != 143 || c.Issues[1] != 144 {
		t.Errorf("expected conflict issues [143 144], got %v", c.Issues)
	}

	// And they end up in adjacent waves.
	waves, _ := CalculateWaves(issues, deps)
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(waves))
	}
	if waves[0].Issues[0].Number != 143 || waves[1].Issues[0].Number != 144 {
		t.Errorf("expected wave0=#143, wave1=#144; got %v / %v",
			waveNumbers(waves[0]), waveNumbers(waves[1]))
	}
}

func TestSerializeFileOverlapsProseMentionNotSerialized(t *testing.T) {
	// #4074 review: the broad extractor surfaces bare common filenames mentioned
	// in prose (README.md, package.json, main.go). Two UNRELATED issues that each
	// merely mention the same common file must NOT be force-serialized — that
	// would erase legitimate parallelism and inject a bogus blockedBy edge.
	for _, f := range []string{"README.md", "package.json", "CHANGELOG.md", "main.go", "node.js"} {
		issues := []SubIssue{
			{Number: 200, Title: "Add login", Files: []string{f}},
			{Number: 201, Title: "Add logout", Files: []string{f}},
		}
		deps, conflicts := SerializeFileOverlaps(issues, map[int][]int{})
		if len(conflicts) != 0 {
			t.Errorf("prose mention of %q must NOT serialize; got conflicts=%v", f, conflicts)
		}
		if len(deps) != 0 {
			t.Errorf("prose mention of %q must inject no deps; got %v", f, deps)
		}
	}
}

func TestSerializeFileOverlapsTransitiveOrderingNoCycle(t *testing.T) {
	// #4074 review: a pre-existing TRANSITIVE chain oriented opposite to the
	// number tie-break must not let serialization inject a cycle. #10→#20→#30
	// (10 depends on 20 depends on 30); #10 and #30 share a real target file.
	// The tie-break would add #30→#10, but #10 already transitively depends on
	// #30, so injecting would form a cycle — the reachability guard must skip it.
	issues := []SubIssue{
		{Number: 10, Title: "A", Files: []string{"lib/shared_widget.dart"}}, // idx 0
		{Number: 20, Title: "B", Files: []string{"lib/other.dart"}},         // idx 1
		{Number: 30, Title: "C", Files: []string{"lib/shared_widget.dart"}}, // idx 2
	}
	pre := map[int][]int{0: {1}, 1: {2}} // 10→20→30

	deps, _ := SerializeFileOverlaps(issues, pre)

	// idx2 (#30) must NOT gain an edge to idx0 (#10) — that would cycle.
	if containsIdx(deps[2], 0) {
		t.Errorf("serialization injected a cycle-forming edge #30→#10; deps=%v", deps)
	}
	// CalculateWaves must still produce a valid (deadlock-free) ordering.
	if waves, _ := CalculateWaves(issues, deps); len(waves) == 0 {
		t.Error("expected a valid wave ordering, got none")
	}
}

func containsIdx(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestParentDir(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"src/main.go", "src"},
		{"a/b/c.ts", "a/b"},
		{"file.go", ""},
	}

	for _, tt := range tests {
		got := parentDir(tt.path)
		if got != tt.want {
			t.Errorf("parentDir(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// ── #79: documentation paths never drive serialization ──────────────────

func TestSerializeFileOverlapsDocsPathNotSerialized(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Files: []string{"docs/spikes/fable-5-behavior-porting.md", "src/a_module.ts"}},
		{Number: 2, Files: []string{"docs/spikes/fable-5-behavior-porting.md", "src/b_module.ts"}},
	}
	deps, conflicts := SerializeFileOverlaps(issues, map[int][]int{})
	if len(deps) != 0 {
		t.Errorf("shared docs/ path must not inject edges, got %v", deps)
	}
	if len(conflicts) != 0 {
		t.Errorf("shared docs/ path must not report conflicts, got %+v", conflicts)
	}
}

func TestSerializableTargetFileDocsExclusion(t *testing.T) {
	cases := map[string]bool{
		"docs/spikes/fable-5-behavior-porting.md": false, // #79: docs never serialize
		"docs/ARCHITECTURE.md":                    false,
		"packages/foo/docs/api-notes.md":          false, // nested /docs/ segment
		"skills/nightgauge-issue-audit/SKILL.md":  true,  // NOT under docs/ — real write target
		"lib/pages/journal_entry_page.dart":       true,  // the #143/#144 class
		"internal/state/runtime_state.go":         true,
	}
	for f, want := range cases {
		if got := serializableTargetFile(f); got != want {
			t.Errorf("serializableTargetFile(%q) = %v, want %v", f, got, want)
		}
	}
}
