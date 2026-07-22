package auth

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// mockChecker implements RepoPermissionChecker for testing.
type mockChecker struct {
	allowed bool
	err     error
	calls   int
}

func (m *mockChecker) HasWriteAccess(_ context.Context, _, _, _ string) (bool, error) {
	m.calls++
	return m.allowed, m.err
}

func makeAuthorizer(users []config.UserMappingEntry, checker RepoPermissionChecker) *Authorizer {
	store := NewUserMappingStore()
	store.Reload(&config.Config{Users: users})
	cache := NewPermissionCache()
	dir, _ := os.MkdirTemp("", "auth-audit-test-*")
	audit := NewAuditWriter(dir)
	return NewAuthorizer(store, cache, audit, checker)
}

func TestAuthorizer_Unmapped(t *testing.T) {
	a := makeAuthorizer(nil, &mockChecker{allowed: true})
	result := a.Authorize(context.Background(), "U_UNKNOWN", "C1", "run", "owner/repo")
	if result.Allowed {
		t.Error("unmapped user should be denied")
	}
	if result.Reason != "unmapped" {
		t.Errorf("expected reason=unmapped, got %q", result.Reason)
	}
	if result.MappedIdentity != "" {
		t.Errorf("expected empty identity for unmapped user, got %q", result.MappedIdentity)
	}
}

func TestAuthorizer_MappedReadTier(t *testing.T) {
	checker := &mockChecker{}
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitHubLogin: "alice"},
	}, checker)

	for _, cmd := range []string{"status", "health", "queue.list", "help"} {
		result := a.Authorize(context.Background(), "U1", "C1", cmd, "owner/repo")
		if !result.Allowed {
			t.Errorf("cmd %q: expected allowed for read-tier mapped user", cmd)
		}
		if checker.calls > 0 {
			t.Errorf("cmd %q: read-tier should not call permission checker", cmd)
		}
	}
}

func TestAuthorizer_MappedWriteTierAllowed(t *testing.T) {
	checker := &mockChecker{allowed: true}
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitHubLogin: "alice"},
	}, checker)

	result := a.Authorize(context.Background(), "U1", "C1", "run", "owner/repo")
	if !result.Allowed {
		t.Errorf("expected allowed for write-tier with write access; reason: %s", result.Reason)
	}
	if checker.calls != 1 {
		t.Errorf("expected 1 API call, got %d", checker.calls)
	}
}

func TestAuthorizer_MappedWriteTierDenied(t *testing.T) {
	checker := &mockChecker{allowed: false}
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitHubLogin: "alice"},
	}, checker)

	result := a.Authorize(context.Background(), "U1", "C1", "stop", "owner/repo")
	if result.Allowed {
		t.Error("expected denied for user without write access")
	}
}

func TestAuthorizer_CacheHit(t *testing.T) {
	checker := &mockChecker{allowed: true}
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitHubLogin: "alice"},
	}, checker)

	// First call — API hit.
	a.Authorize(context.Background(), "U1", "C1", "run", "owner/repo")
	// Second call — should be cache hit.
	a.Authorize(context.Background(), "U1", "C1", "run", "owner/repo")

	if checker.calls != 1 {
		t.Errorf("expected 1 API call total (cache hit on second call), got %d", checker.calls)
	}
}

func TestAuthorizer_CacheMiss_APIError_FailClosed(t *testing.T) {
	checker := &mockChecker{err: errors.New("network error")}
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitHubLogin: "alice"},
	}, checker)

	result := a.Authorize(context.Background(), "U1", "C1", "run", "owner/repo")
	if result.Allowed {
		t.Error("expected fail-closed on API error")
	}
}

func TestAuthorizer_ReadTierSkipsPermissionCheck(t *testing.T) {
	checker := &mockChecker{allowed: false} // would deny if called
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitHubLogin: "alice"},
	}, checker)

	result := a.Authorize(context.Background(), "U1", "C1", "health", "owner/repo")
	if !result.Allowed {
		t.Error("health (read-tier) should bypass permission check and be allowed")
	}
	if checker.calls != 0 {
		t.Error("permission checker should not be called for read-tier commands")
	}
}

func TestAuthorizer_GitLabOnlyUserDeniedWrite(t *testing.T) {
	checker := &mockChecker{allowed: true}
	a := makeAuthorizer([]config.UserMappingEntry{
		{MattermostUserID: "U1", GitLabUsername: "alice-gl"},
	}, checker)

	// GitLab-only user on write-tier command is denied — stub fail-closed.
	result := a.Authorize(context.Background(), "U1", "C1", "run", "owner/repo")
	if result.Allowed {
		t.Error("expected deny for GitLab-only user on write command (stub not implemented)")
	}
	if checker.calls != 0 {
		t.Error("GitHub checker should not be called for GitLab-only user")
	}
}
