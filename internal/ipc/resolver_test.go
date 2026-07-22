package ipc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// fakeDefaultClient returns a *gh.Client with a fake token for testing.
func fakeDefaultClient() *gh.Client {
	return gh.NewClientWithToken("fake-default-token")
}

func TestTokenFingerprint(t *testing.T) {
	t.Run("same token produces same fingerprint", func(t *testing.T) {
		fp1 := tokenFingerprint("my-secret-token")
		fp2 := tokenFingerprint("my-secret-token")
		if fp1 != fp2 {
			t.Errorf("same token produced different fingerprints: %q vs %q", fp1, fp2)
		}
	})

	t.Run("different tokens produce different fingerprints", func(t *testing.T) {
		fp1 := tokenFingerprint("token-a")
		fp2 := tokenFingerprint("token-b")
		if fp1 == fp2 {
			t.Errorf("different tokens produced same fingerprint: %q", fp1)
		}
	})

	t.Run("fingerprint is 8 hex chars", func(t *testing.T) {
		fp := tokenFingerprint("any-token")
		if len(fp) != 8 {
			t.Errorf("fingerprint length = %d, want 8", len(fp))
		}
		for _, c := range fp {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("fingerprint contains non-hex char: %c", c)
			}
		}
	})
}

func TestClientResolver_Resolve_NoRegistry(t *testing.T) {
	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false)

	client, err := resolver.Resolve(context.Background(), "some-owner", "some-repo")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if client != defaultClient {
		t.Error("expected default client when no registry entry exists")
	}
}

func TestClientResolver_RegisterAndResolve(t *testing.T) {
	// Create a temp workspace with a config that sets GITHUB_TOKEN
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Set GITHUB_TOKEN so ResolveTokenChain finds a token
	t.Setenv("GITHUB_TOKEN", "test-token-for-resolver")

	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false)
	resolver.RegisterRepo("test-org", "test-repo", workDir)

	client, err := resolver.Resolve(context.Background(), "test-org", "test-repo")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	// Should NOT be the default client (resolved a new one from config)
	if client == defaultClient {
		t.Error("expected a resolved client, got default client")
	}
}

func TestClientResolver_MtimeInvalidation(t *testing.T) {
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("GITHUB_TOKEN", "test-token-mtime")

	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false)
	resolver.RegisterRepo("test-org", "test-repo", workDir)

	// First resolve — populates cache
	client1, err := resolver.Resolve(context.Background(), "test-org", "test-repo")
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// Second resolve — should hit cache (same client pointer)
	client2, err := resolver.Resolve(context.Background(), "test-org", "test-repo")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if client1 != client2 {
		t.Error("expected cache hit (same client pointer) on second resolve")
	}

	// Mutate config file mtime — sleep to ensure time difference
	time.Sleep(10 * time.Millisecond)
	configYAML2 := "project:\n  owner: test-org\n  number: 2\n"
	if err := os.WriteFile(configPath, []byte(configYAML2), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	// Third resolve — mtime changed, should create new client
	client3, err := resolver.Resolve(context.Background(), "test-org", "test-repo")
	if err != nil {
		t.Fatalf("third Resolve: %v", err)
	}
	if client3 == client1 {
		t.Error("expected new client after config mtime change, got same pointer")
	}
}

func TestClientResolver_Invalidate(t *testing.T) {
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("project:\n  owner: o\n  number: 1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("GITHUB_TOKEN", "test-token-invalidate")

	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false)
	resolver.RegisterRepo("o", "r", workDir)

	// Populate cache
	client1, _ := resolver.Resolve(context.Background(), "o", "r")

	// Invalidate
	resolver.Invalidate("o", "r")

	// Next resolve should create a new client
	client2, _ := resolver.Resolve(context.Background(), "o", "r")
	if client2 == client1 {
		t.Error("expected new client after Invalidate(), got same pointer")
	}
}

func TestClientResolver_ConcurrentResolve(t *testing.T) {
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("project:\n  owner: o\n  number: 1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("GITHUB_TOKEN", "test-token-concurrent")

	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false)
	resolver.RegisterRepo("o", "r", workDir)

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := resolver.Resolve(context.Background(), "o", "r")
			if err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Resolve error: %v", err)
	}
}

func TestClientResolver_AttachesTrackerToResolvedClient(t *testing.T) {
	// Issue #3417: per-repo clients constructed by the resolver must feed
	// the SharedRateLimitTracker so HTTP response headers refresh the shared
	// file and the proactive gate can fire on per-repo calls.
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configYAML := "project:\n  owner: test-org\n  number: 1\ngithub_user: alice\ngithub_auth:\n  tokens:\n    test-org: test-token-tracker-wire\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("GITHUB_TOKEN", "test-token-tracker-wire")

	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	defaultClient := fakeDefaultClient()
	resolver := NewClientResolverWithTracker(defaultClient, false, tracker)
	resolver.RegisterRepo("test-org", "test-repo", workDir)

	client, err := resolver.Resolve(context.Background(), "test-org", "test-repo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := client.RateLimitTracker(); got != tracker {
		t.Fatalf("resolved client tracker = %v, want %v", got, tracker)
	}
	if got, want := client.RateLimitTrackerUser(), "alice"; got != want {
		t.Errorf("resolved client tracker user = %q, want %q", got, want)
	}
}

func TestClientResolver_NoTrackerWhenConstructedWithoutOne(t *testing.T) {
	// Backward compat: NewClientResolver (no tracker variant) must not attach
	// a tracker to resolved clients.
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("project:\n  owner: o\n  number: 1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("GITHUB_TOKEN", "test-token-no-tracker")

	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false) // legacy constructor — no tracker
	resolver.RegisterRepo("o", "r", workDir)

	client, err := resolver.Resolve(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if client.RateLimitTracker() != nil {
		t.Error("expected nil tracker when resolver was constructed without one")
	}
}

func TestClientResolver_FallbackOnConfigLoadError(t *testing.T) {
	// Register a path that doesn't have a config directory
	workDir := t.TempDir()
	// No .nightgauge/config.yaml — config.Load will return defaults
	// but the config.yaml file won't exist, so mtime will be zero time

	defaultClient := fakeDefaultClient()
	resolver := NewClientResolver(defaultClient, false)
	resolver.RegisterRepo("o", "r", workDir)

	// Set GITHUB_TOKEN so token chain can resolve
	t.Setenv("GITHUB_TOKEN", "test-token-fallback")

	client, err := resolver.Resolve(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	// With no config.yaml, config.Load returns defaults (Owner=nightgauge).
	// ResolveTokenChain still finds GITHUB_TOKEN, so a new client is created.
	// This is correct behavior — the resolver doesn't fall back to default
	// when a path IS registered but config load succeeds with defaults.
	if client == nil {
		t.Error("expected non-nil client")
	}
}
