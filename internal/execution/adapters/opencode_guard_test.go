package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// The permission map and the project-config tamper gate (ADR-022 § 9, § 8;
// #1638).

// repoRootForTest is this checkout's root, found from this file's own
// compile-time path (internal/execution/adapters/opencode_guard_test.go) —
// four directories up — rather than a relative "../../.." that breaks the
// moment a test's working directory assumption changes.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// stageSkillAllowedTools reads name's SKILL.md frontmatter for its
// allowed-tools line, the same shape internal/skillrender/render.go's own
// splitFrontmatter/splitTools parse (a duplicate, tiny, test-only reader,
// so this test exercises the real committed skill files rather than a
// hand-typed copy of their tool list, without importing skillrender and
// its heavier Render/Options surface for a single frontmatter field).
func stageSkillAllowedTools(t *testing.T, skillsRoot, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(skillsRoot, name, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.HasPrefix(content, "---\n") {
		t.Fatalf("%s/SKILL.md has no frontmatter", name)
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		t.Fatalf("%s/SKILL.md frontmatter is not closed", name)
	}
	head := content[4 : 4+end]
	for _, line := range strings.Split(head, "\n") {
		trimmed := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(trimmed, "allowed-tools:"); ok {
			return strings.Fields(strings.Trim(strings.TrimSpace(v), "\"'"))
		}
	}
	t.Fatalf("%s/SKILL.md has no allowed-tools frontmatter field", name)
	return nil
}

// openCodeCollectValues walks a decoded JSON value (map[string]any,
// []any or a scalar) and calls visit(path, value) for every scalar leaf,
// path dot-joined from the root. Test-local: production code never needs to
// walk its own output this way, since it never emits the value being
// searched for below.
func openCodeCollectValues(path string, v any, visit func(path string, v any)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			openCodeCollectValues(path+"."+k, t[k], visit)
		}
	case []any:
		for i, e := range t {
			openCodeCollectValues(fmt.Sprintf("%s[%d]", path, i), e, visit)
		}
	default:
		visit(path, v)
	}
}

// openCodeFindAsk returns every path in decoded whose value is the literal
// string "ask" — ADR-022 § 9 forbids it outright in a generated permission
// map. A non-empty result is what TestOpenCodePermissionMap's scan
// assertion fails on.
func openCodeFindAsk(decoded any) []string {
	var hits []string
	openCodeCollectValues("permission", decoded, func(path string, v any) {
		if s, ok := v.(string); ok && s == "ask" {
			hits = append(hits, path)
		}
	})
	return hits
}

// TestOpenCodeScanForAskCatchesAsk is the scan helper's own red/green
// coverage: openCodeFindAsk must report nothing over a real generated map
// (proven again by TestOpenCodePermissionMap) and must report the exact path
// of a synthetic "ask" planted in a copy — proving the scan assertion below
// is not vacuously green.
func TestOpenCodeScanForAskCatchesAsk(t *testing.T) {
	pm := openCodePermissionMap(RunOptions{AllowedTools: []string{"Read", "Bash"}}, "")
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if hits := openCodeFindAsk(decoded); len(hits) != 0 {
		t.Fatalf("a real generated map already contains \"ask\" at %v; the scan or the generator is broken", hits)
	}

	corrupt := map[string]any{}
	if err := json.Unmarshal(raw, &corrupt); err != nil {
		t.Fatal(err)
	}
	bash, ok := corrupt["bash"].(map[string]any)
	if !ok {
		t.Fatalf("permission.bash is not an object: %#v", corrupt["bash"])
	}
	bash["*"] = "ask"
	if hits := openCodeFindAsk(corrupt); len(hits) == 0 || hits[0] != "permission.bash.*" {
		t.Errorf("openCodeFindAsk did not catch a planted \"ask\" at permission.bash.*: got %v", hits)
	}
}

