package gitlab

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DedupeCache deduplicates GitLab webhook deliveries using a SQLite-backed
// store keyed by delivery ID. When the same delivery ID arrives within the
// dedup window the event is accepted idempotently (200 OK, no dispatch).
type DedupeCache struct {
	db     *sql.DB
	window time.Duration
}

// NewDedupeCache opens (or creates) the SQLite dedup database at dbPath.
// Pass ":memory:" for an ephemeral in-process store (tests and default config).
// window is how long a delivery ID is considered "seen" — older entries are
// eligible for pruning.
func NewDedupeCache(dbPath string, window time.Duration) (*DedupeCache, error) {
	if dbPath == "" {
		dbPath = ":memory:"
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("gitlab dedup: open db %q: %w", dbPath, err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS dedup (
			delivery_id TEXT PRIMARY KEY,
			received_at INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("gitlab dedup: create table: %w", err)
	}

	c := &DedupeCache{db: db, window: window}
	return c, nil
}

// IsDuplicate returns true when deliveryID was already seen within the dedup
// window. A false return means the caller should proceed to MarkSeen.
func (c *DedupeCache) IsDuplicate(ctx context.Context, deliveryID string) (bool, error) {
	cutoff := time.Now().Add(-c.window).Unix()
	var count int
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dedup WHERE delivery_id = ? AND received_at > ?`,
		deliveryID, cutoff,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("gitlab dedup: check duplicate: %w", err)
	}
	return count > 0, nil
}

// MarkSeen records deliveryID with the current timestamp. If deliveryID is
// already in the table (e.g. from a prior dedup window) it updates the
// received_at so the window slides forward from the latest delivery attempt.
func (c *DedupeCache) MarkSeen(ctx context.Context, deliveryID string) error {
	now := time.Now().Unix()
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO dedup (delivery_id, received_at) VALUES (?, ?)
		 ON CONFLICT(delivery_id) DO UPDATE SET received_at = excluded.received_at`,
		deliveryID, now,
	)
	if err != nil {
		return fmt.Errorf("gitlab dedup: mark seen: %w", err)
	}
	return nil
}

// Prune removes rows older than the dedup window. Called periodically by the
// background goroutine started in StartPruner.
func (c *DedupeCache) Prune(ctx context.Context) error {
	cutoff := time.Now().Add(-c.window).Unix()
	_, err := c.db.ExecContext(ctx, `DELETE FROM dedup WHERE received_at <= ?`, cutoff)
	if err != nil {
		return fmt.Errorf("gitlab dedup: prune: %w", err)
	}
	return nil
}

// StartPruner launches a background goroutine that calls Prune every window/4.
// Cancel ctx to stop it. Prune errors are logged to stderr (non-fatal).
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

// Close releases the underlying database connection.
func (c *DedupeCache) Close() error {
	return c.db.Close()
}
