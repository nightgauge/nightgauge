// Package knowledge — persistent metadata index for KB v2 (Issue #2964).
//
// This index is distinct from the BM25 recall cache at
// .nightgauge/knowledge/.recall-cache/index.jsonl. The recall cache is
// optimized for BM25 ranking; this index is optimized for backlink and metadata
// queries (path, title, tags, mtime) issued by VSCode via IPC.
//
// Two indexes coexist by design — see decisions.md ADR-001 for this issue.
package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// metadataIndexSchemaVersion is bumped when the on-disk format changes
	// incompatibly. LoadMetadataIndex treats a mismatch as "no index" so the
	// next reindex call rebuilds from scratch.
	metadataIndexSchemaVersion = 1

	// metadataIndexPath is the workspace-relative path for the persistent
	// metadata index. Gitignored — see decisions.md ADR-001.
	metadataIndexPath = ".nightgauge/knowledge/.index.json"

	// metadataIndexMaxEntries caps the index size as a safety guard. Repos
	// approaching this limit should split their KB or revisit scoping.
	metadataIndexMaxEntries = 5000
)

// IndexEntry stores per-file metadata for the persistent knowledge index.
type IndexEntry struct {
	Path      string   `json:"path"`      // workspace-relative path
	Title     string   `json:"title"`     // first H1 or filename
	Tags      []string `json:"tags"`      // from frontmatter
	Backlinks []string `json:"backlinks"` // workspace-relative paths that [[wiki-link]] to this file
	Mtime     int64    `json:"mtime"`     // UnixNano
	Kind      string   `json:"kind"`      // "issue" | "repo-topic" | "workspace"
}

// MetadataIndex is the full persistent index stored at .index.json.
type MetadataIndex struct {
	SchemaVersion int          `json:"schema_version"`
	BuiltAt       string       `json:"built_at"` // RFC3339
	Entries       []IndexEntry `json:"entries"`
}

var firstH1Re = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// BuildMetadataIndex scans all KB scopes and writes the metadata index to
// .nightgauge/knowledge/.index.json. The write is atomic — the data is
// written to a .tmp file and renamed (POSIX-atomic; effectively atomic on
// Windows for same-directory renames). See ADR-005 in decisions.md.
//
// Returns the populated index alongside any I/O error. The function is
// resilient to partial KB trees: missing directories are skipped silently and
// individual file-read errors are logged-and-skipped (no partial-index aborts).
func BuildMetadataIndex(workdir string) (*MetadataIndex, error) {
	if workdir == "" {
		return nil, fmt.Errorf("workdir is required")
	}

	entries, err := scanKBFiles(workdir)
	if err != nil {
		return nil, fmt.Errorf("scan KB files: %w", err)
	}

	if len(entries) > metadataIndexMaxEntries {
		entries = entries[:metadataIndexMaxEntries]
	}

	computeBacklinks(workdir, entries)

	// Stable order: lexicographic by path so callers can rely on diffability.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	idx := &MetadataIndex{
		SchemaVersion: metadataIndexSchemaVersion,
		BuiltAt:       time.Now().UTC().Format(time.RFC3339),
		Entries:       entries,
	}

	if err := writeIndexAtomic(workdir, idx); err != nil {
		return nil, fmt.Errorf("write index: %w", err)
	}

	return idx, nil
}

// LoadMetadataIndex reads the persistent index from disk. Returns (nil, nil)
// when the file does not exist or its schema version does not match — callers
// should treat this as "no index" and either build one or degrade gracefully.
func LoadMetadataIndex(workdir string) (*MetadataIndex, error) {
	path := filepath.Join(workdir, metadataIndexPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read index: %w", err)
	}
	var idx MetadataIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		// Corrupt — treat as missing.
		return nil, nil
	}
	if idx.SchemaVersion != metadataIndexSchemaVersion {
		return nil, nil
	}
	return &idx, nil
}

// BacklinksFor returns the workspace-relative paths that link to the given
// target path. Returns nil when the index is nil, when no entry matches the
// target, or when no other entry links to it.
func BacklinksFor(idx *MetadataIndex, targetPath string) []string {
	if idx == nil {
		return nil
	}
	for _, e := range idx.Entries {
		if e.Path == targetPath {
			if len(e.Backlinks) == 0 {
				return nil
			}
			out := make([]string, len(e.Backlinks))
			copy(out, e.Backlinks)
			return out
		}
	}
	return nil
}

// FindByTitle returns entries whose title contains the query as a
// case-insensitive substring. This is a supplementary lookup; ranked search
// goes through the recall package.
func FindByTitle(idx *MetadataIndex, query string) []IndexEntry {
	if idx == nil || query == "" {
		return nil
	}
	q := strings.ToLower(query)
	var out []IndexEntry
	for _, e := range idx.Entries {
		if strings.Contains(strings.ToLower(e.Title), q) {
			out = append(out, e)
		}
	}
	return out
}

