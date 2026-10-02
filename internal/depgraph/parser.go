package depgraph

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
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
	// Unresolved marks a reference whose repository token names no repository
	// the workspace can identify: a name that is not one of its repositories,
	// or a short name two of them share. Repo then holds the token as written,
	// never an "owner/repo", so the reference resolves to no issue: the
	// dispatcher holds the declaring issue (fails closed) until the line names
	// a repository, instead of gating on the declaring repository's own
	// same-numbered issue or dropping the dependency (#2349).
	Unresolved bool
}

// WorkspaceRepoAliases builds the alias map that a body-declared dependency's
// repo token resolves through, from the workspace's own repositories. Each
// "owner/name" slug contributes its full spelling and its bare name, so
// "Blocked by widget-api #12", "Blocked by widget-api#12" and
// "Blocked by example-org/widget-api#12" all reach example-org/widget-api#12.
//
// Before #2349 every caller passed nil, and nil meant a built-in map of the
// documentation's example repositories (acme/platform, acme/mobile, …). In a
// workspace whose repositories carry any other names, a sibling's short name
// resolved to nothing: glued to the `#` it dropped the dependency, and with a
// space it gated on the declaring repository's own same-numbered issue. Only the
// full owner/repo spelling worked, and `platform` resolved to an acme
// repository no board holds.
//
// A bare name that two of the slugs share (org-a/app and org-b/app) maps to "":
// it names a repository, just not one this map can choose, so a reference
// through it is Unresolved and holds the issue rather than gating on either
// repository or on the declaring repository's own #N. The full spellings still
// resolve. Slugs that are not exactly "owner/name" are skipped. Keys match
// case-insensitively, so no case variants are added.
func WorkspaceRepoAliases(slugs []string) map[string]string {
	aliases := make(map[string]string, 2*len(slugs))
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		owner, name, ok := strings.Cut(slug, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			continue
		}
		aliases[slug] = slug
		short := strings.ToLower(name)
		if prev, seen := aliases[short]; seen && !strings.EqualFold(prev, slug) {
			aliases[short] = "" // ambiguous: kept, so it is never read as prose
			continue
		}
		aliases[short] = slug
	}
	return aliases
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
	//
	// Every gap in a keyword, and between it and its reference, is spaces or
	// tabs and never a line break: a declaration lives on one line, as the
	// same-repo pass, the non-gating markers and SourceLine all assume. A
	// lead that crossed the break read a keyword ending one line onto the
	// reference opening the next, so prose hard-wrapped after "does not …
	// depend on" declared the reference it denied (#2349 review).
	depBlockedByCore = `blocked[ \t_-]*by\b`
	depDependsOnCore = `depends?[ \t_-]*on\b`

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
	depKeywordLead = depKeywordWrap + `[ \t]*:?` + depKeywordWrap + `[ \t]*`

	// repoSegment is one part of a repository's spelling: an owner, or a
	// repository name. A name may contain dots and begin with one
	// (nightgauge.dev, example.github.io, .github) but never ends with one, so
	// a sentence's full stop is not read as part of the name. Before the
	// #2349 review the class had no dot at all, so a dotted repository could
	// be named in no slug spelling: "nightgauge.dev #12" gated on the
	// declaring repository's own #12 and "org/nightgauge.dev#12" was dropped.
	repoSegment = `\.?[\w-]+(?:\.[\w-]+)*`

	// repoToken is a repository spelled short ("widget-api") or in full
	// ("example-org/widget-api"). Every pattern below that reads a repository
	// in front of a `#N` composes it, so they cannot disagree about what a
	// repository name may contain.
	repoToken = repoSegment + `(?:/` + repoSegment + `)?`
)

