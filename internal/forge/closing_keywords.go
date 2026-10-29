package forge

import (
	"regexp"
	"strconv"
)

// closingKeywordRE matches GitHub's closing keywords followed by an issue
// reference: `#N`, `owner/repo#N`, or an issue URL `https://host/owner/repo/issues/N`
// (#2687). It is the only definition: the pr-merge AC backstop and the
// post-merge hook both read it, so they agree on what closes an issue.
// Group 1 is the reference text; group 2 or 3 is the issue number.
var closingKeywordRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[ \t]*:?[ \t]*` +
	`((?:[\w.-]+/[\w.-]+)?#(\d+)|https?://[^\s/]+/[\w.-]+/[\w.-]+/issues/(\d+))\b`)

// ClosingRef is one closing-keyword reference in a body. Ref is the text after
// the keyword (`#7`, `o/r#7`, or the URL); Number is the issue it names.
type ClosingRef struct {
	Ref    string
	Number int
}

func closingRef(m []string) ClosingRef {
	num := m[2]
	if num == "" {
		num = m[3]
	}
	n, _ := strconv.Atoi(num)
	return ClosingRef{Ref: m[1], Number: n}
}

// ClosingRefs returns every closing-keyword reference in body, in order.
func ClosingRefs(body string) []ClosingRef {
	var out []ClosingRef
	for _, m := range closingKeywordRE.FindAllStringSubmatch(body, -1) {
		out = append(out, closingRef(m))
	}
	return out
}

// ClosesIssue reports whether body carries a closing keyword for issue n. It
// matches on the number only, whatever repository the reference names.
func ClosesIssue(body string, n int) bool {
	for _, r := range ClosingRefs(body) {
		if r.Number == n {
			return true
		}
	}
	return false
}

// RewriteClosingRefs replaces each closing-keyword reference that rewrite
// selects with `Refs <Ref>`, so it no longer closes the issue.
func RewriteClosingRefs(s string, rewrite func(ClosingRef) bool) string {
	return closingKeywordRE.ReplaceAllStringFunc(s, func(match string) string {
		r := closingRef(closingKeywordRE.FindStringSubmatch(match))
		if !rewrite(r) {
			return match
		}
		return "Refs " + r.Ref
	})
}
