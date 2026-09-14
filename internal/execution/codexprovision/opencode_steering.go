package codexprovision

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// OpenCode repository steering (ADR-022 § 8, § 11, #1626).
//
// Observed on opencode 1.18.30: the switch that keeps a repository's
// opencode.json out of a run also hides its AGENTS.md and CLAUDE.md,
// OPENCODE_DISABLE_CLAUDE_CODE_PROMPT drops the CLAUDE.md fallback, and an
// `@path` import is never followed. An `instructions` entry holding an
// absolute path is loaded in every one of those configurations. So an
// OpenCode stage is handed its repository's steering as instructions entries,
// and OpenCode is left to discover none of it: the file repositorySteering
// picks, the rule Codex and Gemini steering follow, and every file it imports.
//
// Imports resolve the way Claude Code resolves them in a CLAUDE.md, relative
// to the importing file, and are confined to the worktree: a URL, a path in
// the home directory, an absolute path, one whose `..` leaves the worktree and
// a symbolic link out of it are each left out with a warning. Every file
// Nightgauge reads for the stage, the baseline steering's sources included,
// is read through a worktreeReader, so none of the steering it gives OpenCode
// comes from outside the repository or from anywhere remote. Imports are
// followed openCodeImportDepth deep, and a file already given is never given
// twice, so a cycle ends. An `@name` that names no file in the worktree, such
// as a person's handle, is ignored, as Claude Code ignores it.

// openCodeImportDepth is how many imports deep the steering is followed from
// the repository's steering file.
const openCodeImportDepth = 3

// openCodeImportRE finds an `@path` import: an @ at the start of a line or
// after whitespace, then the path, in which `\ ` is a space.
var openCodeImportRE = regexp.MustCompile(`(?:^|\s)@((?:[^\s\\]|\\ )+)`)

// urlSchemeRE is a URL's scheme and `://`.
var urlSchemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// openCodeInstructionNameRE is the shape of the last element of every
// instructions entry. OpenCode 1.18.30 loads an absolute entry by globbing
// its last element in its directory (read from its bundled source), so a name
// with a pattern character could load files the entry does not name.
var openCodeInstructionNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// OpenCodeInstructionPathError says why path cannot be an instructions entry,
// or returns nil. An entry must be an absolute, clean path whose last element
// OpenCode cannot read as a pattern (openCodeInstructionNameRE), and it holds
// no brace, which OpenCode's {env:...} and {file:...} substitution reads, and
// no control character. adapters.BuildOpenCodeConfig applies the same check
// to every entry it writes.
func OpenCodeInstructionPathError(path string) error {
	switch {
	case urlSchemeRE.MatchString(path):
		return fmt.Errorf("%q is a URL, and an OpenCode run is never given a remote instruction", path)
	case !filepath.IsAbs(path) || filepath.Clean(path) != path:
		return fmt.Errorf("%q is not an absolute, clean path", path)
	case strings.ContainsAny(path, "{}") || strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return fmt.Errorf("%q holds a brace or a control character, which OpenCode would read as a config reference", path)
	case !openCodeInstructionNameRE.MatchString(filepath.Base(path)):
		return fmt.Errorf("the name %q holds a character OpenCode would read as a file pattern (an instructions entry's name may hold only letters, digits, '.', '_' and '-')", filepath.Base(path))
	}
	return nil
}

