package adapters

// The OpenCode permission map and the project-config tamper gate (ADR-022
// § 9, § 8; #1638).
//
// BuildOpenCodeConfig (opencode_config.go) is the sole authority for a
// dispatch's per-run config, so the permission map it sets on
// openCodeConfigJSON.Permission is built here and nowhere else: the same
// builder that already resolves the endpoint and pins the dispatched model
// also derives `permission` from the stage's RunOptions.AllowedTools. This is
// a separate write path from addNightgaugePluginToConfig (opencode.go), which
// decodes the builder's own output generically to add the top-level `plugin`
// key #1635 needs — that decode-and-re-encode round-trips whatever this file
// sets on `permission` unchanged, because it touches no other key.
//
// A stage's tools are governed by #1624's Claude Code → OpenCode tool table,
// OpenCodeToolForClaudeTool (adapter.go) — already merged and already read by
// failure classification (opencode_usage.go's OpenCodeAutoRejectMarker), so
// this file consults the same table rather than a second copy of it
// (openCodePermissionKeyForClaudeTool). Checked against the six stage
// skills' declared `allowed-tools` and opencode 1.18.30's own tool set
// (ADR-022 § 6, observed: bash, edit, write, read, grep, glob, task,
// todowrite, skill and webfetch), the shared table has no entry for
// NotebookEdit, WebSearch, TodoWrite or Skill; openCodeSupplementaryToolPermission
// covers exactly those four, a disjoint name set from the shared table's, so
// the two can never disagree. 1.18.30 has no permission key of its own for
// "write" — the headless-posture notice names the rejected permission "edit"
// for every write tool (ADR-022 § 9) — so Write, Edit, MultiEdit and
// NotebookEdit all resolve to the same "edit" permission key.
//
// Every generated map sets exactly the keys the issue names, explicitly:
//
//   - "*" is the default any other permission (an MCP tool's, for instance)
//     resolves to. It is always "deny": nothing outside this map's explicit
//     keys is ever granted by default, and an empty or missing AllowedTools
//     never produces a "*": "allow" map.
//   - "list" has no AllowedTools entry of its own. opencode's directory
//     listing is a read-adjacent capability, so it follows "read": a stage
//     that may Read may also list.
//   - "doom_loop" is always "deny" — the issue: "makes the upstream loop
//     guard an explicit failure."
//   - "external_directory" is never a plain allow/deny scalar: it is always
//     the pattern map openCodeExternalDirectoryPermission builds, "*":
//     "deny" plus the allow-list (openCodeExternalDirectoryAllowList).
//   - "bash", "read" and "edit" are always pattern maps too, even when the
//     stage's tools grant nothing: the deny-list backstop
//     (openCodeBashDenyBackstop, openCodeSecretDenyBackstop,
//     openCodeProjectConfigDenyBackstop) is present in every map, per the
//     issue's own acceptance criterion, not only when the base tool is
//     allowed.
//
// No value is ever "ask": ADR-022 § 9 forbids it outright, because a
// permission that resolves to "ask" is auto-rejected by opencode itself and
// the process exits 0 — a silent stop that reads as success.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// openCodeAllow and openCodeDeny are the only two actions a generated
// permission map ever uses. openCodeAsk is deliberately not a constant here:
// nothing in this file may emit it (ADR-022 § 9).
const (
	openCodeAllow = "allow"
	openCodeDeny  = "deny"
)

// openCodePermissionKeyForClaudeTool maps a Claude Code AllowedTools base
// name (after stripping a "(...)" scope, openCodeToolScope) to the opencode
// permission key it governs. #1624's own table, OpenCodeToolForClaudeTool
// (adapter.go), is consulted first — it is what failure classification
// (opencode_usage.go's OpenCodeAutoRejectMarker) already reads, so the
// permission map this file builds and the marker that later classifies a
// rejection of it must never disagree on a name they both know. It covers
// Bash, Read, Write, Edit, MultiEdit, Glob, Grep, Task and WebFetch.
// openCodeSupplementaryToolPermission is consulted only for a name absent
// from that table — NotebookEdit, WebSearch, TodoWrite and Skill, which the
// issue's key list needs and #1624's classification table has no use for —
// so the two maps own disjoint name sets and can never diverge on one they
// share. A tool absent from both (AskUserQuestion, an MCP tool such as
// "mcp__foo__bar") sets no permission key directly: it is covered by the
// top-level "*" default, always "deny".
var openCodeSupplementaryToolPermission = map[string]string{
	"NotebookEdit": "edit",
	"WebSearch":    "websearch",
	"TodoWrite":    "todowrite",
	"Skill":        "skill",
}

// openCodePermissionKeyForClaudeTool is openCodeSupplementaryToolPermission
// layered under OpenCodeToolForClaudeTool (#1624): the shared table wins for
// any name it knows.
func openCodePermissionKeyForClaudeTool(name string) (string, bool) {
	if key, ok := OpenCodeToolForClaudeTool(name); ok {
		return key, true
	}
	key, ok := openCodeSupplementaryToolPermission[name]
	return key, ok
}

