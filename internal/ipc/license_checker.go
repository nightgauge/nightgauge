package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// IpcLicenseChecker routes license validation through the TypeScript
// PlatformApiClient via the IPC pipeline.validateLicense event +
// pipeline.licenseResult request.
//
// Flow:
//  1. Go emits "pipeline.validateLicense" event with LicenseCheckRequest
//  2. TypeScript LicensePreflight validates via PlatformApiClient
//  3. TypeScript sends "pipeline.licenseResult" request with LicenseCheckResult
//  4. Go receives result and returns it to the scheduler
type IpcLicenseChecker struct {
	server *Server

	// pendingResults holds channels waiting for license results.
	// Keyed by issueNumber.
	pendingResults map[int]chan LicenseCheckResult
	mu             sync.Mutex

	// lastConfirmedStatus is the most recent DEFINITIVE status TypeScript
	// reported (i.e. an actual platform response, not a degraded/offline
	// fallback). Empty until the first confirmed result arrives. Used by
	// CheckLicense's timeout branch (#4156): a REVOKED/SUSPENDED license
	// confirmed moments ago must not silently fail open just because a
	// single re-validation round-trip timed out — that would defeat the
	// point of enforcement. Any other last-confirmed status (active,
	// expired, or none yet) preserves the original fail-open behavior so
	// flaky connectivity never falsely blocks a legitimately active license.
	lastConfirmedStatus string
}

// NewIpcLicenseChecker creates an IPC-backed license checker.
func NewIpcLicenseChecker(srv *Server) *IpcLicenseChecker {
	return &IpcLicenseChecker{
		server:         srv,
		pendingResults: make(map[int]chan LicenseCheckResult),
	}
}

// CheckLicense implements orchestrator.LicenseChecker by sending the request
// to TypeScript via IPC and waiting for the result.
func (c *IpcLicenseChecker) CheckLicense(ctx context.Context, issueNumber int) (*orchestrator.LicenseCheckResult, error) {
	// Create pending result channel
	ch := make(chan LicenseCheckResult, 1)
	c.mu.Lock()
	c.pendingResults[issueNumber] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pendingResults, issueNumber)
		c.mu.Unlock()
	}()

	// Emit event to TypeScript
	c.server.Emit("pipeline.validateLicense", LicenseCheckRequest{
		IssueNumber: issueNumber,
	})

	// Wait for result with context timeout
	select {
	case result := <-ch:
		// TypeScript delivered a result, but with no confirmed status of its
		// own (e.g. its inner platform-HTTP call itself degraded — see
		// LicensePreflight.handleError). That must not silently re-open a
		// PREVIOUSLY confirmed revoked/suspended license (#4156): apply the
		// same fail-closed guard as the ctx.Done() branch below. A result
		// that DOES carry a status (a genuine reconfirmation) always wins.
		if result.Status == "" {
			if blocked, ok := c.failClosedOnLastConfirmed(); ok {
				return blocked, nil
			}
		}
		return &orchestrator.LicenseCheckResult{
			Allowed:    result.Allowed,
			Tier:       result.Tier,
			Reason:     result.Reason,
			ActionURL:  result.ActionURL,
			CacheUntil: result.CacheUntil,
			Status:     result.Status,
		}, nil
	case <-ctx.Done():
		// Timeout / server unreachable for THIS round-trip. Distinguish a
		// CONFIRMED bad license from a merely-unavailable one (#4156):
		//   - last confirmed revoked/suspended → fail CLOSED. We know for a
		//     fact this license is bad; losing connectivity to re-confirm it
		//     must not re-open the door.
		//   - anything else (never confirmed, active, expired, or the status
		//     field wasn't populated) → fail OPEN exactly as before, so a
		//     flaky connection never falsely blocks a legitimately active
		//     license or breaks normal offline usability.
		if blocked, ok := c.failClosedOnLastConfirmed(); ok {
			return blocked, nil
		}
		return &orchestrator.LicenseCheckResult{
			Allowed: true,
			Tier:    "community",
		}, nil
	}
}

// failClosedOnLastConfirmed returns a blocking LicenseCheckResult (ok=true)
// when the last DEFINITIVE status Go has ever received for any license
// check is revoked/suspended. Shared by both the "TS delivered a degraded
// result" and "Go's own wait timed out" paths (#4156) — either way, Go has
// no fresher information than "this license was confirmed bad," so it must
// not treat the current ambiguity as an all-clear.
func (c *IpcLicenseChecker) failClosedOnLastConfirmed() (*orchestrator.LicenseCheckResult, bool) {
	c.mu.Lock()
	lastStatus := c.lastConfirmedStatus
	c.mu.Unlock()
	if lastStatus != platform.LicenseStatusRevoked && lastStatus != platform.LicenseStatusSuspended {
		return nil, false
	}
	return &orchestrator.LicenseCheckResult{
		Allowed: false,
		Tier:    "",
		Status:  lastStatus,
		Reason: fmt.Sprintf(
			"license was last confirmed %s; re-validation was inconclusive — blocking rather than re-opening a known-bad license",
			lastStatus,
		),
	}, true
}

// DeliverResult delivers a license result from TypeScript to the waiting
// CheckLicense call. Called by the pipeline.licenseResult IPC handler.
//
// Also records result.Status as the last CONFIRMED status (#4156) whenever
// TypeScript reports one — independent of whether the corresponding
// CheckLicense call is still waiting (a result can race a local timeout and
// still be the most up-to-date information Go has ever received).
func (c *IpcLicenseChecker) DeliverResult(result LicenseCheckResult) bool {
	c.mu.Lock()
	if result.Status != "" {
		c.lastConfirmedStatus = result.Status
	}
	ch, ok := c.pendingResults[result.IssueNumber]
	c.mu.Unlock()

	if !ok {
		return false // No pending request for this issue
	}

	// Non-blocking send (channel is buffered with capacity 1)
	select {
	case ch <- result:
		return true
	default:
		return false
	}
}

// LastConfirmedStatus returns the most recent definitive license status
// TypeScript has reported (empty until the first confirmed result arrives).
// Exposed for tests and diagnostics.
func (c *IpcLicenseChecker) LastConfirmedStatus() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastConfirmedStatus
}

// Verify IpcLicenseChecker implements the LicenseChecker interface at compile time.
var _ orchestrator.LicenseChecker = (*IpcLicenseChecker)(nil)

// RegisterLicenseResultHandler registers the pipeline.licenseResult IPC method
// on the given server, routing results to the IpcLicenseChecker.
func RegisterLicenseResultHandler(srv *Server, checker *IpcLicenseChecker) {
	srv.methods["pipeline.licenseResult"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p LicenseCheckResult
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if !checker.DeliverResult(p) {
			return nil, fmt.Errorf("no pending license check for #%d", p.IssueNumber)
		}
		return map[string]string{"status": "ok"}, nil
	}
}