// openCodeBackstopEntries are the deny-list backstop patterns the issue
// names, read back from the map under test (map[string]any) as plain
// strings, independent of openCodePatternMap's own MarshalJSON.
func openCodeBackstopEntries(t *testing.T, permission map[string]any, key string, patterns []string) {
	t.Helper()
	obj, ok := permission[key].(map[string]any)
	if !ok {
		t.Fatalf("permission.%s is not an object: %#v", key, permission[key])
	}
	for _, p := range patterns {
		if obj[p] != openCodeDeny {
			t.Errorf("permission.%s[%q] = %v, want %q (the deny-list backstop; the issue requires it present in every map)", key, p, obj[p], openCodeDeny)
		}
	}
}

// TestOpenCodePermissionMap is the golden per-stage-skill test: for each of
// the six stage skills' real, committed AllowedTools, the generated
// permission map sets every key the issue names, no value is "ask", the
// deny-list backstop is present in bash/read/edit regardless of what the
// skill's tools grant, and external_directory allow-lists the skill's own
// directory. Deleting a backstop entry, or scoping any key to a bare
// "allow" without the backstop, fails this test (openCodeBackstopEntries).
func TestOpenCodePermissionMap(t *testing.T) {
	skillsRoot := filepath.Join(repoRootForTest(t), "skills")
	// A real git worktree, not a bare directory: every real dispatch's
	// WorktreeDir is one (Manager.RunStage's own worktree setup), and edit's
	// deny patterns are computed relative to it (openCodeWorktreeRelativeDirPatterns,
	// #1638 fix round finding 1/7) — a bare directory would silently exercise
	// only the "/" fallback branch, the one a non-git fixture used to hide
	// this exact bug behind.
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := gittest.InitRepo(t, worktreeDir, "-q", "-b", "main")
	writeRepoFile(t, filepath.Join(worktree, "README.md"), "worktree\n")
	gittest.Run(t, worktree, "add", "-A")
	gittest.Run(t, worktree, "commit", "-qm", "base")

	for _, name := range openCodeStageSkillDirs {
		t.Run(name, func(t *testing.T) {
			tools := stageSkillAllowedTools(t, skillsRoot, name)
			opts := RunOptions{
				AllowedTools: tools,
				SkillPath:    filepath.Join(skillsRoot, name, "SKILL.md"),
				WorktreeDir:  worktree,
			}
			pm := openCodePermissionMap(opts, "")
			raw, err := json.Marshal(pm)
			if err != nil {
				t.Fatal(err)
			}

			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if hits := openCodeFindAsk(decoded); len(hits) != 0 {
				t.Errorf("the generated map contains \"ask\" at %v (ADR-022 § 9 forbids it)", hits)
			}

			var permission map[string]any
			if err := json.Unmarshal(raw, &permission); err != nil {
				t.Fatal(err)
			}

			wantKeys := []string{"*", "read", "edit", "glob", "grep", "list", "bash", "task",
				"webfetch", "websearch", "skill", "todowrite", "doom_loop", "external_directory"}
			var gotKeys []string
			for k := range permission {
				gotKeys = append(gotKeys, k)
			}
			sort.Strings(gotKeys)
			sort.Strings(wantKeys)
			if !slices.Equal(gotKeys, wantKeys) {
				t.Fatalf("permission keys = %v, want exactly %v", gotKeys, wantKeys)
			}

			if permission["*"] != openCodeDeny {
				t.Errorf(`permission["*"] = %v, want %q`, permission["*"], openCodeDeny)
			}
			if permission["doom_loop"] != openCodeDeny {
				t.Errorf(`permission["doom_loop"] = %v, want %q`, permission["doom_loop"], openCodeDeny)
			}

			// The six stage skills all declare Read Write Edit Glob Grep
			// Bash Task (checked below so this test fails loudly, not
			// silently, if a skill's frontmatter ever narrows), and none
			// declares WebFetch, WebSearch, Skill or TodoWrite.
			wantTools := []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "Task"}
			sortedTools := slices.Clone(tools)
			sort.Strings(sortedTools)
			sortedWant := slices.Clone(wantTools)
			sort.Strings(sortedWant)
			if !slices.Equal(sortedTools, sortedWant) {
				t.Fatalf("%s/SKILL.md allowed-tools = %v, want exactly %v; this test's scalar-permission assertions below assume this set", name, tools, wantTools)
			}

			for _, allowedKey := range []string{"glob", "grep", "list", "task"} {
				if permission[allowedKey] != openCodeAllow {
					t.Errorf("permission[%q] = %v, want %q", allowedKey, permission[allowedKey], openCodeAllow)
				}
			}
			for _, deniedKey := range []string{"webfetch", "websearch", "skill", "todowrite"} {
				if permission[deniedKey] != openCodeDeny {
					t.Errorf("permission[%q] = %v, want %q", deniedKey, permission[deniedKey], openCodeDeny)
				}
			}

			read, ok := permission["read"].(map[string]any)
			if !ok || read["*"] != openCodeAllow {
				t.Errorf(`permission.read["*"] = %v, want %q (Read is in AllowedTools)`, permission["read"], openCodeAllow)
			}
			edit, ok := permission["edit"].(map[string]any)
			if !ok || edit["*"] != openCodeAllow {
				t.Errorf(`permission.edit["*"] = %v, want %q (Write/Edit are in AllowedTools)`, permission["edit"], openCodeAllow)
			}
			bash, ok := permission["bash"].(map[string]any)
			if !ok || bash["*"] != openCodeAllow {
				t.Errorf(`permission.bash["*"] = %v, want %q (Bash is in AllowedTools)`, permission["bash"], openCodeAllow)
			}

			openCodeBackstopEntries(t, permission, "bash", openCodeBashDenyBackstop)
			openCodeBackstopEntries(t, permission, "read", openCodeSecretDenyBackstop)
			openCodeBackstopEntries(t, permission, "edit", openCodeSecretDenyBackstop)
			openCodeBackstopEntries(t, permission, "edit", openCodeProjectConfigDenyBackstop)

			extDir, ok := permission["external_directory"].(map[string]any)
			if !ok {
				t.Fatalf("permission.external_directory is not an object: %#v", permission["external_directory"])
			}
			if extDir["*"] != openCodeDeny {
				t.Errorf(`permission.external_directory["*"] = %v, want %q`, extDir["*"], openCodeDeny)
			}
			for _, skillDirPattern := range openCodeDirPatterns(filepath.Join(skillsRoot, name)) {
				if extDir[skillDirPattern] != openCodeAllow {
					t.Errorf("permission.external_directory[%q] = %v, want %q (the stage's own skill dir, ADR-022's NIGHTGAUGE_SKILL_DIR allow-list entry)", skillDirPattern, extDir[skillDirPattern], openCodeAllow)
				}
			}
			// edit's own deny pattern is relative to the worktree root, not
			// the skill dir's absolute path: a bounded probe against opencode
			// 1.18.30 in a real git worktree (TestProbeA9SkillEditInGitWorktree,
			// ADR-022's 2026-09-15 correction) found the absolute-path form
			// never matches there (#1638 fix round finding 1/7).
			for _, skillDirPattern := range openCodeWorktreeRelativeDirPatterns(worktree, filepath.Join(skillsRoot, name)) {
				if edit[skillDirPattern] != openCodeDeny {
					t.Errorf("permission.edit[%q] = %v, want %q (the skill dir is read-only: Read succeeds, Edit is denied)", skillDirPattern, edit[skillDirPattern], openCodeDeny)
				}
			}
			for _, p := range openCodeTmpDirAllowPatterns {
				if extDir[p] != openCodeAllow {
					t.Errorf("permission.external_directory[%q] = %v, want %q", p, extDir[p], openCodeAllow)
				}
			}
		})
	}
}

