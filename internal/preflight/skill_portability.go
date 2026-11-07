// Skill portability linter (#4029). Walks every Markdown file under skills/
// (SKILL.md plus _includes/ and _shared/ supporting files) and fails when a
// skill embeds a host-specific binary path that breaks cross-adapter
// portability — concretely, a hardcoded VSCode-extension path.
//
// Skills are documented as portable across Claude, Codex, Copilot, Cursor and
// Gemini "without modification". Binary discovery must therefore be
// provider-neutral: the host that spawns the skill exports $NIGHTGAUGE_BIN
// (skillRunner / the Go auto-CLI manager) and the PREFLIGHT cascade falls back
// to PATH → repo bin → canonical-repo bin → ~/go/bin. A
// `~/.vscode/extensions/...` path is VSCode-only and silently fails to resolve
// under any other adapter, so it must never appear in a skill. The Claude-only
// `claude-plugins/.../guard.sh` keeps that glob deliberately (it is NOT a skill
// and is not scanned by this gate) — see #4029 / PREFLIGHT.md.
//
// Mirrors scripts/lint-skills/portability.sh — same scope, same pattern, same
// exit-code semantics. The Go form is what CI runs (faster, no bash required);
// the shell form is the developer-friendly path.
//
// Schema version 1 — field names (v, root, files_checked, findings, warnings)
// are stable and consumed by callers via fixed jq paths.
package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SkillPortabilityResult is the stable JSON output schema for
// `nightgauge preflight skill-portability`.
type SkillPortabilityResult struct {
	V            int                       `json:"v"`             // schema version, always 1
	Root         string                    `json:"root"`          // absolute path
	FilesChecked int                       `json:"files_checked"` // count of skill/supporting .md files inspected
	Findings     []SkillPortabilityFinding `json:"findings"`      // one entry per occurrence
	Warnings     []string                  `json:"warnings"`      // non-fatal issues (read errors, etc.)
}

// SkillPortabilityFinding describes one offending line in a skill file.
type SkillPortabilityFinding struct {
	SkillFile string `json:"skill_file"` // path relative to Root
	Line      int    `json:"line"`       // 1-based line number
	Check     string `json:"check"`      // portability check identifier
	Match     string `json:"match"`      // line content (trimmed)
}

// Portability check identifiers used in the Check field of a finding.
const (
	// CheckVSCodeBinaryPath flags a hardcoded VSCode-extension binary path —
	// the canonical non-portable anti-pattern this gate exists to prevent.
	CheckVSCodeBinaryPath = "vscode_extension_path"

	// CheckStopHook flags a `hooks:` key in SKILL.md frontmatter. Stop-hook
	// completion gates silently never fire on non-Claude adapters (spike #33
	// finding D2); completion verification lives in Go StageGates instead
	// (internal/orchestrator/gates, #55).
	CheckStopHook = "hooks_frontmatter"

	// CheckTruncatedBinaryCascade flags a fenced bash binary-discovery block
	// that lost any rung from the canonical PREFLIGHT.md cascade (#55, #365).
	CheckTruncatedBinaryCascade = "truncated_binary_cascade"

	// CheckHooksDirReference flags a `$HOOKS_DIR/<file>` call naming a file
	// that is not in the plugin's hooks directory. The skill guarded each call
	// with `[ -x ]`, so a deleted script degraded to a warning nobody read and
	// the step silently did nothing (#2764).
	CheckHooksDirReference = "hooks_dir_reference"

	// CheckSourceRepoPath flags a `packages/nightgauge[-sdk]/` path. Shipped
	// skills run in any repository, where that path does not exist (#2764).
	CheckSourceRepoPath = "source_repo_path"

	// CheckClaudeSkillFlag flags `claude --skill`, a flag Claude Code does not
	// have. Plugin skills run as `/nightgauge:<short>` (#2764).
	CheckClaudeSkillFlag = "claude_skill_flag"
)

// SkillPortabilityOptions controls a single linter run.
type SkillPortabilityOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
	// SkillsDir is the skill tree to scan. When empty, <Root>/skills is used.
	// A relative path resolves against Root. Set it to scan a built bundle
	// (packages/nightgauge/dist/skills) rather than the source tree.
	SkillsDir string
}

// vscodeExtensionPathRE matches a reference to the VSCode-extension binary
// directory. Anchored on `.vscode/extensions` followed (anywhere later on the
// line) by the nightgauge extension id — this catches the historical glob
// `$HOME/.vscode/extensions/nightgauge.nightgauge-*/dist/bin/nightgauge`
// and any restatement of it, while not tripping on the unrelated
// `<root>/.vscode/` workspace-manifest directory used by workspace-init skills.
// Case-insensitive (?i) for defense-in-depth against a mis-cased extension id.
var vscodeExtensionPathRE = regexp.MustCompile(`(?i)\.vscode/extensions/nightgauge`)

