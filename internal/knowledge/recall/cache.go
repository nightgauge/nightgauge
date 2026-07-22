package recall

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	cacheVersion = 1
	cacheDir     = ".nightgauge/knowledge/.recall-cache"
	cacheFile    = "index.jsonl"
)

// cacheHeader is the first line of the JSONL cache file.
type cacheHeader struct {
	Version int     `json:"version"`
	BuiltAt string  `json:"built_at"`
	K1      float64 `json:"k1"`
	B       float64 `json:"b"`
}

// CacheEntry is one line in the JSONL cache (lines after the header).
type CacheEntry struct {
	Path         string         `json:"path"`
	Mtime        int64          `json:"mtime"` // UnixNano
	Kind         string         `json:"kind"`
	IssueNum     int            `json:"issue_number,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	Repos        []string       `json:"repos,omitempty"`
	Tokens       []string       `json:"tokens"`
	TermFreq     map[string]int `json:"term_freq"`
	Graduated    bool           `json:"graduated,omitempty"`
	GraduateDest string         `json:"graduate_dest,omitempty"`
}

func cachePath(workdir string) string {
	return filepath.Join(workdir, cacheDir, cacheFile)
}

// loadFromCache reads the JSONL cache and validates mtime for every entry.
// Returns nil when the cache is missing, corrupt, or its parameters differ from k1/b.
func loadFromCache(workdir string, k1, b float64) ([]*Document, error) {
	path := cachePath(workdir)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// Read header line.
	if !scanner.Scan() {
		return nil, fmt.Errorf("empty cache file")
	}
	var header cacheHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return nil, fmt.Errorf("parse cache header: %w", err)
	}
	if header.Version != cacheVersion {
		return nil, fmt.Errorf("cache version mismatch: got %d want %d", header.Version, cacheVersion)
	}
	if header.K1 != k1 || header.B != b {
		return nil, fmt.Errorf("BM25 params changed: k1=%.3f b=%.3f vs cached k1=%.3f b=%.3f", k1, b, header.K1, header.B)
	}

	var docs []*Document
	stale := false

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var entry CacheEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip malformed lines — fall back to full rebuild.
			stale = true
			continue
		}
		// Stat the file to validate mtime.
		absPath := filepath.Join(workdir, entry.Path)
		info, err := os.Stat(absPath)
		if err != nil {
			// File removed — skip.
			stale = true
			continue
		}
		if info.ModTime().UnixNano() != entry.Mtime {
			// Stale — need full rebuild.
			stale = true
			break
		}
		docs = append(docs, &Document{
			ID:           entry.Path,
			Path:         entry.Path,
			Kind:         entry.Kind,
			IssueNumber:  entry.IssueNum,
			Tags:         entry.Tags,
			Repos:        entry.Repos,
			Tokens:       entry.Tokens,
			TermFreq:     entry.TermFreq,
			Graduated:    entry.Graduated,
			GraduateDest: entry.GraduateDest,
		})
	}

	if stale {
		return nil, fmt.Errorf("cache is stale")
	}
	return docs, nil
}

// saveToCache writes docs to the JSONL cache file, overwriting any prior cache.
func saveToCache(workdir string, docs []*Document, k1, b float64) error {
	dir := filepath.Join(workdir, cacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	path := cachePath(workdir)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	// Write header.
	header := cacheHeader{
		Version: cacheVersion,
		K1:      k1,
		B:       b,
	}
	if err := enc.Encode(header); err != nil {
		return fmt.Errorf("write cache header: %w", err)
	}

	// Write one entry per document.
	for _, doc := range docs {
		absPath := filepath.Join(workdir, doc.Path)
		info, err := os.Stat(absPath)
		if err != nil {
			continue
		}
		entry := CacheEntry{
			Path:         doc.Path,
			Mtime:        info.ModTime().UnixNano(),
			Kind:         doc.Kind,
			IssueNum:     doc.IssueNumber,
			Tags:         doc.Tags,
			Repos:        doc.Repos,
			Tokens:       doc.Tokens,
			TermFreq:     doc.TermFreq,
			Graduated:    doc.Graduated,
			GraduateDest: doc.GraduateDest,
		}
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("write cache entry %s: %w", doc.Path, err)
		}
	}

	return nil
}
