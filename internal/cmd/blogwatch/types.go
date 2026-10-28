// Package blogwatch backs `nightgauge blog-watch` and the
// nightgauge-blog-watch skill (#185): it detects new posts on allowlisted
// https sources, screens their text, and keeps an append-only decision log
// with exactly one verdict per post.
//
// Everything a model must not be trusted to decide lives here: whether a
// source may be fetched at all, whether a post is new, whether a verdict is
// one of the five, and what labels and body shape a filed issue gets. Post
// text is untrusted third-party input; the log never stores it.
package blogwatch

import (
	"fmt"
	"strings"
)

// Verdict is the one outcome recorded for a post.
type Verdict string

// The five verdicts. ParseVerdict accepts nothing else.
const (
	VerdictAdopted        Verdict = "Adopted"
	VerdictAlreadyCovered Verdict = "Already covered"
	VerdictAlreadyFiled   Verdict = "Already filed"
	VerdictDeclined       Verdict = "Declined"
	VerdictDeferred       Verdict = "Deferred"
)

// Verdicts lists the closed verdict set in display order.
var Verdicts = []Verdict{VerdictAdopted, VerdictAlreadyCovered, VerdictAlreadyFiled, VerdictDeclined, VerdictDeferred}

// ParseVerdict returns the verdict s names exactly, or an error.
func ParseVerdict(s string) (Verdict, error) {
	for _, v := range Verdicts {
		if string(v) == s {
			return v, nil
		}
	}
	names := make([]string, len(Verdicts))
	for i, v := range Verdicts {
		names[i] = fmt.Sprintf("%q", v)
	}
	return "", fmt.Errorf("unknown verdict %q (want one of %s)", s, strings.Join(names, ", "))
}

// Source is one configured index (a sitemap, feed or listing page).
type Source struct {
	Name          string `json:"name" yaml:"name"`
	IndexURL      string `json:"index_url" yaml:"index_url"`
	PostURLPrefix string `json:"post_url_prefix" yaml:"post_url_prefix"`
}

// Decision is one row of the decision log. It never holds a page body.
type Decision struct {
	URL        string  `json:"url"`
	Source     string  `json:"source,omitempty"`
	Verdict    Verdict `json:"verdict"`
	Reason     string  `json:"reason"`
	Ref        string  `json:"ref,omitempty"`
	Issue      int     `json:"issue,omitempty"`
	RecordedAt string  `json:"recorded_at"`
}

// Post is a detected post URL and the source it came from.
type Post struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

// Skipped is a source that was not fetched or yielded nothing.
type Skipped struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// DetectResult is the output of `blog-watch detect`.
type DetectResult struct {
	New        []Post    `json:"new"`
	Skipped    []Skipped `json:"skipped"`
	KnownCount int       `json:"known_count"`
}