// openCodeBashDenyBackstop are the bash patterns the issue's deny-list
// backstop names, always denied whatever the stage's tools grant. Enforcement
// is #1635's careful-gate plugin; this is the backstop, and a glob deny-list
// can be routed around (`sh -c`), so it is not the only control.
var openCodeBashDenyBackstop = []string{"rm -rf *", "rm -fr *", "git push --force*", "git push -f*", "git push * --force*"}

// openCodeSecretDenyBackstop are the read/edit patterns the issue's deny-list
// backstop names ("*.env", ".env*", "**/.ssh/**", "**/id_rsa*" and the gh
// hosts pattern — TestOpenCodeDenyListBackstopComplete's golden checks these
// five are present, independent of this var, so a shorter var still fails
// that test), plus "**/*.env" and "**/.env*": opencode 1.18.30's own anchored
// Wildcard.match means "*.env" and ".env*" only ever match a ROOT-level file
// (worktree-relative patterns have no leading "**", so "*" cannot cross a "/"
// the way a real glob's "*" would); a nested secret file such as
// apps/web/.env.local needs the "**/" prefix to be caught at all. Without it,
// a Read-granted stage's read "*": allow backstop is the last matching rule
// for that file, and it is readable (#1638 fix round finding: a routed
// #1752 request, TestProbeA9NestedEnvLocalRead).
//
// "*.env.*" and "**/*.env.*" widen this past ".env"/".env*"'s own shape (a
// file whose name STARTS with, or literally IS, ".env"): opencode 1.18.30's
// own bundled default `read` guard denies both "*.env" and "*.env.*"
// (ADR-022's failure-wording amendment, "A stage allowed Read that reaches
// for *.env"), and a file such as config/prod.env.local — ".env" is an
// INFIX, the name neither starts nor ends with it — matches neither of this
// backstop's other patterns at any depth, root or nested, without this pair
// (#1638 fix round finding, a routed #1752 request).
var openCodeSecretDenyBackstop = []string{
	"*.env", ".env*", "**/*.env", "**/.env*", "*.env.*", "**/*.env.*",
	"**/.ssh/**", "**/id_rsa*", openCodeGhHostsPattern,
}

// openCodeGhHostsPattern matches gh's hosts file under any GH_CONFIG_DIR
// (opencode_isolation.go points GH_CONFIG_DIR at the operator's own gh
// config directory, so the pattern cannot be a fixed absolute path).
const openCodeGhHostsPattern = "**/gh/hosts.yml"

// openCodeProjectConfigDenyBackstop are the edit-only patterns the issue
// names: a stage must never rewrite the OpenCode config the next stage in the
// same worktree loads (the tamper gate, openCodeProjectConfigTamperCheck
// below, is the other half of that control).
var openCodeProjectConfigDenyBackstop = []string{"opencode.json*", ".opencode/**"}

// openCodePatternMap is an insertion-ordered permission pattern object
// (PermissionObjectConfig, opencode's config schema). Observed on 1.18.30
// (ADR-022 § 8), a merged permission map keeps the key order of the lowest
// config layer that has the key and applies the LAST matching rule, so within
// one object, order carries meaning. Go's map[string]string marshals with its
// keys sorted alphabetically (encoding/json), which cannot be trusted to keep
// "*" first and a deny-list backstop last in general, so this type marshals
// in the order set was called, once per pattern (a later set on the same
// pattern replaces its action in place, not at the end).
type openCodePatternMap struct {
	keys   []string
	values map[string]string
}

func newOpenCodePatternMap() *openCodePatternMap {
	return &openCodePatternMap{values: map[string]string{}}
}

func (m *openCodePatternMap) set(pattern, action string) *openCodePatternMap {
	if _, ok := m.values[pattern]; !ok {
		m.keys = append(m.keys, pattern)
	}
	m.values[pattern] = action
	return m
}

