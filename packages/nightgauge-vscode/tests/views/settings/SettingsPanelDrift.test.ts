/**
 * SettingsPanelDrift.test.ts
 *
 * Tests for tier-drift banner, per-key drift badges, and Move action
 * surfaced in the settings panel (Issue #3645).
 *
 * Tests operate directly on getSettingsHtml() with mocked tierAuditEntries,
 * following the same pattern as SettingsHtml.core.test.ts.
 */

import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { TierAuditEntry } from "../../../src/services/IpcClientBase";

const mockWebview = { cspSource: "test-csp" } as any;

function makeDriftEntry(key: string, effectiveTier: string, targetTier: string): TierAuditEntry {
  return {
    key,
    effectiveTier,
    effectiveSource: `/workspace/.nightgauge/config.yaml`,
    targetTier,
    status: `DRIFT — ${targetTier} key in ${effectiveTier} config`,
  };
}

function makeOkEntry(key: string, tier: string): TierAuditEntry {
  return {
    key,
    effectiveTier: tier,
    effectiveSource: `/workspace/.nightgauge/config.yaml`,
    targetTier: tier,
    status: "OK",
  };
}

// Sentinel that appears in rendered drift banner elements but NOT in CSS rules
const DRIFT_BANNER_SENTINEL = 'role="alert"';
// Sentinel that appears in rendered drift badge elements but NOT in CSS rules
const DRIFT_BADGE_SENTINEL = ">Drift<";
// Sentinel that appears in rendered move buttons but NOT in CSS rules
const DRIFT_MOVE_SENTINEL = ">Move to";

describe("Drift banner", () => {
  it("renders when drift entries present and banner not dismissed", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
      driftBannerDismissed: false,
    });

    expect(html).toContain(DRIFT_BANNER_SENTINEL);
    expect(html).toContain("1 setting stored in the wrong config tier.");
  });

  it("renders plural count for multiple drift entries", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [
        makeDriftEntry("pipeline.auto_fix", "machine", "project"),
        makeDriftEntry("ui.core.adapter", "project", "machine"),
      ],
      driftBannerDismissed: false,
    });

    expect(html).toContain(DRIFT_BANNER_SENTINEL);
    expect(html).toContain("2 settings stored in the wrong config tier.");
  });

  it("is absent when no drift entries present", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeOkEntry("pipeline.auto_fix", "project")],
      driftBannerDismissed: false,
    });

    expect(html).not.toContain(DRIFT_BANNER_SENTINEL);
  });

  it("is absent when tierAuditEntries is empty", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [],
      driftBannerDismissed: false,
    });

    expect(html).not.toContain(DRIFT_BANNER_SENTINEL);
  });

  it("is absent when dismissed", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
      driftBannerDismissed: true,
    });

    expect(html).not.toContain(DRIFT_BANNER_SENTINEL);
  });

  it("is absent when no options provided", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {});

    expect(html).not.toContain(DRIFT_BANNER_SENTINEL);
  });

  it("includes dismiss button posting dismissDriftBanner message", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
      driftBannerDismissed: false,
    });

    expect(html).toContain("dismissDriftBanner");
    expect(html).toContain("drift-banner-dismiss");
  });

  it("includes showDriftedKeysOnly button", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
      driftBannerDismissed: false,
    });

    expect(html).toContain("showDriftedKeysOnly");
  });
});

describe("Drift badge per-key", () => {
  it("renders drift badge with tooltip text for a DRIFT key", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
    });

    expect(html).toContain(DRIFT_BADGE_SENTINEL);
    expect(html).toContain("Tier drift: stored in machine, target project");
  });

  it("does not render drift badge for OK-status entry", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeOkEntry("pipeline.auto_fix", "project")],
    });

    expect(html).not.toContain(DRIFT_BADGE_SENTINEL);
  });

  it("does not render drift badge when no audit entries provided", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {});

    expect(html).not.toContain(DRIFT_BADGE_SENTINEL);
  });
});

describe("Move action", () => {
  it("renders moveTierKey button for a drifted key", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
    });

    expect(html).toContain("moveTierKey");
    expect(html).toContain(DRIFT_MOVE_SENTINEL);
  });

  it("Move button includes correct key and targetTier in message payload", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeDriftEntry("pipeline.auto_fix", "machine", "project")],
    });

    expect(html).toContain("key:'pipeline.auto_fix'");
    expect(html).toContain("targetTier:'project'");
  });

  it("does not render Move button for OK-status entry", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config, new Set(), {}, undefined, {
      tierAuditEntries: [makeOkEntry("pipeline.auto_fix", "project")],
    });

    expect(html).not.toContain(DRIFT_MOVE_SENTINEL);
  });
});
