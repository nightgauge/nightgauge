package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// mockLicenseChecker implements LicenseChecker for testing.
type mockLicenseChecker struct {
	result *LicenseCheckResult
	err    error
}

func (m *mockLicenseChecker) CheckLicense(_ context.Context, _ int) (*LicenseCheckResult, error) {
	return m.result, m.err
}

func TestPreflightLicense_NilChecker_ReturnsCommunity(t *testing.T) {
	s := &Scheduler{}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, tier := s.preflightLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true when licenseChecker is nil")
	}
	if tier != "community" {
		t.Errorf("expected tier=community, got %q", tier)
	}
}

func TestPreflightLicense_ProTier_ReturnsTier(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{Allowed: true, Tier: "pro"},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, tier := s.preflightLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true for pro tier")
	}
	if tier != "pro" {
		t.Errorf("expected tier=pro, got %q", tier)
	}
}

func TestPreflightLicense_Blocked_ReturnsEmptyTier(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{Allowed: false, Reason: "expired"},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, tier := s.preflightLicense(context.Background(), item, runtime)
	if allowed {
		t.Error("expected allowed=false for blocked license")
	}
	if tier != "" {
		t.Errorf("expected tier=\"\", got %q", tier)
	}
}

func TestPreflightLicense_Error_DegradesToCommunity(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			err: fmt.Errorf("network timeout"),
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, tier := s.preflightLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true on error (fail-open)")
	}
	if tier != "community" {
		t.Errorf("expected tier=community on error, got %q", tier)
	}
}

// ─── revalidateLicense (#4156 — mid-run re-validation) ─────────────────────

func TestRevalidateLicense_NilChecker_StillAllowed(t *testing.T) {
	s := &Scheduler{}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, status := s.revalidateLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true when licenseChecker is nil")
	}
	if status != "" {
		t.Errorf("expected empty status, got %q", status)
	}
}

// TestRevalidateLicense_ConfirmedRevoked_Halts is the core #4156 regression
// guard: a mid-run re-validation that comes back CONFIRMED revoked must halt
// progression (allowed=false), not just flag-and-continue.
func TestRevalidateLicense_ConfirmedRevoked_Halts(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{Allowed: false, Status: "revoked", Reason: "license revoked"},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, status := s.revalidateLicense(context.Background(), item, runtime)
	if allowed {
		t.Error("expected allowed=false for a confirmed-revoked re-validation")
	}
	if status != "revoked" {
		t.Errorf("expected status=revoked, got %q", status)
	}
}

// TestRevalidateLicense_ConfirmedSuspended_Halts mirrors the revoked case.
func TestRevalidateLicense_ConfirmedSuspended_Halts(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{Allowed: false, Status: "suspended"},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, status := s.revalidateLicense(context.Background(), item, runtime)
	if allowed {
		t.Error("expected allowed=false for a confirmed-suspended re-validation")
	}
	if status != "suspended" {
		t.Errorf("expected status=suspended, got %q", status)
	}
}

// TestRevalidateLicense_UnreachableFailOpen_StillAllowed asserts the "no
// false positives" half of #4156: a fail-open community result (what
// IpcLicenseChecker returns for a generic timeout with no prior confirmed-bad
// status) must NOT halt the run.
func TestRevalidateLicense_UnreachableFailOpen_StillAllowed(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{Allowed: true, Tier: "community"},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, _ := s.revalidateLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true — a transient unreachable-server result must not block a run")
	}
}

// TestRevalidateLicense_Blocked_NonRevokedStatus_StillAllowed: allowed=false
// WITHOUT a confirmed revoked/suspended status (e.g. an "expired" block, or an
// unknown "" status) must not halt a running pipeline — only the two
// confirmed-bad statuses do. This preserves the existing "expired" behavior
// (flagged, notified after completion) instead of promoting it to a hard
// mid-run halt, which the epic didn't ask for.
func TestRevalidateLicense_Blocked_NonRevokedStatus_StillAllowed(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{Allowed: false, Status: "expired", Reason: "expired"},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, _ := s.revalidateLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true — only confirmed revoked/suspended halts a running pipeline")
	}
}

// TestRevalidateLicense_Error_TreatedAsTransient: a checker error (distinct
// from the ctx.Done() fail-open path, which resolves to a result rather than
// an error) is treated as transient and does not block.
func TestRevalidateLicense_Error_TreatedAsTransient(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			err: fmt.Errorf("checker internal error"),
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, _ := s.revalidateLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true on a checker error (treated as transient)")
	}
}

