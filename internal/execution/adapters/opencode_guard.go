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
// backstop names.
var openCodeSecretDenyBackstop = []string{"*.env", ".env*", "**/.ssh/**", "**/id_rsa*", openCodeGhHostsPattern}

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
// pattern g grants, the secret and project-config deny-list backstops, and,
// when skillDirPattern is not empty, a deny for it — the read-only half of
// the external_directory allow-list (manager.go's NIGHTGAUGE_SKILL_DIR): a
// stage may Read under it but never Edit it.
func openCodeEditPermission(g *openCodeToolGrant, skillDirPatterns []string) *openCodePatternMap {
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
	for _, pattern := range skillDirPatterns {
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

// openCodeTmpAllowList is every /tmp/... literal a scan of the six stage
// skills (skills/nightgauge-{issue-pickup,feature-planning,feature-dev,
// feature-validate,pr-create,pr-merge}) found, normalized: a shell
// substitution within the literal ($(...), ${...}, $$, or a bare $VAR)
// becomes "*". TestOpenCodeTmpAllowListCoversStageSkills re-scans the skills
// and fails when one introduces a literal this list does not cover.
var openCodeTmpAllowList = []string{
	"/tmp/planning_tmp.json",
	"/tmp/fixed.go.bak",
	"/tmp/*.ng-revert.bak",
	"/tmp/ng-test-exec-*.json",
	"/tmp/verify-ui-dev-server-*.log",
	"/tmp/automerge.err",
	"/tmp/ib_group_b_git.json",
	"/tmp/ib_group_c_files.json",
}

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
// with "*" and collapses a run of "*" into one, the same normalization
// openCodeTmpAllowList's entries were hand-derived with. Exported to this
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

// openCodeStageSkillDirs are the six stage skills the /tmp allow-list and the
// external-directory allow-list (NIGHTGAUGE_SKILL_DIR) are scoped to.
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
// checks every one of them is in openCodeTmpAllowList.
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

// openCodeTmpAllowPatterns is openCodeTmpAllowList, each pattern also emitted
// under /private/tmp: on macOS, opencode reports the resolved path, and
// /tmp is a symlink to /private/tmp there (ADR-022 § 9's last bullet: "The
// permission map is built against resolved paths").
func openCodeTmpAllowPatterns() []string {
	out := make([]string, 0, len(openCodeTmpAllowList)*2)
	for _, p := range openCodeTmpAllowList {
		out = append(out, p, "/private"+p)
	}
	return out
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

// openCodeEditDenyPatterns is openCodeDirPatterns with each pattern's
// leading path separator stripped, de-duplicated. A bounded probe against
// opencode 1.18.30 (TestOpenCodeIncludesReadAllowed; also reproduced in
// isolation for "edit" specifically) found that "edit"'s own pattern
// matching (unlike "external_directory"'s, ADR-022's amendment dated
// 2026-09-15 records the contrast) never matches a pattern that starts with
// "/" against the tool call's own absolute filePath — the identical pattern,
// stripped of its leading slash, matches correctly. Every other pattern this
// file sets on "edit" (openCodeSecretDenyBackstop,
// openCodeProjectConfigDenyBackstop) already has no leading slash, so this
// conversion is needed only for a dynamic, absolute directory pattern such
// as the skill dir's.
func openCodeEditDenyPatterns(dir string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range openCodeDirPatterns(dir) {
		p = strings.TrimPrefix(p, string(filepath.Separator))
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
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
// allow-list": NIGHTGAUGE_SKILL_DIR, the NIGHTGAUGE_BIN dir, the context and
// output file dirs when they are outside the worktree, and the six stage
// skills' /tmp literals in both /tmp and /private/tmp forms. binDir is the
// running nightgauge binary's directory (NIGHTGAUGE_BIN's dir); the caller
// resolves it (OpenCodeBinDir), because BuildOpenCodeConfig is pure and
// os.Executable reads process state, not OpenCodeConfigInput.
func openCodeExternalDirectoryAllowList(opts RunOptions, binDir string) []string {
	var allow []string
	allow = append(allow, openCodeDirPatterns(openCodeSkillDir(opts))...)
	allow = append(allow, openCodeDirPatterns(binDir)...)
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
	allow = append(allow, openCodeTmpAllowPatterns()...)
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
	skillDirEditDenyPatterns := openCodeEditDenyPatterns(openCodeSkillDir(opts))
	return &openCodePermissionJSON{
		Wildcard:          openCodeDeny,
		Read:              openCodeReadPermission(grants["read"]),
		Edit:              openCodeEditPermission(grants["edit"], skillDirEditDenyPatterns),
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
		ExternalDirectory: openCodeExternalDirectoryPermission(openCodeExternalDirectoryAllowList(opts, binDir)),
	}
}

// The project-config tamper gate (ADR-022 § 8; #1638).
//
// openCodeProjectConfigTamperCheck refuses a dispatch when the worktree's
// opencode.json, opencode.jsonc or .opencode/ differ from the base branch:
// modified, staged, untracked or git-ignored. A stage must not rewrite the
// OpenCode config the next stage in the same worktree runs under
// (addNightgaugePluginToConfig and BuildOpenCodeConfig together are the only
// writers Nightgauge itself uses; this gate is what refuses one a stage wrote
// on its own).
//
// `git status --porcelain=v1 --ignored`, scoped to exactly those paths, is
// the check: a fresh worktree's HEAD is the base branch's tip, so anything it
// reports is a difference from that tip, tracked or not. git itself does not
// follow a symlink to list untracked files inside its target, so a symlinked
// .opencode is reported as itself — the issue's "symlinks are compared as
// links, not followed" — without this function resolving anything by hand.
// Nothing under those paths is ever read; only named.
//
// A worktreeDir that is not a git repository at all (a non-pipeline caller,
// or a test fixture with no .git) has no base branch to differ from, so it is
// skipped, never refused: every real dispatch's WorktreeDir is a git
// worktree by construction (Manager.RunStage's own worktree setup, which this
// hook runs after), so the skip is theoretical in production and only keeps
// this function total for a caller that has none.
func openCodeProjectConfigTamperCheck(ctx context.Context, worktreeDir string) error {
	if worktreeDir == "" {
		return nil
	}
	// --untracked-files=all is explicit, not left to git's default or an
	// operator's global status.showUntrackedFiles: the default ("normal")
	// collapses an entirely untracked directory to one line (".opencode/",
	// naming no file inside it), and the issue requires each offending path
	// named.
	cmd := exec.CommandContext(ctx, "git", "-C", worktreeDir, "status", "--porcelain=v1", "--ignored", "--untracked-files=all",
		"--", "opencode.json", "opencode.jsonc", ".opencode")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if strings.Contains(stderr, "not a git repository") {
				return nil
			}
			return fmt.Errorf("opencode: checking %s for project-config tamper: git status: %s", worktreeDir, stderr)
		}
		return fmt.Errorf("opencode: checking %s for project-config tamper: %w", worktreeDir, err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		paths = append(paths, strings.TrimSpace(line[3:]))
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	return fmt.Errorf(
		"opencode: refused: %s's opencode.json, opencode.jsonc or .opencode/ differs from the base branch (modified, untracked or git-ignored): %s. "+
			"A stage must not rewrite the OpenCode config the next stage in this worktree runs under. See docs/decisions/022-opencode-multi-provider-adapter.md § 8",
		worktreeDir, strings.Join(paths, ", "))
}
