package gitlab

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// SharedTrackerMinCheckIntervalSecs is how long a cached rate-limit reading is
// considered fresh. Within this window, callers reuse the persisted entry
// instead of issuing a fresh read.
const SharedTrackerMinCheckIntervalSecs = 15

// sharedTrackerFileVersion is bumped whenever the on-disk schema changes in a
// non-backward-compatible way.
const sharedTrackerFileVersion = 1

// defaultRateLimitSleepCapSecs caps how long checkRateLimitGate will sleep.
// Beyond this cap, it returns ErrRateLimitGated instead of blocking.
const defaultRateLimitSleepCapSecs = 300

// SharedTrackerEntry is the persisted state for one GitLab instance.
type SharedTrackerEntry struct {
	Remaining int   `json:"remaining"`
	Limit     int   `json:"limit"`
	ResetAt   int64 `json:"resetAt"`   // Unix seconds
	CheckedAt int64 `json:"checkedAt"` // Unix seconds — when this reading was taken
}

// sharedTrackerFile is the on-disk shape.
type sharedTrackerFile struct {
	Version int                            `json:"version"`
	Entries map[string]*SharedTrackerEntry `json:"entries"`
}

// SharedRateLimitTracker persists GitLab API rate-limit state to a per-instance
// file so multiple processes coordinate instead of each burning quota independently.
//
// Concurrency: individual Go processes use the internal mutex; cross-process
// safety relies on atomic-rename writes. Stale reads are tolerated — callers
// check CheckedAt and treat entries older than SharedTrackerMinCheckIntervalSecs
// as stale.
type SharedRateLimitTracker struct {
	path string
	mu   sync.Mutex
}

// NewSharedRateLimitTracker constructs a tracker rooted at the given path.
func NewSharedRateLimitTracker(path string) *SharedRateLimitTracker {
	return &SharedRateLimitTracker{path: path}
}

// nonAlphanumRe matches characters that are not alphanumeric for slug sanitization.
var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// leadingTrailingHyphenRe matches leading and trailing hyphens.
var leadingTrailingHyphenRe = regexp.MustCompile(`^-+|-+$`)

// hostSlug sanitizes a GitLab instance hostname for use in a filename by
// stripping the port and replacing non-alphanumeric runs with hyphens.
func hostSlug(host string) string {
	// Strip port if present.
	if i := len(host) - 1; i >= 0 {
		for i >= 0 && host[i] != ':' && host[i] != ']' {
			i--
		}
		if i >= 0 && host[i] == ':' {
			host = host[:i]
		}
	}
	slug := nonAlphanumRe.ReplaceAllString(host, "-")
	// Trim leading/trailing hyphens.
	slug = leadingTrailingHyphenRe.ReplaceAllString(slug, "")
	if slug == "" {
		slug = "default"
	}
	return slug
}

// DefaultSharedTrackerPath returns the path under $HOME the tracker uses for
// the given GitLab instance host.
func DefaultSharedTrackerPath(host string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	filename := "ratelimit-gitlab-" + hostSlug(host) + ".json"
	return filepath.Join(home, ".nightgauge", filename), nil
}

// keyFor normalizes the instance key. Empty instance collapses to "default".
func keyForInstance(instance string) string {
	if instance == "" {
		return "default"
	}
	return instance
}

// Get returns the persisted entry for the instance along with whether it is fresh
// (within SharedTrackerMinCheckIntervalSecs). Missing / corrupt files yield
// (nil, false, nil) — callers should treat that as "no data, query fresh".
func (t *SharedRateLimitTracker) Get(instance string) (*SharedTrackerEntry, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	file, err := t.readLocked()
	if err != nil {
		return nil, false, err
	}
	entry, ok := file.Entries[keyForInstance(instance)]
	if !ok || entry == nil {
		return nil, false, nil
	}
	fresh := time.Now().Unix()-entry.CheckedAt < SharedTrackerMinCheckIntervalSecs
	return entry, fresh, nil
}

// RateLimitInfo holds the current rate limit state.
type RateLimitInfo struct {
	Remaining int   `json:"remaining"`
	Limit     int   `json:"limit"`
	ResetAt   int64 `json:"resetAt"` // Unix timestamp
}

// Set persists info for the instance, merging with any existing entries. The
// write is atomic (temp file + rename) so concurrent readers never observe a
// partial write.
func (t *SharedRateLimitTracker) Set(instance string, info *RateLimitInfo) error {
	if info == nil {
		return fmt.Errorf("nil RateLimitInfo")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	file, err := t.readLocked()
	if err != nil {
		return err
	}
	if file.Entries == nil {
		file.Entries = make(map[string]*SharedTrackerEntry)
	}
	file.Entries[keyForInstance(instance)] = &SharedTrackerEntry{
		Remaining: info.Remaining,
		Limit:     info.Limit,
		ResetAt:   info.ResetAt,
		CheckedAt: time.Now().Unix(),
	}
	file.Version = sharedTrackerFileVersion
	return t.writeLocked(file)
}

// SetFromHeaders updates the tracker from GitLab rate-limit response headers.
// GitLab uses `RateLimit-Remaining`, `RateLimit-Limit`, `RateLimit-Reset`
// (no X- prefix on modern CE ≥16.x). Returns true when headers parsed cleanly
// and the entry was updated.
//
// Behavior:
//   - All three headers must parse as integers; any failure is a no-op.
//   - Out-of-order responses do not roll back newer observations.
func (t *SharedRateLimitTracker) SetFromHeaders(instance, remaining, limit, reset string) (bool, error) {
	if remaining == "" || limit == "" || reset == "" {
		return false, nil
	}
	r, err := strconv.Atoi(remaining)
	if err != nil {
		return false, nil
	}
	l, err := strconv.Atoi(limit)
	if err != nil {
		return false, nil
	}
	rs, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return false, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	file, err := t.readLocked()
	if err != nil {
		return false, err
	}
	if file.Entries == nil {
		file.Entries = make(map[string]*SharedTrackerEntry)
	}

	now := time.Now().Unix()
	existing := file.Entries[keyForInstance(instance)]
	if existing != nil && existing.CheckedAt > now {
		// Out-of-order: a newer entry already exists, don't roll backward.
		return false, nil
	}

	file.Entries[keyForInstance(instance)] = &SharedTrackerEntry{
		Remaining: r,
		Limit:     l,
		ResetAt:   rs,
		CheckedAt: now,
	}
	file.Version = sharedTrackerFileVersion
	if err := t.writeLocked(file); err != nil {
		return false, err
	}
	return true, nil
}

// readLocked loads the tracker file; a missing or corrupt file yields an
// empty (but valid) tracker so the first writer bootstraps state cleanly.
func (t *SharedRateLimitTracker) readLocked() (*sharedTrackerFile, error) {
	empty := &sharedTrackerFile{Version: sharedTrackerFileVersion, Entries: map[string]*SharedTrackerEntry{}}
	data, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return nil, fmt.Errorf("read tracker: %w", err)
	}
	var file sharedTrackerFile
	if err := json.Unmarshal(data, &file); err != nil {
		return empty, nil
	}
	if file.Version != sharedTrackerFileVersion {
		return empty, nil
	}
	if file.Entries == nil {
		file.Entries = map[string]*SharedTrackerEntry{}
	}
	return &file, nil
}

// writeLocked persists file atomically. The temp file lives in the same
// directory as the target so os.Rename remains atomic on every major OS.
func (t *SharedRateLimitTracker) writeLocked(file *sharedTrackerFile) error {
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tracker: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ratelimit-gitlab-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, t.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename tracker: %w", err)
	}
	return nil
}
