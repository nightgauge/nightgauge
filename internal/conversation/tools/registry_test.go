package tools

import (
	"slices"
	"testing"
)

// The conversation tool set is enumerable and pinned (#1570, #1587): adding
// a tool, removing one, or making one mutating, turns this red, so the
// boundary only changes by a reviewed edit to this list. create_issue,
// act_on_attention_card and dispatch_run (#1894) are the only tools that
// change anything, and none writes the working tree
// (TestTools_NeverChangeTheWorkingTree).
func TestRegistry_IsExactlyTheAdmittedSet(t *testing.T) {
	want := map[string]bool{
		"read_file":             false,
		"list_directory":        false,
		"search_files":          false,
		"get_issue":             false,
		"search_issues":         false,
		"get_pull_request":      false,
		"create_issue":          true,
		"pipeline_status":       false,
		"list_running_jobs":     false,
		"list_attention_cards":  false,
		"act_on_attention_card": true,
		"dispatch_run":          true,
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
	if got := MutatingNames(); !slices.Equal(got, []string{"act_on_attention_card", "create_issue", "dispatch_run"}) {
		t.Errorf("mutating tools = %v, want [act_on_attention_card create_issue dispatch_run]", got)
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
