package forge

import "testing"

// One table for every closing-keyword form (#2687). stages and hooks delegate
// here, so this table is what both agree on.
func TestClosingKeywords(t *testing.T) {
	cases := []struct {
		body    string
		n       int
		closes  bool
		rewrite string // RewriteClosingRefs selecting issue n
	}{
		{"Closes #7", 7, true, "Refs #7"},
		{"fixes: o/r#7", 7, true, "Refs o/r#7"},
		{"Resolved https://github.com/o/r/issues/7", 7, true, "Refs https://github.com/o/r/issues/7"},
		{"closes http://ghe.example/o/r/issues/7", 7, true, "Refs http://ghe.example/o/r/issues/7"},
		{"Refs https://github.com/o/r/issues/7", 7, false, "Refs https://github.com/o/r/issues/7"},
		{"Closes https://github.com/o/r/pull/7", 7, false, "Closes https://github.com/o/r/pull/7"},
		{"Closes https://github.com/o/r/issues/7x", 7, false, "Closes https://github.com/o/r/issues/7x"},
		{"Closes #70", 7, false, "Closes #70"},
		{"Closes https://github.com/o/r/issues/12", 1, false, "Closes https://github.com/o/r/issues/12"},
		{"Closes https://github.com/o/r/issues/12", 12, true, "Refs https://github.com/o/r/issues/12"},
		{
			"Fixes #3, closes https://github.com/o/r/issues/7 and resolves o/r#9",
			7, true,
			"Fixes #3, Refs https://github.com/o/r/issues/7 and resolves o/r#9",
		},
	}
	for _, c := range cases {
		if got := ClosesIssue(c.body, c.n); got != c.closes {
			t.Errorf("ClosesIssue(%q, %d) = %v, want %v", c.body, c.n, got, c.closes)
		}
		got := RewriteClosingRefs(c.body, func(r ClosingRef) bool { return r.Number == c.n })
		if got != c.rewrite {
			t.Errorf("RewriteClosingRefs(%q, %d) = %q, want %q", c.body, c.n, got, c.rewrite)
		}
	}
}

func TestClosingRefsNumbers(t *testing.T) {
	body := "Fixes #3, closes https://github.com/o/r/issues/7 and resolves o/r#9"
	refs := ClosingRefs(body)
	want := []ClosingRef{{"#3", 3}, {"https://github.com/o/r/issues/7", 7}, {"o/r#9", 9}}
	if len(refs) != len(want) {
		t.Fatalf("ClosingRefs = %+v, want %+v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ClosingRefs[%d] = %+v, want %+v", i, refs[i], want[i])
		}
	}
}