// stopHookRE matches the `hooks:` frontmatter key at column 0. Skill bodies
// never legitimately start a line with a bare `hooks:`; the only historical
// occurrences were the Claude-only Stop-hook completion gates removed in #55.
var stopHookRE = regexp.MustCompile(`^hooks:\s*$`)

// Cascade checks are block-scoped: a complete cascade elsewhere in the same
// Markdown file must not hide a truncated sibling (#365). The six rungs are
// the provider-neutral discovery contract from PREFLIGHT.md.
var (
	cascadeStartRE  = regexp.MustCompile(`BINARY="\$\{NIGHTGAUGE_BIN`)
	bashFenceOpenRE = regexp.MustCompile("(?i)^\\s*([`~]{3,})\\s*(bash|sh|shell)\\s*$")
	cascadeRungs    = []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "NIGHTGAUGE_BIN", pattern: regexp.MustCompile(`BINARY="\$\{NIGHTGAUGE_BIN`)},
		{name: "PATH", pattern: regexp.MustCompile(`command -v nightgauge`)},
		{name: "repo bin", pattern: regexp.MustCompile(`git rev-parse --show-toplevel`)},
		{name: "canonical repo bin", pattern: regexp.MustCompile(`git rev-parse --git-common-dir`)},
		{name: "dev repo bin gate", pattern: regexp.MustCompile(`NIGHTGAUGE_DEV_REPO_BIN:-`)},
		{name: "go bin", pattern: regexp.MustCompile(`go/bin/nightgauge`)},
	}
)

// hooksDirRefRE captures the file a `$HOOKS_DIR/...` or `${HOOKS_DIR}/...`
// reference names. A trailing `.` is sentence punctuation, trimmed by the caller.
var hooksDirRefRE = regexp.MustCompile(`\$\{?HOOKS_DIR\}?/([A-Za-z0-9_./-]+)`)

// sourceRepoPathRE matches a path into this repository's TypeScript packages.
var sourceRepoPathRE = regexp.MustCompile(`packages/nightgauge(-sdk)?\b`)

// claudeSkillFlagRE matches the nonexistent `claude --skill` invocation.
var claudeSkillFlagRE = regexp.MustCompile(`claude --skill\b`)

// sourceRepoPathExempt lists lines that name a source-repository path on
// purpose, keyed by skill-tree-relative file and the exact trimmed line so an
// exemption cannot leak to another line or file.
var sourceRepoPathExempt = map[string]map[string]string{
	"nightgauge-model-watch/SKILL.md": {
		"request, they edit `packages/nightgauge-sdk/src/eval/model-registry.json`,": "hands a registry patch to a Nightgauge maintainer, who edits this repository",
	},
	"nightgauge-release-watch/assessments/TEMPLATE.md": {
		"- [ ] SDK (`packages/nightgauge-sdk/`) — New modules or changes to existing?": "assesses impact on Nightgauge's own components",
		"- [ ] VSCode extension (`packages/nightgauge/`) — UI changes?":                "assesses impact on Nightgauge's own components",
	},
}

// historyFile reports whether a skill-tree file records history rather than
// instructions: a changelog quotes the old text it replaced, and is not
// bundled into dist/skills.
func historyFile(rel string) bool {
	return filepath.Base(rel) == "CHANGELOG.md"
}

