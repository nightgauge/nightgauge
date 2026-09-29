package recall

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
)

const (
	// cacheVersion 2 added the lifecycle fields (trust tier, status,
	// stale_after). The bump is load-bearing, not cosmetic: without it an
	// existing warm cache with unchanged mtimes loads as valid and every
	// document reads unverified/stable forever — the exact silent wrong
	// answer lifecycle weighting exists to prevent.
	cacheVersion = 2
	// cacheName is this cache's directory under the cache home (ADR-024 § 6):
	// the index lives at <cache home>/recall/<root-key>/index.jsonl, never in
	// the working tree.
	cacheName = "recall"
	cacheFile = "index.jsonl"
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
	TrustTier    string         `json:"trust_tier,omitempty"`
	Status       string         `json:"status,omitempty"`
	// StaleAfter is cached as the raw stamp so expiry stays a query-time
	// comparison. A cached bool would never flip: an entry expiring is a
	// clock event with no file change, and the cache is keyed on mtime.
	StaleAfter string `json:"stale_after,omitempty"`
}

// RootKey is the <root-key> the recall cache is filed under: a stable hash of
// the canonical (absolute, symlink-resolved) repository root. Each linked
// worktree indexes its own checkout, whose knowledge files differ, and two
// clones never collide (ADR-024 § 6).
func RootKey(workdir string) string {
	root := workdir
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])[:16]
}

// CachePath is where the recall cache for workdir lives, resolved without
// creating anything. An error means there is no usable cache home; the index
// is then rebuilt in memory for the call.
func CachePath(workdir string) (string, error) {
	home, err := layout.CacheHomePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, cacheName, RootKey(workdir), cacheFile), nil
}

// InvalidateCache deletes workdir's recall cache so the next BuildIndex does a
// full scan. A cache that does not exist, or no cache home, is not an error.
func InvalidateCache(workdir string) error {
	path, err := CachePath(workdir)
	if err != nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// loadFromCache reads the JSONL cache and validates it against refs — the
// documents that exist on disk right now.
//
// Validating per-entry mtimes alone answers "did anything I already know about
// change?", which is silent about ADDED files: a document absent from the cache
// is never stat-ed, so it can never be found stale. refs closes that hole, and
// the caller enumerates it with the same function the scanner uses.
//
// Returns an error when the cache is missing, corrupt, stale, or its parameters
// differ from k1/b — in every case the caller falls back to a full scan.
func loadFromCache(workdir string, k1, b float64, refs []docRef) ([]*Document, error) {
	path, err := CachePath(workdir)
	if err != nil {
		return nil, err
	}
	// Only a regular file is read: a symlink planted in the cache would
	// otherwise point the reader anywhere.
	if info, err := os.Lstat(path); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cache %s is not a regular file", path)
	}
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
			TrustTier:    entry.TrustTier,
			Status:       entry.Status,
			StaleAfter:   entry.StaleAfter,
		})
	}

	if stale {
		return nil, fmt.Errorf("cache is stale")
	}

	// Any document on disk that the cache does not carry makes the cache stale.
	// Without this the document stays unreachable until some ALREADY indexed
	// file happens to change, and "nothing new was indexed" is indistinguishable
	// from "nothing new exists".
	cached := make(map[string]struct{}, len(docs))
	for _, d := range docs {
		cached[d.Path] = struct{}{}
	}
	for _, r := range refs {
		relPath, err := filepath.Rel(workdir, r.absPath)
		if err != nil {
			continue
		}
		if _, ok := cached[relPath]; !ok {
			return nil, fmt.Errorf("cache is missing %s", relPath)
		}
	}

	return docs, nil
}

// saveToCache writes docs to the JSONL cache file under the cache home,
// replacing any prior cache. The directories are created 0700 and none of
// them may be a symlink (layout.CacheDir); the file is written to a temporary
// name and renamed into place, so a symlink at the cache file's path is
// replaced, never followed. An error leaves nothing in the working tree.
func saveToCache(workdir string, docs []*Document, k1, b float64) (err error) {
	dir, err := layout.CacheDir(cacheName, RootKey(workdir))
	if err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	f, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close cache file: %w", cerr)
		}
		if err == nil {
			if rerr := os.Rename(tmp, filepath.Join(dir, cacheFile)); rerr != nil {
				err = fmt.Errorf("install cache file: %w", rerr)
			}
		}
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

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
			TrustTier:    doc.TrustTier,
			Status:       doc.Status,
			StaleAfter:   doc.StaleAfter,
		}
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("write cache entry %s: %w", doc.Path, err)
		}
	}

	return nil
}
