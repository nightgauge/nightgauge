// Command release-text-pattern-check fails a release when any text file it
// ships matches a YARA text pattern that has already flagged a Nightgauge
// release.
//
// v0.8.4 was held because InQuest's Microsoft_Excel_Hidden_Macrosheet rule
// matched `displayState = "hidden"` in the bundled extension.cjs (#3102). The
// patterns live in one data file, configs/release-text-patterns.yaml, so the
// next rule that hits a release is one entry, not new code.
//
//	release-text-pattern-check --patterns FILE PATH...
//
// Each PATH is a .tar.gz/.tgz archive, a .vsix/.zip archive, or a raw file.
// Every regular entry of an archive is read. An entry is text when its first
// 8 KiB hold no NUL byte and it is not an ELF or Mach-O binary; every text
// entry is matched line by line against every pattern.
//
// Exit codes:
//
//	0  at least one text file was checked and none matched
//	1  at least one text file matched a pattern
//	2  nothing was asserted: bad usage, an unreadable path or patterns file,
//	   an empty or invalid pattern list, or no text file checked at all (a
//	   HARNESS ERROR line says which)
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const record = "docs/RELEASE_BINARIES.md"

// sniffLen is how much of an entry decides whether it is text.
const sniffLen = 8 << 10

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// pattern is one entry of the data file.
type pattern struct {
	Rule    string `yaml:"rule"`
	Source  string `yaml:"source"`
	Release string `yaml:"release"`
	Pattern string `yaml:"pattern"`
	re      *regexp.Regexp
}

type patternFile struct {
	Patterns []pattern `yaml:"patterns"`
}

// loadPatterns reads and compiles the data file. Every field is required:
// a pattern without its rule and release cannot say why it blocks.
func loadPatterns(path string) ([]pattern, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pf patternFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&pf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(pf.Patterns) == 0 {
		return nil, fmt.Errorf("%s: no patterns", path)
	}
	for i := range pf.Patterns {
		p := &pf.Patterns[i]
		if p.Rule == "" || p.Source == "" || p.Release == "" || p.Pattern == "" {
			return nil, fmt.Errorf("%s: pattern %d needs rule, source, release and pattern", path, i)
		}
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: pattern %d (%s): %w", path, i, p.Rule, err)
		}
		p.re = re
	}
	return pf.Patterns, nil
}

// tally accumulates the outcome across every path.
type tally struct {
	checked int
	matches int
	harness bool
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release-text-pattern-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	patternsPath := fs.String("patterns", "", "YARA text-pattern data file")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "HARNESS ERROR: usage: release-text-pattern-check --patterns FILE PATH...")
		return 2
	}
	if *patternsPath == "" || fs.NArg() == 0 {
		fmt.Fprintln(stderr, "HARNESS ERROR: usage: release-text-pattern-check --patterns FILE PATH...")
		return 2
	}
	pats, err := loadPatterns(*patternsPath)
	if err != nil {
		fmt.Fprintf(stderr, "HARNESS ERROR: %v\n", err)
		return 2
	}
	var t tally
	for _, path := range fs.Args() {
		checkPath(path, pats, &t, stdout, stderr)
	}
	switch {
	case t.matches > 0:
		fmt.Fprintf(stdout, "::error::%d match(es) of a YARA text pattern that flagged a release; fix the text at its cause, never allowlist it (see %s)\n", t.matches, record)
		return 1
	case t.harness:
		return 2
	case t.checked == 0:
		fmt.Fprintln(stderr, "HARNESS ERROR: no text file was checked; nothing was asserted")
		return 2
	}
	fmt.Fprintf(stdout, "ok   %d text file(s) checked against %d pattern(s)\n", t.checked, len(pats))
	return 0
}

func checkPath(path string, pats []pattern, t *tally, stdout, stderr io.Writer) {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		checkTarGz(path, pats, t, stdout, stderr)
	case strings.HasSuffix(lower, ".vsix"), strings.HasSuffix(lower, ".zip"):
		checkZip(path, pats, t, stdout, stderr)
	default:
		f, err := os.Open(path)
		if err != nil {
			harness(t, stderr, path, err)
			return
		}
		defer f.Close()
		checkEntry(path, f, pats, t, stdout, stderr)
	}
}

func harness(t *tally, stderr io.Writer, label string, err error) {
	fmt.Fprintf(stderr, "HARNESS ERROR: %s: %v\n", label, err)
	t.harness = true
}

func checkTarGz(path string, pats []pattern, t *tally, stdout, stderr io.Writer) {
	f, err := os.Open(path)
	if err != nil {
		harness(t, stderr, path, err)
		return
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		harness(t, stderr, path, err)
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			harness(t, stderr, path, err)
			return
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		checkEntry(path+"!"+hdr.Name, tr, pats, t, stdout, stderr)
	}
}

func checkZip(path string, pats []pattern, t *tally, stdout, stderr io.Writer) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		harness(t, stderr, path, err)
		return
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if !zf.Mode().IsRegular() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			harness(t, stderr, path+"!"+zf.Name, err)
			continue
		}
		checkEntry(path+"!"+zf.Name, rc, pats, t, stdout, stderr)
		rc.Close()
	}
}

// checkEntry matches one entry if it is text; binaries are skipped.
func checkEntry(label string, r io.Reader, pats []pattern, t *tally, stdout, stderr io.Writer) {
	br := bufio.NewReaderSize(r, sniffLen)
	head, err := br.Peek(sniffLen)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		harness(t, stderr, label, err)
		return
	}
	if !isText(head) {
		return
	}
	t.checked++
	// Match the whole entry, not line by line: YARA's \s matches newlines, so
	// a hit can span a line break. Bundles are megabytes at most.
	text, err := io.ReadAll(br)
	if err != nil {
		harness(t, stderr, label, err)
		return
	}
	for _, p := range pats {
		for _, loc := range p.re.FindAllIndex(text, -1) {
			line := 1 + bytes.Count(text[:loc[0]], []byte{'\n'})
			fmt.Fprintf(stdout, "::error::%s:%d: %s (hit %s, %s) matched %q\n",
				label, line, p.Rule, p.Release, p.Source, text[loc[0]:loc[1]])
			t.matches++
		}
	}
}

// isText reports whether head looks like text: no NUL byte, not an executable.
func isText(head []byte) bool {
	if len(head) >= 4 {
		m := head[:4]
		for _, magic := range [][]byte{
			{0x7f, 'E', 'L', 'F'},
			{0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe},
			{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe},
			{0xca, 0xfe, 0xba, 0xbe},
		} {
			if bytes.Equal(m, magic) {
				return false
			}
		}
	}
	return bytes.IndexByte(head, 0) < 0
}
