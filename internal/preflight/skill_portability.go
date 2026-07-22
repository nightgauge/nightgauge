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

	// CheckTruncatedBinaryCascade flags a binary-discovery cascade that lost
	// rungs relative to the canonical PREFLIGHT.md block — a file that starts
	// the cascade (BINARY="${NIGHTGAUGE_BIN…) but never reaches the final
	// ~/go/bin fallback drifted from the contract (#55, spike #33 D5).
	CheckTruncatedBinaryCascade = "truncated_binary_cascade"
)

// SkillPortabilityOptions controls a single linter run.
type SkillPortabilityOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
}

// vscodeExtensionPathRE matches a reference to the VSCode-extension binary
// directory. Anchored on `.vscode/extensions` followed (anywhere later on the
// line) by the nightgauge extension id — this catches the historical glob
// `$HOME/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`
// and any restatement of it, while not tripping on the unrelated
// `<root>/.vscode/` workspace-manifest directory used by workspace-init skills.
// Case-insensitive (?i) for defense-in-depth against a mis-cased extension id.
var vscodeExtensionPathRE = regexp.MustCompile(`(?i)\.vscode/extensions/nightgauge`)

// stopHookRE matches the `hooks:` frontmatter key at column 0. Skill bodies
// never legitimately start a line with a bare `hooks:`; the only historical
// occurrences were the Claude-only Stop-hook completion gates removed in #55.
var stopHookRE = regexp.MustCompile(`^hooks:\s*$`)

// cascadeStartRE / cascadeFinalRungRE detect the PREFLIGHT binary-discovery
// cascade and its mandatory final rung. Any file that opens the cascade must
// also carry the ~/go/bin fallback, or it has drifted from PREFLIGHT.md.
var (
	cascadeStartRE     = regexp.MustCompile(`BINARY="\$\{NIGHTGAUGE_BIN`)
	cascadeFinalRungRE = regexp.MustCompile(`go/bin/nightgauge`)
)

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

	skillsDir := filepath.Join(root, "skills")
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
		lines := strings.Split(string(data), "\n")
		fileHasFinalRung := cascadeFinalRungRE.Match(data)
		cascadeFlagged := false
		for i, line := range lines {
			if vscodeExtensionPathRE.MatchString(line) {
				result.Findings = append(result.Findings, finding(rel, i+1, CheckVSCodeBinaryPath, line))
			}
			if stopHookRE.MatchString(line) && strings.HasSuffix(path, "SKILL.md") {
				result.Findings = append(result.Findings, finding(rel, i+1, CheckStopHook, line))
			}
			if !cascadeFlagged && !fileHasFinalRung && cascadeStartRE.MatchString(line) {
				cascadeFlagged = true // one finding per file is enough
				result.Findings = append(result.Findings, finding(rel, i+1, CheckTruncatedBinaryCascade, line))
			}
		}
	}

	return result, nil
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
