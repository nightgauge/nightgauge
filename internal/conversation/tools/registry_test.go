package tools

import (
	"slices"
	"testing"
)

// The conversation tool set is enumerable and pinned (#1570): adding a tool,
// or making one mutating, turns this red, so the boundary only widens by a
// reviewed edit to this list. create_issue is the only tool that changes
// anything, and none writes the working tree (TestTools_NeverChangeTheWorkingTree).
func TestRegistry_IsExactlyTheAdmittedSet(t *testing.T) {
	want := map[string]bool{
		"read_file":        false,
		"list_directory":   false,
		"search_files":     false,
		"get_issue":        false,
		"search_issues":    false,
		"get_pull_request": false,
		"create_issue":     true,
	}
	names := Names()
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want exactly %v", names, want)
	}
	for _, n := range names {
		mutating, ok := want[n]
		if !ok {
			t.Errorf("%s is not an admitted conversation tool", n)
			continue
		}
		if IsMutating(n) != mutating || IsMutating(QualifiedName(n)) != mutating {
			t.Errorf("%s: IsMutating = %v, want %v", n, IsMutating(n), mutating)
		}
	}
	if got := MutatingNames(); !slices.Equal(got, []string{"create_issue"}) {
		t.Errorf("mutating tools = %v, want [create_issue]", got)
	}
	rules := AllowedToolRules()
	if len(rules) != len(want) {
		t.Fatalf("allowed rules = %v", rules)
	}
	for i, n := range names {
		if rules[i] != "mcp__nightgauge__"+n {
			t.Errorf("rule %q for tool %s", rules[i], n)
		}
	}
}

// A tool nobody classified is never assumed harmless (ADR-017's mutation
// floor reads this).
func TestIsMutating_UnknownIsMutating(t *testing.T) {
	for _, n := range []string{"", "write_file", "Bash", "Edit", "mcp__nightgauge__write_file", "mcp__other__read_file"} {
		if !IsMutating(n) {
			t.Errorf("IsMutating(%q) = false, want true", n)
		}
	}
}
