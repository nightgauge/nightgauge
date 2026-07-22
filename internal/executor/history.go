// Package executor — history store for remote command execution records.
package executor

import (
	"sync"
	"time"
)

// historyCapacity is the maximum number of entries retained in the circular buffer.
const historyCapacity = 50

// CommandHistoryEntry records the outcome of a single dispatched command.
type CommandHistoryEntry struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Status      string     `json:"status"` // "success" | "failure" | "pending"
	ReceivedAt  time.Time  `json:"receivedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	DurationMs  int64      `json:"durationMs,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// CommandPollingStatus reflects the current remote-command polling state.
type CommandPollingStatus struct {
	Active       bool       `json:"active"`
	LastPolledAt *time.Time `json:"lastPolledAt,omitempty"`
	PendingCount int        `json:"pendingCount"`
	ErrorCount   int        `json:"errorCount"`
}

// historyStore is a bounded circular buffer of CommandHistoryEntry values.
// All methods are safe for concurrent use.
type historyStore struct {
	mu      sync.RWMutex
	entries []CommandHistoryEntry

	// polling state
	active       bool
	lastPolledAt *time.Time
	pendingCount int
	errorCount   int
}

func newHistoryStore() *historyStore {
	return &historyStore{
		entries: make([]CommandHistoryEntry, 0, historyCapacity),
	}
}

// add appends an entry, evicting the oldest when at capacity.
func (h *historyStore) add(e CommandHistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.entries) >= historyCapacity {
		h.entries = h.entries[1:]
	}
	h.entries = append(h.entries, e)
}

// getAll returns a copy of all entries (oldest-first).
func (h *historyStore) getAll() []CommandHistoryEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]CommandHistoryEntry, len(h.entries))
	copy(out, h.entries)
	return out
}

// setPollingStatus updates the polling state fields atomically.
func (h *historyStore) setPollingStatus(active bool, lastPolledAt *time.Time, pendingCount, errorCount int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active = active
	h.lastPolledAt = lastPolledAt
	h.pendingCount = pendingCount
	h.errorCount = errorCount
}

// getPollingStatus returns the current polling state.
func (h *historyStore) getPollingStatus() CommandPollingStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return CommandPollingStatus{
		Active:       h.active,
		LastPolledAt: h.lastPolledAt,
		PendingCount: h.pendingCount,
		ErrorCount:   h.errorCount,
	}
}
