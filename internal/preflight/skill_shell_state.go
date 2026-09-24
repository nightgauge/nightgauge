// Skill shell-state gate (#1932).
//
// A skill presents its phases as separate fenced shell blocks, and an agent
// runs each block in its own `Bash` tool call. Each call is a fresh process,
// so a variable assigned in one block is gone in the next. A block that reads
// a pipeline identifier ($ISSUE_NUMBER, $BRANCH, $BRANCH_NAME, $REPO) it did
// not derive itself reads an empty string whenever it runs alone, and the
// `${VAR:-}` habit turns that empty string into a valid blank. #1919 lost a
// branch that way; #1927's feature-validate recorded quality-gate metrics
// against `--issue ""` and its gate reported the done work as skipped.
//
// This gate fails such a block so the defect cannot be reintroduced:
//
//	underived_identifier  a shell block reads a pipeline identifier before
//	                      (or without) assigning it in that same block.
//	blank_fallback        a shell block expands a pipeline identifier with an
//	                      empty default (`${ISSUE_NUMBER:-}`), which turns a
//	                      missing value into a blank instead of a failure.
//
// Scope is the stage skills the orchestrator renders
// (skillrender.StageSkillDirs) plus skills/_shared/, which those renders
// inline. Those are the skills that run with NIGHTGAUGE_ISSUE_NUMBER and
// NIGHTGAUGE_REPO in their environment, so every block has a durable source
// to derive from.
// Only fenced bash/sh/shell blocks are read; prose and output examples do not
// run. The derivation pattern is documented in skills/README.md.
//
// Schema version 1 — field names (v, root, files_checked, findings, warnings)
// are stable and consumed by callers via fixed jq paths.
package preflight

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// Shell-state check identifiers used in the Check field of a finding.
const (
	CheckUnderivedIdentifier = "underived_identifier"
	CheckBlankFallback       = "blank_fallback"
)

// ShellStateIdentifiers are the pipeline identifiers a stage skill passes
// between phases. Each must be derived in every block that reads it.
var ShellStateIdentifiers = []string{"ISSUE_NUMBER", "BRANCH", "BRANCH_NAME", "REPO"}

// SkillShellStateResult is the stable JSON output schema for
// `nightgauge preflight skill-shell-state`.
type SkillShellStateResult struct {
	V            int               `json:"v"`             // schema version, always 1
	Root         string            `json:"root"`          // absolute path
	FilesChecked int               `json:"files_checked"` // count of .md files inspected
	Findings     []SkillShellState `json:"findings"`      // one entry per identifier per block
	Warnings     []string          `json:"warnings"`      // non-fatal issues (read errors, etc.)
}

// SkillShellState describes a single finding.
type SkillShellState struct {
	Check      string `json:"check"`      // underived_identifier or blank_fallback
	File       string `json:"file"`       // path relative to Root
	Line       int    `json:"line"`       // 1-based line of the offending read
	Identifier string `json:"identifier"` // e.g. ISSUE_NUMBER
	Match      string `json:"match"`      // offending line content (trimmed)
}

// SkillShellStateOptions controls a single gate run.
type SkillShellStateOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
}

// shellFenceRE matches the opening fence of a shell block, indented or not.
var shellFenceRE = regexp.MustCompile("^\\s*```(?:bash|sh|shell)\\s*$")

// anyFenceRE matches any fence line (opening with a language, or closing).
var anyFenceRE = regexp.MustCompile("^\\s*```")

type identifierPatterns struct {
	name   string
	read   *regexp.Regexp // $NAME or ${NAME...}
	assign *regexp.Regexp // NAME=..., export NAME=...
	bind   *regexp.Regexp // read ... NAME, for NAME in
	blank  *regexp.Regexp // ${NAME:-} or ${NAME-}
}

func compileIdentifier(name string) identifierPatterns {
	q := regexp.QuoteMeta(name)
	return identifierPatterns{
		name: name,
		read: regexp.MustCompile(`\$(?:\{` + q + `(?:[^A-Za-z0-9_]|$)|` + q + `(?:[^A-Za-z0-9_]|$))`),
		assign: regexp.MustCompile(`(?:^|[\s;&|(])(?:export\s+|local\s+|readonly\s+|declare\s+)?` +
			q + `=`),
		bind:  regexp.MustCompile(`\bread\b[^\n#]*\s` + q + `\b|\bfor\s+` + q + `\s+in\b`),
		blank: regexp.MustCompile(`\$\{` + q + `:?-\}`),
	}
}

