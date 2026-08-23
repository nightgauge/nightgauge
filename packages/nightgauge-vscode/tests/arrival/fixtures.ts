/**
 * fixtures.ts — the one door into the data-arrival fixture store (#746).
 *
 * Arrival tests must never inline a payload. Every transport response comes
 * from `tests/fixtures/arrival/`, is listed in that directory's
 * `manifest.json`, and is checked against the struct that serialises it by
 * `fixtureContract.test.ts`. Routing every read through this module is what
 * makes "is this fixture accounted for?" a question with a mechanical answer.
 *
 * @see tests/fixtures/arrival/PROVENANCE.md — how each payload was obtained
 *      and how to re-record it.
 */

import * as fs from "node:fs";
import * as path from "node:path";

export const ARRIVAL_FIXTURE_ROOT = path.resolve(__dirname, "../fixtures/arrival");

/** Every fixture path the manifest declares, relative to the store root. */
export interface FixtureManifestEntry {
  path: string;
  transport: "ipc" | "https" | "filesystem";
  method: string;
  upstream: string;
  contract: {
    goFile: string;
    checks: { at: string; type: string }[];
    note?: string;
  } | null;
  note?: string;
}

export interface FixtureManifest {
  fixtures: FixtureManifestEntry[];
}

export function readManifest(): FixtureManifest {
  const raw = fs.readFileSync(path.join(ARRIVAL_FIXTURE_ROOT, "manifest.json"), "utf-8");
  return JSON.parse(raw) as FixtureManifest;
}

/**
 * Placeholders keep the discovery fixtures out of the date trap.
 *
 * `DiscoveryActivityService` buckets runs into "this week". A fixture with a
 * frozen absolute timestamp passes on the day it is recorded and then rots
 * into a silent zero seven days later — an arrival test that stops asserting
 * arrival without ever going red. Substituting at load time keeps the
 * recording verbatim on disk and honest at runtime.
 */
const PLACEHOLDERS: Record<string, () => string> = {
  __RECENT_MINUS_1D__: () => new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
  __RECENT_MINUS_2D__: () => new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString(),
  __RECENT_MINUS_30D__: () => new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString(),
};

function substitutePlaceholders(text: string): string {
  let out = text;
  for (const [token, value] of Object.entries(PLACEHOLDERS)) {
    out = out.split(token).join(value());
  }
  return out;
}

/** Read a fixture verbatim (placeholders unresolved) — for the contract test. */
export function readFixtureRaw(relPath: string): unknown {
  const full = path.join(ARRIVAL_FIXTURE_ROOT, relPath);
  return JSON.parse(fs.readFileSync(full, "utf-8"));
}

/**
 * Load a fixture with placeholders resolved. Every call returns a fresh deep
 * copy, so a test that mutates what it stubs cannot leak into the next one.
 */
export function loadFixture<T>(relPath: string): T {
  const full = path.join(ARRIVAL_FIXTURE_ROOT, relPath);
  if (!fs.existsSync(full)) {
    throw new Error(
      `arrival fixture not found: ${relPath}. Fixtures live in tests/fixtures/arrival/ ` +
        `and must be listed in its manifest.json — see PROVENANCE.md.`
    );
  }
  return JSON.parse(substitutePlaceholders(fs.readFileSync(full, "utf-8"))) as T;
}

// ---------------------------------------------------------------------------
// Named accessors — one per transport payload the dashboard tabs consume.
// ---------------------------------------------------------------------------

export const arrivalFixtures = {
  /** platform.getAnalyticsHealth → Health tab */
  analyticsHealth: () => loadFixture<Record<string, unknown>>("platform/analytics-health.json"),
  /** platform.getAnalyticsRuns → Runs tab */
  analyticsRuns: () => loadFixture<Record<string, unknown>>("platform/analytics-runs.json"),
  /** platform.getAnalyticsTrends → Trends tab */
  analyticsTrends: () => loadFixture<Record<string, unknown>>("platform/analytics-trends.json"),
  /** platform.getCostAnalytics → Cost tab */
  costAnalytics: () => loadFixture<Record<string, unknown>>("platform/cost-analytics.json"),
  /** platform.auditListReports → Compliance tab */
  complianceReports: () => loadFixture<Record<string, unknown>>("platform/compliance-reports.json"),
  /** audit.verifyIntegrity → Audit tab's Retention & Integrity panel */
  auditIntegrity: () => loadFixture<Record<string, unknown>>("platform/audit-integrity.json"),
  /** platform.getUsageSummary → Overview tab quota panel */
  usageSummary: () => loadFixture<Record<string, unknown>>("platform/usage-summary.json"),
  /** GET /v1/audit-log (HTTPS + session JWT) → Audit tab */
  auditLog: () => loadFixture<Record<string, unknown>>("platform/audit-log.json"),
  /** pr.list → Dependencies tab */
  prList: () => loadFixture<Record<string, unknown>[]>("github/pr-list.json"),
  /** issue.list (label type:epic) → Epics tab */
  epicIssues: () => loadFixture<Record<string, unknown>[]>("github/issue-list-epics.json"),
  /** .nightgauge/release-watch/creation-log.json → Discovery tab */
  discoveryCreationLog: () => loadFixture<Record<string, unknown>>("discovery/creation-log.json"),
  /** .nightgauge/improvement-runs/latest.json → Discovery tab */
  discoveryImprovementRun: () =>
    loadFixture<Record<string, unknown>>("discovery/improvement-runs-latest.json"),
  /** .nightgauge/release-watch/backlog.json → Discovery tab */
  discoveryBacklog: () => loadFixture<Record<string, unknown>[]>("discovery/backlog.json"),
};

/**
 * Recorded pipeline JSONL — the local-telemetry transport for the Overview,
 * Pipeline, Analytics and History tabs. Unlike the platform payloads this is
 * a genuine capture from a real workspace, already in the tree since the
 * health-history work; the arrival tier reuses it rather than inventing a
 * second copy of the same recording.
 */
export const RECORDED_HISTORY_JSONL = path.resolve(
  __dirname,
  "../fixtures/telemetry/health-history-multi-run.jsonl"
);
