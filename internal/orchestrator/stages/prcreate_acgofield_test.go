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
	for _, span := range []string{"localhost:8080", "On:true", "Base: https://x.io", "On: a:b"} {
		if _, ok := d.goFieldValue(span, "x.go"); ok {
			t.Errorf("%q: want not applicable", span)
		}
	}
	d = goDiff("x.go", "package x\n\ntype T struct{ Name string }\n\nvar v = T{\"a:b\"}\n", 5, 5)
	if g, ok := d.goFieldValue(`Name: "a:b"`, "x.go"); !ok || len(g.added) != 1 {
		t.Errorf(`Name: "a:b": got %+v ok=%v, want matched on line 5`, g, ok)
	}
}

// A span the guard accepts but the Go matcher rejects is judged in a named
// Go file by its text, so a missing one stays unmet; and a changed Markdown
// file beside a named, resolved Go file leaves the span unmet.
func TestVerifyACAgainstDiff_GoFieldGuardMatcherGap(t *testing.T) {
	const p = "internal/x/x.go"
	const src = "package x\n\ntype T struct{ A int; On bool }\n\nvar v = T{1, false}\n"
	got, ok := VerifyACAgainstDiff("`internal/x/x.go` sets `On: a:b`.", nil, goDiff(p, src, 5, 5), ACTestState{})
	wantStatus(t, "guard-only span in Go", got, ok, ACUnmet)

	const md = "README.md"
	diff := "diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p +
		"\n@@ -0,0 +5,1 @@\n+var v = T{1, false}\n" +
		"diff --git a/" + md + " b/" + md + "\n--- a/" + md + "\n+++ b/" + md +
		"\n@@ -0,0 +1,1 @@\n+Some text.\n"
	d := memDiff(diff, map[string]string{p: src, md: "Some text.\n"})
	got, ok = VerifyACAgainstDiff("`internal/x/x.go` sets `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "resolved Go beside unrelated markdown", got, ok, ACUnmet)
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

// #3424 AC 6, not Go: a field span answered only by files that carry no
// field order (Markdown here) is not verifiable when absent, met when the
// text is there; a resolved Go file named beside it still decides unmet,
// and a YAML file lacking the key path keeps its unmet verdict.
func TestVerifyACAgainstDiff_GoFieldNotGo(t *testing.T) {
	const md = "README.md"
	got, ok := VerifyACAgainstDiff("`README.md` sets `On: true`.", nil,
		goDiff(md, "# X\n\nSome text.\n", 3, 3), ACTestState{})
	wantStatus(t, "markdown lacks span", got, ok, "")
	got, ok = VerifyACAgainstDiff("`README.md` sets `On: true`.", nil,
		goDiff(md, "# X\n\nOn: true\n", 3, 3), ACTestState{})
	wantStatus(t, "markdown adds span", got, ok, ACMet)

	const g = "internal/x/x.go"
	const src = "package x\n\ntype T struct{ A int; On bool }\n\nvar v = T{1, false}\n"
	diff := "diff --git a/" + g + " b/" + g + "\n--- a/" + g + "\n+++ b/" + g +
		"\n@@ -0,0 +5,1 @@\n+var v = T{1, false}\n" +
		"diff --git a/" + md + " b/" + md + "\n--- a/" + md + "\n+++ b/" + md +
		"\n@@ -0,0 +3,1 @@\n+Some text.\n"
	d := memDiff(diff, map[string]string{g: src, md: "# X\n\nSome text.\n"})
	got, ok = VerifyACAgainstDiff("`internal/x/x.go` and `README.md` set `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "resolved Go beside markdown", got, ok, ACUnmet)

	got, ok = VerifyACAgainstDiff("`a.yaml` sets `On: true`.", nil,
		goDiff("a.yaml", "Off: true\n", 1, 1), ACTestState{})
	wantStatus(t, "yaml lacks key path", got, ok, ACUnmet)
}

// #3424 AC 6, type not found: a Go file whose branch content cannot be
// read is undecided, so a field span it lacks is not verifiable.
func TestGoFieldValue_UnreadableIsUndecided(t *testing.T) {
	d := goDiff("x.go", "package x\n", 1, 1)
	d.content["x.go"] = acContent{}
	if g, ok := d.goFieldValue("On: true", "x.go"); !ok || !g.undecided {
		t.Errorf("unreadable Go file: got %+v ok=%v, want undecided", g, ok)
	}
	if !acGoFieldUndecided("On: true", []string{"x.go"}, d) {
		t.Error("unreadable Go file: want span undecided")
	}
}

// #3424 AC 6, type not found: a criterion naming a Go file the branch
// cannot read is not verifiable, never unmet.
func TestVerifyACAgainstDiff_GoFieldUnreadable(t *testing.T) {
	const p = "internal/x/x.go"
	d := goDiff(p, "package x\n\nvar v = T{1, true}\n", 3, 3)
	d.content[p] = acContent{}
	got, ok := VerifyACAgainstDiff("`internal/x/x.go` sets `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "unreadable Go file", got, ok, "")
}

// Review fix: the verdict depends only on the files the clause names. An
// unrelated parsable Go file never makes a named TypeScript file decide, and
// a Go file the diff deletes never makes a resolved named file undecided.
func TestVerifyACAgainstDiff_GoFieldScopeOnly(t *testing.T) {
	const ts = "src/app.ts"
	const crit = "`src/app.ts` sets `retries: 3`."
	got, ok := VerifyACAgainstDiff(crit, nil, goDiff(ts, "const c = { retries: 5 };\n", 1, 1), ACTestState{})
	wantStatus(t, "ts alone", got, ok, "")
	const g = "main.go"
	diff := "diff --git a/" + ts + " b/" + ts + "\n--- a/" + ts + "\n+++ b/" + ts +
		"\n@@ -0,0 +1,1 @@\n+const c = { retries: 5 };\n" +
		"diff --git a/" + g + " b/" + g + "\n--- a/" + g + "\n+++ b/" + g +
		"\n@@ -0,0 +3,1 @@\n+var x = 1\n"
	d := memDiff(diff, map[string]string{ts: "const c = { retries: 5 };\n", g: "package main\n\nvar x = 1\n"})
	got, ok = VerifyACAgainstDiff(crit, nil, d, ACTestState{})
	wantStatus(t, "ts beside unrelated go", got, ok, "")

	const x = "internal/x/x.go"
	const y = "internal/x/y.go"
	const src = "package x\n\ntype T struct{ A int; On bool }\n\nvar v = T{1, false}\n"
	diff = "diff --git a/" + x + " b/" + x + "\n--- a/" + x + "\n+++ b/" + x +
		"\n@@ -0,0 +5,1 @@\n+var v = T{1, false}\n" +
		"diff --git a/" + y + " b/" + y + "\ndeleted file mode 100644\n--- a/" + y + "\n+++ /dev/null\n" +
		"@@ -1,1 +0,0 @@\n-package x\n"
	d = memDiff(diff, map[string]string{x: src})
	got, ok = VerifyACAgainstDiff("`internal/x/x.go` sets `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "resolved beside deleted go", got, ok, ACUnmet)
	if f := d.Files[y]; f == nil || f.Status != "deleted" {
		t.Fatalf("fixture: %s not parsed as deleted: %+v", y, f)
	}
	if acGoFieldUndecided("On: true", []string{y}, d) {
		t.Error("deleted Go file: want span decided")
	}
}

// Review fix: a named Go file that resolves still leaves the span not
// verifiable when another changed Go file sets it positionally under a type
// that file cannot resolve.
func TestVerifyACAgainstDiff_GoFieldWideUnresolved(t *testing.T) {
	const a = "internal/x/types.go"
	const b = "internal/x/x_test.go"
	const asrc = "package x\n\ntype T struct{ A int; On bool }\n"
	const bsrc = "package x\n\nvar v = T{1, true}\n"
	diff := "diff --git a/" + a + " b/" + a + "\n--- a/" + a + "\n+++ b/" + a +
		"\n@@ -0,0 +3,1 @@\n+type T struct{ A int; On bool }\n" +
		"diff --git a/" + b + " b/" + b + "\n--- a/" + b + "\n+++ b/" + b +
		"\n@@ -0,0 +3,1 @@\n+var v = T{1, true}\n"
	d := memDiff(diff, map[string]string{a: asrc, b: bsrc})
	got, ok := VerifyACAgainstDiff("`internal/x/types.go` declares the table that sets `On: true`.", nil, d, ACTestState{})
	wantStatus(t, "named resolves, other file unresolved", got, ok, "")
}

// Operator steer: only `name: value` (identifier, colon, whitespace, value)
// is a field span. A missing URL or host:port span in a Markdown- or
// TS-scoped criterion stays unmet; a field span on a non-Go file is not
// verifiable; a positional Go field still resolves as before.
func TestVerifyACAgainstDiff_FieldSpanGuard(t *testing.T) {
	const pos = "package x\n\ntype T struct{ A int; On bool }\n\nvar v = T{1, %s}\n"
	cases := []struct {
		name, crit, path, content string
		line                      int
		want                      string
	}{
		{"md missing url", "`README.md` links `https://x.io/a`.", "README.md", "# X\n\nSome text.\n", 3, ACUnmet},
		{"md missing host:port", "`README.md` names `localhost:8080`.", "README.md", "# X\n\nSome text.\n", 3, ACUnmet},
		{"ts missing url", "`src/app.ts` calls `https://x.io/a`.", "src/app.ts", "const c = 1;\n", 1, ACUnmet},
		{"ts missing host:port", "`src/app.ts` dials `localhost:8080`.", "src/app.ts", "const c = 1;\n", 1, ACUnmet},
		{"md field span", "`README.md` sets `On: true`.", "README.md", "# X\n\nSome text.\n", 3, ""},
		{"ts field span", "`src/app.ts` sets `retries: 3`.", "src/app.ts", "const c = { retries: 5 };\n", 1, ""},
		{"go positional met", "`internal/x/x.go` sets `On: true`.", "internal/x/x.go", fmt.Sprintf(pos, "true"), 5, ACMet},
		{"go positional unmet", "`internal/x/x.go` sets `On: true`.", "internal/x/x.go", fmt.Sprintf(pos, "false"), 5, ACUnmet},
	}
	for _, c := range cases {
		got, ok := VerifyACAgainstDiff(c.crit, nil, goDiff(c.path, c.content, c.line, c.line), ACTestState{})
		wantStatus(t, c.name, got, ok, c.want)
	}
	for span, want := range map[string]bool{
		"On: true": true, `Name: "a:b"`: true, "wantRecord: false": true,
		"https://x.io/a": false, "localhost:8080": false, "On:true": false,
		"Base: https://x.io": false,
	} {
		if got := acIsFieldSpan(span); got != want {
			t.Errorf("acIsFieldSpan(%q) = %v, want %v", span, got, want)
		}
	}
}
