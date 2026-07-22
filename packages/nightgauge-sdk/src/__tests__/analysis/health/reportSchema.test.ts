import { describe, it, expect } from "vitest";
import { HealthReportSchema } from "../../../analysis/health/reportSchema.js";

function makeValidReport() {
  return {
    schema_version: "1.0" as const,
    generated_at: "2026-02-20T00:00:00.000Z",
    analysis_period: {
      start_date: "2026-01-20T00:00:00.000Z",
      end_date: "2026-02-20T00:00:00.000Z",
      period_days: 31,
    },
    metadata: {
      data_sources: [
        { name: "pipeline_runs", record_count: 120 },
        { name: "stage_events", record_count: 540 },
      ],
      total_records: 660,
      analysis_duration_ms: 1234,
    },
    summary: {
      overall_score: 78,
      overall_status: "good" as const,
      total_findings: 5,
      critical_findings: 1,
      cross_references: 2,
      text: "Pipeline health is good with minor reliability concerns.",
    },
    dimensions: {
      reliability: {
        dimension: "reliability",
        score: 82,
        status: "good" as const,
        has_enough_data: true,
        sample_size: 120,
        findings: [
          {
            id: "finding-001",
            severity: "medium" as const,
            title: "Elevated retry rate",
            description: "The retry rate exceeds the expected threshold.",
            impact: "Increases pipeline duration and token usage.",
            recommendation: "Investigate flaky network calls in feature-dev stage.",
            confidence: "high" as const,
          },
        ],
        metrics: { success_rate: 0.91, retry_rate: 0.09 },
      },
    },
    cross_references: [
      {
        id: "xref-001",
        dimensions: ["reliability", "cost"],
        severity: "high" as const,
        title: "Retries amplify cost",
        description: "High retry rate in reliability correlates with elevated cost.",
        correlated_findings: ["finding-001"],
        confidence: "medium" as const,
      },
    ],
    trend_comparison: {
      has_baseline: false,
    },
    data_quality: {
      dimensions_with_data: 1,
      dimensions_without_data: 0,
      avg_sample_size: 120,
      lowest_sample_size: 120,
    },
  };
}

describe("HealthReportSchema — valid reports", () => {
  it("accepts a fully valid report", () => {
    const result = HealthReportSchema.safeParse(makeValidReport());
    expect(result.success).toBe(true);
  });

  it("accepts a report without optional issue_references", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(true);
  });

  it("accepts a report without optional recommendation_effectiveness", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(true);
  });

  it("accepts a report with issue_references present", () => {
    const report = {
      ...makeValidReport(),
      issue_references: [
        {
          finding_id: "finding-001",
          issue_number: 42,
          issue_url: "https://github.com/nightgauge/nightgauge/issues/42",
        },
      ],
    };
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(true);
  });

  it("accepts a report with recommendation_effectiveness present", () => {
    const report = {
      ...makeValidReport(),
      recommendation_effectiveness: {
        total_recommendations: 10,
        implemented_count: 6,
        pending_count: 2,
        not_created_count: 2,
        improved_count: 5,
        no_effect_count: 1,
        effectiveness_percent: 83.3,
      },
    };
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(true);
  });

  it("accepts a dimension with period_comparison present", () => {
    const report = makeValidReport();
    report.dimensions.reliability = {
      ...report.dimensions.reliability,
      period_comparison: {
        current_value: 82,
        baseline_value: 75,
        change_percent: 9.33,
        direction: "improving" as const,
        is_significant: true,
      },
    } as typeof report.dimensions.reliability;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(true);
  });

  it("accepts a trend_comparison with all optional fields populated", () => {
    const report = {
      ...makeValidReport(),
      trend_comparison: {
        has_baseline: true,
        overall_score_change: 5.2,
        overall_direction: "improving" as const,
        per_dimension: {
          reliability: {
            current_value: 82,
            baseline_value: 75,
            change_percent: 9.33,
            direction: "improving" as const,
            is_significant: true,
          },
        },
      },
    };
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(true);
  });
});

