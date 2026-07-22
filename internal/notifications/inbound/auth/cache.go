package auth

import (
	"fmt"
	"sync"
	"time"
)

const permCacheTTL = 5 * time.Minute

// cacheEntry holds a cached permission check result.
type cacheEntry struct {
	allowed  bool
	cachedAt time.Time
}

// PermissionCache stores TTL-bounded GitHub/GitLab repo permission check
// results. Key format: "provider:login:owner/repo". On cache miss AND API
// error, the caller must fail closed (deny) — the cache itself has no
// knowledge of errors; that logic lives in Authorizer.
type PermissionCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

// NewPermissionCache returns an empty permission cache.
func NewPermissionCache() *PermissionCache {
	return &PermissionCache{entries: make(map[string]cacheEntry)}
}

// Get returns (allowed, hit). A hit is only reported when the entry exists
// AND has not expired (cachedAt + permCacheTTL > now).
func (c *PermissionCache) Get(key string) (allowed bool, hit bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return false, false
	}
	if time.Since(e.cachedAt) >= permCacheTTL {
		return false, false
	}
	return e.allowed, true
}

// Set stores a permission result for the given key with the current timestamp.
func (c *PermissionCache) Set(key string, allowed bool) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{allowed: allowed, cachedAt: time.Now()}
	c.mu.Unlock()
}

// CacheKey returns a canonical cache key for a permission check.
func CacheKey(provider, login, repoSlug string) string {
	return fmt.Sprintf("%s:%s:%s", provider, login, repoSlug)
}
