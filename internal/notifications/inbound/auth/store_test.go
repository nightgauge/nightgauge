package auth

import (
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

func TestUserMappingStore_GetEmpty(t *testing.T) {
	s := NewUserMappingStore()
	_, ok := s.Get("U123")
	if ok {
		t.Error("expected Get to return false on empty store")
	}
}

func TestUserMappingStore_Reload_Get(t *testing.T) {
	s := NewUserMappingStore()
	cfg := &config.Config{
		Users: []config.UserMappingEntry{
			{MattermostUserID: "U123", GitHubLogin: "alice"},
			{MattermostUserID: "U456", GitLabUsername: "bob-gl"},
		},
	}
	s.Reload(cfg)

	entry, ok := s.Get("U123")
	if !ok {
		t.Fatal("expected U123 to be present")
	}
	if entry.GitHubLogin != "alice" {
		t.Errorf("expected GitHubLogin=alice, got %q", entry.GitHubLogin)
	}

	entry, ok = s.Get("U456")
	if !ok {
		t.Fatal("expected U456 to be present")
	}
	if entry.GitLabUsername != "bob-gl" {
		t.Errorf("expected GitLabUsername=bob-gl, got %q", entry.GitLabUsername)
	}

	_, ok = s.Get("U999")
	if ok {
		t.Error("expected unknown user to return false")
	}
}

func TestUserMappingStore_Reload_AtomicSwap(t *testing.T) {
	s := NewUserMappingStore()

	cfg1 := &config.Config{
		Users: []config.UserMappingEntry{
			{MattermostUserID: "U1", GitHubLogin: "user1"},
		},
	}
	s.Reload(cfg1)
	if s.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", s.Len())
	}

	// Reload with different config — old entry must be gone.
	cfg2 := &config.Config{
		Users: []config.UserMappingEntry{
			{MattermostUserID: "U2", GitHubLogin: "user2"},
			{MattermostUserID: "U3", GitHubLogin: "user3"},
		},
	}
	s.Reload(cfg2)
	if s.Len() != 2 {
		t.Fatalf("expected 2 entries after reload, got %d", s.Len())
	}
	if _, ok := s.Get("U1"); ok {
		t.Error("U1 should have been removed by second Reload")
	}
}

func TestUserMappingStore_Reload_NilConfig(t *testing.T) {
	s := NewUserMappingStore()
	cfg := &config.Config{
		Users: []config.UserMappingEntry{
			{MattermostUserID: "U1", GitHubLogin: "user1"},
		},
	}
	s.Reload(cfg)
	s.Reload(nil) // nil config clears all entries
	if s.Len() != 0 {
		t.Errorf("expected 0 entries after nil reload, got %d", s.Len())
	}
}

func TestUserMappingStore_ConcurrentAccess(t *testing.T) {
	s := NewUserMappingStore()
	cfg := &config.Config{
		Users: []config.UserMappingEntry{
			{MattermostUserID: "U1", GitHubLogin: "alice"},
		},
	}
	s.Reload(cfg)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Get("U1")
		}()
		go func() {
			defer wg.Done()
			s.Reload(cfg)
		}()
	}
	wg.Wait()
}
