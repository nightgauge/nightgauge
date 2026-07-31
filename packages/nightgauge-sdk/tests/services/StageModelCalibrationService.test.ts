import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  StageModelCalibrationService,
  MIN_CALIBRATION_SAMPLES,
  type StageModelCalibrationInput,
  type StageModelCalibrationTable,
} from "../../src/services/StageModelCalibrationService.js";

function makeInput(
  overrides: Partial<StageModelCalibrationInput> = {}
): StageModelCalibrationInput {
  return {
    stage: "feature-dev",
    model: "sonnet",
    cost_usd: 5.0,
    input_tokens: 500_000,
    output_tokens: 10_000,
    ...overrides,
  };
}

describe("StageModelCalibrationService", () => {
  let tempDir: string;

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "stage-model-calibration-test-"));
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  describe("buildFromHistory", () => {
    it("returns empty buckets for empty input", () => {
      const table = StageModelCalibrationService.buildFromHistory([]);
      expect(table.schema_version).toBe("1");
      expect(table.total_records_analyzed).toBe(0);
      expect(Object.keys(table.buckets)).toHaveLength(0);
    });

    it("computes correct stats for a single record per (stage, model)", () => {
      const records = [
        makeInput({
          stage: "pr-create",
          model: "haiku",
          cost_usd: 0.4,
          input_tokens: 300_000,
          output_tokens: 4_000,
        }),
      ];
      const table = StageModelCalibrationService.buildFromHistory(records);

      const bucket = table.buckets["pr-create"]?.["haiku"];
      expect(bucket).toBeDefined();
      expect(bucket!.sample_count).toBe(1);
      expect(bucket!.median_cost_usd).toBe(0.4);
      expect(bucket!.p25_cost_usd).toBe(0.4);
      expect(bucket!.p75_cost_usd).toBe(0.4);
      expect(bucket!.median_input_tokens).toBe(300_000);
      expect(bucket!.median_output_tokens).toBe(4_000);
    });

    it("computes correct percentiles for multiple records in the same cell", () => {
      const records = [1, 2, 3, 4, 5].map((cost) =>
        makeInput({ stage: "feature-dev", model: "sonnet", cost_usd: cost })
      );
      const table = StageModelCalibrationService.buildFromHistory(records);

      const bucket = table.buckets["feature-dev"]!["sonnet"]!;
      expect(bucket.sample_count).toBe(5);
      expect(bucket.median_cost_usd).toBe(3);
      expect(bucket.p25_cost_usd).toBe(2);
      expect(bucket.p75_cost_usd).toBe(4);
    });

    it("keeps distinct models for the same stage independent", () => {
      const records = [
        ...[1, 2, 3].map((cost) =>
          makeInput({ stage: "feature-dev", model: "sonnet", cost_usd: cost })
        ),
        ...[10, 20, 30].map((cost) =>
          makeInput({ stage: "feature-dev", model: "opus", cost_usd: cost })
        ),
      ];
      const table = StageModelCalibrationService.buildFromHistory(records);

      expect(table.buckets["feature-dev"]!["sonnet"]!.median_cost_usd).toBe(2);
      expect(table.buckets["feature-dev"]!["opus"]!.median_cost_usd).toBe(20);
      expect(table.total_records_analyzed).toBe(6);
    });

    it("keeps distinct stages for the same model independent", () => {
      const records = [
        ...[1, 2, 3].map((cost) =>
          makeInput({ stage: "feature-dev", model: "sonnet", cost_usd: cost })
        ),
        ...[0.1, 0.2, 0.3].map((cost) =>
          makeInput({ stage: "pr-create", model: "sonnet", cost_usd: cost })
        ),
      ];
      const table = StageModelCalibrationService.buildFromHistory(records);

      expect(table.buckets["feature-dev"]!["sonnet"]!.median_cost_usd).toBe(2);
      expect(table.buckets["pr-create"]!["sonnet"]!.median_cost_usd).toBeCloseTo(0.2, 5);
    });

    it("skips records missing stage or model", () => {
      const records: StageModelCalibrationInput[] = [
        makeInput({ stage: "", model: "sonnet" }),
        makeInput({ stage: "feature-dev", model: "" }),
        makeInput({ stage: "feature-dev", model: "sonnet" }),
      ];
      const table = StageModelCalibrationService.buildFromHistory(records);
      expect(table.total_records_analyzed).toBe(1);
    });
  });

  describe("lookupBucket", () => {
    it("returns null cell for a missing (stage, model) pair", () => {
      const table = StageModelCalibrationService.buildFromHistory([makeInput()]);
      const result = StageModelCalibrationService.lookupBucket(table, "pr-merge", "opus");
      expect(result.cell).toBeNull();
      expect(result.sample_count).toBe(0);
    });

    it("does not fall back to a different model's bucket", () => {
      const table = StageModelCalibrationService.buildFromHistory([
        makeInput({ stage: "feature-dev", model: "opus", cost_usd: 20 }),
      ]);
      // sonnet has no data for feature-dev — must not silently borrow opus's cell
      const result = StageModelCalibrationService.lookupBucket(table, "feature-dev", "sonnet");
      expect(result.cell).toBeNull();
    });

    it("returns the exact cell when present", () => {
      const table = StageModelCalibrationService.buildFromHistory([
        makeInput({ stage: "feature-dev", model: "sonnet", cost_usd: 7.5 }),
      ]);
      const result = StageModelCalibrationService.lookupBucket(table, "feature-dev", "sonnet");
      expect(result.cell?.median_cost_usd).toBe(7.5);
      expect(result.sample_count).toBe(1);
    });

    it("handles a null table gracefully", () => {
      const result = StageModelCalibrationService.lookupBucket(null, "feature-dev", "sonnet");
      expect(result.cell).toBeNull();
    });

    it("handles an undefined table gracefully", () => {
      const result = StageModelCalibrationService.lookupBucket(undefined, "feature-dev", "sonnet");
      expect(result.cell).toBeNull();
    });
  });

  describe("MIN_CALIBRATION_SAMPLES threshold", () => {
    it("is 5, matching the documented minimum for a trusted cell", () => {
      expect(MIN_CALIBRATION_SAMPLES).toBe(5);
    });
  });

  describe("save/load round-trip", () => {
    it("saves and loads a table with an atomic write", async () => {
      const table = StageModelCalibrationService.buildFromHistory([
        makeInput({ stage: "feature-dev", model: "sonnet", cost_usd: 9.0 }),
      ]);
      const filePath = path.join(tempDir, "stage-model-calibration.json");

      await StageModelCalibrationService.save(filePath, table);
      const loaded = await StageModelCalibrationService.load(filePath);

      expect(loaded).not.toBeNull();
      expect(loaded!.buckets["feature-dev"]!["sonnet"]!.median_cost_usd).toBe(9.0);
    });

    it("returns null for a missing file", async () => {
      const loaded = await StageModelCalibrationService.load(
        path.join(tempDir, "does-not-exist.json")
      );
      expect(loaded).toBeNull();
    });

    it("returns null for malformed JSON", async () => {
      const filePath = path.join(tempDir, "malformed.json");
      await fs.writeFile(filePath, "{not valid json", "utf-8");
      const loaded = await StageModelCalibrationService.load(filePath);
      expect(loaded).toBeNull();
    });

    it("returns null for an unknown schema version", async () => {
      const filePath = path.join(tempDir, "unknown-schema.json");
      const bogus: unknown = { schema_version: "99", buckets: {} };
      await fs.writeFile(filePath, JSON.stringify(bogus), "utf-8");
      const loaded = await StageModelCalibrationService.load(filePath);
      expect(loaded).toBeNull();
    });

    it("returns null when buckets is missing", async () => {
      const filePath = path.join(tempDir, "no-buckets.json");
      const bogus: unknown = { schema_version: "1" };
      await fs.writeFile(filePath, JSON.stringify(bogus), "utf-8");
      const loaded = await StageModelCalibrationService.load(filePath);
      expect(loaded).toBeNull();
    });
  });

  describe("getDefaultPath", () => {
    it("resolves under .nightgauge/pipeline/", () => {
      const resolved = StageModelCalibrationService.getDefaultPath("/workspace");
      expect(resolved).toBe(
        path.join("/workspace", ".nightgauge", "pipeline", "stage-model-calibration.json")
      );
    });
  });

  describe("computePercentile", () => {
    it("returns 0 for empty array", () => {
      expect(StageModelCalibrationService.computePercentile([], 50)).toBe(0);
    });

    it("returns the single value for a one-element array", () => {
      expect(StageModelCalibrationService.computePercentile([42], 75)).toBe(42);
    });

    it("interpolates linearly between adjacent values", () => {
      expect(StageModelCalibrationService.computePercentile([1, 2, 3, 4], 50)).toBeCloseTo(2.5, 5);
    });
  });

  it("round-trips: buildFromHistory → save → load → lookupBucket", async () => {
    const records = [1, 2, 3, 4, 5, 6].map((cost) =>
      makeInput({ stage: "feature-planning", model: "opus", cost_usd: cost })
    );
    const table: StageModelCalibrationTable =
      StageModelCalibrationService.buildFromHistory(records);
    const filePath = path.join(tempDir, "table.json");
    await StageModelCalibrationService.save(filePath, table);

    const loaded = await StageModelCalibrationService.load(filePath);
    const { cell, sample_count } = StageModelCalibrationService.lookupBucket(
      loaded,
      "feature-planning",
      "opus"
    );
    expect(sample_count).toBe(6);
    expect(cell!.p75_cost_usd).toBeGreaterThan(cell!.median_cost_usd);
  });
});