// within reports whether path is root or lies under it. Both are absolute and
// clean.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// importTokens returns the `@path` imports of a markdown document, in order,
// outside fenced code blocks and inline code spans.
func importTokens(content string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		if summaryFenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range openCodeImportRE.FindAllStringSubmatch(withoutCodeSpans(line), -1) {
			if p := strings.ReplaceAll(m[1], `\ `, " "); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// withoutCodeSpans blanks every inline code span of line: a run of backticks,
// up to the next run of exactly as many.
func withoutCodeSpans(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		open := i
		for i < len(line) && line[i] == '`' {
			i++
		}
		n := i - open
		closed := -1
		for k := i; k < len(line); {
			if line[k] != '`' {
				k++
				continue
			}
			run := k
			for k < len(line) && line[k] == '`' {
				k++
			}
			if k-run == n {
				closed = k
				break
			}
		}
		if closed < 0 {
			b.WriteString(line[open:i])
			continue
		}
		b.WriteString(strings.Repeat(" ", closed-open))
		i = closed
	}
	return b.String()
}

// steeringWalk collects a repository's steering files as instructions entries.
type steeringWalk struct {
	root     string // the worktree, symbolic links resolved
	read     readFunc
	seen     map[string]bool
	paths    []string
	warnings []string
}

func (w *steeringWalk) rel(path string) string {
	if r, err := filepath.Rel(w.root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

func (w *steeringWalk) warnf(format string, args ...any) {
	w.warnings = append(w.warnings, "steering: "+fmt.Sprintf(format, args...))
}

// openCodeInstructions returns the instructions entries of the repository's
// own steering in root, a worktree whose symbolic links are resolved: the
// file repositorySteering picks, then every file it imports, depth first, in
// the order they appear. Each is an absolute path inside root with its
// symbolic links resolved. Every file is read through read, a worktreeReader's
// in production, so a steering file it refuses is passed over as absent, and
// the rule falls through to CLAUDE.md; the reader's own warnings name it.
// These warnings say what else was left out.
func openCodeInstructions(root string, read readFunc) (paths, warnings []string) {
	w := &steeringWalk{root: root, read: read, seen: map[string]bool{}}
	name, _ := repositorySteering(root, read)
	if name == "" {
		return nil, nil
	}
	file, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil || !within(root, file) {
		// read has just found it inside the worktree; it changed since.
		w.warnf("%s changed while it was read, so it is not named in instructions", name)
		return nil, w.warnings
	}
	if err := OpenCodeInstructionPathError(file); err != nil {
		w.warnf("%s is not given to OpenCode: %v", name, err)
		return nil, w.warnings
	}
	raw, _ := read(file)
	scan := stripManagedSteeringBlock(raw)
	if name == "CLAUDE.md" {
		// The rule picked CLAUDE.md because AGENTS.md has no content of its
		// own, so its adapter import brings nothing.
		scan = stripLeadingAgentsImport(raw)
	}
	w.seen[file] = true
	w.paths = append(w.paths, file)
	w.visit(file, scan, 0)
	return w.paths, w.warnings
}

// visit adds the files content imports, which file at depth holds, and
// theirs in turn.
func (w *steeringWalk) visit(file, content string, depth int) {
	from := w.rel(file)
	for _, tok := range importTokens(content) {
		shown := tok
		if len(shown) > 200 {
			shown = shown[:200] + "..."
		}
		switch {
		case urlSchemeRE.MatchString(tok):
			w.warnf("%s imports %q, a URL; an OpenCode run is never given a remote instruction", from, "@"+shown)
			continue
		case tok == "~" || strings.HasPrefix(tok, "~/"):
			w.warnf("%s imports %q, a path in the home directory; only files inside the worktree are given to OpenCode", from, "@"+shown)
			continue
		case filepath.IsAbs(tok):
			w.warnf("%s imports %q, an absolute path; only files inside the worktree are given to OpenCode", from, "@"+shown)
			continue
		}
		joined := filepath.Join(filepath.Dir(file), tok)
		if !within(w.root, joined) {
			w.warnf("%s imports %q, which leaves the worktree; only files inside it are given to OpenCode", from, "@"+shown)
			continue
		}
		if _, err := os.Lstat(joined); err != nil {
			continue // names no file: a handle or a mention, not an import
		}
		target, err := filepath.EvalSymlinks(joined)
		if err != nil {
			continue
		}
		if !within(w.root, target) {
			w.warnf("%s imports %q, a symbolic link to a file outside the worktree; only files inside it are given to OpenCode", from, "@"+shown)
			continue
		}
		if fi, err := os.Stat(target); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if w.seen[target] {
			continue // given already: a cycle, or a file imported twice
		}
		if depth+1 > openCodeImportDepth {
			w.warnf("%s imports %q, more than %d imports deep, so it is not followed", from, "@"+shown, openCodeImportDepth)
			continue
		}
		if err := OpenCodeInstructionPathError(target); err != nil {
			w.warnf("%s imports %q, which is not given to OpenCode: %v", from, "@"+shown, err)
			continue
		}
		w.seen[target] = true
		raw, _ := w.read(target)
		if strings.TrimSpace(StripManagedSteering(raw)) == "" {
			continue // nothing of the repository's own to give
		}
		w.paths = append(w.paths, target)
		w.visit(target, raw, depth+1)
	}
}
