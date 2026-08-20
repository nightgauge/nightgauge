/**
 * Playwright tests for inbound rendering of platform-backed tabs against a
 * realistic populated payload, an empty-but-successful payload, and each
 * `PlatformFailureKind` (Issue #751 — coverage gap left by #744's outbound-
 * only tier, following #748's failure-state rewrite and #752's loading
 * states). Fixtures are real getDashboardHtml() output from
 * scripts/generate-dashboard-html.ts, not a hand-written mirror.
 *
 * Coverage depth: Runs (the tab #751 names as the priority case) and
 * Compliance (the tab PlatformFailureHtml.ts's own docstring names as the
 * motivating bug — a rejected credential told to "upgrade your plan") get
 * the full 5-kind PlatformFailureKind matrix. The remaining platform tabs
 * (Trends, Health, Cost) get 2 representative kinds — one that renders the
 * sign-in CTA (unauthorized/not_configured) and one that renders the retry
 * CTA (server_error/offline) — since the copy itself is already exhaustively
 * unit-tested per-kind in tests/views/dashboard/tabs/PlatformFailureHtml.test.ts;
 * what these tests add is that the real rendered DOM actually reflects it.
 * Dependabot has no PlatformFailureKind (raw fetch-error text) and Discovery
 * has no platform failures at all (local-file-based) — both still get
 * populated/empty/error coverage in their own shape.
 *
 * Prerequisite: `npx tsx scripts/generate-dashboard-html.ts` (or
 * `npm run test:e2e`) must have run first.
 */

import { test, expect } from "@playwright/test";
import { loadTabFixture } from "../helpers/dashboard-fixtures.js";

// ---------------------------------------------------------------------------
// Shared invariants (#748): only `forbidden` may mention role/plan, and only
// `server_error` (the one genuinely-transient kind PlatformFailureHtml.ts
// renders text for) may say "transient". Checked across every failure
// fixture below, tab by tab, on the real rendered DOM.
// ---------------------------------------------------------------------------

function assertFailureCopyInvariants(text: string, kind: string): void {
  if (kind === "forbidden") {
    expect(text).toMatch(/role|plan/);
  } else {
    expect(text).not.toMatch(/different role or plan/);
  }
  if (kind !== "server_error") {
    expect(text).not.toContain("transient");
  }
}

// ---------------------------------------------------------------------------
// Runs tab — full 5-kind matrix
// ---------------------------------------------------------------------------

