import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Mock } from "vitest";

// Mock fs/promises at the module boundary
vi.mock("node:fs/promises", () => ({
  mkdir: vi.fn().mockResolvedValue(undefined),
  appendFile: vi.fn().mockResolvedValue(undefined),
  readFile: vi.fn(),
}));

import * as fs from "node:fs/promises";
import {
  SelectiveTestMetricsCollector,
  AVG_TOKENS_PER_TEST,
  AVG_MS_PER_TEST,
  SONNET_OUTPUT_RATE_PER_MILLION,
} from "../../analysis/SelectiveTestMetricsCollector.js";
import type { SelectiveTestResult } from "../../tools/selective-test-runner/types.js";

// ── Test Data Factories ────────────────────────────────────────────

function makeSelectiveResult(overrides: Partial<SelectiveTestResult> = {}): SelectiveTestResult {
  return {
    mode: "selective",
    reason: "3 tests identified via dependency graph",
    testFiles: [
      "packages/sdk/src/__tests__/foo.test.ts",
      "packages/sdk/src/__tests__/bar.test.ts",
      "packages/sdk/src/__tests__/baz.test.ts",
    ],
    impactLevel: "isolated",
    totalTests: 10,
    selectedTests: 3,
    skippedTests: 7,
    vitestArgs: ["packages/sdk/src/__tests__/foo.test.ts"],
    ...overrides,
  };
}

function makeContext(
  overrides: Partial<{
    issueNumber: number;
    branch: string;
    validateCostUsd: number;
    prNumber?: number;
  }> = {}
) {
  return {
    issueNumber: 42,
    branch: "feat/42-my-feature",
    validateCostUsd: 0.01,
    ...overrides,
  };
}

// ── Tests ──────────────────────────────────────────────────────────

