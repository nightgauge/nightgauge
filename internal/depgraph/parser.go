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
	"platform":              "acme/platform",
	"acme-platform":         "acme/platform",
	"flutter":               "acme/mobile",
	"acme-mobile":           "acme/mobile",
	"angular":               "acme/dashboard",
	"acme-dashboard":        "acme/dashboard",
	"core":                  "nightgauge/nightgauge",
	"nightgauge":            "nightgauge/nightgauge",
	"nightgauge/nightgauge": "nightgauge/nightgauge",
	"acme/platform":         "acme/platform",
	"acme/mobile":           "acme/mobile",
	"acme/dashboard":        "acme/dashboard",
}

// The dependency keyword, defined ONCE and composed into every pattern that
// needs it. Before #1505 each pattern spelled its own `blocked\s+by` /
// `depends?\s+on`, which meant the prose spelling was the only one recognised
// — and authors routinely write the board edge's own field name instead,
// copied straight from `blockedBy` / `dependsOn`. A body reading
//
//	This issue is `blockedBy` acme/platform#1253 …
//
// produced no edge at all and the issue was dispatched over an open blocker.
//
// The keyword therefore accepts every spelling of the same word pair: the two
// halves may be joined by whitespace, `-`, `_`, or nothing at all (camelCase,
// which is case-insensitive here and so needs no separate branch), and the
// whole keyword may be wrapped in the backticks or asterisks Markdown authors
// put around a field name.
//
// The trailing `\b` is load-bearing: without it `blockedBySomething#5` — an
// identifier, not a declaration — would match and gate the issue on #5.
const (
	// depBlockedByCore and depDependsOnCore are the two halves. They are kept
	// as complete pairs rather than a `(?:blocked|depends?)…(?:by|on)`
	// cross-product so that "blocked on" and "depends by" stay unmatched.
	depBlockedByCore = `blocked[\s_-]*by\b`
	depDependsOnCore = `depends?[\s_-]*on\b`

	// depKeywordCore is either of them.
	depKeywordCore = `(?:` + depBlockedByCore + `|` + depDependsOnCore + `)`

	// depKeywordWrap is the Markdown emphasis that may hug the keyword:
	// `` `blockedBy` ``, `**Depends on**`.
	depKeywordWrap = "[`*]*"

	// depKeyword is the keyword plus its optional wrapping. Compile it
	// case-insensitively — every pattern below does.
	depKeyword = depKeywordWrap + depKeywordCore + depKeywordWrap

	// depKeywordLead is what may sit between the keyword and the reference it
	// introduces: the closing wrapper, an optional colon, more wrapping
	// (`**Depends on:**`), and whitespace.
	depKeywordLead = depKeywordWrap + `\s*:?` + depKeywordWrap + `\s*`
)

