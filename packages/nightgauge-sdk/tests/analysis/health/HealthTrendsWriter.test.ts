/**
 * Unit tests for HealthTrendsWriter
 *
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { HealthTrendEntry } from "../../../src/analysis/health/types.js";

// Mock the entire fs/promises module before imports
vi.mock("node:fs/promises", () => ({
  mkdir: vi.fn().mockResolvedValue(undefined),
  appendFile: vi.fn().mockResolvedValue(undefined),
  readFile: vi.fn(),
  writeFile: vi.fn().mockResolvedValue(undefined),
}));

import * as fs from "node:fs/promises";
import { HealthTrendsWriter } from "../../../src/analysis/health/HealthTrendsWriter.js";

// ── Test fixtures ────────────────────────────────────────────────────────────

const BASE_ENTRY: HealthTrendEntry = {
  schema_version: "1",
  timestamp: "2026-01-15T10:00:00Z",
  run_id: "2026-01-15T10:00:00Z",
  issue_number: 100,
  overall_score: 75,
  dimensions: {
    "token-economics": 80,
    "cost-health": 70,
    "stage-effectiveness": 78,
    "model-routing": 65,
    reliability: 85,
    "learning-effectiveness": 60,
    "pipeline-velocity": 72,
  },
  significant_findings: ["Low cache hit rate", "Cost spike detected"],
};

function makeEntry(overrides: Partial<HealthTrendEntry> = {}): HealthTrendEntry {
  return { ...BASE_ENTRY, ...overrides };
}

function makeEntries(count: number): HealthTrendEntry[] {
  return Array.from({ length: count }, (_, i) =>
    makeEntry({
      timestamp: `2026-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      run_id: `2026-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
      overall_score: 60 + i,
    })
  );
}

// ── append() ─────────────────────────────────────────────────────────────────

describe("HealthTrendsWriter.append", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("creates directory and appends a JSON line", async () => {
    const entry = makeEntry();
    await HealthTrendsWriter.append("/workspace", entry);

    expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining(".nightgauge/health"), {
      recursive: true,
    });
    expect(fs.appendFile).toHaveBeenCalledWith(
      expect.stringContaining("trends.jsonl"),
      JSON.stringify(entry) + "\n",
      "utf-8"
    );
  });

  it("does not throw when appendFile throws", async () => {
    vi.mocked(fs.appendFile).mockRejectedValueOnce(new Error("EACCES"));
    await expect(HealthTrendsWriter.append("/workspace", makeEntry())).resolves.toBeUndefined();
  });

  it("does not throw when mkdir throws", async () => {
    vi.mocked(fs.mkdir).mockRejectedValueOnce(new Error("EPERM"));
    await expect(HealthTrendsWriter.append("/workspace", makeEntry())).resolves.toBeUndefined();
  });

  it("skips write and warns when entry fails schema validation", async () => {
    const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const badEntry = {
      ...makeEntry(),
      schema_version: "99",
    } as unknown as HealthTrendEntry;
    await HealthTrendsWriter.append("/workspace", badEntry);
    expect(fs.appendFile).not.toHaveBeenCalled();
    expect(consoleSpy).toHaveBeenCalledWith(expect.stringContaining("Invalid trend entry"));
    consoleSpy.mockRestore();
  });
});

// ── read() ───────────────────────────────────────────────────────────────────

describe("HealthTrendsWriter.read", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns empty array when file does not exist", async () => {
    vi.mocked(fs.readFile).mockRejectedValueOnce(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );
    const result = await HealthTrendsWriter.read("/workspace");
    expect(result).toEqual([]);
  });

  it("parses valid JSONL and returns entries", async () => {
    const entries = makeEntries(3);
    const content = entries.map((e) => JSON.stringify(e)).join("\n") + "\n";
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const result = await HealthTrendsWriter.read("/workspace");
    expect(result).toHaveLength(3);
    expect(result[0].overall_score).toBe(entries[0].overall_score);
  });

  it("skips malformed lines and continues", async () => {
    const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const good = JSON.stringify(makeEntry({ overall_score: 77 }));
    const content = `${good}\nnot-valid-json\n${good}\n`;
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const result = await HealthTrendsWriter.read("/workspace");
    expect(result).toHaveLength(2);
    consoleSpy.mockRestore();
  });

  it("skips invalid schema lines with a warning", async () => {
    const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const good = JSON.stringify(makeEntry());
    const bad = JSON.stringify({ schema_version: "99", junk: true });
    const content = `${good}\n${bad}\n`;
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const result = await HealthTrendsWriter.read("/workspace");
    expect(result).toHaveLength(1);
    expect(consoleSpy).toHaveBeenCalled();
    consoleSpy.mockRestore();
  });

  it("respects limit option — returns last N entries", async () => {
    const entries = makeEntries(10);
    const content = entries.map((e) => JSON.stringify(e)).join("\n") + "\n";
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const result = await HealthTrendsWriter.read("/workspace", { limit: 5 });
    expect(result).toHaveLength(5);
    // Last 5 entries (tail semantics)
    expect(result[0].overall_score).toBe(entries[5].overall_score);
    expect(result[4].overall_score).toBe(entries[9].overall_score);
  });

  it("filters by startDate and endDate", async () => {
    const entries = [
      makeEntry({ timestamp: "2026-01-01T10:00:00Z" }),
      makeEntry({ timestamp: "2026-01-15T10:00:00Z" }),
      makeEntry({ timestamp: "2026-01-30T10:00:00Z" }),
    ];
    const content = entries.map((e) => JSON.stringify(e)).join("\n") + "\n";
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const result = await HealthTrendsWriter.read("/workspace", {
      startDate: new Date("2026-01-10T00:00:00Z"),
      endDate: new Date("2026-01-20T00:00:00Z"),
    });
    expect(result).toHaveLength(1);
    expect(result[0].timestamp).toBe("2026-01-15T10:00:00Z");
  });

  it("returns all entries when no opts specified", async () => {
    const entries = makeEntries(5);
    const content = entries.map((e) => JSON.stringify(e)).join("\n") + "\n";
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const result = await HealthTrendsWriter.read("/workspace");
    expect(result).toHaveLength(5);
  });
});

// ── pruneOldEntries() ─────────────────────────────────────────────────────────

describe("HealthTrendsWriter.pruneOldEntries", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns 0 when file does not exist", async () => {
    vi.mocked(fs.readFile).mockRejectedValueOnce(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );
    const count = await HealthTrendsWriter.pruneOldEntries("/workspace");
    expect(count).toBe(0);
    expect(fs.writeFile).not.toHaveBeenCalled();
  });

  it("removes entries older than retentionDays and returns pruned count", async () => {
    const now = new Date();
    const old = new Date(now);
    old.setDate(old.getDate() - 100); // 100 days ago — beyond 90-day default

    const oldEntry = makeEntry({ timestamp: old.toISOString() });
    const recentEntry = makeEntry({ timestamp: now.toISOString() });
    const content = JSON.stringify(oldEntry) + "\n" + JSON.stringify(recentEntry) + "\n";

    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const pruned = await HealthTrendsWriter.pruneOldEntries("/workspace", 90);
    expect(pruned).toBe(1);
    expect(fs.writeFile).toHaveBeenCalledWith(
      expect.stringContaining("trends.jsonl"),
      expect.stringContaining(recentEntry.timestamp),
      "utf-8"
    );
  });

  it("keeps all entries when none are older than retentionDays", async () => {
    const now = new Date();
    const recent1 = makeEntry({ timestamp: now.toISOString() });
    const recent2 = makeEntry({
      timestamp: new Date(now.getTime() - 24 * 60 * 60 * 1000).toISOString(),
    });
    const content = JSON.stringify(recent1) + "\n" + JSON.stringify(recent2) + "\n";

    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    const pruned = await HealthTrendsWriter.pruneOldEntries("/workspace", 90);
    expect(pruned).toBe(0);
    // File is rewritten with both entries kept
    expect(fs.writeFile).toHaveBeenCalled();
  });

  it("uses custom retentionDays when provided", async () => {
    const now = new Date();
    const fiveDaysAgo = new Date(now);
    fiveDaysAgo.setDate(fiveDaysAgo.getDate() - 5);

    const oldEntry = makeEntry({ timestamp: fiveDaysAgo.toISOString() });
    const content = JSON.stringify(oldEntry) + "\n";
    vi.mocked(fs.readFile).mockResolvedValueOnce(content as never);

    // Use retentionDays=3 — the 5-day-old entry should be pruned
    const pruned = await HealthTrendsWriter.pruneOldEntries("/workspace", 3);
    expect(pruned).toBe(1);
  });
});

// ── getFilePath() ─────────────────────────────────────────────────────────────

describe("HealthTrendsWriter.getFilePath", () => {
  it("returns path within .nightgauge/health/", () => {
    const p = HealthTrendsWriter.getFilePath("/my/workspace");
    expect(p).toContain(".nightgauge");
    expect(p).toContain("health");
    expect(p).toContain("trends.jsonl");
  });
});
