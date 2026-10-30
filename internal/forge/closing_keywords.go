package forge

import (
	"regexp"
	"strconv"
	"strings"
)

// closingKeywordRE matches GitHub's closing keywords followed by an issue
// reference: `#N`, `owner/repo#N`, or an issue URL `https://host/owner/repo/issues/N`
// (#2687). It matches a URL on any host so RewriteClosingRefs defangs every
// one in untrusted text; ClosingRef.Targets counts only github.com, the one
// host GitHub acts on. It is the only Go definition: the pr-merge AC backstop, the
// post-merge hook and the stranded follow-up producer all read it, so they
// agree on what closes an issue. packages/nightgauge/src/utils/prDetection.ts
// keeps a TypeScript copy of the same grammar.
// Group 1 is the reference text; group 2 or 5 is the "owner/repo" it names
// (empty for a bare `#N`); group 3 or 6 is the issue number; group 4 is a URL's host.
var closingKeywordRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b\s*:?\s*` +
	`(([\w.-]+/[\w.-]+)?#(\d+)|https?://([^\s/]+)/([\w.-]+/[\w.-]+)/issues/(\d+))\b`)

// ClosingRef is one closing-keyword reference in a body. Ref is the text after
// the keyword (`#7`, `o/r#7`, or the URL); Repo is the "owner/repo" the
// reference names, empty for a bare `#7`; Host is a URL reference's host,
// empty otherwise; Number is the issue it names.
type ClosingRef struct {
	Ref    string
	Repo   string
	Host   string
	Number int
}

// Targets reports whether the reference closes issue n of repo ("owner/repo").
// A bare `#N` names the PR's own repository, so it targets any repo; a
// reference that names a repository targets only that one. Issue numbers
// overlap across repositories, so `Fixes other/repo#42` never closes this
// repository's #42. A URL on a host other than github.com targets nothing:
// GitHub does not act on it.
func (r ClosingRef) Targets(repo string, n int) bool {
	if r.Host != "" && !strings.EqualFold(r.Host, "github.com") {
		return false
	}
	return r.Number == n && (r.Repo == "" || strings.EqualFold(r.Repo, repo))
}

func closingRef(m []string) ClosingRef {
	if m[3] != "" {
		n, _ := strconv.Atoi(m[3])
		return ClosingRef{Ref: m[1], Repo: m[2], Number: n}
	}
	n, _ := strconv.Atoi(m[6])
	return ClosingRef{Ref: m[1], Repo: m[5], Host: m[4], Number: n}
}

// ClosingRefs returns every closing-keyword reference in body, in order.
func ClosingRefs(body string) []ClosingRef {
	var out []ClosingRef
	for _, m := range closingKeywordRE.FindAllStringSubmatch(body, -1) {
		out = append(out, closingRef(m))
	}
	return out
}

// ClosesIssue reports whether a PR body in repo ("owner/repo") carries a
// closing keyword for that repository's issue n.
func ClosesIssue(body, repo string, n int) bool {
	for _, r := range ClosingRefs(body) {
		if r.Targets(repo, n) {
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
