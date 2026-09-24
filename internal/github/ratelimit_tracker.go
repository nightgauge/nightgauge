package github

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
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
const sharedTrackerFileVersion = 2

// The rate-limit pools this tracker distinguishes. GitHub bills REST and
// GraphQL against SEPARATE budgets, each with its own remaining count and its
// own reset second, and it names the pool it charged in the response's
// X-RateLimit-Resource header. Before v2 the tracker kept ONE slot per user,
// so whichever response landed last overwrote the other — a healthy core
// reading routinely masked an exhausted graphql one, which is the pool that
// actually runs out. Entries are keyed by (user, resource) from v2 on; v1
// entries carry no resource and are dropped on read by the version check.
const (
	ResourceCore    = "core"
	ResourceGraphQL = "graphql"
)

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

// NewSharedRateLimitTracker constructs a tracker over the file at path —
// normally DefaultSharedTrackerPath. Pass an explicit path in tests.
func NewSharedRateLimitTracker(path string) *SharedRateLimitTracker {
	return &SharedRateLimitTracker{path: path}
}

// DefaultSharedTrackerPath returns <STATE>/rate-limit.json, the machine-wide
// tracker file under the machine-state root (layout.StateHome, ADR-024 § 8).
// A pre-ADR-024 ~/.nightgauge/rate-limit.json is moved there on first use.
// The file is a cold-start hint: its absence is not an error, and an error
// here (no usable state root) leaves callers ungated, never failing.
func DefaultSharedTrackerPath() (string, error) {
	return layout.StateHintFile(sharedTrackerFileName)
}

// sharedTrackerFileName is the tracker file's name under the state root.
const sharedTrackerFileName = "rate-limit.json"

// keyFor normalizes the GitHub user key used in the tracker file. Empty user
// collapses to "default" so workspaces with no explicit gh user still share
// state.
func keyFor(user, resource string) string {
	if user == "" {
		user = "default"
	}
	if resource == "" {
		// An unlabelled reading is charged to core: REST is the only caller
		// that can reach here without a resource, and guessing graphql would
		// let a REST reading gate GraphQL traffic.
		resource = ResourceCore
	}
	return user + "|" + resource
}

// resourceOf reports the pool a key belongs to, for the cross-key scan in
// GetBudget. A key written by an older build has no separator and is treated
// as core.
func resourceOf(key string) string {
	if i := strings.LastIndex(key, "|"); i >= 0 {
		return key[i+1:]
	}
	return ResourceCore
}

// Get returns the persisted entry for user along with whether it is fresh
// (within SharedTrackerMinCheckIntervalSecs). Missing / corrupt files yield
// (nil, false, nil) — callers should treat that as "no data, query fresh".
func (t *SharedRateLimitTracker) Get(user, resource string) (*SharedTrackerEntry, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	file, err := t.readLocked()
	if err != nil {
		return nil, false, err
	}
	entry, ok := file.Entries[keyFor(user, resource)]
	if !ok || entry == nil {
		return nil, false, nil
	}
	fresh := time.Now().Unix()-entry.CheckedAt < SharedTrackerMinCheckIntervalSecs
	return entry, fresh, nil
}

// GetBudget returns the reading that governs user's next call, which is not
// always user's own entry.
//
// The tracker file is keyed by gh username, but GitHub's primary rate limit is
// keyed by ACCOUNT. One account reaches this file under more than one key: the
// IPC server's default client wires the tracker with an empty user (collapsing
// to "default"), while the per-repo resolver and the per-user clients wire it
// with the resolved gh username. Both spend the same pool, and before this each
// key saw only its own share — so both gates believed roughly twice the real
// budget remained, and neither ever observed the other's exhaustion. An
// operator's file has been observed carrying "default" and a username key with
// different Remaining values against an identical ResetAt.
//
// ResetAt is the discriminator. GitHub's window is per account, so two entries
// reporting the SAME non-zero reset second are the same pool; a genuinely
// different account is on its own window. Among those, the lowest Remaining is
// the truth, because a reading can only be stale in the direction of having
// spent more since. Picking the most constrained entry therefore fails toward
// waiting rather than toward burning, which is the safe direction for a gate.
//
// The returned bool is the freshness of the entry actually returned.
func (t *SharedRateLimitTracker) GetBudget(user, resource string) (*SharedTrackerEntry, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	file, err := t.readLocked()
	if err != nil {
		return nil, false, err
	}
	self := keyFor(user, resource)
	entry := file.Entries[self]
	if entry == nil {
		return nil, false, nil
	}
	governing := entry
	if entry.ResetAt > 0 {
		for key, other := range file.Entries {
			if other == nil || key == self {
				continue
			}
			// Same pool only. A core reading must never govern a GraphQL
			// call: the two budgets are independent, and core is almost
			// always the healthier of the two, so letting it in here would
			// reintroduce exactly the masking this split exists to end.
			if resourceOf(key) != resourceOf(self) {
				continue
			}
			if other.ResetAt == entry.ResetAt && other.Remaining < governing.Remaining {
				governing = other
			}
		}
	}
	fresh := time.Now().Unix()-governing.CheckedAt < SharedTrackerMinCheckIntervalSecs
	return governing, fresh, nil
}

// Set persists info for user, merging with any existing entries. The write is
// atomic (temp file + rename) so concurrent readers never observe a partial
// write.
func (t *SharedRateLimitTracker) Set(user, resource string, info *RateLimitInfo) error {
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
	file.Entries[keyFor(user, resource)] = &SharedTrackerEntry{
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
func (t *SharedRateLimitTracker) SetFromHeaders(user, resource, remaining, limit, reset string) (bool, error) {
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
	existing := file.Entries[keyFor(user, resource)]
	if existing != nil && existing.CheckedAt > now {
		// Out-of-order: an entry persisted with a newer CheckedAt already
		// wins. Don't roll quota observations backward.
		return false, nil
	}

	file.Entries[keyFor(user, resource)] = &SharedTrackerEntry{
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
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

// GetBudgetAcrossPools reports the most constrained pool for user, for a
// caller that cannot know which budget its call will spend.
//
// `gh` subprocesses are exactly that caller: the gate runs before the child
// starts, and `gh issue view` bills GraphQL while `gh api repos/...` bills
// core. Gating on the lower of the two may hold a call that would have been
// affordable, which costs latency; the alternative — guessing core and letting
// a GraphQL call through on an exhausted GraphQL budget — costs the window.
// Waiting is the recoverable error, so it is the one chosen here.
func (t *SharedRateLimitTracker) GetBudgetAcrossPools(user string) (*SharedTrackerEntry, bool, error) {
	var governing *SharedTrackerEntry
	var fresh bool
	for _, resource := range []string{ResourceCore, ResourceGraphQL} {
		entry, entryFresh, err := t.GetBudget(user, resource)
		if err != nil {
			return nil, false, err
		}
		if entry == nil {
			continue
		}
		// An elapsed window says nothing about the budget now, so it must not
		// win the comparison on a low Remaining it can no longer justify.
		if entry.ResetAt > 0 && entry.ResetAt <= time.Now().Unix() {
			continue
		}
		if governing == nil || entry.Remaining < governing.Remaining {
			governing, fresh = entry, entryFresh
		}
	}
	return governing, fresh, nil
}
