import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { HealthReportGenerator } from "../../../analysis/health/HealthReportGenerator.js";
import { HealthReportSchema } from "../../../analysis/health/reportSchema.js";
import type {
  HealthAnalysisResult,
  DimensionResult,
  Finding,
  CrossReference,
  RecommendationReport,
} from "../../../analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../analysis/health/types.js";

vi.mock("node:fs/promises");

// ── Test Data Factories ────────────────────────────────────────────

function makeFinding(overrides: Partial<Finding> = {}): Finding {
  return {
    id: "te-1",
    dimension: "token-economics",
    severity: "high",
    title: "High token waste",
    description: "Token waste detected",
    impact: "Increased costs",
    recommendation: "Optimize prompts",
    evidence: {},
    confidence: "high",
    ...overrides,
  };
}

function makeDimensionResult(overrides: Partial<DimensionResult> = {}): DimensionResult {
  return {
    dimension: "token-economics",
    score: 75,
    status: "good",
    findings: [],
    metrics: { avg_waste_ratio: 0.15 },
    hasEnoughData: true,
    sampleSize: 20,
    ...overrides,
  };
}

function makeAnalysisResult(overrides: Partial<HealthAnalysisResult> = {}): HealthAnalysisResult {
  return {
    dimensions: {
      "token-economics": makeDimensionResult(),
    },
    crossReferences: [],
    overallScore: 75,
    overallStatus: "good",
    summary: "Pipeline is operating well across 1 dimension(s).",
    analyzedAt: "2026-02-20T10:00:00Z",
    config: DEFAULT_HEALTH_CONFIG,
    ...overrides,
  };
}

function makeRecommendationReport(
  overrides: Partial<RecommendationReport> = {}
): RecommendationReport {
  return {
    assessed_at: "2026-02-20T10:00:00Z",
    effectiveness: {
      total_recommendations: 5,
      implemented_count: 3,
      pending_count: 1,
      not_created_count: 1,
      improved_count: 2,
      no_effect_count: 1,
      effectiveness_percent: 66.7,
    },
    recurring_findings: [],
    self_assessment: {
      total_health_checks: 10,
      avg_finding_count: 3,
      finding_count_trend: "stable",
      recommendation_follow_through_rate: 0.6,
      overall_effectiveness: "mixed",
    },
    entries: [],
    ...overrides,
  };
}

const OUTPUT_DIR = "/tmp/test-health-reports";

// ── Tests ──────────────────────────────────────────────────────────

