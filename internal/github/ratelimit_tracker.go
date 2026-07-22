package github

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// SharedTrackerMinCheckIntervalSecs is how long a cached rate-limit reading is
// considered fresh. Within this window, callers reuse the persisted entry
// instead of issuing a new GraphQL rateLimit query.
//
// As of #3291, every HTTP response feeds X-RateLimit-* headers into the
// tracker for free, so we no longer need a wide cache window to avoid burning
// quota on the rateLimit probe itself. 15s gives near-real-time visibility
// across multiple workspaces.
const SharedTrackerMinCheckIntervalSecs = 15

// sharedTrackerFileVersion is bumped whenever the on-disk schema changes in a
// non-backward-compatible way. Readers silently drop entries from older
// versions.
const sharedTrackerFileVersion = 1

// SharedTrackerEntry is the persisted state for one GitHub user.
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

// SharedRateLimitTracker persists GitHub API rate-limit state to a per-user
// file so multiple VSCode windows / Go IPC processes coordinate instead of
// each burning quota independently.
//
// Concurrency: individual Go processes use the internal mutex; cross-process
// safety relies on atomic-rename writes. Stale reads are tolerated — callers
// check CheckedAt and re-query GraphQL when the entry is older than
// SharedTrackerMinCheckIntervalSecs.
type SharedRateLimitTracker struct {
	path string
	mu   sync.Mutex
}

// NewSharedRateLimitTracker constructs a tracker rooted at
// $HOME/.nightgauge/rate-limit.json. Pass an explicit path in tests.
func NewSharedRateLimitTracker(path string) *SharedRateLimitTracker {
	return &SharedRateLimitTracker{path: path}
}

// DefaultSharedTrackerPath returns the path under $HOME the tracker uses by
// default. Returns an error when $HOME is unresolvable (very rare).
func DefaultSharedTrackerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".nightgauge", "rate-limit.json"), nil
}

// keyFor normalizes the GitHub user key used in the tracker file. Empty user
// collapses to "default" so workspaces with no explicit gh user still share
// state.
func keyFor(user string) string {
	if user == "" {
		return "default"
	}
	return user
}

// Get returns the persisted entry for user along with whether it is fresh
// (within SharedTrackerMinCheckIntervalSecs). Missing / corrupt files yield
// (nil, false, nil) — callers should treat that as "no data, query fresh".
func (t *SharedRateLimitTracker) Get(user string) (*SharedTrackerEntry, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	file, err := t.readLocked()
	if err != nil {
		return nil, false, err
	}
	entry, ok := file.Entries[keyFor(user)]
	if !ok || entry == nil {
		return nil, false, nil
	}
	fresh := time.Now().Unix()-entry.CheckedAt < SharedTrackerMinCheckIntervalSecs
	return entry, fresh, nil
}

// Set persists info for user, merging with any existing entries. The write is
// atomic (temp file + rename) so concurrent readers never observe a partial
// write.
func (t *SharedRateLimitTracker) Set(user string, info *RateLimitInfo) error {
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
	file.Entries[keyFor(user)] = &SharedTrackerEntry{
		Remaining: info.Remaining,
		Limit:     info.Limit,
		ResetAt:   info.ResetAt,
		CheckedAt: time.Now().Unix(),
	}
	file.Version = sharedTrackerFileVersion
	return t.writeLocked(file)
}

// SetFromHeaders updates the tracker from raw GitHub rate-limit response
// headers. Returns true when the headers parsed cleanly and the entry was
// updated.
//
// Behavior:
//   - All three headers must parse as integers; if any fails, the call is a
//     no-op (returns false, nil) — partial data is worse than stale data.
//   - The persisted entry's CheckedAt is set to time.Now() so freshness
//     tracking works even when no GraphQL probe ran.
//   - Older readings (smaller CheckedAt) are *not* allowed to overwrite a
//     newer reading. This prevents out-of-order responses from rolling back
//     observed quota.
//
// Header names are case-insensitive per RFC 7230; pass the X-RateLimit-*
// values as plain strings.
func (t *SharedRateLimitTracker) SetFromHeaders(user, remaining, limit, reset string) (bool, error) {
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
	existing := file.Entries[keyFor(user)]
	if existing != nil && existing.CheckedAt > now {
		// Out-of-order: an entry persisted with a newer CheckedAt already
		// wins. Don't roll quota observations backward.
		return false, nil
	}

	file.Entries[keyFor(user)] = &SharedTrackerEntry{
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
		// Corruption (partial write from older version, manual edit, etc.) —
		// don't explode the caller's flow. A subsequent Set() will overwrite
		// with a valid file.
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
	tmp, err := os.CreateTemp(dir, ".rate-limit-*.json.tmp")
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
