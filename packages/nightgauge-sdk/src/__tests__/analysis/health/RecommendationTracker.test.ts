import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { execSync } from "node:child_process";
import { RecommendationTracker } from "../../../analysis/health/RecommendationTracker.js";
import type {
  RecommendationHistoryEntry,
  HealthAnalysisResult,
  FindingToIssueResult,
  GeneratedIssue,
  DimensionResult,
} from "../../../analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../analysis/health/types.js";

vi.mock("node:fs/promises");
vi.mock("node:child_process");

// ── Test Data Factories ────────────────────────────────────────────

function makeEntry(
  overrides: Partial<RecommendationHistoryEntry> = {}
): RecommendationHistoryEntry {
  return {
    schema_version: "1",
    finding_id: "te-1",
    created_at: "2026-01-15T10:00:00Z",
    severity: "high",
    dimension: "token-economics",
    title: "[HEALTH] High token waste detected",
    recommendation: "Reduce token waste by optimizing prompts",
    issue_number: 42,
    issue_url: "https://github.com/nightgauge/nightgauge/issues/42",
    issue_state: "open",
    metric_before: 60,
    health_report_ref: "report-2026-01.json",
    ...overrides,
  };
}

function makeAnalysisResult(
  dimensionOverrides: Partial<Record<string, Partial<DimensionResult>>> = {}
): HealthAnalysisResult {
  const baseDimension: DimensionResult = {
    dimension: "token-economics",
    score: 75,
    status: "good",
    findings: [],
    metrics: {},
    hasEnoughData: true,
    sampleSize: 10,
  };

  const dimensions: HealthAnalysisResult["dimensions"] = {};
  for (const [key, overrides] of Object.entries(dimensionOverrides)) {
    dimensions[key as keyof typeof dimensions] = {
      ...baseDimension,
      dimension: key as DimensionResult["dimension"],
      ...overrides,
    } as DimensionResult;
  }

  return {
    dimensions,
    crossReferences: [],
    overallScore: 70,
    overallStatus: "good",
    summary: "Test analysis result",
    analyzedAt: "2026-02-01T10:00:00Z",
    config: DEFAULT_HEALTH_CONFIG,
  };
}

function makeIssueResult(generatedIssues: Partial<GeneratedIssue>[] = []): FindingToIssueResult {
  return {
    totalFindings: generatedIssues.length,
    filteredFindings: generatedIssues.length,
    duplicatesSkipped: 0,
    issuesCreated: generatedIssues.length,
    epicsCreated: 0,
    generatedIssues: generatedIssues.map((gi) => ({
      findingId: "te-1",
      title: "[HEALTH] High token waste detected",
      body: "## Description\nToken waste is high.\n\n## Recommendation\nReduce token waste by optimizing prompts.\n\n## Evidence\nSee report.",
      labels: ["priority:high"],
      severity: "high" as const,
      dimension: "token-economics" as const,
      issueNumber: 100,
      issueUrl: "https://github.com/nightgauge/nightgauge/issues/100",
      ...gi,
    })),
    epicGroups: [],
    dryRun: false,
    healthReportRef: "report-2026-01.json",
  };
}

const WORKSPACE = "/tmp/test-workspace";
const FILE_PATH = path.join(WORKSPACE, ".nightgauge/pipeline/recommendation-history.jsonl");

// ── Tests ──────────────────────────────────────────────────────────

