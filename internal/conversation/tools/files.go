package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// maxFileBytes is the largest file read_file returns or search_files
	// scans.
	maxFileBytes = 1 << 20
	// maxReadLines bounds one read_file call.
	maxReadLines = 2000
	// maxListEntries bounds one directory listing.
	maxListEntries = 1000
	// maxSearchFiles and maxSearchResults bound one search.
	maxSearchFiles   = 20000
	maxSearchResults = 200
	// maxLineChars bounds one matched line in a search result.
	maxLineChars = 300
)

// skippedDirs are not searched: .git is secret, and dependency trees are
// read directly when needed rather than scanned.
var skippedDirs = map[string]bool{".git": true, "node_modules": true}

// readText reads a resolved file through the root: refused when it is not a
// regular file, too large or binary; a Nightgauge config file comes back with
// its credential fields redacted.
func (s *Toolset) readText(rel string) (string, *Error) {
	f, err := s.fs.root.Open(rel)
	if err != nil {
		return "", openErr(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", openErr(err)
	}
	if !info.Mode().IsRegular() {
		return "", fail(CodeNotText, "the path is not a regular file")
	}
	if info.Size() > maxFileBytes {
		return "", fail(CodeNotText, fmt.Sprintf("the file is larger than %d bytes", maxFileBytes))
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", openErr(err)
	}
	if bytes.IndexByte(raw[:min(len(raw), 8000)], 0) >= 0 || !utf8.Valid(raw) {
		return "", fail(CodeNotText, "the file is not UTF-8 text")
	}
	if nightgaugeConfig(rel) {
		return redactConfig(raw)
	}
	return clean(string(raw)), nil
}

func (s *Toolset) workspace() (*workspaceFS, *Error) {
	if s.fs == nil {
		return nil, fail(CodeNotFound, "the workspace root is not available on this host")
	}
	return s.fs, nil
}

func (s *Toolset) readFile(raw json.RawMessage) (string, *Error) {
	var a struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		MaxLines  int    `json:"max_lines"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	w, err := s.workspace()
	if err != nil {
		return "", err
	}
	rel, err := w.resolve(a.Path)
	if err != nil {
		return "", err
	}
	if !s.canReadPath(w.abs(rel)) {
		return "", errRepoNotReadable
	}
	text, err := s.readText(rel)
	if err != nil {
		return "", err
	}
	start := max(a.StartLine, 1)
	limit := a.MaxLines
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	lines := strings.SplitAfter(text, "\n")
	if text == "" {
		lines = nil
	}
	end := min(start-1+limit, len(lines))
	var shown string
	if start-1 < len(lines) {
		shown = strings.Join(lines[start-1:end], "")
	}
	return untrusted("file "+filepath.ToSlash(rel), map[string]any{
		"path":        filepath.ToSlash(rel),
		"total_lines": len(lines),
		"start_line":  start,
		"truncated":   end < len(lines),
		"text":        shown,
	}), nil
}

func (s *Toolset) listDirectory(raw json.RawMessage) (string, *Error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	w, err := s.workspace()
	if err != nil {
		return "", err
	}
	rel, err := w.resolve(a.Path)
	if err != nil {
		return "", err
	}
	if !s.canReadPath(w.abs(rel)) {
		return "", errRepoNotReadable
	}
	f, oerr := w.root.Open(rel)
	if oerr != nil {
		return "", openErr(oerr)
	}
	defer f.Close()
	entries, rerr := f.ReadDir(-1)
	if rerr != nil {
		return "", fail(CodeNotText, "the path is not a directory")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	var out []entry
	for _, e := range entries {
		if secretPath(filepath.Join(rel, e.Name())) {
			continue // never named: a listing is no way around the deny list
		}
		kind := "file"
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			kind = "symlink"
		case e.IsDir():
			kind = "dir"
		case !e.Type().IsRegular():
			kind = "other"
		}
		out = append(out, entry{Name: clean(e.Name()), Type: kind})
		if len(out) == maxListEntries {
			break
		}
	}
	return untrusted("directory "+filepath.ToSlash(rel), map[string]any{
		"path": filepath.ToSlash(rel), "entries": out, "truncated": len(out) == maxListEntries,
	}), nil
}

func (s *Toolset) searchFiles(ctx context.Context, raw json.RawMessage) (string, *Error) {
	var a struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" || len(a.Pattern) > 1000 {
		return "", fail(CodeInvalidArguments, "pattern must be 1 to 1000 characters")
	}
	re, rerr := regexp.Compile(a.Pattern)
	if rerr != nil {
		return "", fail(CodeInvalidArguments, "pattern is not a valid RE2 regular expression")
	}
	limit := a.MaxResults
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, maxSearchResults)
	w, err := s.workspace()
	if err != nil {
		return "", err
	}
	start, err := w.resolve(a.Path)
	if err != nil {
		return "", err
	}
	matches := []string{}
	scanned := 0
	truncated := false
	walkErr := filepath.WalkDir(w.abs(start), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skipped
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, rerr := filepath.Rel(w.dir, path)
		if rerr != nil || (rel != "." && !filepath.IsLocal(rel)) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if rel != "." && (skippedDirs[strings.ToLower(d.Name())] || secretPath(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks are not followed: a search never leaves the tree it
		// walks. read_file resolves one that stays inside.
		if !d.Type().IsRegular() || secretPath(rel) || !s.canReadPath(path) {
			return nil
		}
		if scanned++; scanned > maxSearchFiles || len(matches) >= limit {
			truncated = true
			return fs.SkipAll
		}
		text, terr := s.readText(rel)
		if terr != nil {
			return nil
		}
		sc := bufio.NewScanner(strings.NewReader(text))
		sc.Buffer(make([]byte, 0, 64<<10), maxFileBytes)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if !re.MatchString(line) {
				continue
			}
			if len(line) > maxLineChars {
				line = strings.ToValidUTF8(line[:maxLineChars], "") + "…"
			}
			matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), n, line))
			if len(matches) >= limit {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && ctx.Err() != nil {
		return "", fail(CodeInvalidArguments, "the search was cancelled")
	}
	return untrusted("search of "+filepath.ToSlash(start), map[string]any{
		"pattern": a.Pattern, "matches": matches, "truncated": truncated,
	}), nil
}