// TestOpenCodePermissionMapEmptyAllowedToolsNeverAllows: an empty or missing
// AllowedTools must never produce a bare "*": "allow" — the issue's own
// wording — for any key.
func TestOpenCodePermissionMapEmptyAllowedToolsNeverAllows(t *testing.T) {
	pm := openCodePermissionMap(RunOptions{}, "")
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var permission map[string]any
	if err := json.Unmarshal(raw, &permission); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"*", "glob", "grep", "list", "task", "webfetch", "websearch", "skill", "todowrite", "doom_loop"} {
		if permission[key] != openCodeDeny {
			t.Errorf("permission[%q] = %v, want %q with no AllowedTools", key, permission[key], openCodeDeny)
		}
	}
	for _, key := range []string{"read", "edit", "bash", "external_directory"} {
		obj, ok := permission[key].(map[string]any)
		if !ok || obj["*"] != openCodeDeny {
			t.Errorf("permission.%s[\"*\"] = %v, want %q with no AllowedTools", key, permission[key], openCodeDeny)
		}
	}
}

// TestOpenCodePermissionMapNeverAllowsBinDir is #1638 fix round finding
// 5/9's own closure (item 4): a scan of the six stage skills' own committed
// text finds no concrete Read, cat or cd of a path under NIGHTGAUGE_BIN —
// every reference either runs $BINARY (bash execution, never gated by
// external_directory) or reads $(dirname "$BINARY") into PATH (a shell
// variable, not a filesystem read) — so the generated external_directory
// allow-list must never carry binDir's own pattern, whatever binDir is.
func TestOpenCodePermissionMapNeverAllowsBinDir(t *testing.T) {
	binDir := t.TempDir()
	pm := openCodePermissionMap(RunOptions{AllowedTools: []string{"Read", "Edit", "Write"}, WorktreeDir: t.TempDir()}, binDir)
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var permission map[string]any
	if err := json.Unmarshal(raw, &permission); err != nil {
		t.Fatal(err)
	}
	extDir, ok := permission["external_directory"].(map[string]any)
	if !ok {
		t.Fatalf("permission.external_directory is not an object: %#v", permission["external_directory"])
	}
	for _, pattern := range openCodeDirPatterns(binDir) {
		if action, present := extDir[pattern]; present {
			t.Errorf("permission.external_directory[%q] = %v, want the key absent: NIGHTGAUGE_BIN must not be external_directory-allow-listed", pattern, action)
		}
	}
}

