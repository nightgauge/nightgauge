package codexprovision

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// AGENTS.md managed-block markers (HTML comments — AGENTS.md is markdown). The
// generator owns everything between them; user content outside is preserved.
// Mirrors steeringSources.ts (#4028).
const (
	steeringManagedBegin = "<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->"
	steeringManagedEnd   = "<!-- END NIGHTGAUGE MANAGED STEERING -->"
)

var (
	trailNLSteer = regexp.MustCompile(`\n+$`)
	leadNLSteer  = regexp.MustCompile(`^\n+`)
)

// readFileGracefully returns a file's contents, or ("", false) if absent.
func readFileGracefully(filePath string) (string, bool) {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return "", false
	}
	return string(b), true
}

var (
	summaryHeadingRe = regexp.MustCompile(`^(#{1,6})([ \t]|$)`)
	summaryBlankRe   = regexp.MustCompile(`^[ \t\r]*$`)
	summaryFenceRe   = regexp.MustCompile("^[ \t]*(```|~~~)")
)

// extractSummary returns up to maxLines lines of a markdown document's
// meaningful content, across sections. A heading is kept only when body text
// follows it before a heading of the same or a higher level, so a title that
// is immediately followed by a sub-heading keeps the sub-section's body
// instead of ending the summary at the title (issue 1675). Runs of blank lines
// collapse to one, and lines inside fenced code blocks are body, never
// headings. Mirrors steeringSources.extractSummary byte-for-byte.
func extractSummary(content string, maxLines int) string {
	type heading struct {
		level      int
		line       string
		blankAfter bool
	}
	var (
		result       []string
		isHeading    []bool
		pending      []heading
		pendingBlank bool
		inFence      bool
	)
	emit := func(line string, head bool) bool {
		if len(result) >= maxLines {
			return false
		}
		if pendingBlank && len(result) > 0 {
			result = append(result, "")
			isHeading = append(isHeading, false)
			pendingBlank = false
			if len(result) >= maxLines {
				return false
			}
		}
		pendingBlank = false
		result = append(result, line)
		isHeading = append(isHeading, head)
		return len(result) < maxLines
	}
	for _, line := range strings.Split(content, "\n") {
		if !inFence {
			if m := summaryHeadingRe.FindStringSubmatch(line); m != nil {
				level := len(m[1])
				kept := pending[:0]
				for _, h := range pending {
					if h.level < level {
						kept = append(kept, h)
					}
				}
				pending = append(kept, heading{level: level, line: line})
				continue
			}
			if summaryBlankRe.MatchString(line) {
				if n := len(pending); n > 0 {
					pending[n-1].blankAfter = true
				} else {
					pendingBlank = true
				}
				continue
			}
		}
		if summaryFenceRe.MatchString(line) {
			inFence = !inFence
		}
		cont := true
		for _, h := range pending {
			if cont = emit(h.line, true); !cont {
				break
			}
			pendingBlank = h.blankAfter
		}
		pending = pending[:0]
		if !cont || !emit(line, false) {
			break
		}
	}
	// The line budget can run out between a heading and its body; a trailing
	// heading is then as empty as any other bodiless heading, so drop it.
	for n := len(result); n > 0 && (isHeading[n-1] || summaryBlankRe.MatchString(result[n-1])); n-- {
		result = result[:n-1]
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

// readProjectDescription summarises the repository's canonical agent contract:
// the user part of AGENTS.md (the managed steering block is stripped first so
// generated steering is never read back). CLAUDE.md is only a fallback, for a
// repository with no usable AGENTS.md; a leading `@AGENTS.md` import is skipped
// there because it is an adapter line, not a description (issue 1675).
func readProjectDescription(projectRoot string) string {
	if agentsMd, ok := readFileGracefully(filepath.Join(projectRoot, "AGENTS.md")); ok {
		if userPart := strings.TrimSpace(stripManagedSteeringBlock(agentsMd)); userPart != "" {
			return extractSummary(userPart, 50)
		}
	}
	if claudeMd, ok := readFileGracefully(filepath.Join(projectRoot, "CLAUDE.md")); ok {
		if body := strings.TrimSpace(stripLeadingAgentsImport(claudeMd)); body != "" {
			return extractSummary(body, 50)
		}
	}
	return ""
}

// stripLeadingAgentsImport drops the first non-blank line of a CLAUDE.md when it
// is the `@AGENTS.md` (or `@./AGENTS.md`) import. Mirrors
// steeringSources.stripLeadingAgentsImport.
func stripLeadingAgentsImport(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if summaryBlankRe.MatchString(line) {
			continue
		}
		t := strings.Trim(line, " \t\r")
		if t == "@AGENTS.md" || t == "@./AGENTS.md" {
			return strings.Join(lines[i+1:], "\n")
		}
		return content
	}
	return content
}

// readFirstAvailable reads the first existing file from candidates, returning
// its summary at maxLines, or "".
func readFirstAvailable(maxLines int, candidates ...string) string {
	for _, c := range candidates {
		if content, ok := readFileGracefully(c); ok {
			return extractSummary(content, maxLines)
		}
	}
	return ""
}

func readStandards(projectRoot string) string {
	return readFirstAvailable(80,
		filepath.Join(projectRoot, "standards", "code-standards.md"),
		filepath.Join(projectRoot, "docs", "CODE_STANDARDS.md"),
	)
}

func readSecurity(projectRoot string) string {
	return readFirstAvailable(60,
		filepath.Join(projectRoot, "standards", "security.md"),
		filepath.Join(projectRoot, "docs", "SECURITY_AND_ERROR_HANDLING.md"),
	)
}

func readGitWorkflow(projectRoot string) string {
	return readFirstAvailable(40, filepath.Join(projectRoot, "docs", "GIT_WORKFLOW.md"))
}

// assembleSteeringContent builds the inner content of the AGENTS.md managed
// block: provider-neutral baseline steering (project, standards, security, git
// workflow, key rules). Mirrors CodexContextGenerator.assembleContent — stable
// (no per-issue task; that arrives via the prompt) so regeneration is
// idempotent. It is never committed: see guard.go (issue 1675).
func assembleSteeringContent(projectRoot string) string {
	var sections []string
	sections = append(sections,
		"# Nightgauge Pipeline Steering (Codex)\n",
		"_This block is managed by the Nightgauge pipeline. Edits inside the_\n"+
			"_markers are overwritten; add your own guidance outside them._\n",
	)
	if desc := readProjectDescription(projectRoot); desc != "" {
		sections = append(sections, "## Project\n", desc+"\n")
	}
	if std := readStandards(projectRoot); std != "" {
		sections = append(sections, "## Coding Standards\n", std+"\n")
	}
	if sec := readSecurity(projectRoot); sec != "" {
		sections = append(sections, "## Security\n", sec+"\n")
	}
	if git := readGitWorkflow(projectRoot); git != "" {
		sections = append(sections, "## Git Workflow\n", git+"\n")
	}
	sections = append(sections,
		"## Key Rules\n",
		"- Never push directly to main",
		"- Never hardcode secrets",
		"- Follow existing patterns in the codebase",
		"",
	)
	// Trim ALL trailing whitespace to mirror the TS `.trimEnd()` (not just
	// newlines) so both paths produce identical bytes.
	return strings.TrimRightFunc(strings.Join(sections, "\n"), unicode.IsSpace)
}

// upsertManagedSteeringBlock inserts/replaces the managed block in existing,
// preserving user content. Marker match is plain substring (the markers are
// unique HTML comments), mirroring steeringSources.upsertManagedBlock.
func upsertManagedSteeringBlock(existing string, hasExisting bool, blockInner string) string {
	wrapped := steeringManagedBegin + "\n" + blockInner + "\n" + steeringManagedEnd
	if !hasExisting || strings.TrimSpace(existing) == "" {
		return wrapped + "\n"
	}
	beginIdx := strings.Index(existing, steeringManagedBegin)
	endIdx := strings.Index(existing, steeringManagedEnd)
	if beginIdx != -1 && endIdx != -1 && endIdx > beginIdx {
		before := trailNLSteer.ReplaceAllString(existing[:beginIdx], "")
		after := leadNLSteer.ReplaceAllString(existing[endIdx+len(steeringManagedEnd):], "")
		switch {
		case before == "" && after == "":
			return wrapped + "\n"
		case before == "":
			return wrapped + "\n\n" + after
		case after == "":
			return before + "\n\n" + wrapped + "\n"
		default:
			return before + "\n\n" + wrapped + "\n\n" + after
		}
	}
	// No managed block yet — append below the user's content.
	return trailNLSteer.ReplaceAllString(existing, "") + "\n\n" + wrapped + "\n"
}

// stripManagedSteeringBlock removes the managed block, preserving user content.
func stripManagedSteeringBlock(existing string) string {
	beginIdx := strings.Index(existing, steeringManagedBegin)
	endIdx := strings.Index(existing, steeringManagedEnd)
	if beginIdx == -1 || endIdx == -1 || endIdx < beginIdx {
		return existing
	}
	before := trailNLSteer.ReplaceAllString(existing[:beginIdx], "")
	after := leadNLSteer.ReplaceAllString(existing[endIdx+len(steeringManagedEnd):], "")
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after
	}
}

// IsOnlyManagedSteeringChange reports whether two AGENTS.md snapshots differ
// only by Nightgauge's generated steering block.
func IsOnlyManagedSteeringChange(committed, working string) bool {
	return strings.TrimSpace(stripManagedSteeringBlock(committed)) ==
		strings.TrimSpace(stripManagedSteeringBlock(working))
}

// computeNextAgentsMd is the pure transform: given the existing AGENTS.md text
// (hasExisting=false ≈ no file) and the project root, return the next AGENTS.md.
func computeNextAgentsMd(existing string, hasExisting bool, projectRoot string) string {
	return upsertManagedSteeringBlock(existing, hasExisting, assembleSteeringContent(projectRoot))
}
