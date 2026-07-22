package gitlab

import (
	"context"
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

func TestForgeAdapter_LazyServiceConstruction(t *testing.T) {
	c := NewClient("", "tok")
	a := NewForgeAdapter(c)

	if a.issues != nil {
		t.Error("expected lazy issues field to start nil")
	}
	first := a.Issues()
	second := a.Issues()
	if first != second {
		t.Error("Issues() should cache the same instance")
	}
	if a.PRs() == nil {
		t.Error("PRs() returned nil")
	}
	if a.Project() == nil || a.Board() == nil || a.CI() == nil ||
		a.Labels() == nil || a.Rulesets() == nil || a.Auth() == nil {
		t.Error("expected non-nil stub services")
	}
}

func TestForgeRegistration_Active(t *testing.T) {
	client, err := forge.New(forge.Config{Kind: forge.KindGitLab, Token: "fake"})
	if err != nil {
		t.Fatalf("forge.New(gitlab): %v", err)
	}
	if client == nil {
		t.Fatal("nil client")
	}
}

func TestStubServices_ReturnUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	a := NewForgeAdapter(c)
	ctx := context.Background()

	// Sample one method per stub service to keep this tight; deeper coverage
	// for individual methods lives in their service tests. Project + Board
	// services are no longer stubs (landed in #3357); CI + Rulesets are no
	// longer stubs (landed in #3359) — exercise the still-stubbed methods.
	if err := a.Project().AddBlockedByNumber(ctx, "o", "r", 1, 2); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("Project.AddBlockedByNumber: %v", err)
	}
	if _, err := a.Project().UpdateEpicEstimates(ctx, "o", "r", 1); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("Project.UpdateEpicEstimates: %v", err)
	}
	if _, err := a.Labels().List(ctx); !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("Labels.List: %v", err)
	}
	// Auth is no longer a stub — real implementation landed in #3354.
	// Auth tests live in auth_test.go.
}

func TestForgeRegistration_AppliesProjectConfig(t *testing.T) {
	client, err := forge.New(forge.Config{
		Kind:                forge.KindGitLab,
		Token:               "tok",
		Owner:               "nightgauge/nightgauge",
		ProjectNumber:       7,
		BoardStatusStrategy: "state-only",
	})
	if err != nil {
		t.Fatalf("forge.New: %v", err)
	}
	a, ok := client.(*ForgeAdapter)
	if !ok {
		t.Fatalf("expected *ForgeAdapter, got %T", client)
	}
	if a.owner != "nightgauge" || a.repo != "nightgauge" {
		t.Errorf("owner/repo = %q/%q", a.owner, a.repo)
	}
	if a.boardID != 7 {
		t.Errorf("boardID = %d", a.boardID)
	}
	if a.strategy != StrategyStateOnly {
		t.Errorf("strategy = %q", a.strategy)
	}
}
