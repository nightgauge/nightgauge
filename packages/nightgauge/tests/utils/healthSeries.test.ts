/**
 * The one health series (#1898), extension side.
 *
 * Exactly one entry per run, computed by the SDK engine; the entry written is
 * the entry returned (and therefore the number displayed); a run with no data
 * is recorded as null rather than omitted or coerced; the trend chart never
 * plots a null as zero.
 */

import { afterEach, describe, expect, it } from "vitest";
import * as fs from "node:fs";
import type { HealthTrendEntry } from "@nightgauge/sdk";
import { HealthTrendsWriter } from "@nightgauge/sdk";
import {
  aggregateHealthByDay,
  latestHealthEntry,
  readHealthSeries,
  recordHealthForRun,
} from "../../src/utils/healthSeries";
import { summarizeHealthEntry } from "../../src/views/dashboard/HealthWidget";
import { formatHealthScore } from "../../src/views/dashboard/HealthWidgetHtml";
import { initGitRepo } from "../helpers/cloneLayout";

const roots: string[] = [];
afterEach(() => {
  for (const r of roots.splice(0)) fs.rmSync(r, { recursive: true, force: true });
});

function entry(overrides: Partial<HealthTrendEntry>): HealthTrendEntry {
  return {
    schema_version: "2",
    timestamp: "2026-10-01T10:00:00.000Z",
    run_id: "r",
    issue_number: 1,
    overall_score: 80,
    status: "good",
    dimensions: { reliability: 80 },
    excluded_dimensions: {},
    significant_findings: [],
    ...overrides,
  };
}

describe("recordHealthForRun", () => {
  it("appends exactly one entry per run and returns the entry it wrote", async () => {
    const root = initGitRepo();
    roots.push(root);

    const first = await recordHealthForRun(root, { issueNumber: 11, runId: "run-a" });
    const second = await recordHealthForRun(root, { issueNumber: 12, runId: "run-b" });

    const series = await readHealthSeries(root);
    expect(series).toHaveLength(2);
    expect(series.map((e) => e.run_id)).toEqual(["run-a", "run-b"]);
    // What is returned (and displayed) is what was persisted.
    expect(series[0]).toEqual(first);
    expect(series[1]).toEqual(second);
    expect(await latestHealthEntry(root)).toEqual(second);
    expect(fs.existsSync(HealthTrendsWriter.getFilePath(root))).toBe(true);
  });

  it("records a run with no data as null — not omitted, not coerced", async () => {
    const root = initGitRepo();
    roots.push(root);

    const written = await recordHealthForRun(root, { issueNumber: 3, runId: "empty" });

    expect(written).not.toBeNull();
    expect(written!.overall_score).toBeNull();
    expect(written!.status).toBe("no-data");
    expect(written!.dimensions).toEqual({});
    // Every one of the eight dimensions is named as excluded, with a reason.
    expect(Object.keys(written!.excluded_dimensions)).toHaveLength(8);
    const raw = fs.readFileSync(HealthTrendsWriter.getFilePath(root), "utf-8").trim();
    expect(JSON.parse(raw).overall_score).toBeNull();
  });
});

describe("aggregateHealthByDay", () => {
  it("skips null entries instead of plotting them as zero", () => {
    const chart = aggregateHealthByDay([
      entry({ timestamp: "2026-10-01T09:00:00Z", overall_score: 80 }),
      entry({
        timestamp: "2026-10-01T10:00:00Z",
        overall_score: null,
        status: "no-data",
        dimensions: {},
      }),
      entry({
        timestamp: "2026-10-02T10:00:00Z",
        overall_score: null,
        status: "no-data",
        dimensions: {},
      }),
    ]);
    expect(chart).toEqual([{ date: "2026-10-01", avgScore: 80, count: 1 }]);
  });
});

describe("dashboard headline", () => {
  it("shows N/A — not 0, not critical — before any entry exists", () => {
    const summary = summarizeHealthEntry(null);
    expect(summary.score).toBeNull();
    expect(summary.status).toBe("no-data");
    expect(summary.contributing).toBe(0);
    expect(formatHealthScore(summary.score)).toBe("N/A");
  });

  it("shows the latest entry's own number and how many dimensions made it", () => {
    const summary = summarizeHealthEntry(
      entry({
        overall_score: 83,
        status: "good",
        dimensions: { reliability: 92, "cost-health": 75 },
        excluded_dimensions: { "skill-drift": "no skill-effectiveness records" },
      })
    );
    expect(summary.score).toBe(83);
    expect(summary.contributing).toBe(2);
    expect(summary.dimensions).toHaveLength(8);
    const drift = summary.dimensions.find((d) => d.dimension === "skill-drift");
    expect(drift).toMatchObject({ score: null, excludedReason: "no skill-effectiveness records" });
  });
});
