package inbound

import (
	"fmt"
	"sync"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TokenStore holds the set of verified channel-name → signing-token
// pairs the receiver consults during request authentication. The store
// is intentionally minimal: a sync.RWMutex protecting a string map.
//
// Reload swaps the entire map atomically under the write lock so the
// IPC notifications.reloadTokens method can refresh credentials without
// restarting the binary.
type TokenStore struct {
	mu     sync.RWMutex
	tokens map[string]string
}

// NewTokenStore returns an empty store. Callers typically follow this
// with Reload(cfg) to populate from the loaded config.
func NewTokenStore() *TokenStore {
	return &TokenStore{tokens: make(map[string]string)}
}

// Get returns the resolved signing token for a channel and a bool
// indicating presence. The bool lets the caller distinguish "no such
// channel" from "channel with empty token" — the handler treats both
// uniformly as a 401, but downstream metrics may want to separate them.
func (s *TokenStore) Get(channel string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tok, ok := s.tokens[channel]
	return tok, ok
}

// Reload rebuilds the in-memory map from cfg.Notifiers.Mattermost. Each
// channel's TokenEnv is resolved against the current process env and
// the resulting plaintext token is stored. Channels with unset env vars
// are skipped with a returned (multi-)error so the operator can see
// which channels failed to load — but the store still swaps in the
// successfully-resolved entries so partial config does not produce a
// total outage.
func (s *TokenStore) Reload(cfg *config.Config) error {
	next := make(map[string]string)

	if cfg == nil || cfg.Notifiers == nil || cfg.Notifiers.Mattermost == nil {
		s.swap(next)
		return nil
	}

	var errs []error
	for name, ct := range cfg.Notifiers.Mattermost.Channels {
		if ct == nil {
			continue
		}
		tok, err := ct.ResolveToken()
		if err != nil {
			errs = append(errs, fmt.Errorf("channel %q: %w", name, err))
			continue
		}
		next[name] = tok
	}

	s.swap(next)

	if len(errs) > 0 {
		return joinErrors(errs)
	}
	return nil
}

// swap replaces the entire token map under the write lock.
func (s *TokenStore) swap(next map[string]string) {
	s.mu.Lock()
	s.tokens = next
	s.mu.Unlock()
}

// Len returns the number of channels currently registered. Useful for
// startup logging and tests.
func (s *TokenStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

// joinErrors collapses a slice of errors into a single error whose
// message lists each cause. Avoids a Go 1.20+ errors.Join dependency
// to keep the test surface small.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msg := "multiple channel token errors:"
	for _, e := range errs {
		msg += "\n  - " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