// Compiled regex patterns for parsing cross-repo references.
var (
	// "Blocked by platform #535" / "blocked by acme-mobile #127"
	// Also matches "Blocked by acme/platform#535" and "`blockedBy` acme/platform#535".
	reBlockedBy = regexp.MustCompile(
		`(?i)` + depKeywordWrap + depBlockedByCore + depKeywordLead +
			`([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// "Depends on: platform #NNN" / "depends on acme/platform#NNN"
	// Can match multiple comma/semicolon separated refs on the same line.
	reDependsOn = regexp.MustCompile(
		`(?i)` + depKeywordWrap + depDependsOnCore + depKeywordLead +
			`([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// A dependency DECLARATION keyword anywhere on a line: "Blocked by …",
	// "Depends on …", "`blockedBy`", "**Depends on:**". Used by the same-repo
	// pass to decide whether the bare `#N` tokens on that line are
	// dependencies or ordinary prose references.
	reDeclKeyword = regexp.MustCompile(
		`(?i)` + depKeywordWrap + depKeywordCore + depKeywordLead,
	)

	// A sentence terminator: `.`, `;`, `!` or `?` FOLLOWED BY whitespace or the
	// end of the string. The trailing context is what keeps a period inside a
	// token from ending a sentence — "v1.2", "e.g.", "acme/repo#5.0" all carry
	// a `.` that no author meant as a full stop. Used to bound a dependency
	// keyword's claim to its own sentence; see depDeclarationFragments (#1502).
	reSentenceBreak = regexp.MustCompile(`[.;!?](?:[ \t]|$)`)

	// A reference list CONTINUING after a separator: the remainder begins with
	// another `#N` (optionally repo-qualified). "Blocked by #1187; #1190" is one
	// enumeration written with semicolons, which the "Depends on" spelling has
	// always accepted — so a `;` in front of another reference separates items
	// rather than ending the declaration. A `;` in front of prose ends it.
	reRefListContinues = regexp.MustCompile(`^[ \t]*(?:[\w-]+(?:/[\w-]+)?[ \t]*)?#\d`)

	// A possibly repo-qualified reference: the token in front of a `#N`, and
	// the gap between them. The same-repo pass uses it to decide which `#N`
	// tokens already belong to another repository — see maskQualifiedRefs.
	reQualifiedRef = regexp.MustCompile(
		`([\w-]+(?:/[\w-]+)?)([ \t]*)#\d+`,
	)

	// A bare issue reference with no repo in front of it.
	reBareRef = regexp.MustCompile(`#(\d+)`)

	// Structured section entries:
	// "- ✅ platform #535 — description" / "- ❌ flutter #127" / "- ⚠️ angular #152"
	// / "- owner/repo#535 — description" (unmarked, gates by default — #132)
	//
	// ⏸️ is deliberately part of the marker class even though a ⏸️ entry never
	// becomes an edge: recognizing the marker and then classifying it as
	// non-gating (see isNonGatingLine) makes the outcome an intentional
	// decision rather than an accident of which runes the class happens to
	// contain. The marker group is optional so a bare, unmarked entry also
	// matches — see docs/AUTONOMOUS_ORCHESTRATOR.md for the marker contract.
	reStructuredEntry = regexp.MustCompile(
		`(?m)^[ \t]*-\s*([✅❌⚠️⏸]*)\s*([\w-]+(?:/[\w-]+)?)\s*#(\d+)`,
	)

	// Textual tokens that declare a line to be documentation rather than a
	// gating dependency. Unlike the ⏸️ marker these are ordinary English words
	// that can appear incidentally, so they yield to an explicit dependency
	// declaration on the same line — see isNonGatingLine.
	reNonGatingText = regexp.MustCompile(
		`(?i)\bdeferred\b|\bnot[- ]gating\b|\bnon[- ]gating\b`,
	)

	// A reference INTRODUCED by a parent/child or bookkeeping relation:
	// "Part of #308", "Epic: #5", "Closes #99", "See also #12". This is the
	// SINGLE definition of "this `#N` names an issue for a reason that is not
	// a dependency" — see maskBookkeepingRefs.
	//
	// `Part of #N` is the workspace's parent-epic convention, written by
	// internal/github (sub-issue creation), internal/cmd/spike (materialize)
	// and the PR body builder in internal/orchestrator/stages; it is the line
	// that put this regex here. The rest are the other relations authors
	// habitually park in the same section — closing keywords, tracking links,
	// "see also" — plus the `(Wave N)` planning parenthetical, which carries
	// its own number and must not be mistaken for one.
	//
	// The relation word must sit immediately in front of the reference, so a
	// genuine dependency whose PROSE happens to use one of these words —
	// "- #535 — needed for the epic rollout" — is still a dependency. Matching
	// the word anywhere on the line would reintroduce the quiet direction this
	// regex exists to remove.
	reBookkeepingRef = regexp.MustCompile(
		`(?i)\b(part\s+of|parent(?:\s+issue)?|epic|sub[- ]?issue\s+of|` +
			`child\s+of|tracks|tracked\s+by|related\s+to|related|see\s+also|` +
			`closes|closed\s+by|fixes|fixed\s+by|resolves|resolved\s+by|` +
			`wave)\b[\s:.,;—–-]*#\d+`,
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
		`(?im)^#{1,3}\s+` + depKeywordWrap +
			`(?:` + depKeywordCore + `|dependencies|cross[- ]?repo\s+dependenc)`,
	)

	// Matches any ## header — used to terminate a dependency section.
	reAnyHeader = regexp.MustCompile(`(?m)^#{1,3}\s+[^\n]`)

	// Lines containing a "blocked by" or "depends on" textual marker.
	// URLs appearing on such a line are treated as deps even when the
	// line is outside a dep section (e.g. "Blocked by https://github.com/o/r/issues/42").
	reBlockedByOrDependsOnMarker = regexp.MustCompile(
		`(?i)` + depKeyword,
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

// nonGatingMarker is the ⏸️ "pause" marker. Only the base rune is matched so
// the marker is recognized with or without its U+FE0F variation selector.
const nonGatingMarker = "⏸"

// isNonGatingLine reports whether a body line declares itself documentation
// rather than a gating dependency.
//
// The status markers the "## Cross-Repo Dependencies" format invites authors
// to use are only meaningful if the scheduler honours them: ⏸️ and the textual
// "deferred" / "not-gating" / "non-gating" tokens mean "recorded for context,
// does not block us", while ✅, ❌ and ⚠️ all remain gating.
//
// Precedence, strongest first:
//
//  1. The ⏸️ marker suppresses unconditionally. An author placed it there
//     deliberately, so it wins even on a "Blocked by" line — writing both
//     means the marker.
//  2. An explicit dependency declaration ("Blocked by …" / "Depends on …")
//     defeats the textual tokens. Those tokens are ordinary English words that
//     appear incidentally — "Blocked by platform #491 — needed for the
//     deferred rollout" is a real blocker, and dropping it would dispatch work
//     before its prerequisite, silently and with no operator-visible symptom.
//     A permissive failure like that is strictly worse than the loud one this
//     suppression exists to prevent, so a stray adjective never overrides an
//     author who wrote the declaration outright.
//  3. Otherwise a textual token suppresses.
//
// The contract is documented in docs/AUTONOMOUS_ORCHESTRATOR.md so authors
// know these tokens carry scheduling weight and are not decoration.
func isNonGatingLine(line string) bool {
	if strings.Contains(line, nonGatingMarker) {
		return true
	}
	if reBlockedByOrDependsOnMarker.MatchString(line) {
		return false
	}
	return reNonGatingText.MatchString(line)
}

// maskBookkeepingRefs blanks every reference introduced by a parent/child or
// bookkeeping relation, preserving length, so that what survives a
// dependency-section line is only the references that actually declare a
// dependency. `Part of #308` yields nothing; `- #101` is untouched.
//
// It exists because the dependency-section pass reads a line's POSITION as the
// declaration — there is no keyword to trim against — and authors put the
// membership line inside that section. The body that produced the regression
// was exactly:
//
//	## Dependencies
//
//	Blocked by #300.
//
//	(Wave 3)
//
//	Part of #308
//
// #308 is the parent epic, and an epic never closes before its children, so
// reading `Part of #308` as a dependency deadlocks the issue and — through the
// epic cascade — every sibling in the wave, permanently and silently. That is
// the "fails toward never dispatching" direction: quieter than #1492 and
// strictly worse for throughput (#1497).
//
// Masking rather than dropping the whole line is deliberate: a dependency
// whose description merely mentions one of these words — "- #535 — needed for
// the epic rollout" — must stay a dependency.
func maskBookkeepingRefs(s string) string {
	locs := reBookkeepingRef.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	b := []byte(s)
	for _, loc := range locs {
		for i := loc[0]; i < loc[1]; i++ {
			b[i] = ' '
		}
	}
	return string(b)
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
			// status may be "" for an unmarked entry (marker group is
			// optional). An empty status is treated as gating-unverified,
			// same as ❌/⚠️ — only an explicit ✅ marks Verified true (#132).
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

	// 3b. Repo-qualified references elsewhere in a keyword's own SENTENCE.
	// Patterns 1 and 3 only see a reference sitting immediately after the
	// keyword, so a sentence that enumerates two blockers across a clause —
	//
	//	This issue is `blockedBy` acme/platform#1253 and, per the epic's
	//	Wave-1-first rule, acme/platform#1252 …
	//
	// declared two and yielded one. The bare-`#N` pass already treats a
	// keyword's whole sentence as its claim (see depDeclarationFragments);
	// this makes the qualified spelling agree with it, which is the same
	// symmetry #1492 restored between the cross-repo and same-repo forms.
	//
	// Section fragments are excluded: their references have no keyword and are
	// governed by the structured-entry pattern and its ✅/⏸️ markers.
	for _, frag := range depDeclarationFragments(body) {
		if frag.source == "structured_section" {
			continue
		}
		for _, m := range reQualifiedRef.FindAllStringSubmatchIndex(frag.text, -1) {
			repo := resolveAlias(frag.text[m[2]:m[3]], repoAliases)
			if repo == "" {
				continue // prose in front of a bare reference, not a repo
			}
			num, _ := strconv.Atoi(strings.TrimPrefix(frag.text[m[5]:m[1]], "#"))
			if num > 0 {
				addRef(CrossRepoRef{
					Repo:       repo,
					Number:     num,
					Source:     frag.source,
					SourceLine: frag.line,
				})
			}
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

// depFragment is one slice of an issue body that may declare dependencies,
// plus the whole line it came from (for SourceLine) and the parser source
// label to record on any ref found in it.
type depFragment struct {
	text   string // the part of the line eligible for a bare `#N` scan
	line   string // the whole trimmed line, for diagnostics
	source string // "depends_on" | "body_text" | "structured_section"
}

// sentenceEnd returns the offset at which the sentence beginning at the start
// of s ends, or -1 when s carries no terminator at all (in which case the whole
// remainder is one sentence).
//
// A `;` that is immediately followed by another reference is a LIST separator,
// not a terminator: "Blocked by #1187; #1190" is a single enumeration, a
// spelling the "Depends on" pattern has always accepted. A `;` followed by
// prose — "Blocked by #5; see also #7" — ends the declaration, and the `#7`
// after it is no longer claimed.
func sentenceEnd(s string) int {
	for _, br := range reSentenceBreak.FindAllStringIndex(s, -1) {
		if s[br[0]] == ';' && reRefListContinues.MatchString(s[br[0]+1:]) {
			continue
		}
		return br[0]
	}
	return -1
}

// depDeclarationFragments returns the body slices in which a BARE `#N` (no
// repo token in front of it) counts as a dependency declaration:
//
//  1. The SENTENCE following each "Blocked by" / "Depends on" keyword on a
//     line. Every keyword on the line is honoured, not just the first, so
//     "Blocked by #5. Depends on #6" declares both — with the correct source
//     label on each.
//
//     The text BEFORE a keyword is excluded on purpose — "Closes #99 —
//     depends on #100" declares one dependency, not two — and so is the text
//     AFTER the keyword's sentence ends. A fragment runs from the keyword to
//     the earliest of the next keyword on the line or a sentence terminator
//     (see reSentenceBreak), because a keyword that claims everything to end
//     of line silently promotes an unrelated second sentence into a hard
//     gating edge. The line that produced this was:
//
//     Blocked by Epic #295. Reaches its full value with Epic #301 — …
//
//     One declared dependency, #295; #301 is prose. Reading both held two
//     ready child issues indefinitely, with the epic cascade spreading the
//     hold to their siblings — the same "fails toward never dispatching"
//     direction as #1497, and just as quiet (#1502).
//
//  2. Every line under a `## Blocked by` / `## Depends on` / `## Dependencies`
//     / `## Cross-Repo Dependencies` header, until the next header.
//
// Lines the author marked non-gating (⏸️, "deferred", "not-gating") yield
// nothing, exactly as they do for every other pattern, and so do references
// introduced by a parent/child or bookkeeping relation — `Part of #N` in a
// dependency section is epic membership, not a blocker (see
// maskBookkeepingRefs, #1497).
func depDeclarationFragments(body string) []depFragment {
	if body == "" {
		return nil
	}
	var out []depFragment

	// 1. Dependency-declaration keyword lines, anywhere in the body.
	for _, line := range strings.Split(body, "\n") {
		locs := reDeclKeyword.FindAllStringIndex(line, -1)
		if len(locs) == 0 {
			continue
		}
		if isNonGatingLine(line) {
			continue
		}
		for i, loc := range locs {
			// The fragment ends where the next declaration on the line begins,
			// so neither keyword claims the other's references.
			end := len(line)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			text := line[loc[1]:end]
			// …and no later than the end of the keyword's own sentence.
			if cut := sentenceEnd(text); cut >= 0 {
				text = text[:cut]
			}
			source := "body_text"
			if strings.Contains(strings.ToLower(line[loc[0]:loc[1]]), "depend") {
				source = "depends_on"
			}
			out = append(out, depFragment{
				text:   text,
				line:   strings.TrimSpace(line),
				source: source,
			})
		}
	}

	// 2. Dependency-section bodies. A bare entry under "## Dependencies" is a
	// dependency by virtue of where it sits, with no keyword to repeat.
	for _, loc := range reDepSectionHeader.FindAllStringIndex(body, -1) {
		sectionStart := loc[1]
		if nl := strings.IndexByte(body[sectionStart:], '\n'); nl != -1 {
			sectionStart += nl + 1
		} else {
			continue
		}
		sectionEnd := len(body)
		if remaining := body[sectionStart:]; len(remaining) > 0 {
			if nextLoc := reAnyHeader.FindStringIndex(remaining); nextLoc != nil {
				sectionEnd = sectionStart + nextLoc[0]
			}
		}
		for _, line := range strings.Split(body[sectionStart:sectionEnd], "\n") {
			if strings.TrimSpace(line) == "" || isNonGatingLine(line) {
				continue
			}
			// A keyword line inside a section is already covered by pass 1,
			// which trims the text before the keyword; adding the whole line
			// again would undo that trim.
			if reDeclKeyword.MatchString(line) {
				continue
			}
			// A parent link, a tracking link or a closing keyword names an
			// issue for a reason that is not a dependency. In this pass the
			// line's position IS the declaration, so nothing else would stop
			// "Part of #308" from becoming an edge to the parent epic (#1497).
			text := maskBookkeepingRefs(line)
			if strings.TrimSpace(text) == "" {
				continue
			}
			out = append(out, depFragment{
				text:   text,
				line:   strings.TrimSpace(line),
				source: "structured_section",
			})
		}
	}

	return out
}

// maskQualifiedRefs blanks every REPO-QUALIFIED reference in s, preserving
// length, so that what survives is exactly the bare `#N` tokens. Without it
// "Blocked by platform #535" would yield both a cross-repo edge to
// platform#535 and a same-repo edge to #535 — a hold on an unrelated issue
// that happens to share a number.
//
// A token in front of a `#N` qualifies it only when the token names a
// repository: it resolves through the alias map (or already looks like
// "owner/repo"), or it is glued to the `#` with no space, which is the
// unambiguous "owner/repo#N" / "repo#N" spelling. Everything else is ordinary
// prose — "and #1195", the "-" of a list bullet, a version number — and the
// reference after it is bare.
//
// The residual ambiguity is "Depends on: someunknownrepo #55", which this
// reads as same-repo #55. That is deliberate: an unrecognised token yields a
// dependency the scheduler HOLDS on rather than one it silently drops, and
// holding a dispatch is the recoverable direction. Dropping it is what #1492
// was.
func maskQualifiedRefs(s string, aliases map[string]string) string {
	locs := reQualifiedRef.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	b := []byte(s)
	for _, loc := range locs {
		token := s[loc[2]:loc[3]]
		gap := s[loc[4]:loc[5]]
		if gap != "" && resolveAlias(token, aliases) == "" {
			continue // prose in front of a bare reference, not a repo
		}
		for i := loc[0]; i < loc[1]; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// ParseDependencyRefs is ParseCrossRepoRefs plus the SAME-REPO declaration
// forms, which carry no repo token and so resolve to selfRepo — the repository
// of the issue whose body this is:
//
//	Depends on: #1187
//	Depends on #1187, #1190 and #1195
//	Blocked by #1187
//	Blocked by: #1187
//
// Before #1492 these produced no edge at all, while the cross-repo spellings
// of the same sentence produced one. The scheduler was therefore stricter
// about a dependency in ANOTHER repository than about one in its own: an issue
// whose body said "Depends on: #1187" with #1187 still open was dispatched,
// and feature-planning discovered the prerequisite by reading prose the
// scheduler had ignored.
//
// selfRepo == "" degrades to exactly ParseCrossRepoRefs.
func ParseDependencyRefs(body, selfRepo string, repoAliases map[string]string) []CrossRepoRef {
	refs := ParseCrossRepoRefs(body, repoAliases)
	if body == "" || selfRepo == "" {
		return refs
	}
	if repoAliases == nil {
		repoAliases = DefaultRepoAliases
	}

	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		seen[r.Repo+"#"+strconv.Itoa(r.Number)] = true
	}

	for _, frag := range depDeclarationFragments(body) {
		for _, m := range reBareRef.FindAllStringSubmatch(maskQualifiedRefs(frag.text, repoAliases), -1) {
			num, _ := strconv.Atoi(m[1])
			if num <= 0 {
				continue
			}
			key := selfRepo + "#" + strconv.Itoa(num)
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, CrossRepoRef{
				Repo:       selfRepo,
				Number:     num,
				Source:     frag.source,
				SourceLine: frag.line,
			})
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