// TestOpenCodeDenyListBackstopComplete is the golden's own "deleting one
// makes it fail" coverage: every literal here is copied from the issue's own
// text, independent of openCodeBashDenyBackstop/openCodeSecretDenyBackstop/
// openCodeProjectConfigDenyBackstop, so a change that shortens one of those
// vars — as well as the map they feed — is what this test catches; reading
// the assertion back from the same var it is meant to guard would pass
// vacuously no matter how short the var got.
func TestOpenCodeDenyListBackstopComplete(t *testing.T) {
	pm := openCodePermissionMap(RunOptions{AllowedTools: []string{"Read", "Edit", "Bash"}}, "")
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var permission map[string]any
	if err := json.Unmarshal(raw, &permission); err != nil {
		t.Fatal(err)
	}

	bash := permission["bash"].(map[string]any)
	for _, want := range []string{"rm -rf *", "rm -fr *", "git push --force*", "git push -f*", "git push * --force*"} {
		if bash[want] != openCodeDeny {
			t.Errorf("permission.bash[%q] = %v, want %q", want, bash[want], openCodeDeny)
		}
	}
	read := permission["read"].(map[string]any)
	edit := permission["edit"].(map[string]any)
	for _, m := range []map[string]any{read, edit} {
		// "**/*.env" and "**/.env*" cover a NESTED secret file
		// (apps/web/.env.local): "*.env" and ".env*" alone only ever match a
		// root-level one, since opencode's own pattern matching has no
		// implicit "**" prefix (#1638 fix round, a routed #1752 request).
		for _, want := range []string{"*.env", ".env*", "**/*.env", "**/.env*", "**/.ssh/**", "**/id_rsa*", "**/gh/hosts.yml"} {
			if m[want] != openCodeDeny {
				t.Errorf("= %v, want %q for %q", m[want], openCodeDeny, want)
			}
		}
	}
	for _, want := range []string{"opencode.json*", ".opencode/**"} {
		if edit[want] != openCodeDeny {
			t.Errorf("permission.edit[%q] = %v, want %q", want, edit[want], openCodeDeny)
		}
	}
}

