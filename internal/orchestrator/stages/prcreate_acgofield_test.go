package stages

import (
	"fmt"
	"strings"
	"testing"
)

// goDiff builds an in-memory diff adding lines [from, to] of content to p.
func goDiff(p, content string, from, to int) *ACDiff {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -0,0 +%d,%d @@\n", p, p, p, p, from, to-from+1)
	for _, l := range lines[from-1 : to] {
		b.WriteString("+" + l + "\n")
	}
	return memDiff(b.String(), map[string]string{p: content})
}

// scanTable mirrors the #3425 table in pr_create_diff_scan_test.go: an
// inline struct type with elided elements, wantRecord the fifth field.
func scanTable(record string) string {
	return `package skillrender

import "testing"

func TestDiffScan(t *testing.T) {
	cases := []struct {
		name       string
		scanJSON   string
		scanExit   int
		wantExit   int
		wantRecord bool
		wantOut    string
	}{
		{"critical", "{}", 1, 1, true, "a: critical=1 warnings=0"},
		{"exit1-no-json", ` + "``" + `, 1, 1, ` + record + `, "could not run"},
	}
	_ = cases
}`
}

// #3424 AC 7: the #3425 regression — positional true in the wantRecord
// position is met; positional false there is unmet.
func TestVerifyACAgainstDiff_GoPositionalField3425(t *testing.T) {
	const p = "internal/skillrender/pr_create_diff_scan_test.go"
	const crit = "`pr_create_diff_scan_test.go` has a \"scan cannot run\" case that asserts exit 1 and `wantRecord: true`"
	got, ok := VerifyACAgainstDiff(crit, nil, goDiff(p, scanTable("true"), 15, 15), ACTestState{})
	wantStatus(t, "positional true", got, ok, ACMet)
	if ok && !strings.Contains(got.Evidence, p+":15") {
		t.Errorf("evidence %q does not cite %s:15", got.Evidence, p)
	}
	got, ok = VerifyACAgainstDiff(crit, nil, goDiff(p, scanTable("false"), 15, 15), ACTestState{})
	wantStatus(t, "positional false", got, ok, ACUnmet)
}

func TestGoFieldValue(t *testing.T) {
	const p = "x.go"
	cases := []struct {
		name      string
		src       string
		from, to  int
		tok       string
		added     int
		undecided bool
	}{
		{"keyed", "package x\n\nvar v = T{\n\tTimeout: 30,\n}\n", 4, 4, "Timeout: 30", 1, false},
		{"keyed other value", "package x\n\nvar v = T{\n\tTimeout: 31,\n}\n", 4, 4, "Timeout: 30", 0, false},
		{"positional same-file type", "package x\n\ntype T struct{ A, B int; C string }\n\nvar v = T{1, 2, \"c\"}\n", 5, 5, "C: c", 1, false},
		{"multi-name field list", "package x\n\ntype T struct{ A, B int }\n\nvar v = &T{1, 2}\n", 5, 5, "B: 2", 1, false},
		{"positional wrong value", "package x\n\ntype T struct{ A, B int }\n\nvar v = T{1, 3}\n", 5, 5, "B: 2", 0, false},
		{"elided slice element", "package x\n\ntype T struct{ A int; On bool }\n\nvar v = []T{\n\t{1, true},\n}\n", 6, 6, "On: true", 1, false},
		{"elided pointer element", "package x\n\ntype T struct{ A int; On bool }\n\nvar v = []*T{\n\t{1, true},\n}\n", 6, 6, "On: true", 1, false},
		{"elided map value", "package x\n\ntype T struct{ A int; On bool }\n\nvar v = map[string]T{\n\t\"k\": {1, true},\n}\n", 6, 6, "On: true", 1, false},
		{"embedded field", "package x\n\ntype B struct{}\ntype T struct{ B; On bool }\n\nvar v = T{B{}, true}\n", 6, 6, "On: true", 1, false},
		{"type in another file", "package x\n\nvar v = T{1, true}\n", 3, 3, "On: true", 0, true},
		{"type in another package", "package x\n\nvar v = pkg.T{1, true}\n", 3, 3, "On: true", 0, true},
		{"unresolved but not added", "package x\n\nvar v = T{1, true}\n\nvar w = 1\n", 5, 5, "On: true", 0, false},
		{"unparseable", "package x\n\nvar v = T{1,\n", 3, 3, "On: true", 0, true},
		{"quoted string value", "package x\n\ntype T struct{ Name string }\n\nvar v = T{\"x\"}\n", 5, 5, `Name: "x"`, 1, false},
		{"unquoted string value", "package x\n\ntype T struct{ Name string }\n\nvar v = T{\"x\"}\n", 5, 5, "Name: x", 1, false},
		{"generic type", "package x\n\ntype T[V any] struct{ A V; On bool }\n\nvar v = T[int]{1, true}\n", 5, 5, "On: true", 1, false},
		{"keyed in elided map value", "package x\n\nvar v = map[string]T{\n\t\"k\": {On: true},\n}\n", 4, 4, "On: true", 1, false},
		{"cyclic type", "package x\n\ntype A B\ntype B A\n\nvar v = A{1, true}\n", 6, 6, "On: true", 0, true},
		{"function-local type", "package x\n\nfunc f() {\n\ttype tc struct{ A int; On bool }\n\t_ = []tc{\n\t\t{1, true},\n\t}\n}\n", 6, 6, "On: true", 1, false},
		{"shadowed type", "package x\n\ntype tc struct{ On bool; A int }\n\nfunc f() {\n\ttype tc struct{ A int; On bool }\n\t_ = []tc{\n\t\t{1, true},\n\t}\n}\n", 8, 8, "On: true", 0, true},
		{"set off the added lines", "package x\n\ntype T struct{ A int; On bool }\n\nvar v = T{1, true}\nvar w = 1\n", 6, 6, "On: true", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := goDiff(p, c.src, c.from, c.to)
			got, ok := d.goFieldValue(c.tok, p)
			if !ok {
				t.Fatalf("goFieldValue(%q) not applicable", c.tok)
			}
			if len(got.added) != c.added || got.undecided != c.undecided {
				t.Errorf("got added=%v undecided=%v, want %d added, undecided=%v", got.added, got.undecided, c.added, c.undecided)
			}
		})
	}
}

func TestGoFieldValue_NotApplicable(t *testing.T) {
	d := goDiff("a.yaml", "a: b\n", 1, 1)
	if _, ok := d.goFieldValue("a: b", "a.yaml"); ok {
		t.Error("YAML file: want not applicable")
	}
	d = goDiff("x.go", "package x\n", 1, 1)
	if _, ok := d.goFieldValue("plainSymbol", "x.go"); ok {
		t.Error("non-field span: want not applicable")
	}
}

// #3424 AC 6: an unresolved field order leaves the clause not verifiable,
// never unmet; a resolved one that differs stays unmet.
func TestVerifyACAgainstDiff_GoFieldUnresolved(t *testing.T) {
	const p = "internal/x/x.go"
	d := goDiff(p, "package x\n\nvar v = T{1, false}\n", 3, 3)
	got, ok := VerifyACAgainstDiff("`internal/x/x.go` sets `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "type declared elsewhere", got, ok, "")
	d = goDiff(p, "package x\n\ntype T struct{ A int; On bool }\n\nvar v = T{1, false}\n", 5, 5)
	got, ok = VerifyACAgainstDiff("`internal/x/x.go` sets `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "resolved, other value", got, ok, ACUnmet)
}