// Compiled regex patterns for parsing cross-repo references.
var (
	// "Blocked by platform #535" / "blocked by acme-mobile #127"
	// Also matches "Blocked by acme/platform#535" and "`blockedBy` acme/platform#535".
	reBlockedBy = regexp.MustCompile(
		`(?i)` + depKeywordWrap + depBlockedByCore + depKeywordLead +
			`(` + repoToken + `)[ \t]*#(\d+)`,
	)

	// "Depends on: platform #NNN" / "depends on acme/platform#NNN"
	// Can match multiple comma/semicolon separated refs on the same line.
	reDependsOn = regexp.MustCompile(
		`(?i)` + depKeywordWrap + depDependsOnCore + depKeywordLead +
			`(` + repoToken + `)[ \t]*#(\d+)`,
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
	//
	// Closing emphasis, brackets and quotes may sit between the terminator and
	// the whitespace: "**A consumer depends on this epic.** Post-merge (#948)"
	// ends its sentence at the `.`, and reading on promoted #948 — an issue
	// that depends on the epic — into the epic's own blocker (#1937).
	reSentenceBreak = regexp.MustCompile("[.;!?][*_)\\]\"'`\u201d\u2019]*(?:[ \t]|$)")

	// Two references joined by a relation arrow: "#479 ← #478", "#5 -> #6".
	// A fragment carrying one describes an edge between OTHER issues — the
	// epic's "blockedBy wiring: #479 ← #478, #480 ← #478" — not a dependency
	// of the issue whose body it is in (#1937).
	reRelationArrow = regexp.MustCompile(
		`#\d+[ \t]*(?:←|→|⟵|⟶|<-+|-+>)[ \t]*(?:` + repoToken + `[ \t]*)?#\d+`,
	)

	// A fenced code block's opening or closing line: three or more backticks
	// or tildes, indented at most three spaces.
	reCodeFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

	// A reference list CONTINUING after a separator: the remainder begins with
	// another `#N` (optionally repo-qualified). "Blocked by #1187; #1190" is one
	// enumeration written with semicolons, which the "Depends on" spelling has
	// always accepted — so a `;` in front of another reference separates items
	// rather than ending the declaration. A `;` in front of prose ends it.
	reRefListContinues = regexp.MustCompile(`^[ \t]*(?:` + repoToken + `[ \t]*)?#\d`)

	// A possibly repo-qualified reference: the token in front of a `#N`, the
	// gap between them, and the number. Both passes classify each one through
	// fragmentRefs, so the cross-repo pass and the same-repo pass agree about
	// which `#N` tokens belong to another repository.
	reQualifiedRef = regexp.MustCompile(
		`(` + repoToken + `)([ \t]*)#(\d+)`,
	)

	// A reference in a declaration's REPO POSITION: the token that opens a
	// keyword's fragment, directly after "Blocked by" / "Depends on:", the
	// spot reBlockedBy and reDependsOn read a repository from.
	reLeadingQualifiedRef = regexp.MustCompile(
		`^(` + repoToken + `)([ \t]*)#(\d+)`,
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
	//
	// It is matched one line at a time, against every line under any
	// dependency-section header (see depDeclarationFragments): before the #2349
	// review only "## Cross-Repo Dependencies" read it, so "- widget-api #12"
	// under "## Dependencies" declared nothing at all. The entry's token sits in
	// the repo position, like the token after a keyword. Any list bullet (`-`,
	// `*`, `+`, `1.`), a task checkbox, or none at all may open the entry: a
	// line under the header is an entry by virtue of where it sits.
	reStructuredEntry = regexp.MustCompile(
		`^[ \t]*(?:(?:[-*+]|\d+[.)])[ \t]*)?(?:\[[ xX]?\][ \t]*)?([✅❌⚠️⏸]*)[ \t]*` +
			`(` + repoToken + `)([ \t]*)#(\d+)`,
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
	//
	// The reference may be repo-qualified: "Part of example-org/platform#46" is
	// the same parent link, and once a section line's qualified references
	// gate (#2349 review) an unmasked one would deadlock the issue on its epic
	// exactly as `Part of #308` did.
	reBookkeepingRef = regexp.MustCompile(
		`(?i)\b(part\s+of|parent(?:\s+issue)?|epic|sub[- ]?issue\s+of|` +
			`child\s+of|tracks|tracked\s+by|related\s+to|related|see\s+also|` +
			`closes|closed\s+by|fixes|fixed\s+by|resolves|resolved\s+by|` +
			`wave)\b[\s:.,;—–-]*(?:` + repoToken + `[ \t]*)?#\d+`,
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
//  2. "- ✅/❌/⚠️/⏸️ <repo> #NNN" entries under a dependency-section header
//     ("## Cross-Repo Dependencies", "## Dependencies", "## Blocked by",
//     "## Depends on")
//  3. "Depends on: <repo> #NNN" / "Depends on <repo> #NNN"
//
// plus every other repo-qualified reference in a declaration's sentence or
// section line, and issue URLs in dependency contexts.
//
// A line that carries a non-gating marker (⏸️, or a textual "deferred" /
// "not-gating" / "non-gating" token) yields no reference from any pattern —
// it is documentation, not a dependency. See isNonGatingLine and
// docs/AUTONOMOUS_ORCHESTRATOR.md for the marker contract.
//
// repoAliases maps short names to full "owner/repo" names; build it with
// WorkspaceRepoAliases. A nil map resolves only the full "owner/repo"
// spelling (#2349). A repository token that resolves to nothing yields an
// Unresolved reference wherever it is unmistakably a repository — see
// classifyRepoRef — so a dependency on an unnamed repository holds the issue
// instead of vanishing.
func ParseCrossRepoRefs(body string, repoAliases map[string]string) []CrossRepoRef {
	if body == "" {
		return nil
	}
	body = maskFencedCode(body)

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
	// addClassified adds a classified reference unless it is bare: a bare `#N`
	// is the same-repo pass's (ParseDependencyRefs), not a cross-repo one.
	addClassified := func(c classifiedRef, source, line string) {
		if c.kind == refBare || c.number <= 0 {
			return
		}
		addRef(CrossRepoRef{
			Repo:       c.repo,
			Number:     c.number,
			Source:     source,
			Verified:   c.verified,
			SourceLine: line,
			Unresolved: c.kind == refUnresolved,
		})
	}
	// keywordRef classifies the reference a keyword pattern captured, in the
	// repo position. Groups: 1 the token, 2 the number.
	keywordRef := func(m []int) classifiedRef {
		num, _ := strconv.Atoi(body[m[4]:m[5]])
		glued := body[m[3]:m[4]] == "#"
		return classifyRepoRef(body[m[2]:m[3]], num, glued, true, repoAliases)
	}

	// 1. "Blocked by ..." pattern
	for _, m := range reBlockedBy.FindAllStringSubmatchIndex(body, -1) {
		line := lineAt(body, m[0])
		if isNonGatingLine(line) {
			continue
		}
		addClassified(keywordRef(m), "body_text", line)
	}

	fragments := depDeclarationFragments(body)

	// 2. Entries under a dependency-section header. ⏸️ / "deferred" /
	// "not-gating" entries are documentation the author recorded for
	// context, and depDeclarationFragments has already dropped them; ✅, ❌
	// and ⚠️ all still gate — ⚠️ reads as "watch this", which is a dependency
	// worth honouring (#126). Only an explicit ✅ marks Verified (#132).
	for _, frag := range fragments {
		if frag.source != "structured_section" {
			continue
		}
		if c, ok := sectionEntryRef(frag.text, repoAliases); ok {
			addClassified(c, frag.source, frag.line)
		}
	}

	// 3. "Depends on ..." pattern
	for _, m := range reDependsOn.FindAllStringSubmatchIndex(body, -1) {
		line := lineAt(body, m[0])
		if isNonGatingLine(line) {
			continue
		}
		addClassified(keywordRef(m), "depends_on", line)
	}

	// 3b. Repo-qualified references elsewhere in a keyword's own SENTENCE, or
	// anywhere on a dependency-section line. Patterns 1 and 3 only see a
	// reference sitting immediately after the keyword, so a sentence that
	// enumerates two blockers across a clause —
	//
	//	This issue is `blockedBy` acme/platform#1253 and, per the epic's
	//	Wave-1-first rule, acme/platform#1252 …
	//
	// declared two and yielded one. The bare-`#N` pass already treats a
	// keyword's whole sentence as its claim (see depDeclarationFragments);
	// this makes the qualified spelling agree with it, which is the same
	// symmetry #1492 restored between the cross-repo and same-repo forms.
	//
	// Section lines count too: the bare-`#N` pass reads every one as a
	// declaration, and until the #2349 review the qualified spelling on the
	// same line ("- example-org/widget-api#12" under "## Dependencies")
	// declared nothing.
	for _, frag := range fragments {
		for _, c := range fragmentRefs(frag, repoAliases) {
			addClassified(c, frag.source, frag.line)
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
			if reRelationArrow.MatchString(text) {
				continue
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

// refKind is what a `#N` in a dependency declaration names.
type refKind int

const (
	// refBare is the declaring repository's own #N: nothing in front of it,
	// or prose ("issue #5", "and #6").
	refBare refKind = iota
	// refRepo is #N in the repository its token resolves to.
	refRepo
	// refUnresolved is #N in a repository its token names but the workspace
	// cannot identify (see CrossRepoRef.Unresolved).
	refUnresolved
)

// classifiedRef is one `token #N` reference in a declaration, classified.
type classifiedRef struct {
	kind     refKind
	repo     string // the repository (refRepo), or the token as written (refUnresolved)
	number   int
	verified bool // a section entry marked ✅
	start    int  // the reference's span, token through number, in its text
	end      int
}

// proseQualifiers are words that stand in front of an issue's `#N` to say what
// it is, not which repository it is in: "Blocked by issue #5", "Depends on PR
// #6", "Blocked by Epic #295", "- Needs #7", "PR#8". In the repo position, or
// glued to the `#`, any other word that is not one of the workspace's
// repositories is read as a repository the workspace cannot name, so this list
// is what keeps such prose a reference to the declaring repository's own
// issue. The alias map is consulted first, so a repository that happens to be
// called one of these words still resolves.
var proseQualifiers = func() map[string]bool {
	words := []string{
		// What the reference is.
		"issue", "issues", "pr", "prs", "pull", "request", "requests", "mr",
		"epic", "epics", "story", "stories", "task", "tasks", "ticket", "tickets",
		"bug", "bugs", "feature", "features", "spike", "spikes", "item", "items",
		"sub-issue", "sub-issues", "subissue", "subissues", "parent", "child",
		"sibling", "siblings", "blocker", "blockers", "dependency",
		"dependencies", "prerequisite", "prerequisites", "prereq", "prereqs",
		"follow-up", "followup", "fix", "change", "work", "gh", "no", "number",
		// Words a sentence or a list item puts in front of one.
		"the", "a", "an", "and", "or", "and/or", "both", "either", "all", "also",
		"only", "plus", "then", "see", "via", "per", "on", "in", "of", "by", "to",
		"for", "with", "from", "after", "before", "until", "once", "when", "its",
		"their", "this", "that", "these", "those", "our", "upstream",
		"downstream", "needs", "need", "requires", "require", "waits", "waiting",
		"blocks",
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()

// isProseQualifier reports whether token, in front of a `#N`, is prose rather
// than a repository: a word from proseQualifiers, or a token with no letter in
// it (a count or a version: "2 #5", "1.2 #5").
func isProseQualifier(token string) bool {
	t := strings.ToLower(strings.TrimSpace(token))
	return proseQualifiers[t] || !strings.ContainsFunc(t, unicode.IsLetter)
}

// classifyRepoRef classifies the reference `token #number`. glued is true when
// nothing separates the token from the `#`; repoPosition is true when the token
// sits directly after a dependency keyword ("Blocked by core #12") or after a
// dependency-section entry's bullet and marker ("- ❌ platform #535").
//
//   - A token that resolves through the alias map is that repository's #N.
//   - A short name two workspace repositories share is Unresolved wherever it
//     appears: it plainly names a repository, just not one the parser can
//     choose.
//   - A prose qualifier ("issue", "PR", "epic", …) or a count leaves the
//     reference bare.
//   - A token spelled "owner/repo" is that repository's #N.
//   - Any other token is a repository the workspace cannot name, and the
//     reference is Unresolved, when the token is unmistakably a repository:
//     glued to the `#` (the repo#N spelling) or in the repo position.
//   - Anywhere else a spaced word is prose ("… until release #6"), and the
//     reference is bare.
//
// Unresolved holds the issue: the dispatcher fails closed on a dependency it
// cannot resolve, and the hold names the body line. Before the #2349 review an
// unknown or ambiguous name read as prose when spaced, so "Blocked by core #12"
// gated on the declaring repository's own #12 — which may be closed, and then
// the issue dispatched over its real blocker — and as nothing when glued.
// Holding is the recoverable direction; dropping a dependency is what #1492
// was.
func classifyRepoRef(token string, number int, glued, repoPosition bool, aliases map[string]string) classifiedRef {
	c := classifiedRef{number: number}
	token = strings.TrimSpace(token)
	switch repo, kind := lookupAlias(token, aliases); kind {
	case aliasRepo:
		c.kind, c.repo = refRepo, repo
		return c
	case aliasAmbiguous:
		c.kind, c.repo = refUnresolved, token
		return c
	}
	switch {
	case isProseQualifier(token):
		// bare
	case strings.Contains(token, "/"):
		c.kind, c.repo = refRepo, token
	case glued || repoPosition:
		c.kind, c.repo = refUnresolved, token
	}
	return c
}

// keywordLeadRef classifies the reference in a keyword fragment's repo
// position: the token its text opens with, directly after the keyword.
func keywordLeadRef(text string, aliases map[string]string) (classifiedRef, bool) {
	m := reLeadingQualifiedRef.FindStringSubmatchIndex(text)
	if m == nil {
		return classifiedRef{}, false
	}
	num, _ := strconv.Atoi(text[m[6]:m[7]])
	c := classifyRepoRef(text[m[2]:m[3]], num, m[4] == m[5], true, aliases)
	c.start, c.end = m[2], m[1]
	return c, true
}

// sectionEntryRef classifies the reference in a dependency-section entry's
// repo position — "- ❌ widget-api #12" — and reports false for a line with
// none ("- #12", prose).
func sectionEntryRef(text string, aliases map[string]string) (classifiedRef, bool) {
	m := reStructuredEntry.FindStringSubmatchIndex(text)
	if m == nil {
		return classifiedRef{}, false
	}
	num, _ := strconv.Atoi(text[m[8]:m[9]])
	c := classifyRepoRef(text[m[4]:m[5]], num, m[6] == m[7], true, aliases)
	// status may be "" for an unmarked entry (the marker group is optional),
	// which gates unverified like ❌/⚠️: only an explicit ✅ is Verified (#132).
	c.verified = strings.Contains(text[m[2]:m[3]], "✅")
	c.start, c.end = m[4], m[1]
	return c, true
}

// fragmentRefs classifies every repo-qualified reference in a declaration
// fragment: the one in its repo position first (the keyword's, or the section
// entry's), then each later `token #N`. ParseCrossRepoRefs emits those that
// are not bare and ParseDependencyRefs masks them, so the two passes cannot
// both claim one: "platform #535" is never also the declaring repository's
// #535, a hold on an unrelated issue that happens to share a number.
func fragmentRefs(frag depFragment, aliases map[string]string) []classifiedRef {
	var (
		out  []classifiedRef
		lead classifiedRef
		ok   bool
		rest int
	)
	if frag.source == "structured_section" {
		lead, ok = sectionEntryRef(frag.text, aliases)
	} else {
		lead, ok = keywordLeadRef(frag.text, aliases)
	}
	if ok {
		out = append(out, lead)
		rest = lead.end
	}
	for _, m := range reQualifiedRef.FindAllStringSubmatchIndex(frag.text[rest:], -1) {
		num, _ := strconv.Atoi(frag.text[rest+m[6] : rest+m[7]])
		c := classifyRepoRef(frag.text[rest+m[2]:rest+m[3]], num, m[4] == m[5], false, aliases)
		c.start, c.end = rest+m[2], rest+m[1]
		out = append(out, c)
	}
	return out
}

// maskNonBareRefs blanks, preserving length, every reference in the fragment
// that fragmentRefs does not classify as bare, so what survives is exactly the
// declaring repository's own `#N` tokens.
func maskNonBareRefs(frag depFragment, aliases map[string]string) string {
	var b []byte
	for _, c := range fragmentRefs(frag, aliases) {
		if c.kind == refBare {
			continue
		}
		if b == nil {
			b = []byte(frag.text)
		}
		for i := c.start; i < c.end; i++ {
			b[i] = ' '
		}
	}
	if b == nil {
		return frag.text
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
// selfRepo == "" degrades to exactly ParseCrossRepoRefs. repoAliases is the
// workspace's alias map (WorkspaceRepoAliases), as for ParseCrossRepoRefs.
func ParseDependencyRefs(body, selfRepo string, repoAliases map[string]string) []CrossRepoRef {
	refs := ParseCrossRepoRefs(body, repoAliases)
	if body == "" || selfRepo == "" {
		return refs
	}
	body = maskFencedCode(body)

	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		seen[r.Repo+"#"+strconv.Itoa(r.Number)] = true
	}

	for _, frag := range depDeclarationFragments(body) {
		for _, m := range reBareRef.FindAllStringSubmatch(maskNonBareRefs(frag, repoAliases), -1) {
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

// maskFencedCode blanks every line inside a fenced code block, fences
// included, keeping the line count. A fence quotes: pasted command output, a
// log, an attention card's text. "blocked by #478" inside one is evidence
// someone is reporting, not a dependency the issue declares — #1937's own
// body quoted a card and became an edge to #478.
func maskFencedCode(body string) string {
	if !strings.Contains(body, "```") && !strings.Contains(body, "~~~") {
		return body
	}
	lines := strings.Split(body, "\n")
	fence := ""
	for i, line := range lines {
		m := reCodeFence.FindStringSubmatch(line)
		switch {
		case fence == "" && m != nil:
			fence = m[1]
			lines[i] = ""
		case fence != "":
			// A closing fence uses the opener's character, at least as long.
			if m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) &&
				strings.TrimSpace(line[len(m[0]):]) == "" {
				fence = ""
			}
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// aliasKind is what an alias-map lookup found.
type aliasKind int

const (
	aliasUnknown   aliasKind = iota // no key matches
	aliasRepo                       // a key maps to a repository
	aliasAmbiguous                  // a key maps to "": a name several repositories share
)

// lookupAlias looks raw up in the alias map, exactly and then
// case-insensitively.
func lookupAlias(raw string, aliases map[string]string) (string, aliasKind) {
	raw = strings.TrimSpace(raw)
	full, ok := aliases[raw]
	if !ok {
		for k, v := range aliases {
			if strings.EqualFold(k, raw) {
				full, ok = v, true
				break
			}
		}
	}
	switch {
	case !ok:
		return "", aliasUnknown
	case full == "":
		return "", aliasAmbiguous
	}
	return full, aliasRepo
}

// resolveAlias normalizes a repo reference using the alias map. A name the
// map does not hold is returned as-is when it is spelled "owner/repo", and as
// "" otherwise; so is a name the map marks ambiguous.
func resolveAlias(raw string, aliases map[string]string) string {
	if full, kind := lookupAlias(raw, aliases); kind != aliasUnknown {
		return full
	}
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "/") {
		return raw
	}
	return ""
}