// TestOpenCodeBashScopedEntry: "Bash(git *)" without a bare "Bash" grants
// only that pattern, with the base still "deny" and the backstop deny
// patterns still present and still winning (git push --force* stays denied
// even though "git *" is allowed).
func TestOpenCodeBashScopedEntry(t *testing.T) {
	pm := openCodePermissionMap(RunOptions{AllowedTools: []string{"Bash(git *)"}}, "")
	raw, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var permission map[string]any
	if err := json.Unmarshal(raw, &permission); err != nil {
		t.Fatal(err)
	}
	bash := permission["bash"].(map[string]any)
	if bash["*"] != openCodeDeny {
		t.Errorf(`bash["*"] = %v, want %q: the bare Bash tool was never granted`, bash["*"], openCodeDeny)
	}
	if bash["git *"] != openCodeAllow {
		t.Errorf(`bash["git *"] = %v, want %q`, bash["git *"], openCodeAllow)
	}
	if bash["git push --force*"] != openCodeDeny {
		t.Errorf(`bash["git push --force*"] = %v, want %q even though "git *" is allowed`, bash["git push --force*"], openCodeDeny)
	}

	// Order matters (ADR-022 § 8's last-match-wins): the backstop deny
	// entries must be the LAST entries the pattern map marshals, so nothing
	// added earlier (a scoped allow) can be reordered ahead of them by
	// encoding/json's usual key-sort.
	raw2, err := json.Marshal(openCodeBashPermission(&openCodeToolGrant{scopes: []string{"git *"}}))
	if err != nil {
		t.Fatal(err)
	}
	last := openCodeBashDenyBackstop[len(openCodeBashDenyBackstop)-1]
	wantSuffix := fmt.Sprintf("%q:%q}", last, openCodeDeny)
	if !strings.HasSuffix(string(raw2), wantSuffix) {
		t.Errorf("bash pattern map does not end with the backstop's last entry %s: %s", wantSuffix, raw2)
	}
}

// openCodeTmpCoverageMissing returns, of found (openCodeScanStageSkillTmpLiterals'
// own normalized output), the ones opencode's own external_directory ask —
// dirname(literal) + "/*" (openCodeTmpRequestPattern), never the literal's
// own path — is not covered by openCodeTmpDirAllowPatterns. This is the
// mechanism both TestOpenCodeTmpAllowListCoversStageSkills and its own
// red/green companion share, so a literal is judged covered the same way
// opencode 1.18.30 itself would judge the request, not by literal string
// containment against the source list (#1638 fix round, AC4).
func openCodeTmpCoverageMissing(found []string) []string {
	allow := map[string]bool{}
	for _, p := range openCodeTmpDirAllowPatterns {
		allow[p] = true
	}
	var missing []string
	for _, lit := range found {
		if !allow[openCodeTmpRequestPattern(lit)] {
			missing = append(missing, lit)
		}
	}
	return missing
}

// TestOpenCodeTmpAllowListCoversStageSkills scans the six stage skills for
// /tmp/... literals and fails when one's own external_directory request
// (dirname/"*", opencode's real matching — AC4, #1638 fix round) is not
// covered by openCodeTmpDirAllowPatterns. Every literal the scan finds today
// is flat, directly under /tmp, so this always passes against the committed
// skills; a skill that introduced a NESTED /tmp subdirectory would need its
// own allow entry (this test's own red companion below proves that).
func TestOpenCodeTmpAllowListCoversStageSkills(t *testing.T) {
	skillsRoot := filepath.Join(repoRootForTest(t), "skills")
	found, err := openCodeScanStageSkillTmpLiterals(skillsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("the scan found no /tmp/... literal in the six stage skills; the scan regex or the skill paths are broken")
	}
	if missing := openCodeTmpCoverageMissing(found); len(missing) > 0 {
		t.Errorf("the six stage skills use /tmp paths openCodeTmpDirAllowPatterns does not cover: %v (found: %v)", missing, found)
	}
}

