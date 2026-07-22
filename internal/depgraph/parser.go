package depgraph

import (
	"regexp"
	"strconv"
	"strings"
)

// CrossRepoRef is a dependency reference extracted from an issue body.
type CrossRepoRef struct {
	Repo      string // normalized full repo name (e.g. "acme/platform")
	Number    int
	Source    string // "body_text", "structured_section", "depends_on"
	Verified  bool   // from structured section: checkmark = true
	SourceURL string // original full URL when parsed from a URL reference (empty for slug refs)
}

// DefaultRepoAliases maps short names used in issue bodies to full GitHub
// repo names. Callers may extend or override these.
var DefaultRepoAliases = map[string]string{
	"platform":                           "acme/platform",
	"acme-platform":           "acme/platform",
	"flutter":                            "acme/mobile",
	"acme-mobile":            "acme/mobile",
	"angular":                            "acme/dashboard",
	"acme-dashboard":          "acme/dashboard",
	"core":                               "nightgauge/nightgauge",
	"nightgauge":                    "nightgauge/nightgauge",
	"nightgauge/nightgauge":           "nightgauge/nightgauge",
	"acme/platform":  "acme/platform",
	"acme/mobile":   "acme/mobile",
	"acme/dashboard": "acme/dashboard",
}

// Compiled regex patterns for parsing cross-repo references.
var (
	// "Blocked by platform #535" / "blocked by acme-mobile #127"
	// Also matches "Blocked by acme/platform#535"
	reBlockedBy = regexp.MustCompile(
		`(?i)blocked\s+by\s+([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// "Depends on: platform #NNN" / "depends on acme/platform#NNN"
	// Can match multiple comma/semicolon separated refs on the same line.
	reDependsOn = regexp.MustCompile(
		`(?i)depends?\s+on:?\s+([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// Structured section entries:
	// "- ✅ platform #535 — description" / "- ❌ flutter #127" / "- ⚠️ angular #152"
	reStructuredEntry = regexp.MustCompile(
		`(?m)^[ \t]*-\s*([✅❌⚠️]+)\s+([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// Section header detection for "## Cross-Repo Dependencies"
	reCrossRepoSection = regexp.MustCompile(
		`(?im)^#{1,3}\s+cross[- ]?repo\s+dependenc`,
	)

	// Dependency-declaration section headers. URL-based ref extraction is
	// scoped to the body slice under one of these headers — URLs appearing
	// anywhere else in the body (Goal prose, Plan steps, "see also" links)
	// are descriptive references, not dependencies. See #3635.
	reDepSectionHeader = regexp.MustCompile(
		`(?im)^#{1,3}\s+(blocked\s+by|depends?\s+on|dependencies|cross[- ]?repo\s+dependenc)`,
	)

	// Matches any ## header — used to terminate a dependency section.
	reAnyHeader = regexp.MustCompile(`(?m)^#{1,3}\s+[^\n]`)

	// Lines containing a "blocked by" or "depends on" textual marker.
	// URLs appearing on such a line are treated as deps even when the
	// line is outside a dep section (e.g. "Blocked by https://github.com/o/r/issues/42").
	reBlockedByOrDependsOnMarker = regexp.MustCompile(
		`(?i)(blocked\s+by|depends?\s+on)`,
	)

	// Full GitHub issue URL: https://github.com/owner/repo/issues/N
	reGitHubURL = regexp.MustCompile(
		`https://github\.com/([\w.-]+/[\w.-]+)/issues/(\d+)`,
	)

	// Full GitLab issue URL: https://<host>/group/project/-/issues/N
	// Host may be gitlab.com or a self-hosted instance.
	reGitLabURL = regexp.MustCompile(
		`https://([\w.-]+)/([\w.-]+(?:/[\w.-]+)+)/-/issues/(\d+)`,
	)
)

// extractDepContext returns the body slice(s) that count as dependency
// declarations for URL extraction:
//  1. Body under any ## Blocked by / ## Depends on / ## Dependencies /
//     ## Cross-Repo Dependencies header, until the next ## header or end of body.
//  2. Any individual line containing a "blocked by" or "depends on" textual
//     marker (so "Blocked by https://github.com/o/r/issues/42" works even
//     without a section header).
//
// Returns a single concatenated string. Empty input returns empty string.
// See #3635 — URLs in prose sections (Goal, Plan, etc.) were silently being
// promoted into hard dependency edges, blocking autonomous dispatch.
func extractDepContext(body string) string {
	if body == "" {
		return ""
	}

	var parts []string

	// 1. Dep-section bodies.
	for _, loc := range reDepSectionHeader.FindAllStringIndex(body, -1) {
		// Advance past the header line itself.
		sectionStart := loc[1]
		if nl := strings.IndexByte(body[sectionStart:], '\n'); nl != -1 {
			sectionStart += nl + 1
		}
		// Find next ## header to terminate the section.
		sectionEnd := len(body)
		if remaining := body[sectionStart:]; len(remaining) > 0 {
			if nextLoc := reAnyHeader.FindStringIndex(remaining); nextLoc != nil {
				sectionEnd = sectionStart + nextLoc[0]
			}
		}
		if sectionStart < sectionEnd {
			parts = append(parts, body[sectionStart:sectionEnd])
		}
	}

	// 2. Lines containing dep markers (outside any section).
	for _, line := range strings.Split(body, "\n") {
		if reBlockedByOrDependsOnMarker.MatchString(line) {
			parts = append(parts, line)
		}
	}

	return strings.Join(parts, "\n")
}

// ParseCrossRepoRefs extracts cross-repo dependency references from an issue body.
// It handles three patterns:
//  1. "Blocked by <repo> #NNN"
//  2. "## Cross-Repo Dependencies" section with "- ✅/❌/⚠️ <repo> #NNN" entries
//  3. "Depends on: <repo> #NNN" / "Depends on <repo> #NNN"
//
// repoAliases maps short names to full "owner/repo" names. If nil,
// DefaultRepoAliases is used.
func ParseCrossRepoRefs(body string, repoAliases map[string]string) []CrossRepoRef {
	if body == "" {
		return nil
	}
	if repoAliases == nil {
		repoAliases = DefaultRepoAliases
	}

	seen := make(map[string]bool) // "repo#number" dedup
	var refs []CrossRepoRef

	addRef := func(ref CrossRepoRef) {
		key := ref.Repo + "#" + strconv.Itoa(ref.Number)
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, ref)
	}

	// 1. "Blocked by ..." pattern
	for _, m := range reBlockedBy.FindAllStringSubmatch(body, -1) {
		repo := resolveAlias(m[1], repoAliases)
		num, _ := strconv.Atoi(m[2])
		if repo != "" && num > 0 {
			addRef(CrossRepoRef{Repo: repo, Number: num, Source: "body_text"})
		}
	}

	// 2. Structured "## Cross-Repo Dependencies" section
	if loc := reCrossRepoSection.FindStringIndex(body); loc != nil {
		// Extract the section: from header to next ## header or end of body
		sectionStart := loc[0]
		sectionBody := body[sectionStart:]
		// Find next ## header
		nextHeader := regexp.MustCompile(`(?m)^#{1,3}\s+[^\n]`)
		remaining := sectionBody[len(body[loc[0]:loc[1]]):]
		if nextLoc := nextHeader.FindStringIndex(remaining); nextLoc != nil {
			sectionBody = sectionBody[:len(body[loc[0]:loc[1]])+nextLoc[0]]
		}

		for _, m := range reStructuredEntry.FindAllStringSubmatch(sectionBody, -1) {
			status := m[1]
			repo := resolveAlias(m[2], repoAliases)
			num, _ := strconv.Atoi(m[3])
			if repo != "" && num > 0 {
				verified := strings.Contains(status, "✅")
				addRef(CrossRepoRef{Repo: repo, Number: num, Source: "structured_section", Verified: verified})
			}
		}
	}

	// 3. "Depends on ..." pattern
	for _, m := range reDependsOn.FindAllStringSubmatch(body, -1) {
		repo := resolveAlias(m[1], repoAliases)
		num, _ := strconv.Atoi(m[2])
		if repo != "" && num > 0 {
			addRef(CrossRepoRef{Repo: repo, Number: num, Source: "depends_on"})
		}
	}

	// 4 & 5. URL-based references are extracted only from dependency-declaration
	// contexts (dep-section bodies and blocked-by/depends-on marker lines).
	// URLs in prose (Goal, Plan, "see also") are descriptive references, not
	// dependencies — extracting them was silently blocking autonomous dispatch
	// of issues that mentioned an open parent epic in their narrative. See #3635.
	depContext := extractDepContext(body)
	if depContext != "" {
		// 4. Full GitHub issue URLs.
		for _, m := range reGitHubURL.FindAllStringSubmatch(depContext, -1) {
			repo := resolveAlias(m[1], repoAliases)
			if repo == "" {
				repo = m[1] // accept as-is when not in alias map
			}
			num, _ := strconv.Atoi(m[2])
			if repo != "" && num > 0 {
				addRef(CrossRepoRef{Repo: repo, Number: num, Source: "body_text", SourceURL: m[0]})
			}
		}

		// 5. Full GitLab issue URLs.
		for _, m := range reGitLabURL.FindAllStringSubmatch(depContext, -1) {
			// m[2] is the group/project path (may be multi-level)
			repo := m[2]
			num, _ := strconv.Atoi(m[3])
			if repo != "" && num > 0 {
				addRef(CrossRepoRef{Repo: repo, Number: num, Source: "body_text", SourceURL: m[0]})
			}
		}
	}

	return refs
}

// resolveAlias normalizes a repo reference using the alias map.
// Returns "" if the alias is unknown.
func resolveAlias(raw string, aliases map[string]string) string {
	raw = strings.TrimSpace(raw)
	// Try exact match first
	if full, ok := aliases[raw]; ok {
		return full
	}
	// Try case-insensitive match
	lower := strings.ToLower(raw)
	for k, v := range aliases {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	// If it already looks like "owner/repo", accept it as-is
	if strings.Contains(raw, "/") {
		return raw
	}
	return ""
}
