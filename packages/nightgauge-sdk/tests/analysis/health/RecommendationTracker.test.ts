/**
 * Unit tests for RecommendationTracker (Issue #1103)
 *
 * Tests the JSONL-based recommendation history persistence, retention
 * enforcement, cross-referencing with GitHub, and effectiveness assessment.
 *
 * Mocks node:fs/promises and node:child_process at the module level to avoid
 * real filesystem and shell calls.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Must mock BEFORE importing RecommendationTracker
vi.mock("node:fs/promises", () => ({
  mkdir: vi.fn().mockResolvedValue(undefined),
  appendFile: vi.fn().mockResolvedValue(undefined),
  readFile: vi.fn().mockResolvedValue(""),
  writeFile: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("node:child_process", () => ({
  execSync: vi.fn().mockReturnValue('{"state": "OPEN"}'),
}));

import * as fs from "node:fs/promises";
import { execSync } from "node:child_process";
import { RecommendationTracker } from "../../../src/analysis/health/RecommendationTracker.js";
import { makeRecommendationEntry } from "./fixtures.js";
import type {
  RecommendationHistoryEntry,
  FindingToIssueResult,
  HealthAnalysisResult,
} from "../../../src/analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../src/analysis/health/types.js";

// ── Helpers ───────────────────────────────────────────────────────

const WORKSPACE = "/fake/workspace";

function entryToJsonl(entry: RecommendationHistoryEntry): string {
  return JSON.stringify(entry) + "\n";
}

function makeJsonlContent(entries: RecommendationHistoryEntry[]): string {
  return entries.map((e) => JSON.stringify(e)).join("\n") + "\n";
}

function makeMinimalAnalysisResult(
  overrides: Partial<HealthAnalysisResult> = {}
): HealthAnalysisResult {
  return {
    dimensions: {
      "token-economics": {
        dimension: "token-economics",
        score: 75,
        status: "good",
        findings: [],
        metrics: {},
        hasEnoughData: true,
        sampleSize: 10,
      },
      "cost-health": {
        dimension: "cost-health",
        score: 65,
        status: "fair",
        findings: [],
        metrics: {},
        hasEnoughData: true,
        sampleSize: 10,
      },
    },
    crossReferences: [],
    overallScore: 70,
    overallStatus: "good",
    summary: "Test analysis result.",
    analyzedAt: "2026-02-20T00:00:00Z",
    config: DEFAULT_HEALTH_CONFIG,
    ...overrides,
  };
}

const BASE_ISSUE_RESULT: FindingToIssueResult = {
  totalFindings: 2,
  filteredFindings: 1,
  duplicatesSkipped: 0,
  issuesCreated: 1,
  epicsCreated: 0,
  generatedIssues: [
    {
      findingId: "te-1",
      title: "Low cache hit rate",
      body: "## Recommendations\nEnable caching",
      labels: [],
      severity: "medium",
      dimension: "token-economics",
      issueNumber: 500,
    },
    {
      findingId: "te-2",
      title: "Skipped finding",
      body: "",
      labels: [],
      severity: "low",
      dimension: "token-economics",
      skipped: true,
      skipReason: "Duplicate",
    },
  ],
  epicGroups: [],
  dryRun: false,
};

// ── getFilePath ───────────────────────────────────────────────────

describe("RecommendationTracker.getFilePath()", () => {
  it("returns an absolute path under workspaceRoot", () => {
    const filePath = RecommendationTracker.getFilePath(WORKSPACE);
    expect(filePath).toBe("/fake/workspace/.nightgauge/pipeline/recommendation-history.jsonl");
  });

  it("includes the JSONL filename", () => {
    const filePath = RecommendationTracker.getFilePath(WORKSPACE);
    expect(filePath).toContain("recommendation-history.jsonl");
  });
});

// ── append ────────────────────────────────────────────────────────

describe("RecommendationTracker.append()", () => {
  beforeEach(() => {
    vi.mocked(fs.mkdir).mockReset();
    vi.mocked(fs.appendFile).mockReset();
    vi.mocked(fs.readFile).mockReset();
    vi.mocked(fs.writeFile).mockReset();
    vi.mocked(execSync).mockReset();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.readFile).mockResolvedValue("");
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');
  });

  it("calls mkdir with recursive: true before appending", async () => {
    const entry = makeRecommendationEntry();
    await RecommendationTracker.append(WORKSPACE, entry);

    expect(fs.mkdir).toHaveBeenCalledOnce();
    const [dirArg, optsArg] = vi.mocked(fs.mkdir).mock.calls[0];
    expect(String(dirArg)).toContain(".nightgauge/pipeline");
    expect(optsArg).toEqual({ recursive: true });
  });

  it("appends a JSON line followed by a newline", async () => {
    const entry = makeRecommendationEntry();
    await RecommendationTracker.append(WORKSPACE, entry);

    expect(fs.appendFile).toHaveBeenCalledOnce();
    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const line = String(dataArg);
    expect(line.endsWith("\n")).toBe(true);
    const parsed = JSON.parse(line.trimEnd());
    expect(parsed.finding_id).toBe(entry.finding_id);
    expect(parsed.schema_version).toBe("1");
  });

  it("writes to the correct file path", async () => {
    const entry = makeRecommendationEntry();
    await RecommendationTracker.append(WORKSPACE, entry);

    const [fileArg] = vi.mocked(fs.appendFile).mock.calls[0];
    expect(String(fileArg)).toBe(
      "/fake/workspace/.nightgauge/pipeline/recommendation-history.jsonl"
    );
  });

  it("silently swallows errors when appendFile rejects", async () => {
    vi.mocked(fs.appendFile).mockRejectedValue(new Error("Disk write error"));

    const entry = makeRecommendationEntry();
    // Must not throw
    await expect(RecommendationTracker.append(WORKSPACE, entry)).resolves.toBeUndefined();
  });

  it("silently swallows errors when mkdir rejects", async () => {
    vi.mocked(fs.mkdir).mockRejectedValue(new Error("Permission denied"));

    const entry = makeRecommendationEntry();
    await expect(RecommendationTracker.append(WORKSPACE, entry)).resolves.toBeUndefined();
  });
});

// ── readAll ───────────────────────────────────────────────────────

describe("RecommendationTracker.readAll()", () => {
  beforeEach(() => {
    vi.mocked(fs.mkdir).mockReset();
    vi.mocked(fs.appendFile).mockReset();
    vi.mocked(fs.readFile).mockReset();
    vi.mocked(fs.writeFile).mockReset();
    vi.mocked(execSync).mockReset();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.readFile).mockResolvedValue("");
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');
  });

  it("returns an empty array when the file is empty", async () => {
    vi.mocked(fs.readFile).mockResolvedValue("");
    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toEqual([]);
  });

  it("returns an empty array when the file does not exist (readFile rejects)", async () => {
    vi.mocked(fs.readFile).mockRejectedValue(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );
    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toEqual([]);
  });

  it("parses a single valid JSONL line", async () => {
    const entry = makeRecommendationEntry();
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));

    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toHaveLength(1);
    expect(result[0].finding_id).toBe(entry.finding_id);
  });

  it("parses multiple valid JSONL lines in order", async () => {
    const e1 = makeRecommendationEntry({ finding_id: "te-1" });
    const e2 = makeRecommendationEntry({
      finding_id: "ch-1",
      dimension: "cost-health",
    });
    const e3 = makeRecommendationEntry({
      finding_id: "re-1",
      dimension: "reliability",
    });
    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([e1, e2, e3]));

    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toHaveLength(3);
    expect(result[0].finding_id).toBe("te-1");
    expect(result[1].finding_id).toBe("ch-1");
    expect(result[2].finding_id).toBe("re-1");
  });

  it("skips malformed (non-JSON) lines", async () => {
    const entry = makeRecommendationEntry({ finding_id: "good-entry" });
    const content = "this is not json\n" + JSON.stringify(entry) + "\n" + "{broken json\n";
    vi.mocked(fs.readFile).mockResolvedValue(content);

    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toHaveLength(1);
    expect(result[0].finding_id).toBe("good-entry");
  });

  it('skips entries where schema_version is not "1"', async () => {
    const invalidVersion = {
      ...makeRecommendationEntry({ finding_id: "old-schema" }),
      schema_version: "0",
    };
    const validEntry = makeRecommendationEntry({ finding_id: "valid" });
    const content = JSON.stringify(invalidVersion) + "\n" + JSON.stringify(validEntry) + "\n";
    vi.mocked(fs.readFile).mockResolvedValue(content);

    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toHaveLength(1);
    expect(result[0].finding_id).toBe("valid");
  });

  it("skips blank lines without error", async () => {
    const entry = makeRecommendationEntry();
    const content = "\n" + JSON.stringify(entry) + "\n" + "   \n" + "\n";
    vi.mocked(fs.readFile).mockResolvedValue(content);

    const result = await RecommendationTracker.readAll(WORKSPACE);
    expect(result).toHaveLength(1);
  });
});

// ── enforceRetention ──────────────────────────────────────────────

describe("RecommendationTracker.enforceRetention()", () => {
  beforeEach(() => {
    vi.mocked(fs.mkdir).mockReset();
    vi.mocked(fs.appendFile).mockReset();
    vi.mocked(fs.readFile).mockReset();
    vi.mocked(fs.writeFile).mockReset();
    vi.mocked(execSync).mockReset();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.readFile).mockResolvedValue("");
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');
  });

  it("does nothing when the file does not exist", async () => {
    vi.mocked(fs.readFile).mockRejectedValue(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );

    await RecommendationTracker.enforceRetention(WORKSPACE, 90);
    expect(fs.writeFile).not.toHaveBeenCalled();
  });

  it("removes entries older than the retention window", async () => {
    const now = new Date();
    const oldDate = new Date(now);
    oldDate.setDate(oldDate.getDate() - 100); // 100 days ago — past 90-day default

    const oldEntry = makeRecommendationEntry({
      finding_id: "old",
      created_at: oldDate.toISOString(),
    });
    const recentDate = new Date(now);
    recentDate.setDate(recentDate.getDate() - 30); // 30 days ago — within retention
    const recentEntry = makeRecommendationEntry({
      finding_id: "recent",
      created_at: recentDate.toISOString(),
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([oldEntry, recentEntry]));

    await RecommendationTracker.enforceRetention(WORKSPACE, 90);

    expect(fs.writeFile).toHaveBeenCalledOnce();
    const [, writtenContent] = vi.mocked(fs.writeFile).mock.calls[0];
    const writtenStr = String(writtenContent);

    // Only recent entry should be present
    expect(writtenStr).toContain('"recent"');
    expect(writtenStr).not.toContain('"old"');
  });

  it("keeps entries within the retention window", async () => {
    const now = new Date();
    const recentDate = new Date(now);
    recentDate.setDate(recentDate.getDate() - 5); // 5 days ago

    const entry = makeRecommendationEntry({
      finding_id: "keep-me",
      created_at: recentDate.toISOString(),
    });

    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));

    await RecommendationTracker.enforceRetention(WORKSPACE, 90);

    expect(fs.writeFile).toHaveBeenCalledOnce();
    const [, writtenContent] = vi.mocked(fs.writeFile).mock.calls[0];
    expect(String(writtenContent)).toContain('"keep-me"');
  });

  it("writes an empty string when all entries are expired", async () => {
    const now = new Date();
    const veryOldDate = new Date(now);
    veryOldDate.setDate(veryOldDate.getDate() - 200);

    const oldEntry = makeRecommendationEntry({
      finding_id: "expired",
      created_at: veryOldDate.toISOString(),
    });

    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(oldEntry));

    await RecommendationTracker.enforceRetention(WORKSPACE, 90);

    expect(fs.writeFile).toHaveBeenCalledOnce();
    const [, writtenContent] = vi.mocked(fs.writeFile).mock.calls[0];
    expect(String(writtenContent)).toBe("");
  });

  it("uses the default retention of 90 days when no retentionDays is provided", async () => {
    const now = new Date();

    // 91 days ago — should be pruned
    const expiredDate = new Date(now);
    expiredDate.setDate(expiredDate.getDate() - 91);

    // 89 days ago — should be kept
    const keptDate = new Date(now);
    keptDate.setDate(keptDate.getDate() - 89);

    const expired = makeRecommendationEntry({
      finding_id: "expired",
      created_at: expiredDate.toISOString(),
    });
    const kept = makeRecommendationEntry({
      finding_id: "kept",
      created_at: keptDate.toISOString(),
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([expired, kept]));

    // Call without explicit retentionDays — should default to 90
    await RecommendationTracker.enforceRetention(WORKSPACE);

    const [, writtenContent] = vi.mocked(fs.writeFile).mock.calls[0];
    const written = String(writtenContent);
    expect(written).toContain('"kept"');
    expect(written).not.toContain('"expired"');
  });

  it("silently swallows errors from writeFile", async () => {
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(makeRecommendationEntry()));
    vi.mocked(fs.writeFile).mockRejectedValue(new Error("Write failed"));

    await expect(RecommendationTracker.enforceRetention(WORKSPACE, 90)).resolves.toBeUndefined();
  });
});

// ── recordFromIssueResult ─────────────────────────────────────────

describe("RecommendationTracker.recordFromIssueResult()", () => {
  beforeEach(() => {
    vi.mocked(fs.mkdir).mockReset();
    vi.mocked(fs.appendFile).mockReset();
    vi.mocked(fs.readFile).mockReset();
    vi.mocked(fs.writeFile).mockReset();
    vi.mocked(execSync).mockReset();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.readFile).mockResolvedValue("");
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');
  });

  it("appends only non-skipped issues", async () => {
    const analysisResult = makeMinimalAnalysisResult();
    await RecommendationTracker.recordFromIssueResult(WORKSPACE, BASE_ISSUE_RESULT, analysisResult);

    // BASE_ISSUE_RESULT has 1 non-skipped + 1 skipped → only 1 append call
    expect(fs.appendFile).toHaveBeenCalledOnce();
  });

  it("does not append skipped issues", async () => {
    const allSkippedResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [
        {
          findingId: "skip-1",
          title: "Skipped",
          body: "",
          labels: [],
          severity: "low",
          dimension: "token-economics",
          skipped: true,
          skipReason: "Duplicate",
        },
      ],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      allSkippedResult,
      makeMinimalAnalysisResult()
    );

    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  it("records the correct finding_id in the persisted entry", async () => {
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [BASE_ISSUE_RESULT.generatedIssues[0]],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      singleIssueResult,
      makeMinimalAnalysisResult()
    );

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    expect(parsed.finding_id).toBe("te-1");
  });

  it("extracts recommendation text from the ## Recommendations body section", async () => {
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [
        {
          findingId: "te-1",
          title: "Low cache hit rate",
          body: "## Recommendations\nEnable caching for repeated contexts.",
          labels: [],
          severity: "medium",
          dimension: "token-economics",
          issueNumber: 500,
        },
      ],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      singleIssueResult,
      makeMinimalAnalysisResult()
    );

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    expect(parsed.recommendation).toBe("Enable caching for repeated contexts.");
  });

  it("falls back to title as recommendation when body has no ## Recommendations section", async () => {
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [
        {
          findingId: "te-1",
          title: "Low cache hit rate",
          body: "## Description\nSome description here.",
          labels: [],
          severity: "medium",
          dimension: "token-economics",
          issueNumber: 500,
        },
      ],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      singleIssueResult,
      makeMinimalAnalysisResult()
    );

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    expect(parsed.recommendation).toBe("Low cache hit rate");
  });

  it('records issue_state as "open" when issueNumber is present', async () => {
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [
        {
          findingId: "te-1",
          title: "Low cache hit rate",
          body: "",
          labels: [],
          severity: "medium",
          dimension: "token-economics",
          issueNumber: 500,
        },
      ],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      singleIssueResult,
      makeMinimalAnalysisResult()
    );

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    expect(parsed.issue_state).toBe("open");
  });

  it('records issue_state as "not_created" when issueNumber is absent', async () => {
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [
        {
          findingId: "te-no-number",
          title: "Finding without issue",
          body: "",
          labels: [],
          severity: "medium",
          dimension: "token-economics",
          // No issueNumber
        },
      ],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      singleIssueResult,
      makeMinimalAnalysisResult()
    );

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    expect(parsed.issue_state).toBe("not_created");
  });

  it("records metric_before from the dimension score in analysisResult", async () => {
    const analysisResult = makeMinimalAnalysisResult();
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [
        {
          findingId: "te-1",
          title: "Low cache hit rate",
          body: "",
          labels: [],
          severity: "medium",
          dimension: "token-economics",
          issueNumber: 500,
        },
      ],
    };

    await RecommendationTracker.recordFromIssueResult(WORKSPACE, singleIssueResult, analysisResult);

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    // token-economics score is 75 in makeMinimalAnalysisResult
    expect(parsed.metric_before).toBe(75);
  });

  it('records schema_version "1"', async () => {
    const singleIssueResult: FindingToIssueResult = {
      ...BASE_ISSUE_RESULT,
      generatedIssues: [BASE_ISSUE_RESULT.generatedIssues[0]],
    };

    await RecommendationTracker.recordFromIssueResult(
      WORKSPACE,
      singleIssueResult,
      makeMinimalAnalysisResult()
    );

    const [, dataArg] = vi.mocked(fs.appendFile).mock.calls[0];
    const parsed = JSON.parse(String(dataArg).trimEnd()) as RecommendationHistoryEntry;
    expect(parsed.schema_version).toBe("1");
  });

  it("silently swallows errors (non-critical path)", async () => {
    vi.mocked(fs.mkdir).mockRejectedValue(new Error("mkdir failed"));

    await expect(
      RecommendationTracker.recordFromIssueResult(
        WORKSPACE,
        BASE_ISSUE_RESULT,
        makeMinimalAnalysisResult()
      )
    ).resolves.toBeUndefined();
  });
});

// ── crossReference ────────────────────────────────────────────────

describe("RecommendationTracker.crossReference()", () => {
  beforeEach(() => {
    vi.mocked(fs.mkdir).mockReset();
    vi.mocked(fs.appendFile).mockReset();
    vi.mocked(fs.readFile).mockReset();
    vi.mocked(fs.writeFile).mockReset();
    vi.mocked(execSync).mockReset();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.readFile).mockResolvedValue("");
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');
  });

  it("returns an empty array when there are no entries", async () => {
    vi.mocked(fs.readFile).mockResolvedValue("");
    const result = await RecommendationTracker.crossReference(WORKSPACE);
    expect(result).toEqual([]);
  });

  it("calls gh issue view for open entries with an issue_number", async () => {
    const entry = makeRecommendationEntry({
      issue_number: 200,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    await RecommendationTracker.crossReference(WORKSPACE);

    expect(execSync).toHaveBeenCalledOnce();
    const [cmdArg] = vi.mocked(execSync).mock.calls[0];
    expect(String(cmdArg)).toContain("gh issue view 200");
    expect(String(cmdArg)).toContain("--json state");
  });

  it('updates issue_state to "closed" when gh returns CLOSED state', async () => {
    const entry = makeRecommendationEntry({
      issue_number: 200,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    const result = await RecommendationTracker.crossReference(WORKSPACE);

    expect(result).toHaveLength(1);
    expect(result[0].issue_state).toBe("closed");
  });

  it('keeps issue_state as "open" when gh returns OPEN state', async () => {
    const entry = makeRecommendationEntry({
      issue_number: 200,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    const result = await RecommendationTracker.crossReference(WORKSPACE);

    expect(result).toHaveLength(1);
    expect(result[0].issue_state).toBe("open");
  });

  it("sets assessed_at on updated entries", async () => {
    const entry = makeRecommendationEntry({
      issue_number: 200,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    const result = await RecommendationTracker.crossReference(WORKSPACE);

    expect(result[0].assessed_at).toBeDefined();
    expect(() => new Date(result[0].assessed_at!)).not.toThrow();
  });

  it("skips already-closed entries (does not call gh issue view)", async () => {
    const closedEntry = makeRecommendationEntry({
      issue_number: 200,
      issue_state: "closed",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(closedEntry));

    await RecommendationTracker.crossReference(WORKSPACE);

    expect(execSync).not.toHaveBeenCalled();
  });

  it("skips entries without an issue_number", async () => {
    const noNumberEntry = makeRecommendationEntry({
      issue_number: undefined,
      issue_state: "not_created",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(noNumberEntry));

    await RecommendationTracker.crossReference(WORKSPACE);

    expect(execSync).not.toHaveBeenCalled();
  });

  it("leaves entry unchanged when gh command fails (graceful error handling)", async () => {
    const entry = makeRecommendationEntry({
      finding_id: "stable-entry",
      issue_number: 200,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockImplementation(() => {
      throw new Error("gh: command not found");
    });

    const result = await RecommendationTracker.crossReference(WORKSPACE);

    expect(result).toHaveLength(1);
    expect(result[0].issue_state).toBe("open");
    expect(result[0].finding_id).toBe("stable-entry");
  });

  it("rewrites the file with updated entries", async () => {
    const entry = makeRecommendationEntry({
      issue_number: 200,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    await RecommendationTracker.crossReference(WORKSPACE);

    expect(fs.writeFile).toHaveBeenCalledOnce();
    const [, writtenContent] = vi.mocked(fs.writeFile).mock.calls[0];
    const parsed = JSON.parse(String(writtenContent).split("\n")[0]) as RecommendationHistoryEntry;
    expect(parsed.issue_state).toBe("closed");
  });

  it("processes multiple entries and only queries open ones", async () => {
    const openEntry = makeRecommendationEntry({
      finding_id: "open-one",
      issue_number: 100,
      issue_state: "open",
    });
    const closedEntry = makeRecommendationEntry({
      finding_id: "closed-one",
      issue_number: 101,
      issue_state: "closed",
    });
    const noNumberEntry = makeRecommendationEntry({
      finding_id: "no-number",
      issue_number: undefined,
      issue_state: "not_created",
    });

    vi.mocked(fs.readFile).mockResolvedValue(
      makeJsonlContent([openEntry, closedEntry, noNumberEntry])
    );
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    const result = await RecommendationTracker.crossReference(WORKSPACE);

    // gh should only be called once (for the open entry)
    expect(execSync).toHaveBeenCalledOnce();
    expect(result).toHaveLength(3);
  });
});

// ── detectRecurringFindings ───────────────────────────────────────

describe("RecommendationTracker.detectRecurringFindings()", () => {
  it("returns an empty array when there are no entries", () => {
    const result = RecommendationTracker.detectRecurringFindings([]);
    expect(result).toEqual([]);
  });

  it("returns an empty array when no title appears more than once", () => {
    const entries = [
      makeRecommendationEntry({ finding_id: "te-1", title: "Unique title A" }),
      makeRecommendationEntry({ finding_id: "te-2", title: "Unique title B" }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result).toEqual([]);
  });

  it("groups entries with the same normalized title", () => {
    const entries = [
      makeRecommendationEntry({
        finding_id: "te-1",
        title: "Low cache hit rate",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        finding_id: "te-2",
        title: "Low cache hit rate",
        created_at: "2026-01-15T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result).toHaveLength(1);
    expect(result[0].occurrence_count).toBe(2);
  });

  it("removes [HEALTH] prefix when normalizing titles", () => {
    const entries = [
      makeRecommendationEntry({
        finding_id: "te-1",
        title: "[HEALTH] Low cache hit rate",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        finding_id: "te-2",
        title: "Low cache hit rate",
        created_at: "2026-01-10T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result).toHaveLength(1);
    expect(result[0].occurrence_count).toBe(2);
  });

  it("is case-insensitive when normalizing titles", () => {
    const entries = [
      makeRecommendationEntry({
        finding_id: "te-1",
        title: "Low Cache Hit Rate",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        finding_id: "te-2",
        title: "low cache hit rate",
        created_at: "2026-01-15T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result).toHaveLength(1);
  });

  it("returns correct first_seen and last_seen timestamps", () => {
    const first = "2026-01-01T00:00:00Z";
    const last = "2026-02-01T00:00:00Z";
    const entries = [
      makeRecommendationEntry({ title: "Recurring issue", created_at: last }),
      makeRecommendationEntry({ title: "Recurring issue", created_at: first }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result[0].first_seen).toBe(first);
    expect(result[0].last_seen).toBe(last);
  });

  it("collects all issue_numbers from the group", () => {
    const entries = [
      makeRecommendationEntry({
        title: "Recurring issue",
        issue_number: 100,
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        title: "Recurring issue",
        issue_number: 200,
        created_at: "2026-01-15T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result[0].issue_numbers).toContain(100);
    expect(result[0].issue_numbers).toContain(200);
  });

  it("sets all_closed=true when all entries with issue_numbers are closed", () => {
    const entries = [
      makeRecommendationEntry({
        title: "Recurring issue",
        issue_number: 100,
        issue_state: "closed",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        title: "Recurring issue",
        issue_number: 200,
        issue_state: "closed",
        created_at: "2026-01-15T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result[0].all_closed).toBe(true);
  });

  it("sets all_closed=false when at least one entry is still open", () => {
    const entries = [
      makeRecommendationEntry({
        title: "Recurring issue",
        issue_number: 100,
        issue_state: "closed",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        title: "Recurring issue",
        issue_number: 200,
        issue_state: "open",
        created_at: "2026-01-15T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result[0].all_closed).toBe(false);
  });

  it("handles groups of 3 or more occurrences", () => {
    const entries = [
      makeRecommendationEntry({
        finding_id: "f1",
        title: "Triple occurrence",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        finding_id: "f2",
        title: "Triple occurrence",
        created_at: "2026-01-15T00:00:00Z",
      }),
      makeRecommendationEntry({
        finding_id: "f3",
        title: "Triple occurrence",
        created_at: "2026-02-01T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result).toHaveLength(1);
    expect(result[0].occurrence_count).toBe(3);
  });

  it("returns the dimension from the first (chronologically earliest) entry", () => {
    const entries = [
      makeRecommendationEntry({
        title: "Mixed dimension recurring",
        dimension: "reliability",
        created_at: "2026-01-01T00:00:00Z",
      }),
      makeRecommendationEntry({
        title: "Mixed dimension recurring",
        dimension: "cost-health",
        created_at: "2026-01-15T00:00:00Z",
      }),
    ];
    const result = RecommendationTracker.detectRecurringFindings(entries);
    expect(result[0].dimension).toBe("reliability");
  });
});

// ── assessEffectiveness ───────────────────────────────────────────

describe("RecommendationTracker.assessEffectiveness()", () => {
  beforeEach(() => {
    vi.mocked(fs.mkdir).mockReset();
    vi.mocked(fs.appendFile).mockReset();
    vi.mocked(fs.readFile).mockReset();
    vi.mocked(fs.writeFile).mockReset();
    vi.mocked(execSync).mockReset();

    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.readFile).mockResolvedValue("");
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');
  });

  it("returns a valid RecommendationReport structure", async () => {
    vi.mocked(fs.readFile).mockResolvedValue("");

    const report = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(report).toHaveProperty("assessed_at");
    expect(report).toHaveProperty("effectiveness");
    expect(report).toHaveProperty("recurring_findings");
    expect(report).toHaveProperty("self_assessment");
    expect(report).toHaveProperty("entries");
  });

  it('classifies as "effective" when >= 60% of closed issues improved', async () => {
    // Three closed entries: 2 improved (metric_after > metric_before), 1 no effect
    const e1 = makeRecommendationEntry({
      finding_id: "f1",
      issue_number: 101,
      issue_state: "closed",
      metric_before: 60,
      health_report_ref: "report-A",
    });
    const e2 = makeRecommendationEntry({
      finding_id: "f2",
      issue_number: 102,
      issue_state: "closed",
      metric_before: 55,
      health_report_ref: "report-A",
    });
    const e3 = makeRecommendationEntry({
      finding_id: "f3",
      issue_number: 103,
      issue_state: "closed",
      metric_before: 80,
      health_report_ref: "report-A",
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([e1, e2, e3]));
    // All entries already closed → crossReference won't change state, gh won't be called
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    // Provide metric_after >= metric_before for f1 and f2 via analysis result:
    // token-economics score = 75 > 60 → improved for f1
    // token-economics score = 75 > 55 → improved for f2
    // token-economics score = 75 < 80 → no effect for f3
    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    // All 3 are closed, 2 improved → 66.7% → effective
    expect(result.self_assessment.overall_effectiveness).toBe("effective");
    expect(result.effectiveness.effectiveness_percent).toBeGreaterThanOrEqual(60);
  });

  it('classifies as "mixed" when >= 30% but < 60% of closed issues improved', async () => {
    // 3 closed entries: 1 improved, 2 no effect → 33.3%
    const e1 = makeRecommendationEntry({
      finding_id: "f1",
      issue_number: 101,
      issue_state: "closed",
      dimension: "token-economics",
      metric_before: 60, // token-economics score 75 > 60 → improved
      health_report_ref: "report-B",
    });
    const e2 = makeRecommendationEntry({
      finding_id: "f2",
      issue_number: 102,
      issue_state: "closed",
      dimension: "token-economics",
      metric_before: 90, // token-economics score 75 < 90 → no effect
      health_report_ref: "report-B",
    });
    const e3 = makeRecommendationEntry({
      finding_id: "f3",
      issue_number: 103,
      issue_state: "closed",
      dimension: "token-economics",
      metric_before: 85, // token-economics score 75 < 85 → no effect
      health_report_ref: "report-B",
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([e1, e2, e3]));
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(result.self_assessment.overall_effectiveness).toBe("mixed");
  });

  it('classifies as "ineffective" when < 30% of closed issues improved', async () => {
    // All 3 closed but none improved
    const mkEntry = (id: string, issuNum: number) =>
      makeRecommendationEntry({
        finding_id: id,
        issue_number: issuNum,
        issue_state: "closed",
        dimension: "token-economics",
        metric_before: 95, // 95 > 75 current score → no improvement
        health_report_ref: "report-C",
      });

    const entries = [mkEntry("f1", 101), mkEntry("f2", 102), mkEntry("f3", 103)];
    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent(entries));
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(result.self_assessment.overall_effectiveness).toBe("ineffective");
    expect(result.effectiveness.effectiveness_percent).toBe(0);
  });

  it('classifies as "ineffective" when there are no closed entries', async () => {
    const entry = makeRecommendationEntry({
      issue_number: 100,
      issue_state: "open",
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(result.effectiveness.implemented_count).toBe(0);
    expect(result.effectiveness.effectiveness_percent).toBe(0);
    expect(result.self_assessment.overall_effectiveness).toBe("ineffective");
  });

  it("counts pending, implemented, and not_created correctly", async () => {
    const open = makeRecommendationEntry({
      finding_id: "open-1",
      issue_number: 100,
      issue_state: "open",
    });
    const closed = makeRecommendationEntry({
      finding_id: "closed-1",
      issue_number: 101,
      issue_state: "closed",
    });
    const notCreated = makeRecommendationEntry({
      finding_id: "nc-1",
      issue_number: undefined,
      issue_state: "not_created",
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([open, closed, notCreated]));
    // open entry → gh returns OPEN; closed entry → skipped (already closed)
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(result.effectiveness.total_recommendations).toBe(3);
    expect(result.effectiveness.pending_count).toBe(1);
    expect(result.effectiveness.implemented_count).toBe(1);
    expect(result.effectiveness.not_created_count).toBe(1);
  });

  it("computes improvement_percent correctly for closed entries", async () => {
    const entry = makeRecommendationEntry({
      issue_number: 100,
      issue_state: "closed",
      dimension: "token-economics",
      metric_before: 50,
    });
    vi.mocked(fs.readFile).mockResolvedValue(entryToJsonl(entry));
    vi.mocked(execSync).mockReturnValue('{"state": "CLOSED"}');

    // token-economics score in analysisResult = 75
    // improvement = ((75 - 50) / 50) * 100 = 50%
    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    const updatedEntry = result.entries.find((e) => e.issue_number === 100);
    expect(updatedEntry?.metric_after).toBe(75);
    expect(updatedEntry?.improvement_percent).toBeCloseTo(50, 1);
  });

  it("detects recurring findings within the report", async () => {
    const e1 = makeRecommendationEntry({
      finding_id: "f1",
      title: "Recurring cache issue",
      created_at: "2026-01-01T00:00:00Z",
    });
    const e2 = makeRecommendationEntry({
      finding_id: "f2",
      title: "Recurring cache issue",
      created_at: "2026-01-15T00:00:00Z",
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([e1, e2]));
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(result.recurring_findings).toHaveLength(1);
    expect(result.recurring_findings[0].occurrence_count).toBe(2);
  });

  it("includes total_health_checks based on unique health_report_refs", async () => {
    const e1 = makeRecommendationEntry({
      finding_id: "f1",
      health_report_ref: "report-X",
    });
    const e2 = makeRecommendationEntry({
      finding_id: "f2",
      health_report_ref: "report-Y",
    });
    const e3 = makeRecommendationEntry({
      finding_id: "f3",
      health_report_ref: "report-X", // duplicate ref
    });

    vi.mocked(fs.readFile).mockResolvedValue(makeJsonlContent([e1, e2, e3]));
    vi.mocked(execSync).mockReturnValue('{"state": "OPEN"}');

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    // Two unique refs: report-X, report-Y
    expect(result.self_assessment.total_health_checks).toBe(2);
  });

  it("returns assessed_at as a valid ISO 8601 timestamp", async () => {
    vi.mocked(fs.readFile).mockResolvedValue("");

    const result = await RecommendationTracker.assessEffectiveness(
      WORKSPACE,
      makeMinimalAnalysisResult()
    );

    expect(() => new Date(result.assessed_at)).not.toThrow();
    expect(new Date(result.assessed_at).getTime()).not.toBeNaN();
  });
});