describe("SelectiveTestMetricsCollector", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("record()", () => {
    it("appends a valid JSONL line to the metrics file", async () => {
      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const result = makeSelectiveResult();
      const ctx = makeContext();

      await collector.record(result, ctx);

      const appendMock = fs.appendFile as Mock;
      expect(appendMock).toHaveBeenCalledOnce();

      const [, content] = appendMock.mock.calls[0] as [string, string, string];
      const parsed = JSON.parse(content.trim());

      expect(parsed.schema_version).toBe("1.0");
      expect(parsed.record_type).toBe("selective_run");
      expect(parsed.issue_number).toBe(42);
      expect(parsed.branch).toBe("feat/42-my-feature");
      expect(parsed.selected_tests).toBe(3);
      expect(parsed.skipped_tests).toBe(7);
      expect(parsed.selected_test_files).toHaveLength(3);
    });

    it("computes token savings correctly from skipped tests", async () => {
      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const result = makeSelectiveResult({ skippedTests: 10 });
      const ctx = makeContext({ validateCostUsd: 0.05 });

      await collector.record(result, ctx);

      const appendMock = fs.appendFile as Mock;
      const [, content] = appendMock.mock.calls[0] as [string, string, string];
      const parsed = JSON.parse(content.trim());

      const expectedTokensSaved = 10 * AVG_TOKENS_PER_TEST;
      const expectedTimeSaved = 10 * AVG_MS_PER_TEST;
      const expectedCostSaved = (expectedTokensSaved * SONNET_OUTPUT_RATE_PER_MILLION) / 1_000_000;

      expect(parsed.estimated_tokens_saved).toBe(expectedTokensSaved);
      expect(parsed.estimated_time_saved_ms).toBe(expectedTimeSaved);
      expect(parsed.estimated_cost_saved_usd).toBeCloseTo(expectedCostSaved, 8);
    });

    it("computes full_suite_cost_usd as selective_cost + estimated savings", async () => {
      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const skipped = 5;
      const result = makeSelectiveResult({ skippedTests: skipped });
      const ctx = makeContext({ validateCostUsd: 0.02 });

      await collector.record(result, ctx);

      const appendMock = fs.appendFile as Mock;
      const [, content] = appendMock.mock.calls[0] as [string, string, string];
      const parsed = JSON.parse(content.trim());

      const expectedCostSaved =
        (skipped * AVG_TOKENS_PER_TEST * SONNET_OUTPUT_RATE_PER_MILLION) / 1_000_000;
      expect(parsed.full_suite_cost_usd).toBeCloseTo(0.02 + expectedCostSaved, 8);
      expect(parsed.selective_cost_usd).toBe(0.02);
    });

    it("uses empty array for selected_test_files when testFiles is null", async () => {
      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const result = makeSelectiveResult({ testFiles: null, mode: "full" });
      const ctx = makeContext();

      await collector.record(result, ctx);

      const appendMock = fs.appendFile as Mock;
      const [, content] = appendMock.mock.calls[0] as [string, string, string];
      const parsed = JSON.parse(content.trim());

      expect(parsed.selected_test_files).toEqual([]);
    });

    it("includes pr_number when provided", async () => {
      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      await collector.record(makeSelectiveResult(), makeContext({ prNumber: 99 }));

      const appendMock = fs.appendFile as Mock;
      const [, content] = appendMock.mock.calls[0] as [string, string, string];
      const parsed = JSON.parse(content.trim());

      expect(parsed.pr_number).toBe(99);
    });
  });

  describe("readAll()", () => {
    it("returns empty array when file does not exist", async () => {
      (fs.readFile as Mock).mockRejectedValue(new Error("ENOENT"));
      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const records = await collector.readAll();
      expect(records).toEqual([]);
    });

    it("parses valid JSONL records", async () => {
      const record = {
        schema_version: "1.0",
        record_type: "selective_run",
        issue_number: 1,
        branch: "feat/1-test",
        run_at: "2026-03-01T00:00:00.000Z",
        impact_level: "isolated",
        total_tests: 10,
        selected_tests: 3,
        skipped_tests: 7,
        selected_test_files: [],
        estimated_tokens_saved: 3500,
        estimated_time_saved_ms: 14000,
        estimated_cost_saved_usd: 0.0000525,
        full_suite_cost_usd: 0.0100525,
        selective_cost_usd: 0.01,
      };
      (fs.readFile as Mock).mockResolvedValue(JSON.stringify(record) + "\n");

      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const records = await collector.readAll();
      expect(records).toHaveLength(1);
      expect(records[0].issue_number).toBe(1);
      expect(records[0].skipped_tests).toBe(7);
    });

    it("skips malformed JSONL lines and continues", async () => {
      const validRecord = {
        schema_version: "1.0",
        record_type: "selective_run",
        issue_number: 2,
        branch: "feat/2-test",
        run_at: "2026-03-02T00:00:00.000Z",
        impact_level: "isolated",
        total_tests: 5,
        selected_tests: 2,
        skipped_tests: 3,
        selected_test_files: [],
        estimated_tokens_saved: 1500,
        estimated_time_saved_ms: 6000,
        estimated_cost_saved_usd: 0.0000225,
        full_suite_cost_usd: 0.01002,
        selective_cost_usd: 0.01,
      };
      const content = [
        "NOT VALID JSON{{{",
        JSON.stringify(validRecord),
        '{"schema_version": "wrong"}', // wrong schema — Zod will reject
        "",
      ].join("\n");

      (fs.readFile as Mock).mockResolvedValue(content);

      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const records = await collector.readAll();
      expect(records).toHaveLength(1);
      expect(records[0].issue_number).toBe(2);
    });
  });

  describe("readWindow()", () => {
    it("filters records by timestamp correctly", async () => {
      const now = new Date();
      const recent = new Date(now);
      recent.setDate(recent.getDate() - 3); // 3 days ago — within 7-day window

      const old = new Date(now);
      old.setDate(old.getDate() - 10); // 10 days ago — outside window

      function makeRawRecord(runAt: string, issueNumber: number) {
        return JSON.stringify({
          schema_version: "1.0",
          record_type: "selective_run",
          issue_number: issueNumber,
          branch: `feat/${issueNumber}`,
          run_at: runAt,
          impact_level: "isolated",
          total_tests: 10,
          selected_tests: 3,
          skipped_tests: 7,
          selected_test_files: [],
          estimated_tokens_saved: 3500,
          estimated_time_saved_ms: 14000,
          estimated_cost_saved_usd: 0.0000525,
          full_suite_cost_usd: 0.0100525,
          selective_cost_usd: 0.01,
        });
      }

      const content = [
        makeRawRecord(recent.toISOString(), 1),
        makeRawRecord(old.toISOString(), 2),
      ].join("\n");

      (fs.readFile as Mock).mockResolvedValue(content);

      const collector = new SelectiveTestMetricsCollector("/tmp/metrics.jsonl");
      const records = await collector.readWindow(7);
      expect(records).toHaveLength(1);
      expect(records[0].issue_number).toBe(1);
    });
  });
});