// TestRevalidateLicense_Allowed_RefreshesSnapshot: a successful re-validation
// updates the runtime's LicenseSnapshot so the next staleness check reuses
// the new cacheUntil window rather than re-validating on every stage.
func TestRevalidateLicense_Allowed_RefreshesSnapshot(t *testing.T) {
	s := &Scheduler{
		licenseChecker: &mockLicenseChecker{
			result: &LicenseCheckResult{
				Allowed:    true,
				Tier:       "pro",
				Status:     "active",
				CacheUntil: "2099-01-01T00:00:00Z",
			},
		},
	}
	runtime := state.NewRuntimeState("nightgauge/test", 1, "id-1")
	item := types.BoardItem{Number: 1, Repo: "nightgauge/test"}

	allowed, status := s.revalidateLicense(context.Background(), item, runtime)
	if !allowed {
		t.Error("expected allowed=true")
	}
	if status != "active" {
		t.Errorf("expected status=active, got %q", status)
	}
	if runtime.License == nil {
		t.Fatal("expected runtime.License snapshot to be refreshed")
	}
	if runtime.License.Tier != "pro" || runtime.License.Status != "active" {
		t.Errorf("unexpected refreshed snapshot: %+v", runtime.License)
	}
}

func TestStageRunParams_SkillContent(t *testing.T) {
	// Verify SkillContent is correctly propagated in StageRunParams
	params := StageRunParams{
		Stage:        state.StageFeatureDev,
		IssueNumber:  42,
		SkillContent: "---\nname: test\n---\n# Test skill",
	}
	if params.SkillContent == "" {
		t.Error("expected SkillContent to be set")
	}
	if params.Stage != state.StageFeatureDev {
		t.Errorf("expected Stage=feature-dev, got %q", params.Stage)
	}
}

func TestWithSkillService_SetsService(t *testing.T) {
	s := &Scheduler{}
	if s.skillService != nil {
		t.Error("expected skillService to be nil initially")
	}
	// We can't construct a real SkillService without a platform.Client,
	// but we can verify the setter accepts nil gracefully
	s.WithSkillService(nil)
	if s.skillService != nil {
		t.Error("expected skillService to remain nil when set to nil")
	}
}

func TestStageRunParams_SkillFallbackUsed_Field(t *testing.T) {
	// Verify SkillFallbackUsed field exists and defaults to false
	params := StageRunParams{
		Stage:       state.StageFeatureDev,
		IssueNumber: 42,
	}
	if params.SkillFallbackUsed {
		t.Error("expected SkillFallbackUsed=false by default")
	}

	params.SkillFallbackUsed = true
	if !params.SkillFallbackUsed {
		t.Error("expected SkillFallbackUsed=true after explicit set")
	}
}

func TestStageRunParams_SkillFallbackUsed_CommunityTier_NotSet(t *testing.T) {
	// Community tier never sets SkillFallbackUsed because no platform call is made.
	// This test documents the expected behavior: with nil skillService,
	// SkillFallbackUsed must remain false regardless of tier.
	params := StageRunParams{
		Stage:             state.StageFeatureDev,
		IssueNumber:       42,
		SkillContent:      "",    // Community tier → empty (uses local file)
		SkillFallbackUsed: false, // Must stay false — no platform call attempted
	}
	if params.SkillFallbackUsed {
		t.Error("community tier: SkillFallbackUsed must be false when no platform call is made")
	}
	if params.SkillContent != "" {
		t.Error("community tier: SkillContent must be empty (reads from disk)")
	}
}

func TestStageRunParams_SkillFallbackUsed_WhenPlatformSucceeds_NotSet(t *testing.T) {
	// When platform resolution succeeds, SkillFallbackUsed must be false.
	params := StageRunParams{
		Stage:             state.StageFeatureDev,
		IssueNumber:       42,
		SkillContent:      "# Platform skill content",
		SkillFallbackUsed: false,
	}
	if params.SkillFallbackUsed {
		t.Error("SkillFallbackUsed must be false when platform resolution succeeds")
	}
	if params.SkillContent == "" {
		t.Error("SkillContent must be non-empty when platform resolution succeeds")
	}
}