describe("HealthReportSchema — missing required fields", () => {
  it("rejects a report missing schema_version", () => {
    const { schema_version: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing generated_at", () => {
    const { generated_at: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing analysis_period", () => {
    const { analysis_period: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing metadata", () => {
    const { metadata: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing summary", () => {
    const { summary: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing dimensions", () => {
    const { dimensions: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing cross_references", () => {
    const { cross_references: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing trend_comparison", () => {
    const { trend_comparison: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects a report missing data_quality", () => {
    const { data_quality: _omit, ...report } = makeValidReport() as Record<string, unknown>;
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });
});

describe("HealthReportSchema — invalid types", () => {
  it("rejects overall_score as a string", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      summary: { ...report.summary, overall_score: "high" },
    });
    expect(result.success).toBe(false);
  });

  it("rejects total_records as a string", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      metadata: { ...report.metadata, total_records: "many" },
    });
    expect(result.success).toBe(false);
  });

  it("rejects period_days as a float", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      analysis_period: { ...report.analysis_period, period_days: 30.5 },
    });
    expect(result.success).toBe(false);
  });

  it("rejects record_count as a negative number", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      metadata: {
        ...report.metadata,
        data_sources: [{ name: "pipeline_runs", record_count: -1 }],
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects a dimension score above 100", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: { ...report.dimensions.reliability, score: 101 },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects a dimension score below 0", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: { ...report.dimensions.reliability, score: -5 },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects has_enough_data as a non-boolean", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: { ...report.dimensions.reliability, has_enough_data: 1 },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects effectiveness_percent above 100", () => {
    const report = {
      ...makeValidReport(),
      recommendation_effectiveness: {
        total_recommendations: 10,
        implemented_count: 6,
        pending_count: 2,
        not_created_count: 2,
        improved_count: 5,
        no_effect_count: 1,
        effectiveness_percent: 110,
      },
    };
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });

  it("rejects issue_number as zero (must be positive)", () => {
    const report = {
      ...makeValidReport(),
      issue_references: [
        {
          finding_id: "finding-001",
          issue_number: 0,
          issue_url: "https://github.com/nightgauge/nightgauge/issues/0",
        },
      ],
    };
    const result = HealthReportSchema.safeParse(report);
    expect(result.success).toBe(false);
  });
});

describe("HealthReportSchema — invalid enum values", () => {
  it('rejects schema_version other than "1.0"', () => {
    const result = HealthReportSchema.safeParse({
      ...makeValidReport(),
      schema_version: "2.0",
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid overall_status", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      summary: { ...report.summary, overall_status: "unknown" },
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid finding severity", () => {
    const report = makeValidReport();
    const badFinding = {
      ...report.dimensions.reliability.findings[0],
      severity: "urgent",
    };
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: {
          ...report.dimensions.reliability,
          findings: [badFinding],
        },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid finding confidence", () => {
    const report = makeValidReport();
    const badFinding = {
      ...report.dimensions.reliability.findings[0],
      confidence: "certain",
    };
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: {
          ...report.dimensions.reliability,
          findings: [badFinding],
        },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid dimension status", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: { ...report.dimensions.reliability, status: "average" },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid cross_reference severity", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      cross_references: [{ ...report.cross_references[0], severity: "blocker" }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid cross_reference confidence", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      cross_references: [{ ...report.cross_references[0], confidence: "uncertain" }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid period_comparison direction", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: {
          ...report.dimensions.reliability,
          period_comparison: {
            current_value: 82,
            baseline_value: 75,
            change_percent: 9.33,
            direction: "unchanged",
            is_significant: true,
          },
        },
      },
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid overall_direction in trend_comparison", () => {
    const result = HealthReportSchema.safeParse({
      ...makeValidReport(),
      trend_comparison: {
        has_baseline: true,
        overall_score_change: 3,
        overall_direction: "rising",
      },
    });
    expect(result.success).toBe(false);
  });
});

describe("HealthReportSchema — partial / minimal reports", () => {
  it("accepts a report with an empty dimensions record", () => {
    const result = HealthReportSchema.safeParse({
      ...makeValidReport(),
      dimensions: {},
    });
    expect(result.success).toBe(true);
  });

  it("accepts a report with an empty cross_references array", () => {
    const result = HealthReportSchema.safeParse({
      ...makeValidReport(),
      cross_references: [],
    });
    expect(result.success).toBe(true);
  });

  it("accepts a report with an empty findings array inside a dimension", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      dimensions: {
        reliability: { ...report.dimensions.reliability, findings: [] },
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a report with an empty data_sources array", () => {
    const report = makeValidReport();
    const result = HealthReportSchema.safeParse({
      ...report,
      metadata: { ...report.metadata, data_sources: [] },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a report with an empty issue_references array", () => {
    const result = HealthReportSchema.safeParse({
      ...makeValidReport(),
      issue_references: [],
    });
    expect(result.success).toBe(true);
  });

  it("accepts a report where trend_comparison has no baseline and no optional fields", () => {
    const result = HealthReportSchema.safeParse({
      ...makeValidReport(),
      trend_comparison: { has_baseline: false },
    });
    expect(result.success).toBe(true);
  });
});
