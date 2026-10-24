/**
 * Shared Test Fixtures Factory for Health Analysis Tests
 *
 * Factory functions with partial-override pattern for building realistic
 * pipeline telemetry data. All timestamps use fixed ISO 8601 values spread
 * across a 30-day window to avoid flaky tests.
 *
 * @see Issue #1106 - Comprehensive Test Coverage for Health Analysis
 */

import type { ExecutionHistoryRecord } from "../../../src/analysis/types.js";

import type {
  HealthAnalysisInput,
  HealthScoreEntry,
  RecommendationHistoryEntry,
  HealthDimension,
  Severity,
} from "../../../src/analysis/health/types.js";

// ── Base timestamp for all fixtures ─────────────────────────────────

const BASE_DATE = new Date("2026-01-15T10:00:00Z");

function daysAgo(n: number): string {
  const d = new Date(BASE_DATE);
  d.setDate(d.getDate() - n);
  return d.toISOString();
}

function hoursAgo(n: number): string {
  const d = new Date(BASE_DATE);
  d.setHours(d.getHours() - n);
  return d.toISOString();
}

// ── Pipeline stages ─────────────────────────────────────────────────

const STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

// ── ExecutionHistoryRecord ──────────────────────────────────────────

export function makeExecutionRecord(
  overrides: Partial<ExecutionHistoryRecord> = {}
): ExecutionHistoryRecord {
  return {
    issueNumber: 100,
    stage: "feature-dev",
    model: "sonnet",
    success: true,
    retries: 0,
    inputTokens: 50000,
    outputTokens: 5000,
    cacheReadTokens: 10000,
    cacheCreationTokens: 2000,
    costUsd: 0.25,
    durationMs: 120000,
    timestamp: daysAgo(5),
    selectionSource: "scheduler",
    autoSelectorComplexity: "M",
    ...overrides,
  };
}

// ── HealthScoreEntry ────────────────────────────────────────────────

export function makeHealthScoreEntry(overrides: Partial<HealthScoreEntry> = {}): HealthScoreEntry {
  return {
    timestamp: daysAgo(3),
    score: 75,
    issueNumber: 100,
    ...overrides,
  };
}

// ── RecommendationHistoryEntry ──────────────────────────────────────

export function makeRecommendationEntry(
  overrides: Partial<RecommendationHistoryEntry> = {}
): RecommendationHistoryEntry {
  return {
    schema_version: "1",
    finding_id: "te-1",
    created_at: daysAgo(10),
    severity: "medium" as Severity,
    dimension: "token-economics" as HealthDimension,
    title: "Low cache hit rate",
    recommendation: "Enable prompt caching for repeated context blocks",
    issue_number: 200,
    issue_url: "https://github.com/nightgauge/nightgauge/issues/200",
    issue_state: "open",
    metric_before: 60,
    health_report_ref: "report-2026-01-05",
    ...overrides,
  };
}

// ── Dataset Builders ────────────────────────────────────────────────

/**
 * Build a complete HealthAnalysisInput with sensible defaults.
 * Generates 20 execution records across all pipeline stages.
 */
export function makeDataset(overrides: Partial<HealthAnalysisInput> = {}): HealthAnalysisInput {
  const records: ExecutionHistoryRecord[] = [];
  let issueNum = 100;

  // 4 pipeline runs, each with all 6 stages (but some may fail)
  for (let run = 0; run < 4; run++) {
    for (let s = 0; s < STAGES.length; s++) {
      // One failure per run on feature-validate for realism
      const isFailing = run === 2 && s === 3;
      records.push(
        makeExecutionRecord({
          issueNumber: issueNum,
          stage: STAGES[s],
          success: !isFailing,
          retries: isFailing ? 1 : 0,
          costUsd: 0.15 + s * 0.05,
          durationMs: 60000 + s * 30000,
          timestamp: daysAgo(28 - run * 7 - s),
          inputTokens: 40000 + s * 10000,
          outputTokens: 4000 + s * 1000,
          cacheReadTokens: 8000 + s * 2000,
          cacheCreationTokens: 1500,
        })
      );
    }
    issueNum++;
  }

  return {
    executionHistory: records,
    healthScores: [
      makeHealthScoreEntry({
        timestamp: daysAgo(21),
        score: 70,
        issueNumber: 100,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(14),
        score: 72,
        issueNumber: 101,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(7),
        score: 75,
        issueNumber: 102,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(1),
        score: 78,
        issueNumber: 103,
      }),
    ],
    learningSignals: {
      outcomes: Array.from({ length: 8 }, (_, i) => ({
        completedAt: daysAgo(20 - i * 2),
        predictedModel: "sonnet",
        actualModel: "sonnet",
        predictedSize: "medium",
        actualSize: i === 0 ? "large" : "medium",
      })),
      calibrationUpdatedAt: daysAgo(5),
    },
    skillEffectiveness: Array.from({ length: 6 }, (_, i) => ({
      skillFile: `skills/example-${i}/SKILL.md`,
      stage: "feature-dev",
      commitHash: `abc${i}`,
      changedAt: daysAgo(20 - i),
      beforeWindow: { sampleCount: 10, successRate: 0.8 },
      afterWindow: { sampleCount: 10, successRate: 0.85 },
      delta: 0.05,
      classification: i === 0 ? ("effective" as const) : ("neutral" as const),
      confidence: "low" as const,
      analyzedAt: daysAgo(1),
    })),
    ...overrides,
  };
}