// TestOpenCodeTmpAllowListCoverageFailsOnANewLiteral is
// TestOpenCodeTmpAllowListCoversStageSkills's own red/green coverage: a
// fixture skill introducing a /tmp path under a NESTED subdirectory
// (/tmp/sub/dir/x) must fail the same coverage check. openCodeTmpDirAllowPatterns'
// "/tmp/*" pattern never crosses a "/" (opencode's own matching, like every
// other pattern this file builds), so a nested subdirectory is exactly the
// shape the flat allow-list does NOT cover — unlike a plain new root-level
// /tmp/... literal, which this pair already covers and so proves nothing
// about drift detection.
func TestOpenCodeTmpAllowListCoverageFailsOnANewLiteral(t *testing.T) {
	fixture := t.TempDir()
	dst := filepath.Join(fixture, "nightgauge-feature-dev")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: nightgauge-feature-dev\n---\n\nWrite output to /tmp/sub/dir/opencode-1638-fixture-drift.json\n"
	if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := swapOpenCodeStageSkillDirsForTest([]string{"nightgauge-feature-dev"})
	defer restore()

	found, err := openCodeScanStageSkillTmpLiterals(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if missing := openCodeTmpCoverageMissing(found); len(missing) == 0 {
		t.Fatalf("a fixture skill introducing a nested /tmp/sub/dir/... path was not caught as uncovered: found %v", found)
	}
}

// swapOpenCodeStageSkillDirsForTest replaces openCodeStageSkillDirs with
// dirs until the returned func restores it. Test-only: production code never
// needs to scan a different skill set.
func swapOpenCodeStageSkillDirsForTest(dirs []string) (restore func()) {
	prev := openCodeStageSkillDirs
	openCodeStageSkillDirs = dirs
	return func() { openCodeStageSkillDirs = prev }
}

// TestOpenCodeArgvHasNoBypassFlagsWithPermissionMap extends
// TestOpenCodeNeverEmitsBypassFlags (flag_contract_test.go, #1612): with a
// real permission map built into the per-run config (every AllowedTools
// entry granted), BuildCommand's argv still carries none of the three
// bypass flags. The permission map lives in OPENCODE_CONFIG_CONTENT, never
// argv, so this is a same-answer-different-path check on that boundary.
func TestOpenCodeArgvHasNoBypassFlagsWithPermissionMap(t *testing.T) {
	in, err := OpenCodeConfigInputFor(lmStudioSettings(), RunOptions{
		Stage:        "feature-dev",
		Model:        "lmstudio/qwen/qwen3.8-27b",
		WorktreeDir:  "/work/nightgauge-1638",
		AllowedTools: []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "Task", "WebFetch", "WebSearch", "TodoWrite", "Skill"},
	}, "/run/root", envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	built, err := BuildOpenCodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built.Content, `"permission"`) {
		t.Fatalf("the built config carries no \"permission\" key:\n%s", built.Content)
	}

	a := NewOpenCodeAdapter()
	_, args, env := a.BuildCommand(RunOptions{
		Model:       "lmstudio/qwen/qwen3.8-27b",
		WorktreeDir: "/work/nightgauge-1638",
		RunRoot:     &RunRoot{Env: map[string]string{openCodeConfigContentEnvVar: built.Content}},
	})
	for _, arg := range args {
		if f, ok := openCodeForbiddenFlagIn(arg); ok {
			t.Errorf("argv carries forbidden flag %s as %q with a permission map in place: %q", f, arg, args)
		}
	}
	if f, ok := openCodeForbiddenFlagIn(env[openCodeConfigContentEnvVar]); ok {
		t.Errorf("OPENCODE_CONFIG_CONTENT itself is read as forbidden flag %s; the config must never look like argv", f)
	}
}

