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
	// SourceLine is the trimmed body line the reference was parsed from. It
	// exists so a scheduler that blocks on a body-derived edge can name the
	// prose responsible instead of leaving an operator to read this file (#126).
	SourceLine string
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
	//
	// ⏸️ is deliberately part of the marker class even though a ⏸️ entry never
	// becomes an edge: recognizing the marker and then classifying it as
	// non-gating (see isNonGatingLine) makes the outcome an intentional
	// decision rather than an accident of which runes the class happens to
	// contain. See docs/AUTONOMOUS_ORCHESTRATOR.md for the marker contract.
	reStructuredEntry = regexp.MustCompile(
		`(?m)^[ \t]*-\s*([✅❌⚠️⏸]+)\s+([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// Markers that declare a line to be documentation rather than a gating
	// dependency: the ⏸️ pause marker, or an explicit textual token. A line
	// carrying any of these produces no dependency edge from any pattern.
	// See #126 — a line written to say "this is deferred, it does not gate us"
	// used to block dispatch exactly like a real blocker, and because the
	// epic-blockedBy cascade propagates a parent's blockers to its sub-issues,
	// one such line stalled an entire epic sub-tree with no visible cause.
	reNonGatingMarker = regexp.MustCompile(
		`(?i)⏸|\bdeferred\b|\bnot[- ]gating\b|\bnon[- ]gating\b`,
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

// isNonGatingLine reports whether a body line explicitly declares itself
// documentation rather than a gating dependency.
//
// The status markers the "## Cross-Repo Dependencies" format invites authors
// to use are only meaningful if the scheduler honours them. ⏸️ (and the
// textual "deferred" / "not-gating" / "non-gating" tokens) mean "recorded for
// context, does not block us"; ✅, ❌ and ⚠️ all remain gating. The marker
// contract is documented in docs/AUTONOMOUS_ORCHESTRATOR.md so that authors
// know these tokens carry scheduling weight and are not decoration.
func isNonGatingLine(line string) bool {
	return reNonGatingMarker.MatchString(line)
}

// lineAt returns the whole line containing byte offset off in s, trimmed of
// surrounding whitespace. Offsets outside s yield "".
func lineAt(s string, off int) string {
	if off < 0 || off > len(s) {
		return ""
	}
	start := strings.LastIndexByte(s[:off], '\n') + 1
	end := strings.IndexByte(s[off:], '\n')
	if end == -1 {
		end = len(s)
	} else {
		end += off
	}
	return strings.TrimSpace(s[start:end])
}

// extractDepContext returns the body slice(s) that count as dependency
// declarations for URL extraction:
//  1. Body under any ## Blocked by / ## Depends on / ## Dependencies /
//     ## Cross-Repo Dependencies header, until the next ## header or end of body.
//  2. Any individual line containing a "blocked by" or "depends on" textual
//     marker (so "Blocked by https://github.com/o/r/issues/42" works even
//     without a section header).
//
// Lines marked non-gating (see isNonGatingLine) are excluded from both.
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

	// Drop lines the author explicitly marked as non-gating. A URL sitting on
	// a "deferred / ⏸️" line is documentation even inside a dependency
	// section — without this filter the URL entry form re-creates exactly the
	// edge the marker was written to prevent (#126).
	var kept []string
	for _, line := range strings.Split(strings.Join(parts, "\n"), "\n") {
		if isNonGatingLine(line) {
			continue
		}
		kept = append(kept, line)
	}

	return strings.Join(kept, "\n")
}

// ParseCrossRepoRefs extracts cross-repo dependency references from an issue body.
// It handles three patterns:
//  1. "Blocked by <repo> #NNN"
//  2. "## Cross-Repo Dependencies" section with "- ✅/❌/⚠️/⏸️ <repo> #NNN" entries
//  3. "Depends on: <repo> #NNN" / "Depends on <repo> #NNN"
//
// A line that carries a non-gating marker (⏸️, or a textual "deferred" /
// "not-gating" / "non-gating" token) yields no reference from any pattern —
// it is documentation, not a dependency. See isNonGatingLine and
// docs/AUTONOMOUS_ORCHESTRATOR.md for the marker contract.
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
	for _, m := range reBlockedBy.FindAllStringSubmatchIndex(body, -1) {
		line := lineAt(body, m[0])
		if isNonGatingLine(line) {
			continue
		}
		repo := resolveAlias(body[m[2]:m[3]], repoAliases)
		num, _ := strconv.Atoi(body[m[4]:m[5]])
		if repo != "" && num > 0 {
			addRef(CrossRepoRef{Repo: repo, Number: num, Source: "body_text", SourceLine: line})
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

		for _, m := range reStructuredEntry.FindAllStringSubmatchIndex(sectionBody, -1) {
			line := lineAt(sectionBody, m[0])
			// ⏸️ / "deferred" / "not-gating" entries are documentation the
			// author recorded for context — they must not become scheduler
			// edges. ✅, ❌ and ⚠️ all still gate; ⚠️ reads as "watch this",
			// which is a dependency worth honouring (#126).
			if isNonGatingLine(line) {
				continue
			}
			status := sectionBody[m[2]:m[3]]
			repo := resolveAlias(sectionBody[m[4]:m[5]], repoAliases)
			num, _ := strconv.Atoi(sectionBody[m[6]:m[7]])
			if repo != "" && num > 0 {
				verified := strings.Contains(status, "✅")
				addRef(CrossRepoRef{
					Repo:       repo,
					Number:     num,
					Source:     "structured_section",
					Verified:   verified,
					SourceLine: line,
				})
			}
		}
	}

	// 3. "Depends on ..." pattern
	for _, m := range reDependsOn.FindAllStringSubmatchIndex(body, -1) {
		line := lineAt(body, m[0])
		if isNonGatingLine(line) {
			continue
		}
		repo := resolveAlias(body[m[2]:m[3]], repoAliases)
		num, _ := strconv.Atoi(body[m[4]:m[5]])
		if repo != "" && num > 0 {
			addRef(CrossRepoRef{Repo: repo, Number: num, Source: "depends_on", SourceLine: line})
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
		for _, m := range reGitHubURL.FindAllStringSubmatchIndex(depContext, -1) {
			repo := resolveAlias(depContext[m[2]:m[3]], repoAliases)
			if repo == "" {
				repo = depContext[m[2]:m[3]] // accept as-is when not in alias map
			}
			num, _ := strconv.Atoi(depContext[m[4]:m[5]])
			if repo != "" && num > 0 {
				addRef(CrossRepoRef{
					Repo:       repo,
					Number:     num,
					Source:     "body_text",
					SourceURL:  depContext[m[0]:m[1]],
					SourceLine: lineAt(depContext, m[0]),
				})
			}
		}

		// 5. Full GitLab issue URLs.
		for _, m := range reGitLabURL.FindAllStringSubmatchIndex(depContext, -1) {
			// group 2 is the group/project path (may be multi-level)
			repo := depContext[m[4]:m[5]]
			num, _ := strconv.Atoi(depContext[m[6]:m[7]])
			if repo != "" && num > 0 {
				addRef(CrossRepoRef{
					Repo:       repo,
					Number:     num,
					Source:     "body_text",
					SourceURL:  depContext[m[0]:m[1]],
					SourceLine: lineAt(depContext, m[0]),
				})
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
