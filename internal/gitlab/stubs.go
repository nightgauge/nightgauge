package gitlab

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// This file holds the stub services required to satisfy the aggregate
// forge.ForgeClient interface. Each method returns forge.ErrUnsupported
// (wrapped via %w with a tracking-issue reference) so callers can use
// errors.Is to fall back to a code path that does not require GitLab
// support yet.
//
// Tracking issues:
//   - #3357 — Boards / Iteration / Weight / Health  (LANDED — see board.go, project.go)
//   - #3358 — Sub-issues + blocking + labels CRUD
//   - #3359 — Pipeline status + branch protection (LANDED — see ci.go, rulesets.go)
//   - #3354 — Auth chain (LANDED — see auth.go)
//   - #343  — Dependency security alerts

// --- LabelService stub ---

type LabelService struct{ client *Client }

func NewLabelService(client *Client) *LabelService { return &LabelService{client: client} }

func (l *LabelService) List(ctx context.Context) ([]*forgetypes.Label, error) {
	return nil, unsupported("LabelService.List", "#3358")
}
func (l *LabelService) Create(ctx context.Context, name, description, color string) (*forgetypes.Label, error) {
	return nil, unsupported("LabelService.Create", "#3358")
}
func (l *LabelService) Delete(ctx context.Context, labelID string) error {
	return unsupported("LabelService.Delete", "#3358")
}

// --- RepoService stub (placeholder until a GitLab project metadata adapter lands) ---

// RepoAdapter is the placeholder forge.RepoService for GitLab. The real
// implementation lands alongside the multi-repo workspace resolver (#3361)
// and the auth chain (#3354) — when there is enough surface to fetch a
// canonical project path from GitLab's projects API.
type RepoAdapter struct{ client *Client }

func NewRepoAdapter(client *Client) *RepoAdapter { return &RepoAdapter{client: client} }

func (r *RepoAdapter) RepoMetadata(ctx context.Context, owner, name string) (*forgetypes.Repo, error) {
	return nil, unsupported("RepoService.RepoMetadata", "#3361")
}

// --- SecurityService stub ---

// SecurityAdapter is the placeholder forge.SecurityService for GitLab (#343).
// GitLab's dependency scanning reports findings through a fundamentally
// different surface (a CI job artifact and the Vulnerability Report, rather
// than an always-on per-repository alert feed), so mapping it onto
// forgetypes.SecurityAlerts is real work, not a rename.
//
// Returning the wrapped sentinel keeps the capability visible instead of
// designing it out: a caller can errors.Is its way to a GitLab-shaped path the
// moment one exists, and the coverage producers can tell "this forge does not
// support alerts" apart from "this repository has them switched off" — which
// is exactly the distinction SecurityAlertsStatus draws for GitHub.
type SecurityAdapter struct{ client *Client }

func NewSecurityAdapter(client *Client) *SecurityAdapter { return &SecurityAdapter{client: client} }

func (s *SecurityAdapter) ListOpenAlerts(ctx context.Context, owner, name string) (*forgetypes.SecurityAlerts, error) {
	return nil, unsupported("SecurityService.ListOpenAlerts", "#343")
}

// unsupported builds a wrapped ErrUnsupported for stub methods, including
// the tracking issue so contributors know where the implementation lands.
func unsupported(method, trackingIssue string) error {
	return fmt.Errorf("gitlab.%s: %w (tracked: %s)", method, forge.ErrUnsupported, trackingIssue)
}