// The project-config tamper gate (ADR-022 § 8; #1638).

// tamperFixtureBaseFiles is what every TestOpenCodeTamperGate case's base
// commit (the "base branch") holds: a locked-down opencode.json and a
// .gitignore that ignores a plugin file, so the "ignored" case can write one
// without staging it.
var tamperFixtureBaseFiles = map[string]string{
	"opencode.json": `{"base":true}`,
	".gitignore":    ".opencode/plugins/ignored.ts\n",
}

func TestOpenCodeTamperGateCleanTreePasses(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	if err := openCodeProjectConfigTamperCheck(context.Background(), wt); err != nil {
		t.Fatalf("a clean worktree was refused: %v", err)
	}
}

// TestOpenCodeTamperGateNonGitWorktreeFailsClosed is #1638's trivial-low
// finding: a worktreeDir git reports is not a git repository (a dispatch
// config named a path outside any checkout, or one whose .git a prior stage
// removed or corrupted) must REFUSE, naming the reason — not silently pass
// because there is nothing to compare against. A gate whose entire job is
// refusing an unverifiable worktree must fail closed, not open, on the one
// case where verification itself is impossible.
func TestOpenCodeTamperGateNonGitWorktreeFailsClosed(t *testing.T) {
	wt := t.TempDir() // deliberately never git-initialized
	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a non-git worktree was not refused; the gate must fail closed")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("the refusal does not name the reason: %v", err)
	}
}

// TestOpenCodeTamperGateGitStatusFailsClosed is the gate's OTHER fail-closed
// leg: a git invocation that fails for a reason besides "not a git
// repository" (here, a corrupt .git that makes every git subcommand error)
// must also refuse, naming the worktree — not merely NOT pass silently, but
// return an actionable error, never nil.
func TestOpenCodeTamperGateGitStatusFailsClosed(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	// Corrupt the index (not HEAD or .git itself, which git reports as "not
	// a git repository" — the OTHER leg, its own test above) so `git status`
	// fails for a distinct reason, the same shape a damaged worktree gives.
	if err := os.WriteFile(filepath.Join(wt, ".git", "index"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a worktree whose git status fails was not refused; the gate must fail closed")
	}
	if !strings.Contains(err.Error(), wt) {
		t.Errorf("the refusal does not name the worktree: %v", err)
	}
}

func TestOpenCodeTamperGateModifiedOpencodeJSON(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	writeRepoFile(t, filepath.Join(wt, "opencode.json"), `{"base":true,"tampered":true}`)
	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a modified opencode.json was not refused")
	}
	if !strings.Contains(err.Error(), "opencode.json") {
		t.Errorf("the refusal does not name opencode.json: %v", err)
	}
}

func TestOpenCodeTamperGateUntrackedPlugin(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	writeRepoFile(t, filepath.Join(wt, ".opencode/plugins/x.ts"), "export default {}\n")
	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("an untracked .opencode/plugins/x.ts was not refused")
	}
	if !strings.Contains(err.Error(), ".opencode/plugins/x.ts") {
		t.Errorf("the refusal does not name .opencode/plugins/x.ts: %v", err)
	}
}

func TestOpenCodeTamperGateIgnoredPlugin(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	writeRepoFile(t, filepath.Join(wt, ".opencode/plugins/ignored.ts"), "export default {}\n")
	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a git-ignored .opencode/plugins/ignored.ts was not refused")
	}
	if !strings.Contains(err.Error(), ".opencode/plugins/ignored.ts") {
		t.Errorf("the refusal does not name the ignored file: %v", err)
	}
}

