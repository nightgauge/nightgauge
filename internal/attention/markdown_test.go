package attention

import "testing"

func TestEscapeMarkdown(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"[x](http://e)", `\[x\](http://e)`},
		{"![x](y)", `\!\[x\](y)`},
		{"<b>", `\<b\>`},
		{"&amp;", `\&amp;`},
		{"*x* _y_", `\*x\* \_y\_`},
		{"`code`", "\\`code\\`"},
		{"a|b", `a\|b`},
		{`a\b`, `a\\b`},
		{"~~s~~", `\~\~s\~\~`},
		{"a\nb\r\tc", "a b  c"},
		{"a‮b", "a b"},
		{"a.go:12", "a.go:12"},
		{"/tmp/wt-2865", "/tmp/wt-2865"},
		{"nil deref on empty slice", "nil deref on empty slice"},
		{"", ""},
	} {
		if got := EscapeMarkdown(tc.in); got != tc.want {
			t.Errorf("EscapeMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
