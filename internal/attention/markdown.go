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

// CodeSpan renders a literal value, such as a filesystem path, as a CommonMark
// code span so a reader sees and can copy it exactly. Backslash escapes are
// inert inside a code span, so the content is verbatim apart from control and
// Unicode format characters, which become spaces as in EscapeMarkdown. The
// delimiter is a backtick run one longer than the longest run in the content,
// and the content is padded with a space on each side when it starts or ends
// with a backtick or a space (CommonMark strips one space from each side when
// both are present). An empty value renders as "": an empty span is not valid.
func CodeSpan(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	longest, run := 0, 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = ' '
		}
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
		b.WriteRune(r)
	}
	content := b.String()
	fence := strings.Repeat("`", longest+1)
	first, last := content[0], content[len(content)-1]
	if first == '`' || first == ' ' || last == '`' || last == ' ' {
		content = " " + content + " "
	}
	return fence + content + fence
}
