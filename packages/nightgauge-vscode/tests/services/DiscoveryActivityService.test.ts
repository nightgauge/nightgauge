/**
 * DiscoveryActivityService.test.ts
 *
 * Tests for DiscoveryActivityService (Issue #2434).
 *
 * Covers:
 * 1. Returns null when state files are absent
 * 2. Parses release-watch creation-log.json correctly
 * 3. Parses continuous-improvement latest.json correctly
 * 4. Reads backlog from array root format
 * 5. Reads backlog from { entries: [...] } object format
 * 6. issuesCreatedThisWeek = 0 when run is older than a week
 * 7. issuesCreatedThisWeek = issue count when run is within a week
 * 8. proposalsCreatedThisWeek = 0 when CI run is older than a week
 * 9. proposalsCreatedThisWeek = proposal count when CI run is within a week
 * 10. pendingBacklogCount reflects backlog length
 * 11. lastReleaseWatchAt prefers completed_at over run_started_at
 * 12. Logs a warning on JSON parse errors (not ENOENT)
 * 13. Returns empty backlog on parse error (does not throw)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "fs";
import { DiscoveryActivityService } from "../../src/services/DiscoveryActivityService";
import type {
  ReleaseWatchRunData,
  ContinuousImprovementRunData,
  BacklogEntry,
} from "../../src/services/DiscoveryActivityService";

vi.mock("fs");

const mockFs = vi.mocked(fs);

const WORKSPACE = "/fake/workspace";

function makeService(): DiscoveryActivityService {
  return new DiscoveryActivityService(WORKSPACE);
}

const now = new Date().toISOString();
const twoWeeksAgo = new Date(Date.now() - 14 * 24 * 60 * 60 * 1000).toISOString();

const baseReleaseWatch: ReleaseWatchRunData = {
  run_started_at: now,
  triggered_by: "schedule",
  new_version: "1.2.3",
  since_version: "1.2.2",
  status: "completed",
  issues_created: [
    {
      number: 100,
      title: "Issue A",
      url: "https://github.com/test/100",
      score: 85,
    },
    { number: 101, title: "Issue B", url: "https://github.com/test/101" },
  ],
  issues_backlogged: [],
  issues_deduped: [],
  completed_at: now,
  error: null,
};

const baseCIRun: ContinuousImprovementRunData = {
  run_started_at: now,
  triggered_by: "schedule",
  mode: "dogfood",
  create_issues: true,
  dry_run: false,
  status: "completed",
  proposals_created: [{ number: 200, title: "Proposal X", url: "https://github.com/test/200" }],
  proposals_backlogged: [],
  completed_at: now,
  error: null,
};

const backlogEntries: BacklogEntry[] = [
  { title: "Low-priority change", score: 40, reason: "Minor" },
  { title: "Medium change", score: 65, reason: "Moderate" },
];

// ---------------------------------------------------------------------------
// File mock helpers
// ---------------------------------------------------------------------------

const releaseWatchDir = `${WORKSPACE}/.nightgauge/release-watch`;

// Drives existsSync/readFileSync for file paths AND readdirSync/existsSync for
// the release-watch directory (the multi-provider creation-log glob, #4057).
function mockFiles(files: Record<string, string | null>): void {
  const present = Object.keys(files).filter((k) => files[k] !== null);
  const childrenOf = (dir: string): string[] =>
    present.filter((f) => f.startsWith(`${dir}/`)).map((f) => f.slice(dir.length + 1));

  mockFs.existsSync.mockImplementation((p) => {
    const key = String(p);
    if (key === releaseWatchDir) {
      return childrenOf(releaseWatchDir).length > 0;
    }
    return key in files && files[key] !== null;
  });
  mockFs.readdirSync.mockImplementation((p) => {
    const key = String(p);
    return (key === releaseWatchDir ? childrenOf(releaseWatchDir) : []) as unknown as ReturnType<
      typeof fs.readdirSync
    >;
  });
  mockFs.readFileSync.mockImplementation((p) => {
    const key = String(p);
    if (key in files && files[key] !== null) {
      return files[key] as string;
    }
    throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
  });
}

function noFiles(): void {
  mockFs.existsSync.mockReturnValue(false);
  mockFs.readdirSync.mockImplementation(() => [] as unknown as ReturnType<typeof fs.readdirSync>);
  mockFs.readFileSync.mockImplementation(() => {
    throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
  });
}

const releaseWatchLog = `${releaseWatchDir}/creation-log.json`;
const improvementLog = `${WORKSPACE}/.nightgauge/improvement-runs/latest.json`;
const backlogPath = `${releaseWatchDir}/backlog.json`;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("DiscoveryActivityService", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("when no state files exist", () => {
    it("returns null for releaseWatch and continuousImprovement", async () => {
      noFiles();
      const svc = makeService();
      const data = await svc.getActivityData();
      expect(data.releaseWatch).toBeNull();
      expect(data.continuousImprovement).toBeNull();
    });

    it("returns empty backlog array", async () => {
      noFiles();
      const data = await makeService().getActivityData();
      expect(data.backlog).toEqual([]);
    });

    it("returns zero summary counts", async () => {
      noFiles();
      const data = await makeService().getActivityData();
      expect(data.summary.issuesCreatedThisWeek).toBe(0);
      expect(data.summary.proposalsCreatedThisWeek).toBe(0);
      expect(data.summary.pendingBacklogCount).toBe(0);
      expect(data.summary.lastReleaseWatchAt).toBeNull();
      expect(data.summary.lastContinuousImprovementAt).toBeNull();
    });
  });

  describe("release-watch log parsing", () => {
    it("parses valid creation-log.json", async () => {
      mockFiles({
        [releaseWatchLog]: JSON.stringify(baseReleaseWatch),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.releaseWatch).toMatchObject({
        new_version: "1.2.3",
        since_version: "1.2.2",
        status: "completed",
        issues_created: expect.arrayContaining([
          expect.objectContaining({ number: 100, title: "Issue A" }),
        ]),
      });
    });

    it("returns null for release watch when file has invalid JSON", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      mockFiles({
        [releaseWatchLog]: "{bad json",
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.releaseWatch).toBeNull();
      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("DiscoveryActivityService"),
        expect.any(Error)
      );
      warnSpy.mockRestore();
    });
  });

  describe("continuous-improvement log parsing", () => {
    it("parses valid latest.json", async () => {
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: JSON.stringify(baseCIRun),
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.continuousImprovement).toMatchObject({
        mode: "dogfood",
        status: "completed",
        proposals_created: expect.arrayContaining([expect.objectContaining({ number: 200 })]),
      });
    });
  });

  describe("backlog parsing", () => {
    it("reads backlog in array root format", async () => {
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: null,
        [backlogPath]: JSON.stringify(backlogEntries),
      });
      const data = await makeService().getActivityData();
      expect(data.backlog).toHaveLength(2);
      expect(data.backlog[0].title).toBe("Low-priority change");
    });

    it("reads backlog in { entries: [...] } object format", async () => {
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: null,
        [backlogPath]: JSON.stringify({
          entries: backlogEntries,
          meta: "extra",
        }),
      });
      const data = await makeService().getActivityData();
      expect(data.backlog).toHaveLength(2);
    });

    it("returns empty array when backlog JSON is malformed", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: null,
        [backlogPath]: "!!!",
      });
      const data = await makeService().getActivityData();
      expect(data.backlog).toEqual([]);
      expect(warnSpy).toHaveBeenCalled();
      warnSpy.mockRestore();
    });
  });

  describe("issuesCreatedThisWeek", () => {
    it("returns issue count when run completed within a week", async () => {
      mockFiles({
        [releaseWatchLog]: JSON.stringify(baseReleaseWatch),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.issuesCreatedThisWeek).toBe(2);
    });

    it("returns 0 when run completed more than a week ago", async () => {
      const oldRun: ReleaseWatchRunData = {
        ...baseReleaseWatch,
        completed_at: twoWeeksAgo,
        run_started_at: twoWeeksAgo,
      };
      mockFiles({
        [releaseWatchLog]: JSON.stringify(oldRun),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.issuesCreatedThisWeek).toBe(0);
    });

    it("falls back to run_started_at when completed_at is null", async () => {
      const runWithoutCompleted: ReleaseWatchRunData = {
        ...baseReleaseWatch,
        completed_at: null,
        run_started_at: now,
      };
      mockFiles({
        [releaseWatchLog]: JSON.stringify(runWithoutCompleted),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.issuesCreatedThisWeek).toBe(2);
    });
  });

  describe("proposalsCreatedThisWeek", () => {
    it("returns proposal count when CI run is within a week", async () => {
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: JSON.stringify(baseCIRun),
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.proposalsCreatedThisWeek).toBe(1);
    });

    it("returns 0 when CI run is older than a week", async () => {
      const oldCiRun: ContinuousImprovementRunData = {
        ...baseCIRun,
        completed_at: twoWeeksAgo,
        run_started_at: twoWeeksAgo,
      };
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: JSON.stringify(oldCiRun),
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.proposalsCreatedThisWeek).toBe(0);
    });
  });

  describe("summary fields", () => {
    it("pendingBacklogCount reflects backlog length", async () => {
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: null,
        [backlogPath]: JSON.stringify(backlogEntries),
      });
      const data = await makeService().getActivityData();
      expect(data.summary.pendingBacklogCount).toBe(2);
    });

    it("lastReleaseWatchAt uses completed_at when set", async () => {
      const completedTs = "2026-03-20T09:00:00Z";
      mockFiles({
        [releaseWatchLog]: JSON.stringify({
          ...baseReleaseWatch,
          completed_at: completedTs,
          run_started_at: "2026-03-20T08:00:00Z",
        }),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.lastReleaseWatchAt).toBe(completedTs);
    });

    it("lastContinuousImprovementAt falls back to run_started_at when completed_at is null", async () => {
      const startedTs = "2026-03-20T08:00:00Z";
      mockFiles({
        [releaseWatchLog]: null,
        [improvementLog]: JSON.stringify({
          ...baseCIRun,
          completed_at: null,
          run_started_at: startedTs,
        }),
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.summary.lastContinuousImprovementAt).toBe(startedTs);
    });
  });

  describe("multi-provider creation logs (#4057)", () => {
    const claudeLog = `${releaseWatchDir}/creation-log-claude-code.json`;
    const geminiLog = `${releaseWatchDir}/creation-log-gemini.json`;

    it("aggregates issues_created across every provider's creation log", async () => {
      mockFiles({
        [claudeLog]: JSON.stringify({
          ...baseReleaseWatch,
          new_version: "2.1.183",
          issues_created: [{ number: 100, title: "Claude A", url: "https://x/100" }],
        }),
        [geminiLog]: JSON.stringify({
          ...baseReleaseWatch,
          new_version: "0.47.0",
          issues_created: [{ number: 200, title: "Gemini A", url: "https://x/200" }],
        }),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.releaseWatch?.issues_created).toHaveLength(2);
      expect(data.releaseWatch?.issues_created.map((i) => i.number).sort()).toEqual([100, 200]);
      expect(data.summary.issuesCreatedThisWeek).toBe(2);
    });

    it("sums weekly counts per-run, excluding a stale provider", async () => {
      mockFiles({
        [claudeLog]: JSON.stringify(baseReleaseWatch), // 2 issues, now
        [geminiLog]: JSON.stringify({
          ...baseReleaseWatch,
          completed_at: twoWeeksAgo,
          run_started_at: twoWeeksAgo,
          issues_created: [{ number: 999, title: "Old Gemini", url: "https://x/999" }],
        }),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      // Aggregate view unions all issues (3), but the weekly count excludes the
      // stale provider's run → only the 2 fresh ones.
      expect(data.releaseWatch?.issues_created).toHaveLength(3);
      expect(data.summary.issuesCreatedThisWeek).toBe(2);
    });

    it("folds status to running > failed > completed across providers", async () => {
      mockFiles({
        [claudeLog]: JSON.stringify({ ...baseReleaseWatch, status: "completed" }),
        [geminiLog]: JSON.stringify({ ...baseReleaseWatch, status: "failed", error: "boom" }),
        [improvementLog]: null,
        [backlogPath]: null,
      });
      const data = await makeService().getActivityData();
      expect(data.releaseWatch?.status).toBe("failed");
      expect(data.releaseWatch?.error).toContain("boom");
    });
  });
});