describe("RecommendationTracker", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue(undefined);
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getFilePath()", () => {
    it("returns the correct JSONL path under workspace root", () => {
      const result = RecommendationTracker.getFilePath(WORKSPACE);
      expect(result).toBe(FILE_PATH);
    });
  });

  describe("append()", () => {
    it("creates directory and appends entry as JSON line", async () => {
      const entry = makeEntry();

      await RecommendationTracker.append(WORKSPACE, entry);

      expect(fs.mkdir).toHaveBeenCalledWith(path.dirname(FILE_PATH), {
        recursive: true,
      });
      expect(fs.appendFile).toHaveBeenCalledWith(FILE_PATH, JSON.stringify(entry) + "\n", "utf-8");
    });

    it("swallows errors silently", async () => {
      vi.mocked(fs.mkdir).mockRejectedValue(new Error("Permission denied"));

      // Should not throw
      await RecommendationTracker.append(WORKSPACE, makeEntry());
    });
  });

  describe("readAll()", () => {
    it("returns empty array when file does not exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT: no such file"));

      const result = await RecommendationTracker.readAll(WORKSPACE);
      expect(result).toEqual([]);
    });

    it("parses valid JSONL entries", async () => {
      const entry1 = makeEntry({ finding_id: "te-1" });
      const entry2 = makeEntry({ finding_id: "te-2" });
      const content = JSON.stringify(entry1) + "\n" + JSON.stringify(entry2) + "\n";

      vi.mocked(fs.readFile).mockResolvedValue(content);

      const result = await RecommendationTracker.readAll(WORKSPACE);
      expect(result).toHaveLength(2);
      expect(result[0].finding_id).toBe("te-1");
      expect(result[1].finding_id).toBe("te-2");
    });

    it("skips malformed lines", async () => {
      const valid = makeEntry();
      const content = JSON.stringify(valid) + "\n" + "not-valid-json\n" + "\n";

      vi.mocked(fs.readFile).mockResolvedValue(content);

      const result = await RecommendationTracker.readAll(WORKSPACE);
      expect(result).toHaveLength(1);
    });

    it("skips entries with wrong schema_version", async () => {
      const content = JSON.stringify({ ...makeEntry(), schema_version: "2" }) + "\n";

      vi.mocked(fs.readFile).mockResolvedValue(content);

      const result = await RecommendationTracker.readAll(WORKSPACE);
      expect(result).toHaveLength(0);
    });
  });

  describe("enforceRetention()", () => {
    it("returns silently when file does not exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      await RecommendationTracker.enforceRetention(WORKSPACE);
      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("removes entries older than retention period", async () => {
      const recent = makeEntry({
        created_at: new Date().toISOString(),
      });
      const old = makeEntry({
        created_at: "2020-01-01T00:00:00Z",
        finding_id: "old-1",
      });

      const content = JSON.stringify(old) + "\n" + JSON.stringify(recent) + "\n";

      vi.mocked(fs.readFile).mockResolvedValue(content);

      await RecommendationTracker.enforceRetention(WORKSPACE, 90);

      expect(fs.writeFile).toHaveBeenCalledTimes(1);
      const writtenContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(writtenContent).toContain(recent.finding_id);
      expect(writtenContent).not.toContain("old-1");
    });
  });

  describe("recordFromIssueResult()", () => {
    it("creates one entry per non-skipped generated issue", async () => {
      const analysisResult = makeAnalysisResult({
        "token-economics": { score: 60 },
      });

      const issueResult = makeIssueResult([
        { findingId: "te-1", issueNumber: 100 },
        { findingId: "te-2", issueNumber: 101 },
      ]);

      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      await RecommendationTracker.recordFromIssueResult(WORKSPACE, issueResult, analysisResult);

      // append is called once per non-skipped issue
      expect(fs.appendFile).toHaveBeenCalledTimes(2);
    });

    it("skips entries marked as skipped", async () => {
      const analysisResult = makeAnalysisResult({
        "token-economics": { score: 60 },
      });

      const issueResult = makeIssueResult([
        {
          findingId: "te-1",
          issueNumber: 100,
          skipped: true,
          skipReason: "Duplicate",
        },
        { findingId: "te-2", issueNumber: 101 },
      ]);

      await RecommendationTracker.recordFromIssueResult(WORKSPACE, issueResult, analysisResult);

      expect(fs.appendFile).toHaveBeenCalledTimes(1);
    });

    it("sets issue_state to not_created when no issue number", async () => {
      const analysisResult = makeAnalysisResult({
        "token-economics": { score: 60 },
      });

      const issueResult = makeIssueResult([
        { findingId: "te-1", issueNumber: undefined, issueUrl: undefined },
      ]);

      await RecommendationTracker.recordFromIssueResult(WORKSPACE, issueResult, analysisResult);

      expect(fs.appendFile).toHaveBeenCalledTimes(1);
      const appendedContent = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(appendedContent.trim());
      expect(parsed.issue_state).toBe("not_created");
    });

    it("extracts recommendation from issue body when available", async () => {
      const analysisResult = makeAnalysisResult({
        "token-economics": { score: 60 },
      });

      const issueResult = makeIssueResult([{ findingId: "te-1", issueNumber: 100 }]);

      await RecommendationTracker.recordFromIssueResult(WORKSPACE, issueResult, analysisResult);

      const appendedContent = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
      const parsed = JSON.parse(appendedContent.trim());
      expect(parsed.recommendation).toBe("Reduce token waste by optimizing prompts.");
    });
  });

  describe("crossReference()", () => {
    it("updates open entries with current GitHub issue state", async () => {
      const entry = makeEntry({ issue_state: "open", issue_number: 42 });
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(entry) + "\n");
      vi.mocked(execSync).mockReturnValue(JSON.stringify({ state: "CLOSED" }));

      const result = await RecommendationTracker.crossReference(WORKSPACE);

      expect(result).toHaveLength(1);
      expect(result[0].issue_state).toBe("closed");
      expect(result[0].assessed_at).toBeDefined();
    });

    it("leaves already-closed entries unchanged", async () => {
      const entry = makeEntry({
        issue_state: "closed",
        issue_number: 42,
        assessed_at: "2026-01-01T00:00:00Z",
      });
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(entry) + "\n");

      const result = await RecommendationTracker.crossReference(WORKSPACE);

      expect(execSync).not.toHaveBeenCalled();
      expect(result[0].issue_state).toBe("closed");
    });

    it("leaves entries without issue_number unchanged", async () => {
      const entry = makeEntry({
        issue_state: "not_created",
        issue_number: undefined,
      });
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(entry) + "\n");

      const result = await RecommendationTracker.crossReference(WORKSPACE);

      expect(execSync).not.toHaveBeenCalled();
      expect(result[0].issue_state).toBe("not_created");
    });

    it("handles gh command failure gracefully", async () => {
      const entry = makeEntry({ issue_state: "open", issue_number: 42 });
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(entry) + "\n");
      vi.mocked(execSync).mockImplementation(() => {
        throw new Error("gh: not logged in");
      });

      const result = await RecommendationTracker.crossReference(WORKSPACE);

      expect(result).toHaveLength(1);
      expect(result[0].issue_state).toBe("open"); // unchanged
    });
  });

  describe("assessEffectiveness()", () => {
    it("computes correct effectiveness scores", async () => {
      const closedEntry = makeEntry({
        finding_id: "te-1",
        issue_state: "closed",
        issue_number: 42,
        metric_before: 60,
        dimension: "token-economics",
      });
      const openEntry = makeEntry({
        finding_id: "te-2",
        issue_state: "open",
        issue_number: 43,
        metric_before: 50,
        dimension: "reliability",
      });
      const notCreated = makeEntry({
        finding_id: "te-3",
        issue_state: "not_created",
        issue_number: undefined,
        dimension: "cost-health",
      });

      const content =
        JSON.stringify(closedEntry) +
        "\n" +
        JSON.stringify(openEntry) +
        "\n" +
        JSON.stringify(notCreated) +
        "\n";

      vi.mocked(fs.readFile).mockResolvedValue(content);
      // gh returns current state for open entry
      vi.mocked(execSync).mockReturnValue(JSON.stringify({ state: "OPEN" }));

      const currentAnalysis = makeAnalysisResult({
        "token-economics": { score: 80 }, // improved from 60
        reliability: { score: 50 },
      });

      const report = await RecommendationTracker.assessEffectiveness(WORKSPACE, currentAnalysis);

      expect(report.effectiveness.total_recommendations).toBe(3);
      expect(report.effectiveness.implemented_count).toBe(1);
      expect(report.effectiveness.pending_count).toBe(1);
      expect(report.effectiveness.not_created_count).toBe(1);
      expect(report.effectiveness.improved_count).toBe(1);
      expect(report.effectiveness.effectiveness_percent).toBeCloseTo(100);
    });

    it("handles metric_before of 0 correctly", async () => {
      const entry = makeEntry({
        issue_state: "closed",
        issue_number: 42,
        metric_before: 0,
        dimension: "token-economics",
      });

      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(entry) + "\n");
      vi.mocked(execSync).mockReturnValue("{}");

      const analysis = makeAnalysisResult({
        "token-economics": { score: 50 },
      });

      const report = await RecommendationTracker.assessEffectiveness(WORKSPACE, analysis);

      const updatedEntry = report.entries[0];
      expect(updatedEntry.improvement_percent).toBe(100);
    });

    it("returns empty report when no entries exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      const analysis = makeAnalysisResult({});

      const report = await RecommendationTracker.assessEffectiveness(WORKSPACE, analysis);

      expect(report.effectiveness.total_recommendations).toBe(0);
      expect(report.recurring_findings).toEqual([]);
    });
  });

  describe("detectRecurringFindings()", () => {
    it("groups entries by normalized title", () => {
      const entries = [
        makeEntry({
          title: "[HEALTH] High token waste detected",
          created_at: "2026-01-01T00:00:00Z",
          issue_number: 42,
          issue_state: "closed",
        }),
        makeEntry({
          title: "[health] high token waste detected",
          created_at: "2026-02-01T00:00:00Z",
          issue_number: 43,
          issue_state: "open",
        }),
      ];

      const result = RecommendationTracker.detectRecurringFindings(entries);

      expect(result).toHaveLength(1);
      expect(result[0].occurrence_count).toBe(2);
      expect(result[0].issue_numbers).toEqual([42, 43]);
      expect(result[0].first_seen).toBe("2026-01-01T00:00:00Z");
      expect(result[0].last_seen).toBe("2026-02-01T00:00:00Z");
    });

    it("returns empty array when no recurring findings", () => {
      const entries = [makeEntry({ title: "Finding A" }), makeEntry({ title: "Finding B" })];

      const result = RecommendationTracker.detectRecurringFindings(entries);
      expect(result).toHaveLength(0);
    });

    it("sets all_closed correctly", () => {
      const entries = [
        makeEntry({
          title: "Same finding",
          issue_number: 42,
          issue_state: "closed",
        }),
        makeEntry({
          title: "Same finding",
          issue_number: 43,
          issue_state: "closed",
        }),
      ];

      const result = RecommendationTracker.detectRecurringFindings(entries);
      expect(result[0].all_closed).toBe(true);
    });

    it("sets all_closed to false when not all closed", () => {
      const entries = [
        makeEntry({
          title: "Same finding",
          issue_number: 42,
          issue_state: "closed",
        }),
        makeEntry({
          title: "Same finding",
          issue_number: 43,
          issue_state: "open",
        }),
      ];

      const result = RecommendationTracker.detectRecurringFindings(entries);
      expect(result[0].all_closed).toBe(false);
    });
  });
});
