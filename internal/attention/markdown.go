package attention

import (
	"strings"
	"unicode"
)

// markdownSpecial is the set of CommonMark inline-structure characters that
// EscapeMarkdown backslash-escapes. CommonMark accepts a backslash escape
// before any ASCII punctuation, and `\&` defeats entity references.
const markdownSpecial = "\\`*_[]<>!~|&"

// EscapeMarkdown renders untrusted text as literal text inside a markdown
// DecisionRequest Body. Control and Unicode format characters (newlines, bidi
// overrides) become spaces so the value cannot leave its line, and inline
// markdown structure (links, images, emphasis, code, HTML, tables, entities)
// is backslash-escaped. Line-start markers (#, -, +, digits) are left alone:
// with no newline the value never starts a line, and paths stay readable.
func EscapeMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			b.WriteByte(' ')
		case strings.ContainsRune(markdownSpecial, r):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
