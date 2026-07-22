package forge_test

import (
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"

	// Side-effect imports: register the github and gitlab adapters.
	_ "github.com/nightgauge/nightgauge/internal/github"
	_ "github.com/nightgauge/nightgauge/internal/gitlab"
)

func TestNew_GitHub_ReturnsForgeClient(t *testing.T) {
	client, err := forge.New(forge.Config{
		Kind:          forge.KindGitHub,
		Token:         "fake-token",
		Owner:         "nightgauge",
		ProjectNumber: 1,
	})
	if err != nil {
		t.Fatalf("forge.New(github): unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("forge.New(github): returned nil ForgeClient")
	}

	// Verify all accessors return non-nil services.
	if client.Issues() == nil {
		t.Error("Issues() returned nil")
	}
	if client.PRs() == nil {
		t.Error("PRs() returned nil")
	}
	if client.Project() == nil {
		t.Error("Project() returned nil")
	}
	if client.Board() == nil {
		t.Error("Board() returned nil")
	}
	if client.CI() == nil {
		t.Error("CI() returned nil")
	}
	if client.Labels() == nil {
		t.Error("Labels() returned nil")
	}
	if client.Rulesets() == nil {
		t.Error("Rulesets() returned nil")
	}
	if client.Auth() == nil {
		t.Error("Auth() returned nil")
	}
}

func TestNew_GitLab_ReturnsForgeClient(t *testing.T) {
	client, err := forge.New(forge.Config{
		Kind:  forge.KindGitLab,
		Token: "fake-token",
		Owner: "nightgauge",
	})
	if err != nil {
		t.Fatalf("forge.New(gitlab): unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("forge.New(gitlab): returned nil ForgeClient")
	}

	// Verify all accessors return non-nil services. Stubbed services
	// must still be addressable so callers can hand them through the
	// interface even when methods return ErrUnsupported.
	if client.Issues() == nil {
		t.Error("Issues() returned nil")
	}
	if client.PRs() == nil {
		t.Error("PRs() returned nil")
	}
	if client.Project() == nil {
		t.Error("Project() returned nil")
	}
	if client.Board() == nil {
		t.Error("Board() returned nil")
	}
	if client.CI() == nil {
		t.Error("CI() returned nil")
	}
	if client.Labels() == nil {
		t.Error("Labels() returned nil")
	}
	if client.Rulesets() == nil {
		t.Error("Rulesets() returned nil")
	}
	if client.Auth() == nil {
		t.Error("Auth() returned nil")
	}
}

func TestNew_UnknownKind_ReturnsErrUnsupported(t *testing.T) {
	_, err := forge.New(forge.Config{
		Kind: forge.Kind("bitbucket"),
	})
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("forge.New(bitbucket): expected errors.Is(err, ErrUnsupported), got %v", err)
	}
}

func TestNew_EmptyKind_ReturnsErrUnsupported(t *testing.T) {
	_, err := forge.New(forge.Config{})
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("forge.New({}): expected errors.Is(err, ErrUnsupported), got %v", err)
	}
}

func TestRegisterAdapter_OverridesExistingKind(t *testing.T) {
	// Register a fake adapter for an arbitrary kind, then override and
	// confirm the override wins. This exercises the dispatch table without
	// depending on real adapter behavior.
	const fakeKind forge.Kind = "test-fake"

	calls := 0
	forge.RegisterAdapter(fakeKind, func(cfg forge.Config) (forge.ForgeClient, error) {
		calls++
		return nil, nil
	})
	if _, _ = forge.New(forge.Config{Kind: fakeKind}); calls != 1 {
		t.Errorf("after first register, expected 1 factory call, got %d", calls)
	}

	overridden := 0
	forge.RegisterAdapter(fakeKind, func(cfg forge.Config) (forge.ForgeClient, error) {
		overridden++
		return nil, nil
	})
	if _, _ = forge.New(forge.Config{Kind: fakeKind}); overridden != 1 {
		t.Errorf("after override, expected 1 override call, got %d", overridden)
	}
}