// MarshalJSON writes m's entries as a JSON object in insertion order — never
// the alphabetical order encoding/json would give map[string]string.
func (m *openCodePatternMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(m.values[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// openCodePermissionJSON is opencode's `permission` config value. Every field
// is set explicitly by openCodePermissionMap; none is ever "ask" (ADR-022
// § 9). Struct fields marshal in declaration order, which is what fixes the
// top-level key order — no two top-level keys govern the same tool, so
// nothing here depends on THEIR relative order the way a single pattern
// map's entries depend on each other's.
type openCodePermissionJSON struct {
	Wildcard          string              `json:"*"`
	Read              *openCodePatternMap `json:"read"`
	Edit              *openCodePatternMap `json:"edit"`
	Glob              string              `json:"glob"`
	Grep              string              `json:"grep"`
	List              string              `json:"list"`
	Bash              *openCodePatternMap `json:"bash"`
	Task              string              `json:"task"`
	WebFetch          string              `json:"webfetch"`
	WebSearch         string              `json:"websearch"`
	Skill             string              `json:"skill"`
	TodoWrite         string              `json:"todowrite"`
	DoomLoop          string              `json:"doom_loop"`
	ExternalDirectory *openCodePatternMap `json:"external_directory"`
}

// openCodeToolGrant is what a stage's AllowedTools grants one opencode
// permission key: the bare tool (grants everything the key governs) and/or
// one or more scopes (a Claude Code scoped entry, "Bash(git *)", grants only
// that pattern).
type openCodeToolGrant struct {
	bare   bool
	scopes []string
}

// openCodeToolScope cuts entry at its first "(", the same place
// OpenCodeToolForClaudeTool cuts (strings.Cut(name, "(")), so the base name
// this returns is always what that function would resolve. When entry also
// ends in ")", the text between is returned as the scope, trimmed — the
// content of "Bash(git *)"'s parentheses; a malformed entry with no closing
// paren still yields the right base name, just no scope.
func openCodeToolScope(entry string) (name, scope string, scoped bool) {
	t := strings.TrimSpace(entry)
	i := strings.Index(t, "(")
	if i == -1 {
		return t, "", false
	}
	name = strings.TrimSpace(t[:i])
	if !strings.HasSuffix(t, ")") {
		return name, "", false
	}
	return name, strings.TrimSpace(t[i+1 : len(t)-1]), true
}

// openCodeToolGrants reads allowedTools into one openCodeToolGrant per
// opencode permission key it names. "list" is not set here: it has no
// AllowedTools entry of its own (openCodePermissionMap mirrors it from
// "read" afterward).
func openCodeToolGrants(allowedTools []string) map[string]*openCodeToolGrant {
	grants := map[string]*openCodeToolGrant{}
	for _, entry := range allowedTools {
		name, scope, scoped := openCodeToolScope(entry)
		key, ok := openCodePermissionKeyForClaudeTool(name)
		if !ok {
			continue
		}
		g := grants[key]
		if g == nil {
			g = &openCodeToolGrant{}
			grants[key] = g
		}
		if scoped {
			if scope != "" {
				g.scopes = append(g.scopes, scope)
			}
		} else {
			g.bare = true
		}
	}
	return grants
}

// openCodeScalarPermission is "allow" when g grants anything (bare or
// scoped — a key with no pattern support treats a scope as a bare grant,
// since none of the six stage skills scope a tool this loosely today), else
// "deny". Never "ask".
func openCodeScalarPermission(g *openCodeToolGrant) string {
	if g != nil && (g.bare || len(g.scopes) > 0) {
		return openCodeAllow
	}
	return openCodeDeny
}

// openCodeBashPermission is always a pattern map: "*" from g, every scoped
// pattern g grants, then the deny-list backstop last, so it always wins even
// over a scoped allow that happened to overlap it.
func openCodeBashPermission(g *openCodeToolGrant) *openCodePatternMap {
	pm := newOpenCodePatternMap()
	base := openCodeDeny
	if g != nil && g.bare {
		base = openCodeAllow
	}
	pm.set("*", base)
	if g != nil {
		for _, scope := range g.scopes {
			pm.set(scope, openCodeAllow)
		}
	}
	for _, pattern := range openCodeBashDenyBackstop {
		pm.set(pattern, openCodeDeny)
	}
	return pm
}

// openCodeReadPermission is always a pattern map: "*" from g, every scoped
// pattern g grants, then the secret deny-list backstop.
func openCodeReadPermission(g *openCodeToolGrant) *openCodePatternMap {
	pm := newOpenCodePatternMap()
	base := openCodeDeny
	if g != nil && g.bare {
		base = openCodeAllow
	}
	pm.set("*", base)
	if g != nil {
		for _, scope := range g.scopes {
			pm.set(scope, openCodeAllow)
		}
	}
	for _, pattern := range openCodeSecretDenyBackstop {
		pm.set(pattern, openCodeDeny)
	}
	return pm
}

// openCodeEditPermission is always a pattern map: "*" from g, every scoped
// pattern g grants, the secret and project-config deny-list backstops, and a
// deny for every pattern in denyDirPatterns — the read-only half of the
// external_directory allow-list's directory entries (manager.go's
// NIGHTGAUGE_SKILL_DIR, and the running binary's own NIGHTGAUGE_BIN dir,
// #1638 fix round finding 8): a stage may Read under either, but never Edit
// or Write into it. Without this deny, external_directory's allow of
// NIGHTGAUGE_BIN plus edit's own default "*": "allow" (every stage skill
// grants Edit and Write) lets a stage plant an executable on the operator's
// PATH.
func openCodeEditPermission(g *openCodeToolGrant, denyDirPatterns []string) *openCodePatternMap {
	pm := newOpenCodePatternMap()
	base := openCodeDeny
	if g != nil && g.bare {
		base = openCodeAllow
	}
	pm.set("*", base)
	if g != nil {
		for _, scope := range g.scopes {
			pm.set(scope, openCodeAllow)
		}
	}
	for _, pattern := range openCodeSecretDenyBackstop {
		pm.set(pattern, openCodeDeny)
	}
	for _, pattern := range openCodeProjectConfigDenyBackstop {
		pm.set(pattern, openCodeDeny)
	}
	for _, pattern := range denyDirPatterns {
		pm.set(pattern, openCodeDeny)
	}
	return pm
}

// openCodeExternalDirectoryPermission is always a pattern map: "*": "deny",
// then every pattern in allow as "allow".
func openCodeExternalDirectoryPermission(allow []string) *openCodePatternMap {
	pm := newOpenCodePatternMap()
	pm.set("*", openCodeDeny)
	for _, pattern := range allow {
		pm.set(pattern, openCodeAllow)
	}
	return pm
}

// openCodeTmpDirAllowPatterns are the external_directory allow-list entries
// for every /tmp path the six stage skills use (skills/nightgauge-{issue-
// pickup,feature-planning,feature-dev,feature-validate,pr-create,pr-merge}).
//
// #1638 fix round finding (AC4, ADR-022's dated amendment): opencode 1.18.30
// asks external_directory for dirname(file)+"/*", never the file's own
// path — a probe of a per-file allow entry (`/tmp/automerge.err`, or the
// same with a real wildcard) against a real read/write of exactly that path
// was refused every time, because the pattern that would have to match is
// literally "/tmp/*", not the file's own name at all. A per-file allow-list
// can therefore never work on this binary; only a directory-level entry
// does, one per resolved form of /tmp (macOS resolves /tmp to /private/tmp,
// ADR-022 § 9's last bullet). The accepted cost — every stage skill's read
// and edit tools can reach any OTHER file directly under /tmp or
// /private/tmp, not only its own — is recorded in ADR-022's own amendment,
// alongside the follow-up that moves the six stage skills to a per-run
// scratch directory so this allow entry can be narrowed or removed. The
// secret and project-config deny-list backstops (openCodeSecretDenyBackstop,
// openCodeProjectConfigDenyBackstop) still win over this allow where they
// apply, since edit's own deny entries are checked last (§ "external_directory
// allow-list" / openCodeEditPermission).
//
// Every stage skill's own /tmp literal is FLAT, directly under /tmp with no
// subdirectory (TestOpenCodeTmpAllowListCoversStageSkills asserts this stays
// true, since "*" never crosses a "/" in opencode's own pattern matching): a
// skill that ever needs a NESTED /tmp subdirectory needs its own
// dirname(file)+"/*" entry added here explicitly, this pair does not cover
// it.
var openCodeTmpDirAllowPatterns = []string{"/tmp/*", "/private/tmp/*"}

// openCodeTmpLiteralRE matches a /tmp/... path literal in skill markdown or
// shell text: /tmp/ followed by a run of characters that cannot appear
// outside a path in shell (whitespace, a backslash escape and shell
// metacharacters end it — a source line inside a double-quoted shell string
// often escapes its own closing quote, "\"/tmp/x\"", and the backslash is
// never part of the path).
var openCodeTmpLiteralRE = regexp.MustCompile("/tmp/[^\\s\"'`<>;&|)\\\\]+")

// openCodeShellVarRE matches a shell variable expansion within a scanned
// literal: $(...), ${...}, $$, or a bare $NAME.
var openCodeShellVarRE = regexp.MustCompile(`\$\([^)]*\)|\$\{[^}]*\}|\$\$|\$[A-Za-z_][A-Za-z0-9_]*`)

// openCodeNormalizeTmpLiteral replaces every shell variable expansion in lit
// with "*" and collapses a run of "*" into one. Exported to this
// package's tests as the reference normalization; the scan itself
// (openCodeScanStageSkillTmpLiterals) substitutes a whole file's shell
// variables before extracting a literal, not after (openCodeCollapseStars),
// because a substitution such as $(basename "$FIXED_FILE") holds a quote
// that would otherwise cut the literal match short before its own closing
// paren.
func openCodeNormalizeTmpLiteral(lit string) string {
	return openCodeCollapseStars(openCodeShellVarRE.ReplaceAllString(lit, "*"))
}

// openCodeCollapseStars collapses a run of "*" in s into one.
func openCodeCollapseStars(s string) string {
	for strings.Contains(s, "**") {
		s = strings.ReplaceAll(s, "**", "*")
	}
	return s
}

// openCodeStageSkillDirs are the six stage skills the /tmp coverage scan and
// the external-directory allow-list (NIGHTGAUGE_SKILL_DIR) are scoped to.
var openCodeStageSkillDirs = []string{
	"nightgauge-issue-pickup",
	"nightgauge-feature-planning",
	"nightgauge-feature-dev",
	"nightgauge-feature-validate",
	"nightgauge-pr-create",
	"nightgauge-pr-merge",
}

// openCodeScanStageSkillTmpLiterals scans skillsRoot/<each of
// openCodeStageSkillDirs> for /tmp/... literals (openCodeTmpLiteralRE),
// normalizes each (openCodeNormalizeTmpLiteral) and returns the sorted,
// de-duplicated set found. TestOpenCodeTmpAllowListCoversStageSkills is what
// checks every one of them is covered by openCodeTmpDirAllowPatterns, under
// opencode's own dirname(file)+"/*" matching (openCodeTmpRequestPattern).
func openCodeScanStageSkillTmpLiterals(skillsRoot string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, name := range openCodeStageSkillDirs {
		dir := filepath.Join(skillsRoot, name)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// Shell variables are substituted across the WHOLE file first,
			// not per matched literal: a nested substitution such as
			// $(basename "$FIXED_FILE") holds a quote, which would
			// otherwise cut openCodeTmpLiteralRE's own match short before
			// the substitution's closing paren, because a quote ends a
			// literal. Substituting first removes every quote a shell
			// substitution could hold, so the literal scan after it never
			// sees one.
			normalized := openCodeShellVarRE.ReplaceAllString(string(raw), "*")
			for _, m := range openCodeTmpLiteralRE.FindAllString(normalized, -1) {
				n := openCodeCollapseStars(m)
				if !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// openCodeTmpRequestPattern is the external_directory pattern opencode
// 1.18.30 itself asks permission for when a tool call touches lit, a
// worktree-external /tmp/... path found in a stage skill: dirname(lit) +
// "/*" — never lit's own path (#1638 fix round finding, AC4). This is what
// TestOpenCodeTmpAllowListCoversStageSkills checks every scanned literal
// against openCodeTmpDirAllowPatterns with, so the test's own notion of
// "covered" matches opencode's real matching, not a literal string
// containment check on the source list.
func openCodeTmpRequestPattern(lit string) string {
	return filepath.ToSlash(filepath.Dir(lit)) + "/*"
}

// openCodeDirPatterns returns the external_directory allow pattern(s) for
// dir's content, recursively, each with "/**" appended: dir as given, and,
// when it differs, dir resolved through symlinks (best effort — a failure to
// resolve just skips the second form rather than refusing the config). Both
// forms are needed, empirically, not only on macOS's well-known /tmp ->
// /private/tmp: a bounded probe against opencode 1.18.30 (TestOpenCodeIncludesReadAllowed)
// found a tool call's own filePath reported unresolved
// (/var/folders/.../T/skill/_includes/note.md) even though the same
// directory's macOS TMPDIR prefix is a symlink (/var -> /private/var), so an
// allow-list built only from the resolved form did not match it. nil when
// dir is "".
func openCodeDirPatterns(dir string) []string {
	if dir == "" {
		return nil
	}
	given := filepath.Clean(dir) + "/**"
	patterns := []string{given}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		if p := filepath.Clean(resolved) + "/**"; p != given {
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// openCodeWorktreeRelativeDirPatterns returns the edit-deny pattern(s) for
// dir, expressed the way opencode 1.18.30's edit, write AND read tools ask
// permission: `patterns:[path.relative(Instance.worktree, file)]` (bundled
// source: `n.ask({permission:"edit",patterns:[qo.relative(y.worktree,u)]...})`).
// Instance.worktree is the git worktree root for a git repository, and "/"
// for a directory that is not one.
//
// ADR-022's 2026-09-15 amendment, and the openCodeEditDenyPatterns this
// function replaces, recorded a different, wrong rule ("a pattern starting
// with / never matches for edit"; "a pattern with no glob metacharacter never
// matches"). Both were artifacts of that amendment's own fixture, a plain
// t.TempDir() with no .git, where Instance.worktree really is "/" and a
// leading-slash-stripped absolute path happens to equal the correct relative
// form by coincidence. A second, bounded probe against the real binary in an
// actual git worktree (TestProbeA9SkillEditInGitWorktree, ADR-022's
// 2026-09-15 correction) found the old, absolute-path-derived pattern never
// matches there: an edit of NIGHTGAUGE_SKILL_DIR/_includes/note.md succeeded
// when it must be denied (AC2).
//
// worktreeDir's own git top-level (openCodeGitTopLevel, a filesystem walk-up
// for a ".git" entry — never a `git` subprocess, so this function, like
// openCodePermissionMap, stays pure) is the relative root when worktreeDir is
// one; "/" otherwise, matching opencode's own non-git fallback exactly, which
// TestOpenCodeIncludesReadAllowed's bare t.TempDir() fixture still exercises
// deliberately. Every real dispatch's WorktreeDir IS the git worktree
// Manager.RunStage created, so this finds it as its own top-level directly.
//
// Both the given and the symlink-resolved form of the resolved root and of
// dir are combined (openCodeDirPatterns' own reasoning: opencode can report
// either form for either path, independently, under macOS's /var ->
// /private/var), de-duplicated.
func openCodeWorktreeRelativeDirPatterns(worktreeDir, dir string) []string {
	if dir == "" {
		return nil
	}
	root := string(filepath.Separator)
	if worktreeDir != "" {
		if top, ok := openCodeGitTopLevel(worktreeDir); ok {
			root = top
		}
	}
	roots := []string{filepath.Clean(root)}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		if c := filepath.Clean(r); c != roots[0] {
			roots = append(roots, c)
		}
	}
	dirs := []string{filepath.Clean(dir)}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		if c := filepath.Clean(r); c != dirs[0] {
			dirs = append(dirs, c)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, rt := range roots {
		for _, d := range dirs {
			rel, err := filepath.Rel(rt, d)
			if err != nil {
				continue
			}
			p := filepath.ToSlash(rel) + "/**"
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// openCodeGitTopLevel walks dir and its ancestors for a ".git" entry (file or
// directory, so a linked worktree's own gitdir pointer file counts, not just
// a primary checkout's .git directory), the same directory `git rev-parse
// --show-toplevel` would report — a filesystem walk-up, deliberately never a
// `git` subprocess, so openCodeWorktreeRelativeDirPatterns stays pure. ok is
// false when no ancestor (up to the filesystem root) has one: dir is not
// inside a git repository at all.
func openCodeGitTopLevel(dir string) (top string, ok bool) {
	d := filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// openCodeSkillDir is the directory RunOptions.SkillPath names (manager.go's
// own NIGHTGAUGE_SKILL_DIR: filepath.Dir(skillPath), read by the
// NIGHTGAUGE_SKILL_DIR upsertEnvVar call), or "" when the dispatch names none.
func openCodeSkillDir(opts RunOptions) string {
	if opts.SkillPath == "" {
		return ""
	}
	return filepath.Dir(opts.SkillPath)
}

// openCodeExternalDirectoryAllowList is ADR-022 § "external_directory
// allow-list": NIGHTGAUGE_SKILL_DIR, the context and output file dirs when
// they are outside the worktree, and the six stage skills' /tmp literals in
// both /tmp and /private/tmp forms.
//
// The NIGHTGAUGE_BIN dir is deliberately NOT here (#1638 fix round finding
// 5/9, item 4): a scan of the six stage skills' own committed text finds
// every $NIGHTGAUGE_BIN reference is `BINARY="${NIGHTGAUGE_BIN:-}"` followed
// by running $BINARY (bash execution, never gated by external_directory —
// this ADR's own "the external_directory check is a lexical backstop; bash
// redirection is not covered by it anyway") or
// `export PATH="$(dirname "$BINARY"):$PATH"` (a shell variable assignment,
// not a Read/cat/cd of a path under it) — no skill Reads, cats or cds into
// NIGHTGAUGE_BIN. Allow-listing it bought a stage nothing the six skills
// actually use, while letting a Read tool call inspect the running
// nightgauge binary's own directory (a self-hosted repo's own build output,
// on a machine where NIGHTGAUGE_BIN is the checkout's own bin/, or any
// sibling files an operator placed beside the binary). binDir no longer
// reaches this function (openCodePermissionMap's own edit-deny backstop,
// openCodeWorktreeRelativeDirPatterns, still denies EDITING it as defense in
// depth, belt-and-suspenders past external_directory's own "*": "deny"
// default already refusing everything else there).
func openCodeExternalDirectoryAllowList(opts RunOptions) []string {
	var allow []string
	allow = append(allow, openCodeDirPatterns(openCodeSkillDir(opts))...)
	worktree := opts.WorktreeDir
	// insideWorktree reports whether dir (as given, or resolved) is the
	// worktree or under it, in either's given/resolved form: a file's own
	// directory and the worktree can each be reported unresolved or
	// resolved independently (the same macOS /var -> /private/var gap
	// openCodeDirPatterns documents), so every combination is checked.
	insideWorktree := func(dir string) bool {
		if worktree == "" || dir == "" {
			return false
		}
		dirForms := []string{dir}
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dirForms = append(dirForms, r)
		}
		worktreeForms := []string{worktree}
		if r, err := filepath.EvalSymlinks(worktree); err == nil {
			worktreeForms = append(worktreeForms, r)
		}
		for _, d := range dirForms {
			for _, w := range worktreeForms {
				if d == w || strings.HasPrefix(d, w+string(filepath.Separator)) {
					return true
				}
			}
		}
		return false
	}
	addOutsideWorktree := func(file string) {
		if file == "" {
			return
		}
		dir := filepath.Dir(file)
		if insideWorktree(dir) {
			return
		}
		allow = append(allow, openCodeDirPatterns(dir)...)
	}
	addOutsideWorktree(opts.ContextFile)
	addOutsideWorktree(opts.OutputFile)
	allow = append(allow, openCodeTmpDirAllowPatterns...)
	return allow
}

// OpenCodeBinDir is the running nightgauge binary's directory
// (NIGHTGAUGE_BIN's dir), the source of the permission map's
// external_directory allow-list entry for it (#1638). Exported so the
// adapter's PrepareRunRoot (opencode.go) and the `nightgauge opencode
// config` verb (cmd/nightgauge/opencode.go) call the identical function —
// TestOpenCodeConfigVerbMatchesTheAdapter checks their output byte for byte,
// which a divergent BinDir between the two paths would break. Best-effort: a
// failure to resolve the running binary never refuses the config, the same
// way manager.go's hostBinaryPath's failure never blocks a spawn.
func OpenCodeBinDir() string {
	self, err := os.Executable()
	if err != nil || self == "" {
		return ""
	}
	return filepath.Dir(self)
}

// openCodePermissionMap builds the ADR-022 § 9 / #1638 permission map for
// opts. binDir is OpenCodeBinDir()'s result, threaded in by the caller so
// this function stays pure (BuildOpenCodeConfig's own contract).
func openCodePermissionMap(opts RunOptions, binDir string) *openCodePermissionJSON {
	grants := openCodeToolGrants(opts.AllowedTools)
	if r, ok := grants["read"]; ok {
		if _, has := grants["list"]; !has {
			grants["list"] = &openCodeToolGrant{bare: r.bare, scopes: append([]string(nil), r.scopes...)}
		}
	}
	editDenyDirPatterns := append(
		openCodeWorktreeRelativeDirPatterns(opts.WorktreeDir, openCodeSkillDir(opts)),
		openCodeWorktreeRelativeDirPatterns(opts.WorktreeDir, binDir)...,
	)
	return &openCodePermissionJSON{
		Wildcard:          openCodeDeny,
		Read:              openCodeReadPermission(grants["read"]),
		Edit:              openCodeEditPermission(grants["edit"], editDenyDirPatterns),
		Glob:              openCodeScalarPermission(grants["glob"]),
		Grep:              openCodeScalarPermission(grants["grep"]),
		List:              openCodeScalarPermission(grants["list"]),
		Bash:              openCodeBashPermission(grants["bash"]),
		Task:              openCodeScalarPermission(grants["task"]),
		WebFetch:          openCodeScalarPermission(grants["webfetch"]),
		WebSearch:         openCodeScalarPermission(grants["websearch"]),
		Skill:             openCodeScalarPermission(grants["skill"]),
		TodoWrite:         openCodeScalarPermission(grants["todowrite"]),
		DoomLoop:          openCodeDeny,
		ExternalDirectory: openCodeExternalDirectoryPermission(openCodeExternalDirectoryAllowList(opts)),
	}
}

// The project-config tamper gate (ADR-022 § 8; #1638).
//
// openCodeProjectConfigTamperCheck refuses a dispatch when the worktree's
// opencode.json, opencode.jsonc or .opencode/ differ from the base branch:
// modified, staged, untracked, git-ignored, committed on top of the base tip,
// or hidden from git's own diff machinery by a skip-worktree or
// assume-unchanged bit. A stage must not rewrite the OpenCode config the next
// stage in the same worktree runs under (addNightgaugePluginToConfig and
// BuildOpenCodeConfig together are the only writers Nightgauge itself uses;
// this gate is what refuses one a stage wrote on its own).
//
// Three independent legs, each scoped to exactly those paths, because no
// single git command catches everything a stage could do:
//
//  1. `git status --porcelain=v1 --ignored --untracked-files=all` — every
//     difference from HEAD and the index: modified, staged, untracked or
//     git-ignored. git itself does not follow a symlink to list untracked
//     files inside its target, so a symlinked .opencode is reported as itself
//     — the issue's "symlinks are compared as links, not followed" — without
//     this function resolving anything by hand.
//  2. `git diff --name-only <merge-base> -- <paths>` against the worktree's
//     resolved base ref (openCodeTamperGateBaseRef) — status alone only ever
//     sees a difference from HEAD, which is wrong once a stage commits its
//     tamper: Manager.RunStage reuses one worktree across every stage of a
//     run, and a later stage's HEAD is whatever an earlier stage committed,
//     not the base branch's tip. Comparing to the merge-base instead of HEAD
//     catches a committed change status alone misses. A worktree with no
//     resolvable base ref (no origin remote and no local main/master — never
//     a real dispatch, whose worktree Manager.RunStage clones from the target
//     repository) skips only this leg, not the whole check.
//  3. `git ls-files -v -- <paths>` — a path a stage marked skip-worktree or
//     assume-unchanged (`git update-index --skip-worktree`/`--assume-unchanged`)
//     is invisible to both (1) and (2): git's own diff and status machinery
//     is told to assume it matches the index and never inspect its content.
//     ls-files -v reports every tracked path's flag regardless (uppercase
//     "H" is the normal, unmarked state; anything else is the bit itself,
//     which is offending on its own — a legitimate dispatch never sets one on
//     these paths).
//
// Nothing under those paths is ever read; only named.
//
// worktreeDir == "" is the one open case, kept lenient rather than refused: a
// caller that names no worktree at all is not a real dispatch (every one
// names Manager.RunStage's own worktree), and dozens of PreDispatch tests
// across this package construct a RunOptions with no WorktreeDir to test
// something else entirely. Once worktreeDir is non-empty, though, this
// function fails CLOSED, never open, on anything git itself cannot confirm
// clean: a worktreeDir git reports is not a git repository (a stage's
// dispatch config named a path outside any checkout, or one whose .git a
// prior stage removed or corrupted) refuses with that reason, it does not
// silently pass — #1638 fix round finding: the prior version treated "not a
// git repository" as "nothing to compare, so allow it", exactly backwards
// for a gate whose whole job is refusing an unverifiable worktree. A git
// status failure for any OTHER reason (git missing from PATH, a corrupt
// object database) already returned a refusal below and is unchanged.
func openCodeProjectConfigTamperCheck(ctx context.Context, worktreeDir string) error {
	if worktreeDir == "" {
		return nil
	}
	const gitProtectedPathsDoc = "opencode.json, opencode.jsonc or .opencode/"
	protectedPaths := []string{"opencode.json", "opencode.jsonc", ".opencode"}

	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	// Leg 1: everything status sees relative to HEAD and the index.
	// --untracked-files=all is explicit, not left to git's default or an
	// operator's global status.showUntrackedFiles: the default ("normal")
	// collapses an entirely untracked directory to one line (".opencode/",
	// naming no file inside it), and the issue requires each offending path
	// named.
	statusArgs := append([]string{"status", "--porcelain=v1", "--ignored", "--untracked-files=all", "--"}, protectedPaths...)
	out, err := openCodeGitOutput(ctx, worktreeDir, statusArgs...)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if strings.Contains(stderr, "not a git repository") {
				return fmt.Errorf(
					"opencode: refused: %s is not a git repository, so its %s cannot be verified against a base branch. "+
						"See docs/decisions/022-opencode-multi-provider-adapter.md § 8",
					worktreeDir, gitProtectedPathsDoc)
			}
			return fmt.Errorf("opencode: checking %s for project-config tamper: git status: %s", worktreeDir, stderr)
		}
		return fmt.Errorf("opencode: checking %s for project-config tamper: %w", worktreeDir, err)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		add(line[3:])
	}

	// Leg 2: committed on top of the base branch, which status alone misses
	// in a worktree a later stage reuses (best-effort: a worktree with no
	// resolvable base ref skips only this leg).
	if baseRef, ok := openCodeTamperGateBaseRef(ctx, worktreeDir); ok {
		if mergeBase, err := openCodeGitOutput(ctx, worktreeDir, "merge-base", "HEAD", baseRef); err == nil {
			if mergeBase = strings.TrimSpace(mergeBase); mergeBase != "" {
				diffArgs := append([]string{"diff", "--name-only", mergeBase, "--"}, protectedPaths...)
				if diffOut, err := openCodeGitOutput(ctx, worktreeDir, diffArgs...); err == nil {
					for _, line := range strings.Split(strings.TrimSpace(diffOut), "\n") {
						add(line)
					}
				}
			}
		}
	}

	// Leg 3: a skip-worktree or assume-unchanged bit on a protected path
	// hides a working-tree edit from both legs above.
	lsArgs := append([]string{"ls-files", "-v", "--"}, protectedPaths...)
	if lsOut, err := openCodeGitOutput(ctx, worktreeDir, lsArgs...); err == nil {
		for _, line := range strings.Split(strings.TrimRight(lsOut, "\n"), "\n") {
			if len(line) < 3 || line[0] == 'H' {
				continue
			}
			add(line[2:])
		}
	}

	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	return fmt.Errorf(
		"opencode: refused: %s's %s differs from the base branch (modified, untracked, git-ignored, committed on top of the base tip, or skip-worktree/assume-unchanged): %s. "+
			"A stage must not rewrite the OpenCode config the next stage in this worktree runs under. See docs/decisions/022-opencode-multi-provider-adapter.md § 8",
		worktreeDir, gitProtectedPathsDoc, strings.Join(paths, ", "))
}

// openCodeTamperGateBaseRef resolves the ref openCodeProjectConfigTamperCheck's
// second leg compares worktreeDir against: the remote's default branch when
// one is configured (a real pipeline worktree always has an "origin" remote —
// Manager.RunStage clones it from the target repository), else the local
// branch of the same name, mirroring internal/execution's own
// resolveBaseRef/detectDefaultBranch (worktree_sweep.go). Duplicated locally
// rather than imported: internal/execution imports this package (adapters),
// so the reverse import would cycle. ok is false when neither a remote nor a
// local ref for the resolved default branch name exists — a repository with
// no commit reachable from anywhere but HEAD, which openCodeProjectConfigTamperCheck
// treats as "this leg finds nothing", not as a refusal.
func openCodeTamperGateBaseRef(ctx context.Context, worktreeDir string) (ref string, ok bool) {
	defaultBranch := "main"
	if out, err := openCodeGitOutput(ctx, worktreeDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); name != "" {
			defaultBranch = name
		}
	} else {
		for _, candidate := range []string{"main", "master"} {
			if openCodeGitRefExists(ctx, worktreeDir, "refs/remotes/origin/"+candidate) || openCodeGitRefExists(ctx, worktreeDir, "refs/heads/"+candidate) {
				defaultBranch = candidate
				break
			}
		}
	}
	if openCodeGitRefExists(ctx, worktreeDir, "refs/remotes/origin/"+defaultBranch) {
		return "origin/" + defaultBranch, true
	}
	if openCodeGitRefExists(ctx, worktreeDir, "refs/heads/"+defaultBranch) {
		return defaultBranch, true
	}
	return "", false
}

// openCodeGitRefExists reports whether ref resolves to a commit in dir.
func openCodeGitRefExists(ctx context.Context, dir, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", ref)
	return cmd.Run() == nil
}

// openCodeGitOutput runs a git command in dir and returns its trimmed-of-
// nothing stdout (the caller trims what it needs: a porcelain listing's
// leading status columns are significant).
func openCodeGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}
