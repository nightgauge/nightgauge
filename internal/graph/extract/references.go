package extract

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/graph"
)

// References extracts one `references` edge from the issue node issueRef to
// every repository path the issue body mentions (#139).
//
// It is not part of All(): its source is an issue body, not a repository root.
// Provenance is the issue reference with the 1-based body line that carries
// the path. Each edge is emitted without checking that the path exists — the
// package doc's second rule — so against a graph that lacks the file node the
// edge is reported by graph.Dangling(). A caller decides where a path resolves
// by testing it against each repository's file set.
//
// Edge attrs:
//   - line_text: the trimmed body line, for evidence.
//   - in_acceptance_criteria: "true" when the line sits under a heading that
//     names the acceptance criteria.
//   - dir: "true" when the reference ends in "/" and names a directory.
//
// A path mentioned twice yields one edge, at its first line.
func References(issueRef, body string) Result {
	res := Result{Extractor: "references", Graph: graph.New()}
	if strings.TrimSpace(body) == "" {
		res.Skipped = "no body"
		return res
	}
	prov := graph.Provenance{Extractor: res.Extractor, Source: issueRef}
	from, err := addNode(res.Graph, graph.NodeIssue, issueRef, prov, "", nil)
	if err != nil {
		res.Skipped = err.Error()
		return res
	}

	seen := map[string]bool{}
	inAC := false
	inFence := false
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(trimmed, "#") {
			if h := headingText(trimmed); h != "" {
				inAC = strings.Contains(strings.ToLower(h), "acceptance criteria")
				continue
			}
		}
		for _, p := range pathTokens(line) {
			if seen[p] {
				continue
			}
			seen[p] = true
			ep := graph.Provenance{Extractor: res.Extractor, Source: issueRef, SourceLine: i + 1}
			attrs := graph.Attrs{
				"line_text":              trimmed,
				"in_acceptance_criteria": strconv.FormatBool(inAC),
				"dir":                    strconv.FormatBool(strings.HasSuffix(p, "/")),
			}
			to := graph.MakeNodeID(graph.NodeFile, strings.TrimSuffix(p, "/"))
			if err := addEdge(res.Graph, graph.EdgeReferences, from, to, ep, attrs); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
	}
	return res
}

// headingText returns the text of an ATX markdown heading, or "" when the line
// is not one ("#139" or "#!/bin/sh" are not headings).
func headingText(line string) string {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n > 6 || n >= len(line) || line[n] != ' ' {
		return ""
	}
	return strings.TrimSpace(line[n:])
}

var (
	// tokenSeparators turns markdown punctuation into whitespace so a path in
	// backticks, a link target or a parenthesis is a bare token.
	tokenSeparators = strings.NewReplacer(
		"`", " ", "(", " ", ")", " ", "[", " ", "]", " ", "<", " ", ">", " ",
		"\"", " ", "'", " ", ",", " ", ";", " ", "|", " ", "*", " ",
	)
	// lineSuffix strips a trailing ":123" or ":12-34" location.
	lineSuffix = regexp.MustCompile(`:\d+(-\d+)?$`)
	// extension is a final path segment's file extension.
	extension = regexp.MustCompile(`\.[A-Za-z0-9]{1,10}$`)
	// pathChars is the character set a repository-relative path may use.
	pathChars = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)
)

// pathTokens returns the path-like tokens on one body line: a token containing
// "/" whose last segment has a file extension, or that ends in "/". URLs,
// absolute paths, "owner/repo#N" and "#N" are not paths.
func pathTokens(line string) []string {
	var out []string
	for _, tok := range strings.Fields(tokenSeparators.Replace(line)) {
		if strings.Contains(tok, "://") || strings.HasPrefix(tok, "www.") ||
			strings.Contains(tok, "#") {
			continue
		}
		tok = strings.TrimRight(tok, ".:!?")
		tok = lineSuffix.ReplaceAllString(tok, "")
		tok = strings.TrimPrefix(tok, "./")
		if tok == "" || strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~") ||
			strings.HasPrefix(tok, "../") || !strings.Contains(tok, "/") ||
			!pathChars.MatchString(tok) || strings.Contains(tok, "//") {
			continue
		}
		if strings.HasSuffix(tok, "/") {
			if strings.Trim(tok, "/") == "" {
				continue
			}
			out = append(out, tok)
			continue
		}
		last := tok[strings.LastIndex(tok, "/")+1:]
		if !extension.MatchString(last) || strings.Trim(last, ".") == "" {
			continue
		}
		out = append(out, tok)
	}
	return out
}
