// Package auth provides per-user authorization for inbound Mattermost commands.
// It maps Mattermost user IDs to GitHub/GitLab identities (UserMappingStore),
// caches permission checks (PermissionCache), logs decisions (AuditWriter),
// and orchestrates the full authorization flow (Authorizer).
package auth

import (
	"sync"

	"github.com/nightgauge/nightgauge/internal/config"
)

// UserMappingStore holds the set of Mattermost user ID → GitHub/GitLab identity
// mappings loaded from config. The store is intentionally minimal: a sync.RWMutex
// protecting a map keyed by MattermostUserID.
//
// Reload swaps the entire map atomically under the write lock so the IPC
// notifications.reloadTokens method can refresh mappings without restarting.
type UserMappingStore struct {
	mu       sync.RWMutex
	mappings map[string]config.UserMappingEntry
}

// NewUserMappingStore returns an empty store. Callers typically follow this
// with Reload(cfg) to populate from the loaded config.
func NewUserMappingStore() *UserMappingStore {
	return &UserMappingStore{mappings: make(map[string]config.UserMappingEntry)}
}

// Get returns the UserMappingEntry for a Mattermost user ID and a bool
// indicating presence. The bool lets the caller distinguish unmapped users
// from mapped users with an empty identity.
func (s *UserMappingStore) Get(mattermostUserID string) (config.UserMappingEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.mappings[mattermostUserID]
	return entry, ok
}

// Reload rebuilds the in-memory map from cfg.Users. Any previous mappings
// are discarded atomically under the write lock.
func (s *UserMappingStore) Reload(cfg *config.Config) {
	next := make(map[string]config.UserMappingEntry)
	if cfg != nil {
		for _, entry := range cfg.Users {
			if entry.MattermostUserID != "" {
				next[entry.MattermostUserID] = entry
			}
		}
	}
	s.mu.Lock()
	s.mappings = next
	s.mu.Unlock()
}

// Len returns the number of user mappings currently loaded.
func (s *UserMappingStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mappings)
}
