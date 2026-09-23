package scaffold

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
)

// ErrNotGitWorkTree reports that a directory is not inside a git work tree,
// so there is no info/exclude to write and nothing git would ignore anyway.
var ErrNotGitWorkTree = errors.New("not inside a git work tree")

// IsTracked reports whether relPath (relative to dir) is tracked by git. It is
// false outside a git work tree.
func IsTracked(dir, relPath string) bool {
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "--", relPath)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run() == nil
}

// InsideWorkTree reports whether dir is inside a git work tree.
func InsideWorkTree(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// LocalExcludePath resolves the info/exclude file git reads for dir.
//
// It asks git (`rev-parse --git-path info/exclude`) rather than joining
// ".git/info/exclude": in a linked worktree `.git` is a file, and info/ lives
// in the common dir shared by every worktree of the repository, which is
// where git reads it. The answer must be exactly <git-common-dir>/info/exclude
// and neither info/ nor info/exclude may be a symlink (git resolves a
// symlinked exclude to its target), so the rules can never be redirected
// outside the repository's git dir. Outside a git work tree it returns
// ErrNotGitWorkTree.
func LocalExcludePath(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute",
		"--git-common-dir", "--git-path", "info/exclude")
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", ErrNotGitWorkTree
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || !filepath.IsAbs(lines[0]) || !filepath.IsAbs(lines[1]) {
		return "", fmt.Errorf("git resolved info/exclude to %q, not an absolute path pair", lines)
	}
	commonDir, p := filepath.Clean(lines[0]), filepath.Clean(lines[1])
	want := filepath.Join(commonDir, "info", "exclude")
	for _, candidate := range []string{filepath.Dir(want), want} {
		info, lerr := os.Lstat(candidate)
		if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlinked %s", candidate)
		}
		if lerr != nil && !os.IsNotExist(lerr) {
			return "", fmt.Errorf("inspect %s: %w", candidate, lerr)
		}
	}
	if p != want {
		return "", fmt.Errorf("refusing info/exclude at %s: outside the git dir %s", p, commonDir)
	}
	return p, nil
}

// excludeBlockBegin and excludeBlockEnd delimit a block this package owns in
// info/exclude. The text is byte-identical to the extension's
// writeLocalExcludeBlock (packages/nightgauge-vscode/src/utils/
// localGitExclude.ts), so either writer recognises and replaces the other's
// block instead of appending a second copy.
func excludeBlockBegin(id string) string {
	return "# nightgauge:begin " + id + " (managed per machine; never committed, #1875)"
}

func excludeBlockEnd(id string) string { return "# nightgauge:end " + id }

// WriteExcludeBlock replaces (or appends) the block named id in the
// repository's info/exclude with patterns. Lines outside the block are never
// touched. It returns true when the file changed and false when the block was
// already current; outside a git work tree it returns ErrNotGitWorkTree.
func WriteExcludeBlock(dir, id string, patterns []string) (bool, error) {
	excludePath, err := LocalExcludePath(dir)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", excludePath, err)
	}
	existing := string(data)
	next := replaceBlock(existing, id, patterns)
	if next == existing {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(excludePath), err)
	}
	if err := atomicfile.Write(excludePath, []byte(next), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", excludePath, err)
	}
	return true, nil
}

// replaceBlock is the pure half of WriteExcludeBlock, a line-for-line port of
// the extension's algorithm so both produce the same file from the same input.
func replaceBlock(existing, id string, patterns []string) string {
	begin, end := excludeBlockBegin(id), excludeBlockEnd(id)
	block := strings.Join(append(append([]string{begin}, patterns...), end), "\n") + "\n"

	lines := strings.Split(existing, "\n")
	startIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "# nightgauge:begin "+id+" ") {
			startIdx = i
			break
		}
	}
	endIdx := -1
	for i, l := range lines {
		if i > startIdx && l == end {
			endIdx = i
			break
		}
	}
	if startIdx >= 0 && endIdx > startIdx {
		before := strings.Join(lines[:startIdx], "\n")
		after := strings.Join(lines[endIdx+1:], "\n")
		if before != "" {
			before += "\n"
		}
		return before + block + after
	}
	base := existing
	if base != "" && !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	return base + block
}

// ToRootPatterns rewrites the rules of a .gitignore living in dirRel
// (relative to the repository root, e.g. ".nightgauge") so they mean the same
// thing from the repository root, where info/exclude is evaluated. A port of
// the extension's toRootPatterns: a pattern with a slash anywhere but at its
// end is anchored to the file's directory; one without matches at any depth
// below it. Negation and a trailing directory slash carry over.
func ToRootPatterns(dirRel, content string) []string {
	base := strings.Trim(dirRel, "/")
	var out []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		pattern := strings.TrimPrefix(line, "!")
		dirOnly := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		anchored := strings.Contains(pattern, "/")
		pattern = strings.TrimLeft(pattern, "/")
		rooted := "/" + base + "/**/" + pattern
		if anchored {
			rooted = "/" + base + "/" + pattern
		}
		if negated {
			rooted = "!" + rooted
		}
		if dirOnly {
			rooted += "/"
		}
		out = append(out, rooted)
	}
	return out
}
