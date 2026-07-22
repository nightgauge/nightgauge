package ipc

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// newLicenseTestServer builds a minimal Server sufficient for IpcLicenseChecker
// tests — Emit() only needs a writer and the methods map.
func newLicenseTestServer() *Server {
	return &Server{
		writer:  io.Discard,
		methods: make(map[string]Handler),
	}
}

func TestIpcLicenseChecker_CheckLicense_DeliversResult(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	go func() {
		// Give CheckLicense a moment to register the pending channel.
		time.Sleep(10 * time.Millisecond)
		if !checker.DeliverResult(LicenseCheckResult{
			IssueNumber: 42,
			Allowed:     true,
			Tier:        "pro",
			Status:      platform.LicenseStatusActive,
			CacheUntil:  time.Now().Add(5 * time.Minute).Format(time.RFC3339),
		}) {
			t.Error("DeliverResult should find the pending request")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := checker.CheckLicense(ctx, 42)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if !result.Allowed || result.Tier != "pro" || result.Status != platform.LicenseStatusActive {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestIpcLicenseChecker_DeliverResult_NoPending(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	if checker.DeliverResult(LicenseCheckResult{IssueNumber: 999, Allowed: true}) {
		t.Error("DeliverResult should return false when no CheckLicense is waiting")
	}
}

// TestIpcLicenseChecker_Timeout_NoPriorConfirmation is the pre-existing
// fail-open contract: with no prior confirmed status, a timeout degrades to
// community tier (allowed) exactly as before #4156.
func TestIpcLicenseChecker_Timeout_NoPriorConfirmation(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	// Context that's already expired — CheckLicense hits ctx.Done() immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	result, err := checker.CheckLicense(ctx, 1)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if !result.Allowed || result.Tier != "community" {
		t.Errorf("expected fail-open community result, got %+v", result)
	}
	if result.Status != "" {
		t.Errorf("expected empty status on generic timeout, got %q", result.Status)
	}
}

// TestIpcLicenseChecker_Timeout_AfterConfirmedRevoked is the core #4156
// regression guard: once TypeScript has confirmed the license is REVOKED, a
// later re-validation round-trip that merely times out (server unreachable)
// must NOT re-open the door by falling back to community tier. It fails
// CLOSED using the last confirmed status.
func TestIpcLicenseChecker_Timeout_AfterConfirmedRevoked(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	// Simulate an earlier successful round-trip that confirmed REVOKED —
	// deliver a result directly (as the pipeline.licenseResult IPC handler
	// would) with no CheckLicense currently waiting on it.
	checker.DeliverResult(LicenseCheckResult{
		IssueNumber: 7,
		Allowed:     false,
		Status:      platform.LicenseStatusRevoked,
		Reason:      "license revoked",
	})
	if got := checker.LastConfirmedStatus(); got != platform.LicenseStatusRevoked {
		t.Fatalf("LastConfirmedStatus() = %q, want revoked", got)
	}

	// Now a fresh re-validation round-trip that times out (no DeliverResult
	// call for THIS issue's channel — simulates an unreachable server).
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	result, err := checker.CheckLicense(ctx, 7)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if result.Allowed {
		t.Error("expected Allowed=false — a confirmed-revoked license must fail closed on timeout")
	}
	if result.Status != platform.LicenseStatusRevoked {
		t.Errorf("Status = %q, want revoked", result.Status)
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason explaining the fail-closed block")
	}
}

// TestIpcLicenseChecker_DeliveredDegradedResult_AfterConfirmedRevoked covers
// the OTHER #4156 fail-closed path: TS responds within Go's budget (so the
// select's ch branch wins, not ctx.Done()) but with a degraded, status-less
// result (its own inner platform-HTTP call failed — see
// LicensePreflight.handleError). This must still fail closed given a prior
// confirmed-revoked status, not just the outright-timeout case.
func TestIpcLicenseChecker_DeliveredDegradedResult_AfterConfirmedRevoked(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	// First round-trip: confirms REVOKED.
	checker.DeliverResult(LicenseCheckResult{
		IssueNumber: 11,
		Allowed:     false,
		Status:      platform.LicenseStatusRevoked,
	})

	// Second round-trip: TS responds promptly but degraded (no status —
	// e.g. its platform-HTTP call itself failed and it fell back to
	// community). Deliver concurrently with CheckLicense so the ch branch
	// wins the select.
	go func() {
		time.Sleep(10 * time.Millisecond)
		checker.DeliverResult(LicenseCheckResult{
			IssueNumber: 11,
			Allowed:     true,
			Tier:        "community",
			// Status intentionally empty — a degraded, unconfirmed result.
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := checker.CheckLicense(ctx, 11)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if result.Allowed {
		t.Error("expected Allowed=false — a degraded status-less result must not re-open a confirmed-revoked license")
	}
	if result.Status != platform.LicenseStatusRevoked {
		t.Errorf("Status = %q, want revoked (from the cached last-confirmed status)", result.Status)
	}
}

// TestIpcLicenseChecker_DeliveredReconfirmedActive_SupersedesPriorRevoked
// asserts a genuine NEW confirmation always wins over a stale cached one —
// the fail-closed guard only applies to AMBIGUOUS (status-less) results.
func TestIpcLicenseChecker_DeliveredReconfirmedActive_SupersedesPriorRevoked(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	checker.DeliverResult(LicenseCheckResult{
		IssueNumber: 12,
		Allowed:     false,
		Status:      platform.LicenseStatusRevoked,
	})

	go func() {
		time.Sleep(10 * time.Millisecond)
		// The user renewed/reactivated — the platform now confirms active.
		checker.DeliverResult(LicenseCheckResult{
			IssueNumber: 12,
			Allowed:     true,
			Tier:        "pro",
			Status:      platform.LicenseStatusActive,
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := checker.CheckLicense(ctx, 12)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if !result.Allowed || result.Status != platform.LicenseStatusActive {
		t.Errorf("expected a genuine reconfirmation to supersede the stale revoked cache, got %+v", result)
	}
}

// TestIpcLicenseChecker_Timeout_AfterConfirmedSuspended mirrors the revoked
// case for suspended — both are treated as CONFIRMED-bad statuses (#4156).
func TestIpcLicenseChecker_Timeout_AfterConfirmedSuspended(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	checker.DeliverResult(LicenseCheckResult{
		IssueNumber: 8,
		Allowed:     false,
		Status:      platform.LicenseStatusSuspended,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	result, err := checker.CheckLicense(ctx, 8)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if result.Allowed {
		t.Error("expected Allowed=false for a confirmed-suspended license on timeout")
	}
	if result.Status != platform.LicenseStatusSuspended {
		t.Errorf("Status = %q, want suspended", result.Status)
	}
}

// TestIpcLicenseChecker_Timeout_AfterConfirmedActive_StillFailsOpen asserts
// the "no false positives" half of #4156: a license that was last confirmed
// ACTIVE (or expired, or anything other than revoked/suspended) must keep
// degrading gracefully on a later timeout — a flaky connection must never
// falsely block a legitimately active license.
func TestIpcLicenseChecker_Timeout_AfterConfirmedActive_StillFailsOpen(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)

	checker.DeliverResult(LicenseCheckResult{
		IssueNumber: 9,
		Allowed:     true,
		Tier:        "pro",
		Status:      platform.LicenseStatusActive,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	result, err := checker.CheckLicense(ctx, 9)
	if err != nil {
		t.Fatalf("CheckLicense returned error: %v", err)
	}
	if !result.Allowed || result.Tier != "community" {
		t.Errorf("expected fail-open community degrade despite a prior active confirmation, got %+v", result)
	}
}

func TestIpcLicenseChecker_RegisterLicenseResultHandler_UnknownIssue(t *testing.T) {
	srv := newLicenseTestServer()
	checker := NewIpcLicenseChecker(srv)
	RegisterLicenseResultHandler(srv, checker)

	handler, ok := srv.methods["pipeline.licenseResult"]
	if !ok {
		t.Fatal("pipeline.licenseResult handler not registered")
	}
	_, err := handler(context.Background(), []byte(`{"issueNumber":123,"allowed":true}`))
	if err == nil {
		t.Error("expected an error for a licenseResult with no pending CheckLicense")
	}
}