var shellStatePatterns = func() []identifierPatterns {
	out := make([]identifierPatterns, 0, len(ShellStateIdentifiers))
	for _, n := range ShellStateIdentifiers {
		out = append(out, compileIdentifier(n))
	}
	return out
}()

// RunSkillShellStateCheck walks the stage skills and skills/_shared/ and
// emits a finding for each shell block that reads a pipeline identifier it
// did not derive. Returns a non-error result when findings exist; the caller
// inspects len(result.Findings) to decide the exit code.
func RunSkillShellStateCheck(_ context.Context, opts SkillShellStateOptions) (*SkillShellStateResult, error) {
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

	result := &SkillShellStateResult{
		V:        1,
		Root:     root,
		Findings: []SkillShellState{},
		Warnings: []string{},
	}

	dirs := []string{"_shared"}
	for _, d := range skillrender.StageSkillDirs {
		dirs = append(dirs, d)
	}
	var files []string
	for _, d := range dirs {
		base := filepath.Join(root, "skills", d)
		if _, statErr := os.Stat(base); statErr != nil {
			continue
		}
		walkErr := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !e.IsDir() && filepath.Ext(path) == ".md" {
				files = append(files, path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk %s: %w", base, walkErr)
		}
	}
	sort.Strings(files)
	result.FilesChecked = len(files)

	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		findings, err := scanShellState(path, rel)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		result.Findings = append(result.Findings, findings...)
	}
	return result, nil
}

type shellLine struct {
	n    int
	text string
}

func scanShellState(path, rel string) ([]SkillShellState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		findings []SkillShellState
		inShell  bool
		inOther  bool
		block    []shellLine
		n        int
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
		line := sc.Text()
		switch {
		case inShell:
			if anyFenceRE.MatchString(line) && strings.TrimSpace(line) == "```" {
				findings = append(findings, checkShellBlock(rel, block)...)
				inShell, block = false, nil
				continue
			}
			block = append(block, shellLine{n: n, text: line})
		case inOther:
			if strings.TrimSpace(line) == "```" {
				inOther = false
			}
		case shellFenceRE.MatchString(line):
			inShell = true
		case anyFenceRE.MatchString(line):
			inOther = true
		}
	}
	return findings, sc.Err()
}

// checkShellBlock applies both checks to one block. A block derives an
// identifier when an assignment to it appears on a line before its first
// read. A self-referencing assignment (`ISSUE_NUMBER="${ISSUE_NUMBER:-…}"`)
// is a read of the inherited value, not a derivation; `read` and `for`
// bind the name before the rest of their line reads it.
func checkShellBlock(rel string, block []shellLine) []SkillShellState {
	var out []SkillShellState
	for _, id := range shellStatePatterns {
		derived := false
		reported := false
		for _, l := range block {
			code := stripShellComment(l.text)
			if code == "" {
				continue
			}
			if id.blank.MatchString(code) {
				out = append(out, SkillShellState{
					Check: CheckBlankFallback, File: rel, Line: l.n,
					Identifier: id.name, Match: strings.TrimSpace(l.text),
				})
			}
			reads := id.read.MatchString(code)
			if !derived && (id.bind.MatchString(code) || (id.assign.MatchString(code) && !reads)) {
				derived = true
				continue
			}
			if reads && !derived && !reported {
				out = append(out, SkillShellState{
					Check: CheckUnderivedIdentifier, File: rel, Line: l.n,
					Identifier: id.name, Match: strings.TrimSpace(l.text),
				})
				reported = true
			}
		}
	}
	return out
}

// stripShellComment drops a whole-line comment. Trailing comments are kept:
// telling a `#` that starts a comment from one inside a string or `${#x}`
// needs a shell parser, and a false read in a trailing comment is rare.
func stripShellComment(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "#") {
		return ""
	}
	return t
}