// TestOpenCodeTamperGateCommittedChange is AC6's own probe: a prior stage in
// the same reused worktree (Manager.RunStage reuses one worktree across a
// run's stages) commits opencode.json and a new .opencode/plugins/x.ts on the
// feature branch. `git status` alone is clean — HEAD is the tampered commit,
// not the base branch's tip — so without comparing to a resolved base ref
// (openCodeTamperGateBaseRef), the gate passes a worktree that differs from
// the base branch exactly the way the issue names (#1638 fix round finding
// 4/9).
func TestOpenCodeTamperGateCommittedChange(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	gittest.Run(t, wt, "checkout", "-qb", "feat/x")
	writeRepoFile(t, filepath.Join(wt, "opencode.json"), `{"base":true,"permission":{"bash":"allow"},"plugin":["./.opencode/plugins/x.ts"]}`)
	writeRepoFile(t, filepath.Join(wt, ".opencode", "plugins", "x.ts"), "export default {}\n")
	gittest.Run(t, wt, "add", "-A")
	gittest.Run(t, wt, "commit", "-qm", "stage commit")

	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a committed change to opencode.json and .opencode/plugins/x.ts, relative to the base branch, was not refused")
	}
	for _, want := range []string{"opencode.json", ".opencode/plugins/x.ts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
}

// TestOpenCodeTamperGateSkipWorktree is #1638 fix round finding 4/9's own
// probe: `git update-index --skip-worktree` tells git's own diff and status
// machinery to assume opencode.json matches the index, so a working-tree edit
// under it is invisible to both the status leg and the committed-diff leg —
// only an explicit `git ls-files -v` flag check catches it.
func TestOpenCodeTamperGateSkipWorktree(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	gittest.Run(t, wt, "update-index", "--skip-worktree", "opencode.json")
	writeRepoFile(t, filepath.Join(wt, "opencode.json"), `{"base":true,"tampered":true}`)

	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a skip-worktree-hidden modification of opencode.json was not refused")
	}
	if !strings.Contains(err.Error(), "opencode.json") {
		t.Errorf("the refusal does not name opencode.json: %v", err)
	}
}

func TestOpenCodeTamperGateSymlinkedOpencodeDir(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	outside := t.TempDir()
	writeRepoFile(t, filepath.Join(outside, "plugins", "evil.ts"), "export default {}\n")
	if err := os.Symlink(outside, filepath.Join(wt, ".opencode")); err != nil {
		t.Fatal(err)
	}
	err := openCodeProjectConfigTamperCheck(context.Background(), wt)
	if err == nil {
		t.Fatal("a symlinked .opencode was not refused")
	}
	if !strings.Contains(err.Error(), ".opencode") {
		t.Errorf("the refusal does not name .opencode: %v", err)
	}
	// The symlink is compared as a link, not followed: its target
	// (outside/plugins/evil.ts) is never named, only .opencode itself.
	if strings.Contains(err.Error(), "evil.ts") {
		t.Errorf("the refusal named the symlink's target instead of the link itself: %v", err)
	}
}

// TestOpenCodeTamperGateSpawnsNothing: PreDispatch on a tampered worktree
// returns the tamper refusal before anything past it runs, and no process
// named "opencode" is ever invoked — checked directly, with a fake
// "opencode" on PATH that records its own invocation to a marker file.
func TestOpenCodeTamperGateSpawnsNothing(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	writeRepoFile(t, filepath.Join(wt, "opencode.json"), `{"base":true,"tampered":true}`)

	fakeBinDir := t.TempDir()
	marker := filepath.Join(fakeBinDir, "invoked")
	fake := filepath.Join(fakeBinDir, "opencode")
	script := "#!/bin/sh\necho invoked >> " + marker + "\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+filepath.Dir(realGit))

	a := NewOpenCodeAdapter()
	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	stderr := captureAdapterStderr(t, func() {
		err = a.PreDispatch(context.Background(), RunOptions{
			Model:       "lmstudio/qwen/qwen3.8-27b",
			WorktreeDir: wt,
		})
	})
	if err == nil {
		t.Fatal("PreDispatch on a tampered worktree returned nil")
	}
	if !strings.Contains(err.Error(), "opencode.json") {
		t.Errorf("PreDispatch's refusal does not name opencode.json: %v\nstderr:\n%s", err, stderr)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the fake opencode binary on PATH was invoked; the tamper gate must spawn nothing")
	}
}
