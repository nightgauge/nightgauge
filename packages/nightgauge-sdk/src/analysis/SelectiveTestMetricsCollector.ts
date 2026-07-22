/**
 * SelectiveTestMetricsCollector — records per-PR selective test metrics to a
 * JSONL file and provides read/filter operations.
 *
 * Default storage: `.nightgauge/pipeline/selective-metrics.jsonl`
 *
 * Each line is a `SelectiveTestMetricRecord` JSON object. Malformed lines are
 * silently skipped during reads. Writes are non-blocking appends.
 *
 * Cost savings estimation constants:
 * - `AVG_TOKENS_PER_TEST = 500` — conservative estimate per skipped test
 * - `AVG_MS_PER_TEST = 2000` — conservative wall-clock estimate per skipped test
 * - Output cost rate: sonnet output rate (15.0 USD per million tokens)
 *
 * @see Issue #1975 - Validation & Cost Tracking
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import type { SelectiveTestResult } from "../tools/selective-test-runner/types.js";
import {
  SelectiveTestMetricRecordSchema,
  type SelectiveTestMetricRecord,
} from "./selective-test-metrics-types.js";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Default path relative to the repo root */
export const DEFAULT_METRICS_PATH = ".nightgauge/pipeline/selective-metrics.jsonl";

/** Conservative estimate: tokens consumed per test run by the agent */
export const AVG_TOKENS_PER_TEST = 500;

/**
 * Output rate for Sonnet in USD per million tokens.
 * Used to convert `estimatedTokensSaved → estimatedCostSavedUsd`.
 */
export const SONNET_OUTPUT_RATE_PER_MILLION = 15.0;

/** Conservative estimate: ms of wall-clock time saved per skipped test */
export const AVG_MS_PER_TEST = 2000;

// ---------------------------------------------------------------------------
// SelectiveTestMetricsCollector
// ---------------------------------------------------------------------------

export class SelectiveTestMetricsCollector {
  constructor(private readonly metricsPath: string = DEFAULT_METRICS_PATH) {}

  /**
   * Append one metric record to the JSONL file.
   *
   * @param result - The SelectiveTestResult from the runner
   * @param context - Additional context required for the record
   */
  async record(
    result: SelectiveTestResult,
    context: {
      issueNumber: number;
      branch: string;
      /** Actual cost (USD) of the validate stage when selective testing ran */
      validateCostUsd: number;
      prNumber?: number;
    }
  ): Promise<void> {
    const skipped = result.skippedTests ?? 0;
    const estimatedTokensSaved = skipped * AVG_TOKENS_PER_TEST;
    const estimatedTimeSavedMs = skipped * AVG_MS_PER_TEST;
    const estimatedCostSavedUsd =
      (estimatedTokensSaved * SONNET_OUTPUT_RATE_PER_MILLION) / 1_000_000;

    // Estimate full-suite cost: selective_cost + estimated savings
    const fullSuiteCostUsd = context.validateCostUsd + estimatedCostSavedUsd;

    const record: SelectiveTestMetricRecord = {
      schema_version: "1.0",
      record_type: "selective_run",
      issue_number: context.issueNumber,
      pr_number: context.prNumber,
      branch: context.branch,
      run_at: new Date().toISOString(),
      impact_level: result.impactLevel as SelectiveTestMetricRecord["impact_level"],
      total_tests: result.totalTests,
      selected_tests: result.selectedTests,
      skipped_tests: result.skippedTests,
      selected_test_files: result.testFiles ?? [],
      estimated_tokens_saved: estimatedTokensSaved,
      estimated_time_saved_ms: estimatedTimeSavedMs,
      estimated_cost_saved_usd: estimatedCostSavedUsd,
      full_suite_cost_usd: fullSuiteCostUsd,
      selective_cost_usd: context.validateCostUsd,
    };

    const dir = path.dirname(this.metricsPath);
    await fs.mkdir(dir, { recursive: true });
    await fs.appendFile(this.metricsPath, JSON.stringify(record) + "\n", "utf-8");
  }

  /**
   * Read and parse all records from the JSONL file.
   * Malformed lines are silently skipped.
   */
  async readAll(): Promise<SelectiveTestMetricRecord[]> {
    let content: string;
    try {
      content = await fs.readFile(this.metricsPath, "utf-8");
    } catch {
      return [];
    }

    const records: SelectiveTestMetricRecord[] = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validated = SelectiveTestMetricRecordSchema.parse(parsed);
        records.push(validated as SelectiveTestMetricRecord);
      } catch {
        // Skip malformed lines
      }
    }

    return records;
  }

  /**
   * Read records filtered to a rolling window of `days` days from now.
   *
   * @param days - Number of days to include (e.g. 7 for a weekly window)
   */
  async readWindow(days: number): Promise<SelectiveTestMetricRecord[]> {
    const all = await this.readAll();
    const cutoff = new Date();
    cutoff.setDate(cutoff.getDate() - days);
    const cutoffMs = cutoff.getTime();

    return all.filter((r) => new Date(r.run_at).getTime() >= cutoffMs);
  }
}
