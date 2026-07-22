/**
 * Tests for the Retention & Integrity panel in AuditTabHtml (Issue #3323)
 *
 * Covers:
 * 1. No retentionData → panel is absent from output
 * 2. hasAccess=false → locked panel with enterprise upgrade message
 * 3. isLoading=true → loading placeholder shown
 * 4. Default retention value (730 days) rendered in input
 * 5. Custom retention value rendered in input
 * 6. integrity result valid badge rendered
 * 7. integrity result invalid badge rendered
 * 8. isVerifying=true → buttons disabled, spinner visible
 * 9. errorMessage → error banner shown
 * 10. XSS: user-controlled fields are escaped
 * 11. getAuditTabScript → contains retention message types
 * 12. getAuditTabStyles → contains retention CSS classes
 */

import { describe, it, expect } from "vitest";
import {
  getAuditTabHtml,
  getAuditTabScript,
  getAuditTabStyles,
} from "../../../../src/views/dashboard/tabs/AuditTabHtml";
import type {
  AuditLogData,
  RetentionIntegrityData,
} from "../../../../src/views/dashboard/DashboardState";
import type { RetentionConfig, IntegrityResult } from "../../../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeAuditData(overrides: Partial<AuditLogData> = {}): AuditLogData {
  return {
    entries: [],
    filters: { dateFrom: "", dateTo: "", actionFilter: "", userFilter: "" },
    pagination: { page: 0, pageSize: 20, totalCount: 0, hasPrevPage: false, hasNextPage: false },
    isLoading: false,
    hasAccess: true,
    ...overrides,
  };
}

function makeRetentionConfig(overrides: Partial<RetentionConfig> = {}): RetentionConfig {
  return { retentionDays: 730, updatedAt: "2026-01-01T00:00:00Z", ...overrides };
}

function makeIntegrityResult(overrides: Partial<IntegrityResult> = {}): IntegrityResult {
  return {
    valid: true,
    checkedCount: 500,
    windowDays: 30,
    message: "All entries valid",
    checkedAt: "2026-05-01T00:00:00Z",
    ...overrides,
  };
}

function makeRetentionData(
  overrides: Partial<RetentionIntegrityData> = {}
): RetentionIntegrityData {
  return {
    retentionConfig: makeRetentionConfig(),
    integrityResult: null,
    isLoading: false,
    isVerifying: false,
    hasAccess: true,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Retention & Integrity Panel", () => {
  it("1. no retentionData — panel is absent", () => {
    const html = getAuditTabHtml(makeAuditData());
    expect(html).not.toContain("retention-integrity-panel");
    expect(html).not.toContain("retention-no-access");
  });

  it("2. hasAccess=false — locked panel shown", () => {
    const html = getAuditTabHtml(makeAuditData(), makeRetentionData({ hasAccess: false }));
    expect(html).toContain("retention-no-access");
    expect(html).toContain("Enterprise plan");
    expect(html).not.toContain("retention-integrity-panel");
  });

  it("3. isLoading=true — loading placeholder shown", () => {
    const html = getAuditTabHtml(makeAuditData(), makeRetentionData({ isLoading: true }));
    expect(html).toContain("retention-integrity-panel");
    expect(html).toContain("Loading");
    expect(html).not.toContain("retentionDaysInput");
  });

  it("4. default retention value 730 in input", () => {
    const html = getAuditTabHtml(makeAuditData(), makeRetentionData());
    expect(html).toContain('value="730"');
    expect(html).toContain("retentionDaysInput");
    expect(html).toContain("retentionSaveBtn");
  });

  it("5. custom retention value 365 in input", () => {
    const html = getAuditTabHtml(
      makeAuditData(),
      makeRetentionData({ retentionConfig: makeRetentionConfig({ retentionDays: 365 }) })
    );
    expect(html).toContain('value="365"');
  });

  it("6. valid integrity result badge rendered", () => {
    const html = getAuditTabHtml(
      makeAuditData(),
      makeRetentionData({ integrityResult: makeIntegrityResult({ valid: true }) })
    );
    expect(html).toContain("integrity-result-valid");
    expect(html).toContain("✓ Valid");
    expect(html).toContain("500 entries");
  });

  it("7. invalid integrity result badge rendered", () => {
    const html = getAuditTabHtml(
      makeAuditData(),
      makeRetentionData({
        integrityResult: makeIntegrityResult({ valid: false, message: "Hash mismatch detected" }),
      })
    );
    expect(html).toContain("integrity-result-invalid");
    expect(html).toContain("✗ Invalid");
    expect(html).toContain("Hash mismatch detected");
  });

  it("8. isVerifying=true — buttons disabled, spinner visible", () => {
    const html = getAuditTabHtml(makeAuditData(), makeRetentionData({ isVerifying: true }));
    expect(html).toContain("disabled");
    expect(html).toContain("display:inline");
    expect(html).toContain("integrity-spinner");
  });

  it("9. errorMessage — error banner shown", () => {
    const html = getAuditTabHtml(
      makeAuditData(),
      makeRetentionData({ errorMessage: "Service unavailable" })
    );
    expect(html).toContain("audit-error-banner");
    expect(html).toContain("Service unavailable");
  });

  it("10. XSS: error message is escaped", () => {
    const html = getAuditTabHtml(
      makeAuditData(),
      makeRetentionData({ errorMessage: '<script>alert("xss")</script>' })
    );
    expect(html).not.toContain("<script>");
    expect(html).toContain("&lt;script&gt;");
  });

  it("11. getAuditTabScript — contains retention message types", () => {
    const script = getAuditTabScript();
    expect(script).toContain("retentionUpdate");
    expect(script).toContain("retentionVerifyIntegrity");
    expect(script).toContain("retentionRefresh");
  });

  it("12. getAuditTabStyles — contains retention CSS classes", () => {
    const styles = getAuditTabStyles();
    expect(styles).toContain(".retention-integrity-panel");
    expect(styles).toContain(".retention-card");
    expect(styles).toContain(".integrity-card");
    expect(styles).toContain(".integrity-result-valid");
    expect(styles).toContain(".integrity-result-invalid");
    expect(styles).toContain(".retention-no-access");
  });
});
