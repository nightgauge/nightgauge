package blogwatch

import (
	"fmt"
	"strings"
)

// MaxExcerptLen bounds the one quoted excerpt in an issue body, in runes.
const MaxExcerptLen = 240

// IssueLabels is the closed label set of every issue blog-watch files. It
// never carries a label that would dispatch work or skip triage.
func IssueLabels() []string {
	return []string{"type:feature", "area:blog-watch"}
}

// BuildIssueBody renders an Adopted issue: our measured gap first, at most one
// fenced excerpt, then a Trigger line. The post is the trigger, not the spec.
func BuildIssueBody(gap, excerpt, postURL string) (string, error) {
	gap = strings.TrimSpace(gap)
	excerpt = sanitizeExcerpt(excerpt)
	if gap == "" {
		return "", fmt.Errorf("the gap statement is empty")
	}
	if excerpt != "" && strings.TrimSpace(strings.ReplaceAll(gap, excerpt, "")) == "" {
		return "", fmt.Errorf("the gap statement is only the excerpt; state our gap in our words")
	}
	if strings.Contains(gap, "```") {
		return "", fmt.Errorf("the gap statement must not contain a fenced block")
	}
	if _, ok := Normalize(postURL); !ok {
		return "", fmt.Errorf("post url %q is not an absolute https URL", postURL)
	}
	var b strings.Builder
	b.WriteString(gap)
	b.WriteString("\n\n")
	if excerpt != "" {
		b.WriteString("Excerpt from the post (untrusted, quoted verbatim):\n\n```text\n")
		b.WriteString(excerpt)
		b.WriteString("\n```\n\n")
	}
	b.WriteString("Trigger: ")
	b.WriteString(postURL)
	b.WriteString("\n")
	return b.String(), nil
}

func sanitizeExcerpt(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxExcerptLen {
		s = string(r[:MaxExcerptLen-1]) + "…"
	}
	return s
}
