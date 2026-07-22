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

// extractSummary returns the first markdown section (up to the second H1/H2),
// capped at maxLines. Mirrors steeringSources.extractSummary.
func extractSummary(content string, maxLines int) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	foundFirstHeader := false
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			if foundFirstHeader {
				break
			}
			foundFirstHeader = true
		}
		result = append(result, line)
		if len(result) >= maxLines {
			break
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

// readProjectDescription reads CLAUDE.md, else the user part of AGENTS.md (the
// managed block is stripped first so generated steering is never read back).
func readProjectDescription(projectRoot string) string {
	if claudeMd, ok := readFileGracefully(filepath.Join(projectRoot, "CLAUDE.md")); ok {
		return extractSummary(claudeMd, 50)
	}
	if agentsMd, ok := readFileGracefully(filepath.Join(projectRoot, "AGENTS.md")); ok {
		userPart := strings.TrimSpace(stripManagedSteeringBlock(agentsMd))
		if userPart != "" {
			return extractSummary(userPart, 50)
		}
	}
	return ""
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
// (no per-issue task; that arrives via the prompt) so the block is commit-safe.
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

// computeNextAgentsMd is the pure transform: given the existing AGENTS.md text
// (hasExisting=false ≈ no file) and the project root, return the next AGENTS.md.
func computeNextAgentsMd(existing string, hasExisting bool, projectRoot string) string {
	return upsertManagedSteeringBlock(existing, hasExisting, assembleSteeringContent(projectRoot))
}