// scanKBFiles enumerates every .md file in the knowledge tree across all
// scopes — local issues, cross-repo, and workspace categories — and emits an
// IndexEntry for each. Backlinks are populated by computeBacklinks in a second
// pass.
func scanKBFiles(workdir string) ([]IndexEntry, error) {
	var entries []IndexEntry

	// Local features/ and epics/ directories.
	for _, category := range []string{"features", "epics"} {
		base := filepath.Join(workdir, ".nightgauge", "knowledge", category)
		issues, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, issueDir := range issues {
			if !issueDir.IsDir() {
				continue
			}
			issueAbs := filepath.Join(base, issueDir.Name())
			mdFiles, _ := os.ReadDir(issueAbs)
			for _, mde := range mdFiles {
				if mde.IsDir() || !strings.HasSuffix(mde.Name(), ".md") || mde.Name() == "README.md" {
					continue
				}
				abs := filepath.Join(issueAbs, mde.Name())
				if e, ok := indexFile(workdir, abs, "issue"); ok {
					entries = append(entries, e)
				}
			}
		}
	}

	// Workspace-level categories (product/, cross-repo/, architecture/, glossary/).
	for _, category := range []string{"product", "cross-repo", "architecture", "glossary"} {
		base := filepath.Join(workdir, ".nightgauge", "knowledge", category)
		_ = filepath.Walk(base, func(p string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(info.Name(), ".md") || info.Name() == "README.md" {
				return nil
			}
			if e, ok := indexFile(workdir, p, "workspace"); ok {
				entries = append(entries, e)
			}
			return nil
		})
	}

	// Cross-repo entries declared in workspace config.
	crossEntries, _ := ScanCrossRepoKnowledge(workdir, 200)
	for _, ce := range crossEntries {
		base := filepath.Join(workdir, ce.Path)
		for _, name := range ce.Entries {
			abs := filepath.Join(base, name)
			if e, ok := indexFile(workdir, abs, "repo-topic"); ok {
				entries = append(entries, e)
			}
		}
	}

	return entries, nil
}

// indexFile reads a single markdown file and produces an IndexEntry. The
// boolean return is false when the file cannot be read; callers should skip
// rather than abort the entire index build.
func indexFile(workdir, absPath, kind string) (IndexEntry, bool) {
	info, err := os.Stat(absPath)
	if err != nil {
		return IndexEntry{}, false
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return IndexEntry{}, false
	}
	content := string(data)

	relPath, err := filepath.Rel(workdir, absPath)
	if err != nil {
		relPath = absPath
	}

	var tags []string
	if fm, _ := ParseFrontmatter(content); fm != nil {
		tags = fm.Tags
	}

	title := extractTitle(content, absPath)

	return IndexEntry{
		Path:      filepath.ToSlash(relPath),
		Title:     title,
		Tags:      tags,
		Backlinks: nil, // populated in computeBacklinks
		Mtime:     info.ModTime().UnixNano(),
		Kind:      kind,
	}, true
}

// extractTitle returns the first H1 heading from content, or the filename
// (without extension) when no H1 is present.
func extractTitle(content, absPath string) string {
	if m := firstH1Re.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSuffix(filepath.Base(absPath), ".md")
}

// computeBacklinks resolves every [[wiki-link]] in every entry to its target
// and records the source path under the target's Backlinks field. Wiki-link
// resolution reuses the existing knowledge package resolver so namespace
// semantics ([[#NNNN]], [[topic:term]], [[product:slug]], etc.) match the
// rendered Markdown output.
func computeBacklinks(workdir string, entries []IndexEntry) {
	// Build path → entry-index map for O(1) lookup during backlink resolution.
	pathToIdx := make(map[string]int, len(entries))
	for i, e := range entries {
		pathToIdx[e.Path] = i
	}

	// Map issue directory paths → all index entries inside that directory.
	// Issue-ref wiki-links (e.g. [[#1234]]) resolve to the issue *directory*;
	// every .md file under it counts as a backlink target.
	dirToFiles := make(map[string][]int)
	for i, e := range entries {
		parent := filepath.ToSlash(filepath.Dir(e.Path))
		dirToFiles[parent] = append(dirToFiles[parent], i)
	}

	for _, src := range entries {
		absSrc := filepath.Join(workdir, src.Path)
		data, err := os.ReadFile(absSrc)
		if err != nil {
			continue
		}
		links := ExtractWikiLinks(string(data))
		for _, link := range links {
			resolved, _, exists, _ := resolveWikiLinkGo(link.Raw, absSrc, workdir)
			if !exists {
				continue
			}
			// Strip optional #anchor — backlinks point to files, not anchors.
			resolved = filepath.ToSlash(resolved)
			if idx := strings.Index(resolved, "#"); idx >= 0 {
				resolved = resolved[:idx]
			}

			// Direct file match.
			if targetIdx, ok := pathToIdx[resolved]; ok {
				addBacklink(entries, targetIdx, src.Path)
				continue
			}
			// Directory match → mark every .md file in that dir.
			if fileIdxs, ok := dirToFiles[resolved]; ok {
				for _, fi := range fileIdxs {
					addBacklink(entries, fi, src.Path)
				}
			}
		}
	}

	// Stable backlink ordering per entry — makes index diffs reviewable.
	for i := range entries {
		sort.Strings(entries[i].Backlinks)
	}
}

// addBacklink appends src to entries[targetIdx].Backlinks unless it is
// already present. Slice growth is bounded by the natural KB size.
func addBacklink(entries []IndexEntry, targetIdx int, src string) {
	for _, existing := range entries[targetIdx].Backlinks {
		if existing == src {
			return
		}
	}
	entries[targetIdx].Backlinks = append(entries[targetIdx].Backlinks, src)
}

// writeIndexAtomic serializes idx and writes it to .index.json via a .tmp
// rename to avoid a partial-write race with concurrent IPC readers.
func writeIndexAtomic(workdir string, idx *MetadataIndex) error {
	dir := filepath.Join(workdir, ".nightgauge", "knowledge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create index dir: %w", err)
	}
	finalPath := filepath.Join(workdir, metadataIndexPath)
	tmpPath := finalPath + ".tmp"

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write tmp index: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename index: %w", err)
	}
	return nil
}
