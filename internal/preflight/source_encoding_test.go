package preflight

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSourceFilesAreCleanUTF8 is a standing guard against mojibake — text that
// was already UTF-8, got read back as Latin-1, and was re-encoded as UTF-8.
//
// This is not hypothetical. ff2226fa (the #289 fix) landed 16 double-encoded
// characters into internal/orchestrator/autonomous.go and
// failure_handler.go, and they survived 25 commits because nothing looked. Two
// of them were not comments but operator-facing runtime strings:
//
//	"permission-denied (retryable, harness fault) â will retry after backoff â "
//
// so every attention card and log line that path produced shipped a corrupted
// character to a human. gofmt, go vet, and the whole test suite are all blind
// to it: the bytes are valid UTF-8, they just decode to the wrong characters.
//
// The fingerprint is a C1 control (U+0080–U+009F). An em-dash is E2 80 94; run
// that through the Latin-1 round trip and it becomes C3 A2 C2 80 C2 94, which
// decodes to 'â' + U+0080 + U+0094 — two C1 controls. Real source never
// contains a C1 control, so their presence is proof of the round trip rather
// than a heuristic about it.
//
// If this fails, do not add an exemption or hand-retype the line. Repair the
// bytes: decode the mojibake run as UTF-8, encode it Latin-1, decode it UTF-8
// again, and write that back.
func TestSourceFilesAreCleanUTF8(t *testing.T) {
	var offenders []string

	for _, path := range sourceFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel := relToRepo(t, path)

		if !utf8.Valid(data) {
			offenders = append(offenders, rel+": not valid UTF-8")
			continue
		}
		for i, r := range string(data) {
			if r >= 0x80 && r <= 0x9F {
				offenders = append(offenders, describeMojibake(rel, data, i, r))
				break // one report per file is enough to send someone to the file
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf("found %d file(s) containing double-encoded UTF-8:\n  %s\n\n"+
			"A C1 control character is the signature of text that was UTF-8, read as "+
			"Latin-1, and re-encoded. Repair the bytes (utf-8 decode → latin-1 encode → "+
			"utf-8 decode); do not retype the line by hand and do not exempt the file. "+
			"See ff2226fa, where this shipped corrupted characters in operator-facing "+
			"attention cards for 25 commits (#289).",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// literalUnicodeEscapeInComment matches a `\uXXXX` escape sitting in Go comment
// prose. Inside a string literal that is ordinary Go; inside a comment it is a
// character that lost its encoding somewhere and was written back as its own
// escape sequence, which is how skill_anti_patterns_test.go:315 came to read
// "pattern — must not be flagged".
var literalUnicodeEscapeInComment = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)

// TestGoCommentsHaveNoLiteralUnicodeEscapes catches the other half of the same
// failure: rather than corrupting the bytes, a tool wrote the character out as
// its literal escape text. It is invisible to every encoding check because the
// result is pure ASCII — and it renders as `—` to whoever reads the code.
//
// Only comments are inspected. `"—"` in a string literal is legitimate Go.
func TestGoCommentsHaveNoLiteralUnicodeEscapes(t *testing.T) {
	var offenders []string

	for _, path := range sourceFiles(t) {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		for _, hit := range literalEscapesInGoComments(path) {
			offenders = append(offenders, relToRepo(t, path)+":"+hit)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("found %d Go comment(s) containing a literal \\uXXXX escape:\n  %s\n\n"+
			"Write the character itself. In a comment the escape is not syntax — it is "+
			"what a character looks like after a tool lost its encoding.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// literalEscapesInGoComments returns "<line> contains <escape>" for every
// `\uXXXX` found in a comment of the Go file at path. Comments are reached
// through go/parser rather than by scanning lines, which is what keeps string
// literals — where the escape is ordinary Go — out of the result.
func literalEscapesInGoComments(path string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil // unparseable generated file — not this test's business
	}
	var hits []string
	for _, group := range file.Comments {
		for _, c := range group.List {
			if m := literalUnicodeEscapeInComment.FindString(c.Text); m != "" {
				hits = append(hits, fmt.Sprintf("%d contains %s", fset.Position(c.Pos()).Line, m))
			}
		}
	}
	return hits
}

// TestLiteralEscapeScanIgnoresStringLiterals pins the one distinction the guard
// above rests on. No file in the tree currently carries a `\uXXXX` string
// literal, so without this the "comments only" scoping is asserted by nothing —
// and the first legitimate "é" to land would look like a guard defect and
// invite someone to weaken the check instead of trusting it.
func TestLiteralEscapeScanIgnoresStringLiterals(t *testing.T) {
	src := "package demo\n" +
		"// prose with a lost em-dash \\u2014 here\n" +
		"const A = \"\\u00e9 legitimate escape\"\n" +
		"const B = `\\u2014 in a raw literal`\n"

	path := filepath.Join(t.TempDir(), "demo.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	hits := literalEscapesInGoComments(path)
	if len(hits) != 1 {
		t.Fatalf("hits = %d (%v), want exactly 1 — the comment only", len(hits), hits)
	}
	wantEscape := "\\" + "u2014" // built, not written, so no tool can normalise it away
	if !strings.Contains(hits[0], wantEscape) || !strings.HasPrefix(hits[0], "2 ") {
		t.Errorf("hit = %q, want %s on line 2 (the comment)", hits[0], wantEscape)
	}
}

// describeMojibake reports the offending run with enough context to find it,
// and — when the Latin-1 round trip reverses cleanly — what it was meant to say.
func describeMojibake(rel string, data []byte, byteOffset int, r rune) string {
	line := 1 + strings.Count(string(data[:byteOffset]), "\n")
	desc := fmt.Sprintf("%s:%d: U+%04X (C1 control)", rel, line, r)

	start := byteOffset
	for start > 0 && start > byteOffset-8 {
		start--
	}
	end := byteOffset + 8
	if end > len(data) {
		end = len(data)
	}
	if repaired, ok := reverseLatin1RoundTrip(string(data[start:end])); ok {
		desc += " — reads as " + strconv.Quote(repaired)
	}
	return desc
}

// reverseLatin1RoundTrip undoes one UTF-8 → Latin-1 → UTF-8 double encoding.
// It reports false when the run does not reverse to valid UTF-8, which means
// the corruption is something other than this specific round trip.
func reverseLatin1RoundTrip(s string) (string, bool) {
	raw := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return "", false
		}
		raw = append(raw, byte(r))
	}
	if !utf8.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

// sourceFiles lists the tracked text sources this guard governs.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)

	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "bin", "dist", "out", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("walked the repo and found no .go or .md files — the walk is broken, " +
			"and an empty file set would make this guard pass unconditionally")
	}
	return out
}

func relToRepo(t *testing.T, path string) string {
	t.Helper()
	rel, err := filepath.Rel(repoRoot(t), path)
	if err != nil {
		return path
	}
	return rel
}

// repoRoot walks up from the test's working directory to the directory holding
// go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's working directory")
		}
		dir = parent
	}
}