describe("HealthReportGenerator", () => {
  let generator: HealthReportGenerator;

  beforeEach(() => {
    vi.resetAllMocks();
    generator = new HealthReportGenerator();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ── JSON Report Generation ────────────────────────────────────────

  describe("generateJsonReport()", () => {
    it("returns a valid object that passes HealthReportSchema.parse()", () => {
      const result = makeAnalysisResult();
      const report = generator.generateJsonReport(result);

      expect(() => HealthReportSchema.parse(report)).not.toThrow();
    });

    it("JSON report includes all dimension data", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            score: 80,
            findings: [makeFinding()],
          }),
          reliability: makeDimensionResult({
            dimension: "reliability",
            score: 60,
            status: "fair",
          }),
        },
      });

      const report = generator.generateJsonReport(result);

      expect(report.dimensions["token-economics"]).toBeDefined();
      expect(report.dimensions["token-economics"].score).toBe(80);
      expect(report.dimensions["token-economics"].findings).toHaveLength(1);
      expect(report.dimensions["reliability"]).toBeDefined();
      expect(report.dimensions["reliability"].score).toBe(60);
    });

    it("JSON report with baseline includes trend_comparison with has_baseline: true", () => {
      const baseline = makeAnalysisResult({ overallScore: 65 });
      const current = makeAnalysisResult({ overallScore: 80 });

      const report = generator.generateJsonReport(current, {
        baselineResult: baseline,
      });

      expect(report.trend_comparison.has_baseline).toBe(true);
      expect(report.trend_comparison.overall_score_change).toBe(15);
      expect(report.trend_comparison.overall_direction).toBe("improving");
    });

    it("JSON report without baseline has trend_comparison.has_baseline === false", () => {
      const result = makeAnalysisResult();
      const report = generator.generateJsonReport(result);

      expect(report.trend_comparison.has_baseline).toBe(false);
      expect(report.trend_comparison.overall_score_change).toBeUndefined();
      expect(report.trend_comparison.overall_direction).toBeUndefined();
    });

    it("JSON report with issue references includes them", () => {
      const result = makeAnalysisResult();
      const issueReferences = [
        {
          findingId: "te-1",
          issueNumber: 42,
          issueUrl: "https://github.com/nightgauge/nightgauge/issues/42",
        },
      ];

      const report = generator.generateJsonReport(result, { issueReferences });

      expect(report.issue_references).toBeDefined();
      expect(report.issue_references).toHaveLength(1);
      expect(report.issue_references![0].finding_id).toBe("te-1");
      expect(report.issue_references![0].issue_number).toBe(42);
      expect(report.issue_references![0].issue_url).toBe(
        "https://github.com/nightgauge/nightgauge/issues/42"
      );
    });

    it("JSON report with recommendation report includes effectiveness data", () => {
      const result = makeAnalysisResult();
      const recommendationReport = makeRecommendationReport();

      const report = generator.generateJsonReport(result, {
        recommendationReport,
      });

      expect(report.recommendation_effectiveness).toBeDefined();
      expect(report.recommendation_effectiveness!.total_recommendations).toBe(5);
      expect(report.recommendation_effectiveness!.implemented_count).toBe(3);
      expect(report.recommendation_effectiveness!.effectiveness_percent).toBe(66.7);
    });

    it("summary fields reflect finding counts correctly", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            findings: [
              makeFinding({ severity: "critical" }),
              makeFinding({ id: "te-2", severity: "high" }),
            ],
          }),
        },
        overallScore: 40,
        overallStatus: "poor",
        summary: "Pipeline has issues.",
      });

      const report = generator.generateJsonReport(result);

      expect(report.summary.total_findings).toBe(2);
      expect(report.summary.critical_findings).toBe(1);
      expect(report.summary.overall_score).toBe(40);
      expect(report.summary.overall_status).toBe("poor");
    });

    it("data quality fields are computed from dimensions", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            hasEnoughData: true,
            sampleSize: 20,
          }),
          reliability: makeDimensionResult({
            dimension: "reliability",
            hasEnoughData: false,
            sampleSize: 2,
          }),
        },
      });

      const report = generator.generateJsonReport(result);

      expect(report.data_quality.dimensions_with_data).toBe(1);
      expect(report.data_quality.dimensions_without_data).toBe(1);
      expect(report.data_quality.lowest_sample_size).toBe(2);
      expect(report.data_quality.avg_sample_size).toBe(11); // Math.round((20 + 2) / 2)
    });

    it('schema_version is always "1.0"', () => {
      const report = generator.generateJsonReport(makeAnalysisResult());
      expect(report.schema_version).toBe("1.0");
    });

    it("analysis_period uses provided values when supplied", () => {
      const result = makeAnalysisResult();
      const report = generator.generateJsonReport(result, {
        analysisPeriod: { startDate: "2026-02-01", endDate: "2026-02-20" },
      });

      expect(report.analysis_period.start_date).toBe("2026-02-01");
      expect(report.analysis_period.end_date).toBe("2026-02-20");
      expect(report.analysis_period.period_days).toBe(19);
    });

    it("metadata reflects provided data sources", () => {
      const result = makeAnalysisResult();
      const report = generator.generateJsonReport(result, {
        dataSources: [
          { name: "executionHistory", recordCount: 50 },
          { name: "healthScores", recordCount: 10 },
        ],
        analysisDurationMs: 1234,
      });

      expect(report.metadata.data_sources).toHaveLength(2);
      expect(report.metadata.total_records).toBe(60);
      expect(report.metadata.analysis_duration_ms).toBe(1234);
    });
  });

  // ── Markdown Report Generation ────────────────────────────────────

  describe("generateMarkdownReport()", () => {
    it('Markdown includes "# Pipeline Health Report" header', () => {
      const result = makeAnalysisResult();
      const md = generator.generateMarkdownReport(result);

      expect(md).toContain("# Pipeline Health Report");
    });

    it("Markdown includes executive summary with score", () => {
      const result = makeAnalysisResult({
        overallScore: 75,
        overallStatus: "good",
      });
      const md = generator.generateMarkdownReport(result);

      expect(md).toContain("75/100");
      expect(md).toContain("good");
    });

    it("Markdown includes severity badges for findings", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            findings: [makeFinding({ severity: "high", title: "High token waste" })],
          }),
        },
      });
      const md = generator.generateMarkdownReport(result);

      // High severity badge is 🟠
      expect(md).toContain("🟠");
      expect(md).toContain("High token waste");
    });

    it("Markdown includes trend comparison table when baseline provided", () => {
      const baseline = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({ score: 60 }),
        },
        overallScore: 60,
      });
      const current = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            score: 75,
            periodComparison: {
              currentValue: 75,
              baselineValue: 60,
              changePercent: 25,
              direction: "improving",
              isSignificant: true,
            },
          }),
        },
        overallScore: 75,
      });

      const md = generator.generateMarkdownReport(current, {
        baselineResult: baseline,
      });

      expect(md).toContain("## Trend Comparison");
      expect(md).toContain("| Dimension | Current | Baseline | Change | Trend |");
    });

    it('Markdown includes "## Recommended Actions" when findings exist', () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            findings: [makeFinding()],
          }),
        },
      });
      const md = generator.generateMarkdownReport(result);

      expect(md).toContain("## Recommended Actions");
    });

    it('Markdown does not include "## Recommended Actions" when no findings', () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({ findings: [] }),
        },
      });
      const md = generator.generateMarkdownReport(result);

      expect(md).not.toContain("## Recommended Actions");
    });

    it("Markdown includes recommendation effectiveness when provided", () => {
      const result = makeAnalysisResult();
      const md = generator.generateMarkdownReport(result, {
        recommendationReport: makeRecommendationReport(),
      });

      expect(md).toContain("## Recommendation Effectiveness");
      expect(md).toContain("Total recommendations: 5");
      expect(md).toContain("Implemented: 3");
    });

    it("Markdown includes analysis period when provided", () => {
      const result = makeAnalysisResult();
      const md = generator.generateMarkdownReport(result, {
        analysisPeriod: { startDate: "2026-02-01", endDate: "2026-02-20" },
      });

      expect(md).toContain("2026-02-01");
      expect(md).toContain("2026-02-20");
    });

    it("Markdown includes cross-dimension insights when cross-references exist", () => {
      const crossRef: CrossReference = {
        id: "cr-1",
        dimensions: ["token-economics", "cost-health"],
        severity: "high",
        title: "Token cost correlation",
        description: "High token waste correlates with increased cost.",
        correlatedFindings: ["te-1"],
        confidence: "high",
        evidence: {},
      };
      const result = makeAnalysisResult({ crossReferences: [crossRef] });
      const md = generator.generateMarkdownReport(result);

      expect(md).toContain("## Cross-Dimension Insights");
      expect(md).toContain("Token cost correlation");
    });

    it("Markdown ends with a generated-at footer", () => {
      const result = makeAnalysisResult();
      const md = generator.generateMarkdownReport(result);

      expect(md).toContain("Generated at");
      expect(md).toContain("HealthReportGenerator");
    });
  });

  // ── Console Summary ───────────────────────────────────────────────

  describe("generateConsoleSummary()", () => {
    it("console summary is compact (under 20 lines)", () => {
      const result = makeAnalysisResult();
      const summary = generator.generateConsoleSummary(result);
      const lines = summary.split("\n");

      expect(lines.length).toBeLessThanOrEqual(20);
    });

    it("console summary includes dimension scores", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({ score: 82 }),
        },
      });
      const summary = generator.generateConsoleSummary(result);

      expect(summary).toContain("token-economics");
      expect(summary).toContain("82");
    });

    it("console summary includes top 3 findings", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            findings: [
              makeFinding({
                id: "f1",
                severity: "critical",
                title: "Critical Finding",
              }),
              makeFinding({
                id: "f2",
                severity: "high",
                title: "High Finding",
              }),
              makeFinding({
                id: "f3",
                severity: "medium",
                title: "Medium Finding",
              }),
              makeFinding({ id: "f4", severity: "low", title: "Low Finding" }),
            ],
          }),
        },
      });
      const summary = generator.generateConsoleSummary(result);

      expect(summary).toContain("Top findings:");
      expect(summary).toContain("Critical Finding");
      expect(summary).toContain("High Finding");
      expect(summary).toContain("Medium Finding");
      // The 4th finding (low) should be excluded — only top 3
      expect(summary).not.toContain("Low Finding");
    });

    it("console summary includes trend when baseline provided", () => {
      const baseline = makeAnalysisResult({ overallScore: 60 });
      const current = makeAnalysisResult({ overallScore: 75 });

      const summary = generator.generateConsoleSummary(current, {
        baselineResult: baseline,
      });

      expect(summary).toContain("Trend:");
      expect(summary).toContain("improving");
      expect(summary).toContain("+15");
    });

    it("console summary shows degrading trend when score decreases", () => {
      const baseline = makeAnalysisResult({ overallScore: 80 });
      const current = makeAnalysisResult({ overallScore: 60 });

      const summary = generator.generateConsoleSummary(current, {
        baselineResult: baseline,
      });

      expect(summary).toContain("degrading");
      expect(summary).toContain("-20");
    });

    it("console summary shows stable trend when score is unchanged", () => {
      const baseline = makeAnalysisResult({ overallScore: 75 });
      const current = makeAnalysisResult({ overallScore: 75 });

      const summary = generator.generateConsoleSummary(current, {
        baselineResult: baseline,
      });

      expect(summary).toContain("stable");
      expect(summary).toContain("+0");
    });

    it("console summary includes overall score and status", () => {
      const result = makeAnalysisResult({
        overallScore: 88,
        overallStatus: "good",
      });
      const summary = generator.generateConsoleSummary(result);

      expect(summary).toContain("88/100");
      expect(summary).toContain("GOOD");
    });
  });

  // ── writeReports ─────────────────────────────────────────────────

  describe("writeReports()", () => {
    beforeEach(() => {
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.readdir).mockResolvedValue(
        [] as unknown as Awaited<ReturnType<typeof fs.readdir>>
      );
    });

    it("calls fs.writeFile for JSON and MD files", async () => {
      const result = makeAnalysisResult();

      await generator.writeReports(result, OUTPUT_DIR);

      // mkdir + 2 writeFile calls
      expect(fs.mkdir).toHaveBeenCalledWith(OUTPUT_DIR, { recursive: true });
      expect(fs.writeFile).toHaveBeenCalledTimes(2);
    });

    it("JSON file is named health-report-YYYY-MM-DD.json", async () => {
      const result = makeAnalysisResult();

      const { jsonPath } = await generator.writeReports(result, OUTPUT_DIR);

      expect(path.basename(jsonPath)).toMatch(/^health-report-\d{4}-\d{2}-\d{2}\.json$/);
      expect(path.dirname(jsonPath)).toBe(OUTPUT_DIR);
    });

    it("Markdown file is named health-report-YYYY-MM-DD.md", async () => {
      const result = makeAnalysisResult();

      const { markdownPath } = await generator.writeReports(result, OUTPUT_DIR);

      expect(path.basename(markdownPath)).toMatch(/^health-report-\d{4}-\d{2}-\d{2}\.md$/);
      expect(path.dirname(markdownPath)).toBe(OUTPUT_DIR);
    });

    it("JSON writeFile call contains valid JSON", async () => {
      const result = makeAnalysisResult();

      await generator.writeReports(result, OUTPUT_DIR);

      const jsonWriteCall = vi
        .mocked(fs.writeFile)
        .mock.calls.find((call) => (call[0] as string).endsWith(".json"));

      expect(jsonWriteCall).toBeDefined();
      const content = jsonWriteCall![1] as string;
      expect(() => JSON.parse(content)).not.toThrow();
    });

    it("returns the correct paths", async () => {
      const result = makeAnalysisResult();

      const { jsonPath, markdownPath } = await generator.writeReports(result, OUTPUT_DIR);

      expect(jsonPath).toMatch(/\.json$/);
      expect(markdownPath).toMatch(/\.md$/);
    });
  });

  // ── Retention ─────────────────────────────────────────────────────

  describe("enforceRetention()", () => {
    it("keeps only maxFiles per type when over limit", async () => {
      // Create 25 JSON files (limit is 20 by default)
      const jsonFiles = Array.from(
        { length: 25 },
        (_, i) => `health-report-2026-01-${String(i + 1).padStart(2, "0")}.json`
      );
      // Create 5 MD files (under limit)
      const mdFiles = Array.from(
        { length: 5 },
        (_, i) => `health-report-2026-01-${String(i + 1).padStart(2, "0")}.md`
      );

      vi.mocked(fs.readdir).mockResolvedValue([...jsonFiles, ...mdFiles] as unknown as Awaited<
        ReturnType<typeof fs.readdir>
      >);
      vi.mocked(fs.unlink).mockResolvedValue(undefined);

      await generator.enforceRetention(OUTPUT_DIR, 20);

      // Should delete 5 oldest JSON files
      expect(fs.unlink).toHaveBeenCalledTimes(5);
    });

    it("when under limit, no files are deleted", async () => {
      const jsonFiles = Array.from(
        { length: 10 },
        (_, i) => `health-report-2026-01-${String(i + 1).padStart(2, "0")}.json`
      );
      const mdFiles = Array.from(
        { length: 10 },
        (_, i) => `health-report-2026-01-${String(i + 1).padStart(2, "0")}.md`
      );

      vi.mocked(fs.readdir).mockResolvedValue([...jsonFiles, ...mdFiles] as unknown as Awaited<
        ReturnType<typeof fs.readdir>
      >);
      vi.mocked(fs.unlink).mockResolvedValue(undefined);

      await generator.enforceRetention(OUTPUT_DIR, 20);

      expect(fs.unlink).not.toHaveBeenCalled();
    });

    it("JSON and MD are retained independently", async () => {
      // 22 JSON files (over limit of 20), 5 MD files (under limit)
      const jsonFiles = Array.from(
        { length: 22 },
        (_, i) => `health-report-2026-01-${String(i + 1).padStart(2, "0")}.json`
      );
      const mdFiles = Array.from(
        { length: 5 },
        (_, i) => `health-report-2026-01-${String(i + 1).padStart(2, "0")}.md`
      );

      const deletedFiles: string[] = [];
      vi.mocked(fs.readdir).mockResolvedValue([...jsonFiles, ...mdFiles] as unknown as Awaited<
        ReturnType<typeof fs.readdir>
      >);
      vi.mocked(fs.unlink).mockImplementation(async (filePath) => {
        deletedFiles.push(path.basename(filePath as string));
      });

      await generator.enforceRetention(OUTPUT_DIR, 20);

      // Only 2 JSON files should be deleted, no MD files
      expect(deletedFiles.every((f) => f.endsWith(".json"))).toBe(true);
      expect(deletedFiles).toHaveLength(2);
    });

    it("returns without error when directory does not exist", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(new Error("ENOENT: no such file or directory"));

      // Should not throw
      await expect(generator.enforceRetention("/nonexistent/dir")).resolves.toBeUndefined();
    });

    it("deletes oldest files first (sorted ascending)", async () => {
      const jsonFiles = [
        "health-report-2026-01-03.json",
        "health-report-2026-01-01.json",
        "health-report-2026-01-02.json",
      ];

      const deletedFiles: string[] = [];
      vi.mocked(fs.readdir).mockResolvedValue(
        jsonFiles as unknown as Awaited<ReturnType<typeof fs.readdir>>
      );
      vi.mocked(fs.unlink).mockImplementation(async (filePath) => {
        deletedFiles.push(path.basename(filePath as string));
      });

      await generator.enforceRetention(OUTPUT_DIR, 2);

      // The oldest file should be deleted (2026-01-01 sorts first)
      expect(deletedFiles).toHaveLength(1);
      expect(deletedFiles[0]).toBe("health-report-2026-01-01.json");
    });
  });

  // ── Edge Cases ────────────────────────────────────────────────────

  describe("edge cases", () => {
    it("no findings produces valid report", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({ findings: [] }),
        },
      });

      const report = generator.generateJsonReport(result);

      expect(() => HealthReportSchema.parse(report)).not.toThrow();
      expect(report.summary.total_findings).toBe(0);
      expect(report.summary.critical_findings).toBe(0);
    });

    it("no baseline produces report with has_baseline: false", () => {
      const result = makeAnalysisResult();
      const report = generator.generateJsonReport(result);

      expect(report.trend_comparison.has_baseline).toBe(false);
    });

    it("empty dimensions produces valid report", () => {
      const result = makeAnalysisResult({ dimensions: {} });
      const report = generator.generateJsonReport(result);

      expect(() => HealthReportSchema.parse(report)).not.toThrow();
      expect(Object.keys(report.dimensions)).toHaveLength(0);
      expect(report.data_quality.dimensions_with_data).toBe(0);
      expect(report.data_quality.dimensions_without_data).toBe(0);
      expect(report.data_quality.avg_sample_size).toBe(0);
      expect(report.data_quality.lowest_sample_size).toBe(0);
    });

    it("dimension without enough data renders markdown note about insufficient data", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            hasEnoughData: false,
            sampleSize: 2,
          }),
        },
      });

      const md = generator.generateMarkdownReport(result);

      expect(md).toContain("Insufficient data");
    });

    it("cross-references with empty array produces valid report", () => {
      const result = makeAnalysisResult({ crossReferences: [] });
      const report = generator.generateJsonReport(result);

      expect(report.cross_references).toEqual([]);
      expect(report.summary.cross_references).toBe(0);
    });

    it("cross-references are mapped to JSON report correctly", () => {
      const crossRef: CrossReference = {
        id: "cr-1",
        dimensions: ["token-economics", "cost-health"],
        severity: "medium",
        title: "Correlated waste",
        description: "Token waste and cost are correlated.",
        correlatedFindings: ["te-1", "ch-1"],
        confidence: "medium",
        evidence: { sample: 42 },
      };

      const result = makeAnalysisResult({ crossReferences: [crossRef] });
      const report = generator.generateJsonReport(result);

      expect(report.cross_references).toHaveLength(1);
      expect(report.cross_references[0].id).toBe("cr-1");
      expect(report.cross_references[0].dimensions).toEqual(["token-economics", "cost-health"]);
      expect(report.cross_references[0].correlated_findings).toEqual(["te-1", "ch-1"]);
      expect(report.summary.cross_references).toBe(1);
    });

    it("dimension with periodComparison is reflected in JSON report", () => {
      const result = makeAnalysisResult({
        dimensions: {
          "token-economics": makeDimensionResult({
            periodComparison: {
              currentValue: 75,
              baselineValue: 60,
              changePercent: 25,
              direction: "improving",
              isSignificant: true,
            },
          }),
        },
      });

      const report = generator.generateJsonReport(result);
      const dimReport = report.dimensions["token-economics"];

      expect(dimReport.period_comparison).toBeDefined();
      expect(dimReport.period_comparison!.current_value).toBe(75);
      expect(dimReport.period_comparison!.baseline_value).toBe(60);
      expect(dimReport.period_comparison!.change_percent).toBe(25);
      expect(dimReport.period_comparison!.direction).toBe("improving");
      expect(dimReport.period_comparison!.is_significant).toBe(true);
    });
  });
});
