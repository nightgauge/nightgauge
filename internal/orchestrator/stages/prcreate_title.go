package stages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxTitleRunes caps a pipeline PR title, `<type>(#N): ` included (#2846). The
// squash merge copies the title into main's history, so this is also the
// length of a commit subject there, before GitHub appends ` (#PR)`.
const maxTitleRunes = 72

// Where the text of a rendered title came from.
const (
	titleSourceSummary       = "summary"
	titleSourceSummaryCut    = "summary-truncated"
	titleSourceIssueTitle    = "issue-title"
	titleSourceIssueTitleCut = "issue-title-truncated"
)

// titleScope is the fixed head of every pipeline title. PR lookups key on its
// `(#N)`, so a title never loses it however short the budget gets (#2689).
func titleScope(snap PRCreateSnapshot) string {
	return fmt.Sprintf("%s(#%d): ", titlePrefix(snap.IssueType), snap.IssueNumber)
}

// titleText resolves the text after the scope and names its source. The recorded
// summary wins over the issue title. Pure: RenderTitle's determinism rests on it,
// so callers do any logging, through TitleNote.
func titleText(snap PRCreateSnapshot) (text, source string) {
	budget := maxTitleRunes - utf8.RuneCountInString(titleScope(snap))
	if summary := normalizeTitleText(snap.PRSummary); summary != "" {
		cut, truncated := cutToBudget(summary, budget)
		if truncated {
			return cut, titleSourceSummaryCut
		}
		return cut, titleSourceSummary
	}
	cut, truncated := cutToBudget(normalizeTitleText(snap.IssueTitle), budget)
	if truncated {
		return cut, titleSourceIssueTitleCut
	}
	return cut, titleSourceIssueTitle
}

// normalizeTitleText puts free text on one line, rewrites closing keywords and
// drops any conventional-commit prefix. The title becomes a commit subject, and
// GitHub scans those for closing keywords, so neither source may close an issue.
func normalizeTitleText(s string) string {
	return stripTitlePrefix(UntrustedACText(s))
}

// cutToBudget fits text into budget runes at a word boundary and trims the
// punctuation that boundary strands. A first word longer than the budget has no
// boundary, so that word is cut: the limit outranks the boundary in this one case.
func cutToBudget(text string, budget int) (string, bool) {
	runes := []rune(text)
	if len(runes) <= budget {
		return text, false
	}
	end := budget
	if !unicode.IsSpace(runes[end]) {
		for end > 0 && !unicode.IsSpace(runes[end-1]) {
			end--
		}
	}
	cut := strings.TrimRightFunc(string(runes[:end]), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:-", r)
	})
	if cut == "" {
		return string(runes[:budget]), true
	}
	return cut, true
}

// TitleNote is the stage-log line for a title that did not come straight from
// the recorded summary, and empty when it did. It is the same line on every path
// that renders a title.
func TitleNote(snap PRCreateSnapshot) string {
	switch _, source := titleText(snap); source {
	case titleSourceSummaryCut:
		return fmt.Sprintf("pr-create: #%d pr_summary exceeds the %d-character title limit; title cut at a word boundary", snap.IssueNumber, maxTitleRunes)
	case titleSourceIssueTitle:
		return fmt.Sprintf("pr-create: #%d no pr_summary recorded; title falls back to the issue title", snap.IssueNumber)
	case titleSourceIssueTitleCut:
		return fmt.Sprintf("pr-create: #%d no pr_summary recorded; title falls back to the issue title (cut at a word boundary)", snap.IssueNumber)
	}
	return ""
}

// logTitleNote writes TitleNote to the stage log when there is one to write.
func logTitleNote(snap PRCreateSnapshot) {
	if note := TitleNote(snap); note != "" {
		log.Printf("%s", note)
	}
}

// ReadTitleSnapshot loads only what RenderTitle needs, for the paths that open a
// PR outside the deterministic runner. defaultReadCreateContext is not reused
// because it re-runs AC reconciliation. Either context may be missing, since the
// other can supply the text; no title text at all is an error, so no caller opens
// a PR titled with a bare scope.
func ReadTitleSnapshot(workdir string, issueNumber int) (PRCreateSnapshot, error) {
	snap := PRCreateSnapshot{IssueNumber: issueNumber}
	data, err := readFile(pipelineContextPath(workdir, fmt.Sprintf("issue-%d.json", issueNumber)))
	switch {
	case err == nil:
		var issue struct {
			Title string `json:"title"`
			Type  string `json:"type"`
		}
		if err := json.Unmarshal(data, &issue); err != nil {
			return snap, fmt.Errorf("parse issue context: %w", err)
		}
		snap.IssueTitle, snap.IssueType = issue.Title, issue.Type
	case !errors.Is(err, fs.ErrNotExist):
		return snap, fmt.Errorf("read issue context: %w", err)
	}

	data, err = readFile(pipelineContextPath(workdir, fmt.Sprintf("dev-%d.json", issueNumber)))
	switch {
	case err == nil:
		var dev struct {
			PRSummary string `json:"pr_summary"`
		}
		if err := json.Unmarshal(data, &dev); err != nil {
			return snap, fmt.Errorf("parse dev context: %w", err)
		}
		snap.PRSummary = dev.PRSummary
	case !errors.Is(err, fs.ErrNotExist):
		return snap, fmt.Errorf("read dev context: %w", err)
	}
	if snap.IssueType == "" {
		snap.IssueType = branchType(workdir)
	}
	if text, _ := titleText(snap); text == "" {
		return snap, fmt.Errorf("issue #%d has neither a pr_summary nor an issue title", issueNumber)
	}
	return snap, nil
}

// branchType reads the issue type off a `<type>/<N>-slug` branch, for a title
// rendered without an issue context. An unrecognized branch yields "", which
// titlePrefix renders as chore.
func branchType(workdir string) string {
	out, err := exec.Command("git", "-C", workdir, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	head, _, found := strings.Cut(strings.TrimSpace(string(out)), "/")
	if !found {
		return ""
	}
	return head
}