test.describe("Runs tab — inbound rendering per state", () => {
  test("populated: renders every run row", async ({ page }) => {
    await loadTabFixture(page, "runs--populated");
    await expect(page.locator(".runs-row")).toHaveCount(3);
    await expect(page.locator("body")).toContainText("#701");
  });

  test("empty: renders the no-runs-found state, not a failure", async ({ page }) => {
    await loadTabFixture(page, "runs--empty");
    await expect(page.locator(".runs-empty-state")).toContainText("No Runs Found");
    await expect(page.locator(".runs-no-access")).toHaveCount(0);
  });

  test("loading: renders the loading state", async ({ page }) => {
    await loadTabFixture(page, "runs--loading");
    await expect(page.locator(".runs-loading")).toContainText("Loading pipeline runs");
  });

  for (const kind of ["unauthorized", "forbidden", "server_error", "offline", "not_configured"]) {
    test(`failure(${kind}): renders the classified cause, not a generic message`, async ({
      page,
    }) => {
      await loadTabFixture(page, `runs--failure-${kind}`);
      const panel = page.locator(".runs-no-access");
      await expect(panel).toBeVisible();
      assertFailureCopyInvariants((await panel.innerText()).toLowerCase(), kind);

      if (kind === "unauthorized" || kind === "not_configured") {
        await expect(panel.locator("#runsSignInBtn")).toBeVisible();
      } else {
        await expect(panel.locator("#runsRetryBtn")).toBeVisible();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// Compliance tab — full 5-kind matrix
// ---------------------------------------------------------------------------

test.describe("Compliance tab — inbound rendering per state", () => {
  test("populated: renders the reports table", async ({ page }) => {
    await loadTabFixture(page, "compliance--populated");
    // The report id (rep-1/rep-2) is only in a data-report-id attribute, not
    // visible text — assert on what the row actually renders: type and status.
    await expect(page.locator("body")).toContainText("SOC2");
    await expect(page.locator("body")).toContainText("processing");
    await expect(page.locator('[data-report-id="rep-1"]')).toBeVisible();
    await expect(page.locator(".compliance-empty-state")).toHaveCount(0);
  });

  test("empty: renders the empty-table message inline, not a failure panel", async ({ page }) => {
    await loadTabFixture(page, "compliance--empty");
    await expect(page.locator(".compliance-empty-state")).toContainText(
      "No compliance reports yet"
    );
    await expect(page.locator(".compliance-no-access")).toHaveCount(0);
  });

  test("loading: renders the loading state", async ({ page }) => {
    await loadTabFixture(page, "compliance--loading");
    await expect(page.locator(".compliance-loading")).toContainText("Loading compliance reports");
  });

  for (const kind of ["unauthorized", "forbidden", "server_error", "offline", "not_configured"]) {
    test(`failure(${kind}): renders the classified cause`, async ({ page }) => {
      await loadTabFixture(page, `compliance--failure-${kind}`);
      const panel = page.locator(".compliance-no-access");
      await expect(panel).toBeVisible();
      assertFailureCopyInvariants((await panel.innerText()).toLowerCase(), kind);

      if (kind === "unauthorized" || kind === "not_configured") {
        await expect(panel.locator("#complianceSignInBtn")).toBeVisible();
      } else {
        await expect(panel.locator("#complianceRetryBtn")).toBeVisible();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// Trends tab — populated (>= 7-entry SPARSE_THRESHOLD) / empty / loading /
// 2 representative failure kinds
// ---------------------------------------------------------------------------

test.describe("Trends tab — inbound rendering per state", () => {
  test("populated: renders the three trend charts", async ({ page }) => {
    await loadTabFixture(page, "trends--populated");
    await expect(page.locator(".trends-chart-card")).toHaveCount(3);
  });

  test("empty: renders 'no trends data yet', not a failure", async ({ page }) => {
    await loadTabFixture(page, "trends--empty");
    await expect(page.locator(".trends-empty-title")).toContainText("No trends data yet");
    await expect(page.locator(".trends-no-access")).toHaveCount(0);
  });

  test("loading: renders the loading state", async ({ page }) => {
    await loadTabFixture(page, "trends--loading");
    await expect(page.locator(".trends-empty-title")).toContainText("Loading trends");
  });

  for (const kind of ["unauthorized", "server_error"]) {
    test(`failure(${kind}): renders the classified cause`, async ({ page }) => {
      await loadTabFixture(page, `trends--failure-${kind}`);
      const panel = page.locator(".trends-no-access");
      await expect(panel).toBeVisible();
      assertFailureCopyInvariants((await panel.innerText()).toLowerCase(), kind);
    });
  }
});

// ---------------------------------------------------------------------------
// Health (analytics) tab — populated / empty / 2 representative failure kinds
// ---------------------------------------------------------------------------

test.describe("Health tab — inbound rendering per state", () => {
  test("populated: renders overall score and dimension cards", async ({ page }) => {
    await loadTabFixture(page, "health--populated");
    await expect(page.locator(".health-overall-score")).toContainText("82");
    await expect(page.locator(".health-dim-card")).toHaveCount(2);
  });

  test("empty (no failure): renders the generic connect-to-platform state", async ({ page }) => {
    await loadTabFixture(page, "health--empty");
    await expect(page.locator(".health-empty-title")).toContainText("connect to Nightgauge");
  });

  for (const kind of ["not_configured", "server_error"]) {
    test(`failure(${kind}): renders the classified cause, not the generic empty copy`, async ({
      page,
    }) => {
      await loadTabFixture(page, `health--failure-${kind}`);
      const panel = page.locator(".health-empty-state");
      await expect(panel).toBeVisible();
      const text = (await panel.innerText()).toLowerCase();
      assertFailureCopyInvariants(text, kind);
      // Distinct from the no-failure empty state's title.
      await expect(panel.locator(".health-empty-title")).not.toContainText("connect to Nightgauge");
    });
  }
});

// ---------------------------------------------------------------------------
// Cost (platform) tab — populated / empty / 2 representative failure kinds
// ---------------------------------------------------------------------------

test.describe("Cost tab — inbound rendering per state", () => {
  test("populated: renders the total cost card", async ({ page }) => {
    await loadTabFixture(page, "cost--populated");
    await expect(page.locator(".platform-cost-tab")).toContainText("3.4210");
  });

  test("empty (no failure): renders 'no server-aggregated cost data yet'", async ({ page }) => {
    await loadTabFixture(page, "cost--empty");
    await expect(page.locator(".platform-cost-empty-title")).toContainText(
      "No server-aggregated cost data yet"
    );
    await expect(page.locator(".platform-cost-failure")).toHaveCount(0);
  });

  for (const kind of ["unauthorized", "offline"]) {
    test(`failure(${kind}): renders the classified cause, not the generic empty copy`, async ({
      page,
    }) => {
      await loadTabFixture(page, `cost--failure-${kind}`);
      const panel = page.locator(".platform-cost-failure");
      await expect(panel).toBeVisible();
      const text = (await panel.innerText()).toLowerCase();
      assertFailureCopyInvariants(text, kind);
      await expect(panel.locator(".platform-cost-empty-title")).not.toContainText(
        "No server-aggregated cost data yet"
      );
    });
  }
});

// ---------------------------------------------------------------------------
// Dependencies (Dependabot) tab — no PlatformFailureKind; state is
// undefined (loading) / null (empty) / a raw fetchError string
// ---------------------------------------------------------------------------

test.describe("Dependencies tab — inbound rendering per state", () => {
  test("loading: renders the loading spinner", async ({ page }) => {
    await loadTabFixture(page, "dependencies--loading");
    await expect(page.locator(".dependabot-loading")).toContainText("Loading dependabot PRs");
  });

  test("empty: renders 'no open dependabot PRs found', distinct from a fetch failure", async ({
    page,
  }) => {
    await loadTabFixture(page, "dependencies--empty");
    await expect(page.locator(".dependabot-empty")).toContainText("No open dependabot PRs found");
    await expect(page.locator(".dependabot-fetch-error")).toHaveCount(0);
  });

  test("populated: renders summary counts and PR rows", async ({ page }) => {
    await loadTabFixture(page, "dependencies--populated");
    await expect(page.locator(".dependabot-table tr")).toHaveCount(3); // header + 2 PR rows
    await expect(page.locator("body")).toContainText("#801");
  });

  test("fetch error: renders the raw error text with a retry button, not the empty-list copy", async ({
    page,
  }) => {
    await loadTabFixture(page, "dependencies--fetch-error");
    const panel = page.locator(".dependabot-fetch-error");
    await expect(panel).toContainText("GitHub API rate limit exceeded");
    await expect(panel.locator("#dependabotRetryBtn")).toBeVisible();
    await expect(page.locator(".dependabot-empty")).toHaveCount(0);
  });
});

// ---------------------------------------------------------------------------
// Audit tab (+ Retention & Integrity panel) — no top-level PlatformFailureKind
// on the main no-access state (hardcoded copy, unlike the other 7 tabs), but
// the Retention panel IS classified.
// ---------------------------------------------------------------------------

test.describe("Audit tab — inbound rendering per state", () => {
  test("populated: renders audit entries", async ({ page }) => {
    await loadTabFixture(page, "audit--populated");
    await expect(page.locator("body")).toContainText("pipeline.run");
  });

  test("empty: audit table renders with zero entries, not a no-access panel", async ({ page }) => {
    await loadTabFixture(page, "audit--empty");
    await expect(page.locator(".audit-no-access")).toHaveCount(0);
  });

  test("loading: renders the loading state", async ({ page }) => {
    await loadTabFixture(page, "audit--loading");
    await expect(page.locator(".audit-loading")).toContainText("Loading audit events");
  });

  test("no-access: renders the no-access panel", async ({ page }) => {
    await loadTabFixture(page, "audit--no-access");
    await expect(page.locator(".audit-no-access")).toContainText("No Access");
  });

  test("local fallback: renders the local-telemetry banner with a retry button", async ({
    page,
  }) => {
    await loadTabFixture(page, "audit--local-fallback");
    const banner = page.locator(".audit-local-banner");
    await expect(banner).toBeVisible();
    await expect(banner.locator("#auditRetryBtn")).toBeVisible();
  });
});

test.describe("Retention & Integrity panel — inbound rendering per state", () => {
  test("populated: renders the retention config", async ({ page }) => {
    await loadTabFixture(page, "retention--populated");
    // retentionDays (730) is only an <input value>, not visible text.
    await expect(page.locator("#retentionDaysInput")).toHaveValue("730");
    await expect(page.locator(".retention-integrity-panel")).toContainText(
      "Last updated: 2026-06-01"
    );
  });

  test("loading: renders the loading placeholder", async ({ page }) => {
    await loadTabFixture(page, "retention--loading");
    await expect(page.locator(".retention-integrity-panel")).toContainText("Loading");
  });

  test("no-access: renders the Enterprise-plan gate — the one legitimate role/plan mention outside `forbidden`, since it is hardcoded product copy, not derived from a PlatformFailure", async ({
    page,
  }) => {
    await loadTabFixture(page, "retention--no-access");
    await expect(page.locator(".retention-no-access")).toContainText("Enterprise plan");
  });
});

// ---------------------------------------------------------------------------
// Discovery tab — local-file-based, no PlatformFailureKind
// ---------------------------------------------------------------------------

test.describe("Discovery tab — inbound rendering per state", () => {
  test("populated: renders the release-watch run summary", async ({ page }) => {
    await loadTabFixture(page, "discovery--populated");
    await expect(page.locator("body")).toContainText("Adopt new SDK feature");
  });

  test("empty: renders the no-activity-yet state", async ({ page }) => {
    await loadTabFixture(page, "discovery--empty");
    await expect(page.locator("body")).toContainText("No discovery activity yet");
  });

  test("unavailable: renders the service-not-available placeholder", async ({ page }) => {
    await loadTabFixture(page, "discovery--unavailable");
    await expect(page.locator("body")).toContainText("Discovery service not available");
  });
});