// RunSkillPortabilityCheck walks every Markdown file under skills/ rooted at
// Root and emits a finding for each line that embeds a non-portable
// host-specific binary path. Returns a non-error result even when findings
// exist — the caller inspects len(result.Findings) to decide the gate exit
// code.
func RunSkillPortabilityCheck(_ context.Context, opts SkillPortabilityOptions) (*SkillPortabilityResult, error) {
	root := opts.Root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve root: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a readable directory", root)
	}
	root = abs

	result := &SkillPortabilityResult{
		V:        1,
		Root:     root,
		Findings: []SkillPortabilityFinding{},
		Warnings: []string{},
	}

	skillsDir := opts.SkillsDir
	if skillsDir == "" {
		skillsDir = filepath.Join(root, "skills")
	} else if !filepath.IsAbs(skillsDir) {
		skillsDir = filepath.Join(root, skillsDir)
	}
	hooksDir := filepath.Join(root, "claude-plugins", "nightgauge", "hooks")
	if info, statErr := os.Stat(skillsDir); statErr != nil || !info.IsDir() {
		// No skills/ directory — nothing to check (e.g. a non-nightgauge
		// repo). Report zero files rather than erroring.
		return result, nil
	}

	var files []string
	walkErr := filepath.WalkDir(skillsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("walk %s: %v", path, err))
			return nil
		}
		if d.IsDir() {
			// Skip ephemeral, gitignored runtime dirs (e.g. .claude/agent-memory).
			// They are not skill source. Excluding them also keeps the Go scan in
			// step with the shell mirror's `rg`, which honors .gitignore (#4029).
			if d.Name() == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", skillsDir, walkErr)
	}
	sort.Strings(files)
	result.FilesChecked = len(files)

	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", path, readErr))
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		treeRel, treeErr := filepath.Rel(skillsDir, path)
		if treeErr != nil {
			treeRel = rel
		}
		treeRel = filepath.ToSlash(treeRel)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			for _, check := range lineChecks(treeRel, line, hooksDir) {
				result.Findings = append(result.Findings, finding(rel, i+1, check, line))
			}
			if vscodeExtensionPathRE.MatchString(line) {
				result.Findings = append(result.Findings, finding(rel, i+1, CheckVSCodeBinaryPath, line))
			}
			if stopHookRE.MatchString(line) && strings.HasSuffix(path, "SKILL.md") {
				result.Findings = append(result.Findings, finding(rel, i+1, CheckStopHook, line))
			}
		}
		for _, block := range fencedBashBlocks(lines) {
			body := strings.Join(block.lines, "\n")
			startOffset := firstMatchingLine(block.lines, cascadeStartRE)
			if startOffset < 0 {
				continue
			}
			var missing []string
			for _, rung := range cascadeRungs {
				if !rung.pattern.MatchString(body) {
					missing = append(missing, rung.name)
				}
			}
			if len(missing) == 0 {
				continue
			}
			line := block.lines[startOffset]
			match := fmt.Sprintf("%s [bash block line %d missing: %s]", strings.TrimSpace(line), block.openLine, strings.Join(missing, ", "))
			result.Findings = append(result.Findings, finding(rel, block.openLine+startOffset+1, CheckTruncatedBinaryCascade, match))
		}
	}

	return result, nil
}

// lineChecks returns the #2764 checks one line fails. treeRel is the file's
// path relative to the scanned skill tree.
func lineChecks(treeRel, line, hooksDir string) []string {
	if historyFile(treeRel) || treeRel == "README.md" {
		return nil
	}
	var failed []string
	for _, m := range hooksDirRefRE.FindAllStringSubmatch(line, -1) {
		name := strings.TrimRight(m[1], ".")
		if !hookFileExists(hooksDir, name) {
			failed = append(failed, CheckHooksDirReference)
			break
		}
	}
	if sourceRepoPathRE.MatchString(line) {
		if _, ok := sourceRepoPathExempt[treeRel][strings.TrimSpace(line)]; !ok {
			failed = append(failed, CheckSourceRepoPath)
		}
	}
	if claudeSkillFlagRE.MatchString(line) {
		failed = append(failed, CheckClaudeSkillFlag)
	}
	return failed
}

// hookFileExists reports whether name is a regular file in the plugin's hooks
// directory or its lib/ subdirectory. Skills set HOOKS_DIR to either.
func hookFileExists(hooksDir, name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}
	for _, dir := range []string{hooksDir, filepath.Join(hooksDir, "lib")} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

type fencedBashBlock struct {
	openLine int // 1-based fence line
	lines    []string
}

// fencedBashBlocks extracts bash/sh/shell Markdown fences with their original
// line coordinates. Unclosed blocks extend to EOF so a malformed document
// cannot make a truncated cascade disappear from the gate.
func fencedBashBlocks(lines []string) []fencedBashBlock {
	var blocks []fencedBashBlock
	for i := 0; i < len(lines); i++ {
		match := bashFenceOpenRE.FindStringSubmatch(lines[i])
		if len(match) == 0 || !uniformFence(match[1]) {
			continue
		}
		marker := match[1]
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if closesFence(lines[j], marker[0], len(marker)) {
				end = j
				break
			}
		}
		blocks = append(blocks, fencedBashBlock{openLine: i + 1, lines: lines[i+1 : end]})
		if end < len(lines) {
			i = end
		} else {
			break
		}
	}
	return blocks
}

func uniformFence(marker string) bool {
	if marker == "" {
		return false
	}
	for i := 1; i < len(marker); i++ {
		if marker[i] != marker[0] {
			return false
		}
	}
	return true
}

func closesFence(line string, marker byte, minLen int) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < minLen {
		return false
	}
	for i := range len(trimmed) {
		if trimmed[i] != marker {
			return false
		}
	}
	return true
}

func firstMatchingLine(lines []string, pattern *regexp.Regexp) int {
	for i, line := range lines {
		if pattern.MatchString(line) {
			return i
		}
	}
	return -1
}

// finding builds a SkillPortabilityFinding with the match line trimmed to a
// bounded length.
func finding(rel string, line int, check, match string) SkillPortabilityFinding {
	trimmed := strings.TrimSpace(match)
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "…"
	}
	return SkillPortabilityFinding{SkillFile: rel, Line: line, Check: check, Match: trimmed}
}
