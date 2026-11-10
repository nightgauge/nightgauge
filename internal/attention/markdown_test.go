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

func TestCodeSpan(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain path", "/tmp/wt-2865", "`/tmp/wt-2865`"},
		{"markdown punctuation stays verbatim", `/var/cd_ef/*x*\[y]`, "`/var/cd_ef/*x*\\[y]`"},
		{"inner backtick", "a`b", "``a`b``"},
		{"inner double run", "a``b`c", "```a``b`c```"},
		{"leading backtick", "`a", "`` `a ``"},
		{"trailing backtick", "a`", "`` a` ``"},
		{"leading space", " a", "`  a `"},
		{"newline and bidi become spaces", "a\nb\u202ec", "`a b c`"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CodeSpan(c.in); got != c.want {
				t.Errorf("CodeSpan(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