/**
 * Dataset with all empty arrays — for testing empty-data edge cases.
 */
export function makeEmptyDataset(): HealthAnalysisInput {
  return {
    executionHistory: [],
    healthScores: [],
  };
}

/**
 * Dataset with a minimal number of records (default 1).
 * Useful for testing insufficient-data paths.
 */
export function makeMinimalDataset(count: number = 1): HealthAnalysisInput {
  const records = Array.from({ length: count }, (_, i) =>
    makeExecutionRecord({
      issueNumber: 200 + i,
      timestamp: daysAgo(count - i),
    })
  );

  return {
    executionHistory: records,
    healthScores:
      count >= 2
        ? [
            makeHealthScoreEntry({ timestamp: daysAgo(count), score: 70 }),
            makeHealthScoreEntry({ timestamp: daysAgo(1), score: 72 }),
          ]
        : [],
  };
}

/**
 * Dataset designed to trigger multiple findings across dimensions.
 * - High failure rates
 * - Cost spikes
 * - Low cache hit rates
 * - Worsening trends
 * - Stage bottlenecks
 */
export function makeDegradingDataset(): HealthAnalysisInput {
  const records: ExecutionHistoryRecord[] = [];
  let issueNum = 300;

  // 5 runs with high failure rates, increasing costs, low cache
  for (let run = 0; run < 5; run++) {
    for (let s = 0; s < STAGES.length; s++) {
      // 40% failure rate
      const success = !(run % 2 === 0 && s >= 3);
      // Costs increasing over time
      const costMultiplier = 1 + run * 0.3;
      // Durations increasing over time
      const durationMultiplier = 1 + run * 0.2;
      // Very low cache hits
      const cacheReadTokens = 500;

      records.push(
        makeExecutionRecord({
          issueNumber: issueNum,
          stage: STAGES[s],
          success,
          retries: success ? 0 : 2,
          costUsd: (0.3 + s * 0.1) * costMultiplier,
          durationMs: (80000 + s * 40000) * durationMultiplier,
          timestamp: daysAgo(28 - run * 5 - s),
          inputTokens: 80000 + s * 15000,
          outputTokens: 3000 + s * 500,
          cacheReadTokens,
          cacheCreationTokens: 500,
          selectionSource: "scheduler",
          model: s < 2 ? "haiku" : "opus",
          autoSelectorComplexity: s < 2 ? "L" : "XS",
        })
      );
    }
    issueNum++;
  }

  return {
    executionHistory: records,
    healthScores: [
      makeHealthScoreEntry({
        timestamp: daysAgo(28),
        score: 80,
        issueNumber: 300,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(21),
        score: 72,
        issueNumber: 301,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(14),
        score: 60,
        issueNumber: 302,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(7),
        score: 50,
        issueNumber: 303,
      }),
      makeHealthScoreEntry({
        timestamp: daysAgo(1),
        score: 40,
        issueNumber: 304,
      }),
    ],
    learningSignals: {
      outcomes: Array.from({ length: 10 }, (_, i) => ({
        completedAt: daysAgo(28 - i * 2),
        predictedModel: "sonnet",
        actualModel: i < 3 ? "sonnet" : "opus",
        predictedSize: "small",
        actualSize: i < 3 ? "small" : "large",
      })),
      calibrationUpdatedAt: daysAgo(60),
    },
    skillEffectiveness: Array.from({ length: 6 }, (_, i) => ({
      skillFile: `skills/example-${i}/SKILL.md`,
      stage: "feature-dev",
      commitHash: `def${i}`,
      changedAt: daysAgo(25 - i),
      beforeWindow: { sampleCount: 20, successRate: 0.9 },
      afterWindow: { sampleCount: 10, successRate: i < 4 ? 0.5 : 0.9 },
      delta: i < 4 ? -0.4 : 0,
      classification: i < 4 ? ("regression" as const) : ("neutral" as const),
      confidence: "moderate" as const,
      analyzedAt: daysAgo(1),
    })),
  };
}
