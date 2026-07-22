// Package docs provides deterministic markdown documentation operations.
// SnapshotDiffResult JSON schema is stable — field names and types must not
// change after first merge. Skills parse `nightgauge docs snapshot-diff
// --json` output; any breaking change requires incrementing the V field.
//
// The snapshot-diff verb replaces the bash + curl + sha256sum chain in
// docs-watch Phase 4 (audit row B34). It is non-fatal by design: fetch
// failures for individual URLs are skipped with a warning. Only hard input
// errors (missing files, malformed JSON) return a non-nil error.

package docs

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// SnapshotDiffOptions controls a single snapshot-diff run.
type SnapshotDiffOptions struct {
	// SnapshotFile is the path to an existing snapshot JSON produced by
	// a prior run. Required; the file must exist and contain valid JSON.
	SnapshotFile string
	// URLsFile is a text file with one URL per line (blank lines ignored).
	// Required; the file must exist.
	URLsFile string
	// HTTPClient overrides the default HTTP client. When nil, a client with
	// a 15-second timeout is used (matching the bash script behavior).
	HTTPClient *http.Client
}

// SnapshotDiffResult is the stable JSON output schema for
// `nightgauge docs snapshot-diff`. Schema version 1 — do not rename or
// remove fields after first merge.
type SnapshotDiffResult struct {
	V        int           `json:"v"`       // schema version, always 1
	New      []Entry       `json:"new"`     // pages in URLs file but not in snapshot
	Changed  []ChangeEntry `json:"changed"` // pages with a different hash than snapshot
	Removed  []RemoveEntry `json:"removed"` // pages in snapshot but not in URLs file
	Warnings []string      `json:"warnings,omitempty"`
}

// Entry describes a newly discovered page.
type Entry struct {
	URL  string `json:"url"`
	Hash string `json:"hash"` // sha256 hex of the page body
}

// ChangeEntry describes a page whose content hash differs from the snapshot.
type ChangeEntry struct {
	URL     string `json:"url"`
	Hash    string `json:"hash"`     // current sha256 hex
	OldHash string `json:"old_hash"` // hash recorded in the snapshot
}

// RemoveEntry describes a URL that is in the snapshot but absent from the
// current URLs file.
type RemoveEntry struct {
	URL string `json:"url"`
}

// snapshotFile is the JSON representation of a snapshot index. Only the
// pages map is consumed by snapshot-diff; additional fields are ignored so
// the schema can evolve independently.
type snapshotFile struct {
	Pages map[string]snapshotPage `json:"pages"`
}

type snapshotPage struct {
	Hash string `json:"hash"`
}

// SnapshotDiff computes the diff between the known snapshot and the current
// set of URLs, fetching each URL to compute its sha256 hash. It returns a
// non-nil error only for hard input failures (missing file, malformed JSON).
// Individual fetch failures are appended to Warnings and skipped.
func SnapshotDiff(opts SnapshotDiffOptions) (*SnapshotDiffResult, error) {
	snapshot, err := loadSnapshot(opts.SnapshotFile)
	if err != nil {
		return nil, err
	}

	currentURLs, err := loadURLs(opts.URLsFile)
	if err != nil {
		return nil, err
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	result := &SnapshotDiffResult{
		V:       1,
		New:     []Entry{},
		Changed: []ChangeEntry{},
		Removed: []RemoveEntry{},
	}

	// Build a set from current URLs for O(1) lookup.
	currentSet := make(map[string]struct{}, len(currentURLs))
	for _, u := range currentURLs {
		currentSet[u] = struct{}{}
	}

	// Pages in snapshot but not in current URLs → removed.
	for u := range snapshot.Pages {
		if _, ok := currentSet[u]; !ok {
			result.Removed = append(result.Removed, RemoveEntry{URL: u})
		}
	}

	// Pages in current URLs — compare against snapshot.
	for _, u := range currentURLs {
		hash, warn := fetchHash(client, u)
		if warn != "" {
			result.Warnings = append(result.Warnings, warn)
			continue
		}
		if existing, inSnapshot := snapshot.Pages[u]; inSnapshot {
			if hash != existing.Hash {
				result.Changed = append(result.Changed, ChangeEntry{
					URL:     u,
					Hash:    hash,
					OldHash: existing.Hash,
				})
			}
			// Hash matches — no change; do not add to any list.
		} else {
			result.New = append(result.New, Entry{URL: u, Hash: hash})
		}
	}

	return result, nil
}

// loadSnapshot reads and parses the snapshot JSON file.
func loadSnapshot(path string) (*snapshotFile, error) {
	if path == "" {
		return nil, fmt.Errorf("snapshot-diff: --snapshot is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("snapshot-diff: snapshot file not found: %s", path)
		}
		return nil, fmt.Errorf("snapshot-diff: read snapshot: %w", err)
	}
	var sf snapshotFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("snapshot-diff: malformed snapshot JSON: %w", err)
	}
	if sf.Pages == nil {
		sf.Pages = map[string]snapshotPage{}
	}
	return &sf, nil
}

// loadURLs reads the URLs file and returns non-blank lines, preserving order.
func loadURLs(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("snapshot-diff: --urls is required")
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("snapshot-diff: URLs file not found: %s", path)
		}
		return nil, fmt.Errorf("snapshot-diff: open URLs file: %w", err)
	}
	defer f.Close()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			urls = append(urls, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("snapshot-diff: read URLs file: %w", err)
	}
	return urls, nil
}

// fetchHash GETs the URL and returns the hex-encoded sha256 of the response
// body. Returns ("", warning) on any network or HTTP error so callers can
// skip the URL non-fatally.
func fetchHash(client *http.Client, url string) (string, string) {
	resp, err := client.Get(url) //nolint:noctx // intentional: no ctx plumbed to this helper
	if err != nil {
		return "", fmt.Sprintf("fetch %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Sprintf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", fmt.Sprintf("fetch %s: read body: %v", url, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), ""
}
