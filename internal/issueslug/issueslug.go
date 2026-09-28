// Package issueslug is the one derivation of the kebab-case slug an issue
// title contributes to names: the feature branch (`<prefix>/<N>-<slug>`) and
// the knowledge directory (`<N>-<slug>`). Before #1901 the branch composer and
// the knowledge scaffold each carried their own rule, and they disagreed on
// punctuation, so one issue could be named two ways. Both now call Of, with
// the one length bound MaxLen.
//
// The rule is byte-compatible with the SDK's KnowledgeService.generateSlug:
// lowercase, every run of characters outside [a-z0-9] becomes one "-", trim
// "-" from both ends, cut to MaxLen bytes, trim a trailing "-" the cut left.
package issueslug

import (
	"regexp"
	"strconv"
	"strings"
)

// MaxLen is the slug's length bound in bytes. The slug is ASCII, so bytes and
// characters agree.
const MaxLen = 50

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func normalize(title string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(title), "-"), "-")
}

func truncate(s string) string {
	if len(s) > MaxLen {
		s = s[:MaxLen]
	}
	return strings.TrimRight(s, "-")
}

// Of returns the slug for title. It may be empty when the title has no
// [a-z0-9] characters; callers that need a non-empty name must refuse that.
func Of(title string) string {
	return truncate(normalize(title))
}

// ForIssue returns the slug for an issue's title with a leading duplicate of
// the issue's own number removed ("#227 Fix it" on #227 → "fix-it", #889).
// The duplicate is dropped BEFORE truncation so the MaxLen budget is spent on
// words. Only a leading token that IS number is dropped: "404 page" on #12
// keeps its 404, and "2200 requests" on #22 keeps its 2200.
//
// The branch composer uses ForIssue. The knowledge scaffold uses Of, because
// the SDK scaffolds the same directory with Of's rule; the two differ only for
// a title that opens with its own issue number, and the knowledge directory is
// located by its `<N>-` prefix, never by recomputing the slug.
func ForIssue(number int, title string) string {
	s := normalize(title)
	own := strconv.Itoa(number)
	if s == own {
		s = ""
	} else if strings.HasPrefix(s, own+"-") {
		s = s[len(own)+1:]
	}
	return truncate(s)
}
