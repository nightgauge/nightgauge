// Package gitlab — tests for the stub services that satisfy the aggregate
// forge.ForgeClient surface but defer real implementation to other tracking
// issues (#3358, #3354, #3361). Each test verifies that the method returns a
// wrapped forge.ErrUnsupported so callers can `errors.Is` and fall back.
//
// The contract these tests pin: until the tracking issue lands, callers MUST
// receive a sentinel-bearing error rather than a panic, nil, or a different
// error type.
package gitlab

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

func TestLabelService_List_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewLabelService(c)
	_, err := svc.List(context.Background())
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("LabelService.List: err = %v, want ErrUnsupported chain", err)
	}
	if !strings.Contains(err.Error(), "#3358") {
		t.Errorf("LabelService.List: err = %v, want tracking issue #3358 in message", err)
	}
}

func TestLabelService_Create_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewLabelService(c)
	_, err := svc.Create(context.Background(), "name", "desc", "#ff0000")
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("LabelService.Create: err = %v, want ErrUnsupported", err)
	}
}

func TestLabelService_Delete_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewLabelService(c)
	err := svc.Delete(context.Background(), "label-id")
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("LabelService.Delete: err = %v, want ErrUnsupported", err)
	}
}

// AuthAdapter tests moved to auth_test.go now that the stub is replaced by
// the real implementation from #3354. See TestPATCheckTokenScopes_AllPresent
// and TestWhoami_PAT in that file.

func TestRepoAdapter_RepoMetadata_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	r := NewRepoAdapter(c)
	_, err := r.RepoMetadata(context.Background(), "o", "r")
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("RepoAdapter.RepoMetadata: err = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "#3361") {
		t.Errorf("RepoAdapter.RepoMetadata: err = %v, want tracking issue #3361", err)
	}
}

// TestForgeAdapter_AccessorsReturnNonNil verifies the aggregate adapter
// surface returns non-nil services for every interface, including the stubbed
// ones. A nil accessor would NPE callers before they can fall back to the
// ErrUnsupported branch — which is the contract the stubs exist to honour.
func TestForgeAdapter_AccessorsReturnNonNil(t *testing.T) {
	c := NewClient("", "tok")
	a := NewForgeAdapter(c, WithProject("o", "r"))

	if a.Issues() == nil {
		t.Error("Issues() returned nil")
	}
	if a.PRs() == nil {
		t.Error("PRs() returned nil")
	}
	if a.Project() == nil {
		t.Error("Project() returned nil")
	}
	if a.Board() == nil {
		t.Error("Board() returned nil")
	}
	if a.CI() == nil {
		t.Error("CI() returned nil")
	}
	if a.Labels() == nil {
		t.Error("Labels() returned nil")
	}
	if a.Rulesets() == nil {
		t.Error("Rulesets() returned nil")
	}
	if a.Auth() == nil {
		t.Error("Auth() returned nil")
	}
	if a.Repo() == nil {
		t.Error("Repo() returned nil")
	}
}

// TestProjectService_AddBlockedByNumber_ReturnsErrUnsupported pins the stub
// status of the project-side blocking surface. The IssueService-side
// AddBlockedBy / RemoveBlockedBy was implemented in #3358; the project-keyed
// variants on ProjectService remain stubs until the same surface lands here.
func TestProjectService_AddBlockedByNumber_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	err := p.AddBlockedByNumber(context.Background(), "o", "r", 1, 2)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("ProjectService.AddBlockedByNumber: err = %v, want ErrUnsupported", err)
	}
}

func TestProjectService_RemoveBlockedByNumber_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	err := p.RemoveBlockedByNumber(context.Background(), "o", "r", 1, 2)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("ProjectService.RemoveBlockedByNumber: err = %v, want ErrUnsupported", err)
	}
}

func TestProjectService_UpdateEpicEstimates_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	_, err := p.UpdateEpicEstimates(context.Background(), "o", "r", 99)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("ProjectService.UpdateEpicEstimates: err = %v, want ErrUnsupported", err)
	}
}

// TestUnsupported_HelperWrapsSentinel verifies the small unsupported() helper
// produces an error whose chain includes both forge.ErrUnsupported and the
// caller-supplied tracking-issue identifier.
func TestUnsupported_HelperWrapsSentinel(t *testing.T) {
	err := unsupported("Foo.Bar", "#1234")
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("unsupported(): err = %v, want ErrUnsupported chain", err)
	}
	if !strings.Contains(err.Error(), "Foo.Bar") {
		t.Errorf("unsupported(): err = %v, want method name", err)
	}
	if !strings.Contains(err.Error(), "#1234") {
		t.Errorf("unsupported(): err = %v, want tracking issue", err)
	}
}
