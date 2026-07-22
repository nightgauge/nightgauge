package inbound

import (
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

func TestTokenStore_ReloadEmpty(t *testing.T) {
	store := NewTokenStore()
	if err := store.Reload(nil); err != nil {
		t.Fatalf("Reload(nil) error: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("expected empty store, got %d", store.Len())
	}
	if _, ok := store.Get("dev"); ok {
		t.Fatalf("Get on empty store returned ok=true")
	}
}

func TestTokenStore_ReloadFromConfig(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret-1234")
	t.Setenv("TEST_OPS_TOKEN", "ops-secret-5678")

	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
					"ops": {TokenEnv: "TEST_OPS_TOKEN"},
				},
			},
		},
	}

	store := NewTokenStore()
	if err := store.Reload(cfg); err != nil {
		t.Fatalf("Reload error: %v", err)
	}
	if got, ok := store.Get("dev"); !ok || got != "dev-secret-1234" {
		t.Fatalf("dev token = (%q, %v), want (dev-secret-1234, true)", got, ok)
	}
	if got, ok := store.Get("ops"); !ok || got != "ops-secret-5678" {
		t.Fatalf("ops token = (%q, %v), want (ops-secret-5678, true)", got, ok)
	}
}

func TestTokenStore_ReloadMissingEnvKeepsOthers(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	// Intentionally do not set TEST_MISSING_TOKEN.

	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev":     {TokenEnv: "TEST_DEV_TOKEN"},
					"missing": {TokenEnv: "TEST_MISSING_TOKEN"},
				},
			},
		},
	}

	store := NewTokenStore()
	err := store.Reload(cfg)
	if err == nil {
		t.Fatalf("expected error for missing env var, got nil")
	}
	if got, ok := store.Get("dev"); !ok || got != "dev-secret" {
		t.Fatalf("dev token = (%q, %v), want partial-success population", got, ok)
	}
	if _, ok := store.Get("missing"); ok {
		t.Fatalf("missing channel should not be in the store")
	}
}

func TestTokenStore_ReloadAtomicSwap(t *testing.T) {
	t.Setenv("TEST_TOKEN_A", "old-token")

	cfg1 := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_TOKEN_A"},
				},
			},
		},
	}

	store := NewTokenStore()
	if err := store.Reload(cfg1); err != nil {
		t.Fatalf("Reload cfg1: %v", err)
	}

	t.Setenv("TEST_TOKEN_B", "new-token")
	cfg2 := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"prod": {TokenEnv: "TEST_TOKEN_B"},
				},
			},
		},
	}

	if err := store.Reload(cfg2); err != nil {
		t.Fatalf("Reload cfg2: %v", err)
	}
	// The old "dev" channel should be gone; only "prod" remains.
	if _, ok := store.Get("dev"); ok {
		t.Fatalf("dev still present after reload — atomic swap failed")
	}
	if got, ok := store.Get("prod"); !ok || got != "new-token" {
		t.Fatalf("prod token = (%q, %v), want (new-token, true)", got, ok)
	}
}

// TestTokenStore_ConcurrentGetReload exercises the RWMutex with concurrent
// readers and writers under -race. It is the load-bearing test for the
// thread-safety claim made by TokenStore.
func TestTokenStore_ConcurrentGetReload(t *testing.T) {
	t.Setenv("TEST_RACE_TOKEN", "race-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"race": {TokenEnv: "TEST_RACE_TOKEN"},
				},
			},
		},
	}

	store := NewTokenStore()
	if err := store.Reload(cfg); err != nil {
		t.Fatalf("initial reload: %v", err)
	}

	const goroutines = 16
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if id%2 == 0 {
					_, _ = store.Get("race")
				} else {
					_ = store.Reload(cfg)
				}
			}
		}(i)
	}

	wg.Wait()

	if got, ok := store.Get("race"); !ok || got != "race-secret" {
		t.Fatalf("after concurrent ops, race token = (%q, %v)", got, ok)
	}
}
