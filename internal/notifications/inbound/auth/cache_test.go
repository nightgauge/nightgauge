package auth

import (
	"testing"
	"time"
)

func TestPermissionCache_Hit(t *testing.T) {
	c := NewPermissionCache()
	key := CacheKey("github", "alice", "owner/repo")
	c.Set(key, true)

	allowed, hit := c.Get(key)
	if !hit {
		t.Fatal("expected cache hit")
	}
	if !allowed {
		t.Error("expected allowed=true")
	}
}

func TestPermissionCache_Miss(t *testing.T) {
	c := NewPermissionCache()
	_, hit := c.Get("nonexistent")
	if hit {
		t.Error("expected cache miss for unknown key")
	}
}

func TestPermissionCache_Expired(t *testing.T) {
	c := NewPermissionCache()
	key := CacheKey("github", "bob", "owner/repo")

	// Manually insert an expired entry.
	c.mu.Lock()
	c.entries[key] = cacheEntry{
		allowed:  true,
		cachedAt: time.Now().Add(-(permCacheTTL + time.Second)),
	}
	c.mu.Unlock()

	_, hit := c.Get(key)
	if hit {
		t.Error("expected cache miss for expired entry")
	}
}

func TestPermissionCache_DenyStored(t *testing.T) {
	c := NewPermissionCache()
	key := CacheKey("github", "charlie", "owner/repo")
	c.Set(key, false)

	allowed, hit := c.Get(key)
	if !hit {
		t.Fatal("expected cache hit")
	}
	if allowed {
		t.Error("expected allowed=false (denied result must be cacheable)")
	}
}

func TestCacheKey(t *testing.T) {
	key := CacheKey("github", "alice", "myorg/myrepo")
	if key != "github:alice:myorg/myrepo" {
		t.Errorf("unexpected cache key: %q", key)
	}
}
