package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// memoryStorePath selects a cache that lives only in this process.
const memoryStorePath = ":memory:"

// DedupeCache deduplicates GitLab webhook deliveries by delivery ID. When the
// same delivery ID arrives within the dedup window the event is accepted
// idempotently (200 OK, no dispatch).
//
// The cache is a map behind a mutex. With a file path it is also persisted to
// that file as JSON, rewritten atomically (temp file and rename) on every
// change, so a restarted receiver still recognises recent deliveries. It used
// to be a SQLite database; #3192 dropped the embedded SQLite engine from the
// release CLI, and a set of recent delivery IDs needs no query engine.
type DedupeCache struct {
	mu     sync.Mutex
	seen   map[string]int64 // delivery ID → received_at, Unix seconds
	path   string           // "" when the cache is in memory only
	window time.Duration
}

// NewDedupeCache opens (or creates) the dedupe store at path. Pass "" or
// ":memory:" for an ephemeral in-process store (tests and default config).
// window is how long a delivery ID is considered "seen" — older entries are
// eligible for pruning, and are discarded when an existing file is loaded.
//
// An existing file that is not a valid store is an error, not an empty cache:
// silently starting empty would re-dispatch every delivery GitLab retries.
func NewDedupeCache(path string, window time.Duration) (*DedupeCache, error) {
	c := &DedupeCache{seen: map[string]int64{}, window: window}
	if path == "" || path == memoryStorePath {
		return c, nil
	}
	c.path = path

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c, nil
	case err != nil:
		return nil, fmt.Errorf("gitlab dedup: open store %q: %w", path, err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &c.seen); err != nil {
			return nil, fmt.Errorf("gitlab dedup: read store %q: %w", path, err)
		}
		if c.seen == nil {
			c.seen = map[string]int64{}
		}
	}
	c.dropExpiredLocked(c.cutoff())
	return c, nil
}

// cutoff is the received_at at or before which an entry has left the window.
func (c *DedupeCache) cutoff() int64 {
	return time.Now().Add(-c.window).Unix()
}

// dropExpiredLocked deletes entries received at or before cutoff and reports
// whether any were deleted. c.mu must be held (or c not yet shared).
func (c *DedupeCache) dropExpiredLocked(cutoff int64) bool {
	dropped := false
	for id, at := range c.seen {
		if at <= cutoff {
			delete(c.seen, id)
			dropped = true
		}
	}
	return dropped
}

// persistLocked writes the cache to its file, atomically. c.mu must be held.
func (c *DedupeCache) persistLocked() error {
	if c.path == "" {
		return nil
	}
	data, err := json.Marshal(c.seen)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), filepath.Base(c.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// IsDuplicate returns true when deliveryID was already seen within the dedup
// window. A false return means the caller should proceed to MarkSeen.
func (c *DedupeCache) IsDuplicate(ctx context.Context, deliveryID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("gitlab dedup: check duplicate: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.seen[deliveryID]
	return ok && at > c.cutoff(), nil
}

// MarkSeen records deliveryID with the current timestamp. If deliveryID is
// already recorded (e.g. from a prior dedup window) its received_at is updated
// so the window slides forward from the latest delivery attempt.
func (c *DedupeCache) MarkSeen(ctx context.Context, deliveryID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("gitlab dedup: mark seen: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[deliveryID] = time.Now().Unix()
	if err := c.persistLocked(); err != nil {
		return fmt.Errorf("gitlab dedup: mark seen: %w", err)
	}
	return nil
}

// Prune removes entries older than the dedup window. Called periodically by
// the background goroutine started in StartPruner.
func (c *DedupeCache) Prune(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("gitlab dedup: prune: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dropExpiredLocked(c.cutoff()) {
		return nil
	}
	if err := c.persistLocked(); err != nil {
		return fmt.Errorf("gitlab dedup: prune: %w", err)
	}
	return nil
}

// StartPruner launches a background goroutine that calls Prune every window/4.
// Cancel ctx to stop it. Prune errors are non-fatal and ignored.
func (c *DedupeCache) StartPruner(ctx context.Context) {
	interval := c.window / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.Prune(ctx)
			}
		}
	}()
}

// Close releases the cache. Every change is already on disk, so there is
// nothing to flush.
func (c *DedupeCache) Close() error {
	return nil
}
