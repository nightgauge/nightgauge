import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  CalibrationService,
  type CalibrationInput,
  type CalibrationMode,
  type CalibrationTable,
  type SizeBucket,
} from "../../src/services/CalibrationService.js";

/** Factory for creating test calibration input records */
function makeInput(overrides: Partial<CalibrationInput> = {}): CalibrationInput {
  return {
    outcome: "complete",
    size: "M",
    cost_usd: 5.0,
    duration_ms: 120000,
    total_tokens: 50000,
    ...overrides,
  };
}

describe("CalibrationService", () => {
  let tempDir: string;

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "calibration-test-"));
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  describe("buildFromHistory", () => {
    it("returns empty buckets for empty input", () => {
      const table = CalibrationService.buildFromHistory([]);
      expect(table.schema_version).toBe("2");
      expect(table.total_runs_analyzed).toBe(0);
      expect(Object.keys(table.buckets)).toHaveLength(0);
    });

    it("computes correct stats for a single record per (mode, size)", () => {
      // Records lacking pipeline_mode default to elevated (issue #3216)
      const records = [
        makeInput({
          size: "S",
          cost_usd: 3.0,
          duration_ms: 60000,
          total_tokens: 20000,
        }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      const bucket = table.buckets.elevated?.["S"];
      expect(bucket).toBeDefined();
      expect(bucket!.sample_count).toBe(1);
      expect(bucket!.median_cost_usd).toBe(3.0);
      expect(bucket!.median_duration_ms).toBe(60000);
      expect(bucket!.median_total_tokens).toBe(20000);
      // With single value, all percentiles are the same
      expect(bucket!.p25_cost_usd).toBe(3.0);
      expect(bucket!.p75_cost_usd).toBe(3.0);
    });

    it("computes correct percentiles for multiple records", () => {
      // 5 records with known cost values: 1, 2, 3, 4, 5
      const records = [1, 2, 3, 4, 5].map((cost) =>
        makeInput({
          size: "M",
          cost_usd: cost,
          duration_ms: cost * 10000,
          total_tokens: cost * 1000,
          pipeline_mode: "elevated",
        })
      );
      const table = CalibrationService.buildFromHistory(records);

      const bucket = table.buckets.elevated!["M"]!;
      expect(bucket.sample_count).toBe(5);
      expect(bucket.median_cost_usd).toBe(3); // p50 of [1,2,3,4,5] = 3
      expect(bucket.p25_cost_usd).toBe(2);
      expect(bucket.p75_cost_usd).toBe(4);
    });

    it("excludes records with non-complete outcome", () => {
      const records = [
        makeInput({ outcome: "complete", size: "S", cost_usd: 3.0 }),
        makeInput({ outcome: "failed", size: "S", cost_usd: 100.0 }),
        makeInput({ outcome: "cancelled", size: "S", cost_usd: 200.0 }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(1);
      expect(table.buckets.elevated!["S"]!.sample_count).toBe(1);
      expect(table.buckets.elevated!["S"]!.median_cost_usd).toBe(3.0);
    });

    it("excludes records with null or undefined size", () => {
      const records = [
        makeInput({ size: null, cost_usd: 10.0 }),
        makeInput({ size: undefined, cost_usd: 20.0 }),
        makeInput({ size: "L", cost_usd: 8.0 }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(1);
      expect(table.buckets.elevated!["L"]!.sample_count).toBe(1);
    });

    it("excludes records with invalid size labels", () => {
      const records = [
        makeInput({ size: "XXXL", cost_usd: 10.0 }),
        makeInput({ size: "medium", cost_usd: 20.0 }),
        makeInput({ size: "M", cost_usd: 5.0 }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(1);
      // Only the elevated mode bucket should be populated
      expect(Object.keys(table.buckets)).toEqual(["elevated"]);
      expect(Object.keys(table.buckets.elevated!)).toEqual(["M"]);
    });

    it("aggregates multiple size buckets independently within a mode", () => {
      const records = [
        makeInput({ size: "XS", cost_usd: 1.0 }),
        makeInput({ size: "XS", cost_usd: 2.0 }),
        makeInput({ size: "L", cost_usd: 8.0 }),
        makeInput({ size: "L", cost_usd: 10.0 }),
        makeInput({ size: "L", cost_usd: 12.0 }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(5);
      const elevated = table.buckets.elevated!;
      expect(elevated["XS"]!.sample_count).toBe(2);
      expect(elevated["L"]!.sample_count).toBe(3);
      expect(elevated["XS"]!.median_cost_usd).toBe(1.5);
      expect(elevated["L"]!.median_cost_usd).toBe(10.0);
    });

    it("buckets all performance modes separately (issue #3216)", () => {
      const records = [
        makeInput({ size: "M", cost_usd: 4.0, pipeline_mode: "efficiency" }),
        makeInput({ size: "M", cost_usd: 5.0, pipeline_mode: "elevated" }),
        makeInput({ size: "M", cost_usd: 9.0, pipeline_mode: "maximum" }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(3);
      expect(table.buckets.efficiency?.["M"]!.median_cost_usd).toBe(4.0);
      expect(table.buckets.elevated?.["M"]!.median_cost_usd).toBe(5.0);
      expect(table.buckets.maximum?.["M"]!.median_cost_usd).toBe(9.0);
    });

    it("treats supercharge as maximum (legacy synonym, issue #3009)", () => {
      const records = [
        makeInput({ size: "M", cost_usd: 9.0, pipeline_mode: "supercharge" }),
        makeInput({ size: "M", cost_usd: 11.0, pipeline_mode: "maximum" }),
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(2);
      expect(table.buckets.maximum?.["M"]!.sample_count).toBe(2);
      expect(table.buckets.maximum?.["M"]!.median_cost_usd).toBe(10.0);
      expect(table.buckets.efficiency).toBeUndefined();
      expect(table.buckets.elevated).toBeUndefined();
    });

    it("treats records without pipeline_mode as elevated (legacy default)", () => {
      const records = [
        makeInput({ size: "M", cost_usd: 5.0 }), // no mode
        makeInput({ size: "M", cost_usd: 6.0, pipeline_mode: null }),
        makeInput({ size: "M", cost_usd: 4.0, pipeline_mode: "normal" }), // unknown → elevated
      ];
      const table = CalibrationService.buildFromHistory(records);

      expect(table.total_runs_analyzed).toBe(3);
      expect(table.buckets.elevated?.["M"]!.sample_count).toBe(3);
      expect(table.buckets.efficiency).toBeUndefined();
      expect(table.buckets.maximum).toBeUndefined();
    });
  });

  describe("validateEstimate", () => {
    function makeTable(
      modeBuckets: Partial<
        Record<
          CalibrationMode,
          Partial<
            Record<
              SizeBucket,
              Partial<
                CalibrationTable["buckets"]["elevated"] extends infer T
                  ? T extends Partial<Record<SizeBucket, infer V>>
                    ? V
                    : never
                  : never
              >
            >
          >
        >
      >
    ): CalibrationTable {
      const buckets: CalibrationTable["buckets"] = {};
      for (const [mode, sizeMap] of Object.entries(modeBuckets) as [
        CalibrationMode,
        Partial<
          Record<
            SizeBucket,
            Partial<NonNullable<NonNullable<CalibrationTable["buckets"]["elevated"]>[SizeBucket]>>
          >
        >,
      ][]) {
        if (!sizeMap) continue;
        const sizeOut: Partial<
          Record<
            SizeBucket,
            NonNullable<NonNullable<CalibrationTable["buckets"]["elevated"]>[SizeBucket]>
          >
        > = {};
        for (const [size, overrides] of Object.entries(sizeMap)) {
          sizeOut[size as SizeBucket] = {
            median_cost_usd: 5.0,
            median_duration_ms: 100000,
            median_total_tokens: 40000,
            sample_count: 10,
            p25_cost_usd: 3.0,
            p75_cost_usd: 7.0,
            p25_duration_ms: 60000,
            p75_duration_ms: 140000,
            p25_total_tokens: 25000,
            p75_total_tokens: 55000,
            last_updated: new Date().toISOString(),
            ...overrides,
          };
        }
        buckets[mode] = sizeOut;
      }
      return {
        schema_version: "2",
        updated_at: new Date().toISOString(),
        total_runs_analyzed: 50,
        buckets,
      };
    }

    it("returns not-outlier for estimate within IQR range", () => {
      const table = makeTable({ elevated: { M: {} } });
      const result = CalibrationService.validateEstimate(table, "elevated", "M", 5.0);

      expect(result.is_outlier).toBe(false);
      expect(result.outlier_reasons).toHaveLength(0);
      expect(result.cost_ratio).toBeCloseTo(1.0);
      expect(result.mode_used).toBe("elevated");
    });

    it("detects outlier when estimate exceeds upper fence (IQR)", () => {
      // IQR = 7 - 3 = 4, upper fence = 7 + 1.5 * 4 = 13
      const table = makeTable({ elevated: { M: {} } });
      const result = CalibrationService.validateEstimate(table, "elevated", "M", 15.0);

      expect(result.is_outlier).toBe(true);
      expect(result.outlier_reasons).toHaveLength(1);
      expect(result.outlier_reasons[0]).toContain("exceeds upper fence");
    });

    it("detects outlier when estimate is below lower fence (IQR)", () => {
      const table = makeTable({
        elevated: {
          M: { p25_cost_usd: 4.0, p75_cost_usd: 6.0, median_cost_usd: 5.0 },
        },
      });
      const result = CalibrationService.validateEstimate(table, "elevated", "M", 0.5);

      expect(result.is_outlier).toBe(true);
      expect(result.outlier_reasons[0]).toContain("below lower fence");
    });

    it("uses simple 2x multiplier when sample count < 5", () => {
      const table = makeTable({
        elevated: { S: { sample_count: 3, median_cost_usd: 3.0 } },
      });

      const result1 = CalibrationService.validateEstimate(table, "elevated", "S", 5.0);
      expect(result1.is_outlier).toBe(false);

      const result2 = CalibrationService.validateEstimate(table, "elevated", "S", 7.0);
      expect(result2.is_outlier).toBe(true);
      expect(result2.outlier_reasons[0]).toContain("2x median");
    });

    it("returns null calibration for missing (mode, size)", () => {
      const table = makeTable({ elevated: { M: {} } });
      const result = CalibrationService.validateEstimate(table, "elevated", "XL");

      expect(result.is_outlier).toBe(false);
      expect(result.calibration).toBeNull();
      expect(result.mode_used).toBeNull();
      expect(result.summary).toContain("No calibration data");
    });

    it("falls back to elevated when requested mode bucket is empty", () => {
      // Only elevated.M has data; request efficiency.M → falls back
      const table = makeTable({ elevated: { M: { median_cost_usd: 5.0 } } });
      const result = CalibrationService.validateEstimate(table, "efficiency", "M", 5.0);

      expect(result.calibration).not.toBeNull();
      expect(result.mode_used).toBe("elevated");
      expect(result.summary).toContain("via elevated fallback");
    });

    it("uses requested mode when it has data (no fallback)", () => {
      const table = makeTable({
        efficiency: { M: { median_cost_usd: 4.0 } },
        elevated: { M: { median_cost_usd: 8.0 } },
      });
      const result = CalibrationService.validateEstimate(table, "efficiency", "M", 4.0);

      expect(result.calibration!.median_cost_usd).toBe(4.0);
      expect(result.mode_used).toBe("efficiency");
      expect(result.summary).not.toContain("fallback");
    });

    it("falls back when requested mode bucket has zero samples", () => {
      const table = makeTable({
        efficiency: { M: { sample_count: 0 } },
        elevated: { M: { sample_count: 8, median_cost_usd: 7.0 } },
      });
      const result = CalibrationService.validateEstimate(table, "efficiency", "M", 7.0);

      expect(result.mode_used).toBe("elevated");
      expect(result.calibration!.median_cost_usd).toBe(7.0);
    });

    it("returns no calibration when neither mode nor elevated has data", () => {
      const table = makeTable({});
      const result = CalibrationService.validateEstimate(table, "maximum", "M", 10.0);

      expect(result.calibration).toBeNull();
      expect(result.mode_used).toBeNull();
    });
  });

  describe("computePercentile", () => {
    it("returns 0 for empty array", () => {
      expect(CalibrationService.computePercentile([], 50)).toBe(0);
    });

    it("returns the single value for single-element array", () => {
      expect(CalibrationService.computePercentile([42], 50)).toBe(42);
      expect(CalibrationService.computePercentile([42], 0)).toBe(42);
      expect(CalibrationService.computePercentile([42], 100)).toBe(42);
    });

    it("computes median of odd-count array", () => {
      expect(CalibrationService.computePercentile([1, 2, 3, 4, 5], 50)).toBe(3);
    });

    it("computes median of even-count array with interpolation", () => {
      expect(CalibrationService.computePercentile([1, 2, 3, 4], 50)).toBe(2.5);
    });

    it("computes p25 and p75 correctly", () => {
      const values = [1, 2, 3, 4, 5];
      expect(CalibrationService.computePercentile(values, 25)).toBe(2);
      expect(CalibrationService.computePercentile(values, 75)).toBe(4);
    });

    it("computes boundary percentiles", () => {
      const values = [10, 20, 30];
      expect(CalibrationService.computePercentile(values, 0)).toBe(10);
      expect(CalibrationService.computePercentile(values, 100)).toBe(30);
    });
  });

  describe("save and load", () => {
    it("round-trips a v2 calibration table", async () => {
      const records = [1, 2, 3, 4, 5].map((cost) =>
        makeInput({ size: "M", cost_usd: cost, pipeline_mode: "elevated" })
      );
      const table = CalibrationService.buildFromHistory(records);
      const filePath = path.join(tempDir, "pipeline", "calibration.json");

      await CalibrationService.save(filePath, table);
      const loaded = await CalibrationService.load(filePath);

      expect(loaded).not.toBeNull();
      expect(loaded!.schema_version).toBe("2");
      expect(loaded!.total_runs_analyzed).toBe(5);
      expect(loaded!.buckets.elevated!["M"]!.median_cost_usd).toBe(3);
    });

    it("returns null for nonexistent file", async () => {
      const result = await CalibrationService.load(path.join(tempDir, "does-not-exist.json"));
      expect(result).toBeNull();
    });

    it("returns null for malformed JSON", async () => {
      const filePath = path.join(tempDir, "bad.json");
      await fs.writeFile(filePath, "not valid json", "utf-8");
      const result = await CalibrationService.load(filePath);
      expect(result).toBeNull();
    });

    it("returns null for unknown schema version", async () => {
      const filePath = path.join(tempDir, "wrong-version.json");
      await fs.writeFile(filePath, JSON.stringify({ schema_version: "99", buckets: {} }), "utf-8");
      const result = await CalibrationService.load(filePath);
      expect(result).toBeNull();
    });

    it("creates directory structure if missing", async () => {
      const filePath = path.join(tempDir, "nested", "deep", "calibration.json");
      const table = CalibrationService.buildFromHistory([]);
      await CalibrationService.save(filePath, table);

      const loaded = await CalibrationService.load(filePath);
      expect(loaded).not.toBeNull();
    });
  });

  describe("v1 → v2 migration (issue #3216)", () => {
    /**
     * Build a v1-shape file payload representing the legacy on-disk format.
     */
    function v1Payload(buckets: Record<string, { sample_count: number; median_cost_usd: number }>) {
      const fullBuckets: Record<string, unknown> = {};
      for (const [size, partial] of Object.entries(buckets)) {
        fullBuckets[size] = {
          median_cost_usd: partial.median_cost_usd,
          median_duration_ms: 60000,
          median_total_tokens: 30000,
          sample_count: partial.sample_count,
          p25_cost_usd: partial.median_cost_usd * 0.8,
          p75_cost_usd: partial.median_cost_usd * 1.2,
          p25_duration_ms: 50000,
          p75_duration_ms: 70000,
          p25_total_tokens: 25000,
          p75_total_tokens: 35000,
          last_updated: "2026-01-01T00:00:00Z",
        };
      }
      return {
        schema_version: "1",
        updated_at: "2026-01-01T00:00:00Z",
        total_runs_analyzed: Object.values(buckets).reduce((s, b) => s + b.sample_count, 0),
        buckets: fullBuckets,
      };
    }

    it("migrates v1 → v2 placing legacy buckets under elevated and writes a backup", async () => {
      const filePath = path.join(tempDir, "calibration.json");
      const v1 = v1Payload({ M: { sample_count: 10, median_cost_usd: 5.0 } });
      const original = JSON.stringify(v1, null, 2);
      await fs.writeFile(filePath, original, "utf-8");

      const loaded = await CalibrationService.load(filePath);

      expect(loaded).not.toBeNull();
      expect(loaded!.schema_version).toBe("2");
      expect(loaded!.buckets.elevated?.["M"]!.median_cost_usd).toBe(5.0);
      expect(loaded!.buckets.efficiency).toBeUndefined();
      expect(loaded!.buckets.maximum).toBeUndefined();

      // File on disk has been rewritten as v2
      const onDisk = JSON.parse(await fs.readFile(filePath, "utf-8"));
      expect(onDisk.schema_version).toBe("2");
      expect(onDisk.buckets.elevated.M.median_cost_usd).toBe(5.0);

      // Backup file exists with the original v1 content
      const backupPath = `${filePath}.bak-pre-mode-bucketing`;
      const backupContent = await fs.readFile(backupPath, "utf-8");
      expect(JSON.parse(backupContent).schema_version).toBe("1");
    });

    it("re-reading a v2 file is a no-op (no second backup written)", async () => {
      const filePath = path.join(tempDir, "calibration.json");
      const v1 = v1Payload({ S: { sample_count: 5, median_cost_usd: 3.0 } });
      await fs.writeFile(filePath, JSON.stringify(v1, null, 2), "utf-8");

      // First load: migrates and creates backup
      await CalibrationService.load(filePath);
      const backupPath = `${filePath}.bak-pre-mode-bucketing`;
      const firstBackupMtime = (await fs.stat(backupPath)).mtimeMs;

      // Wait long enough that a second write would change the mtime
      await new Promise((r) => setTimeout(r, 25));

      // Second load: file is now v2, must be a no-op
      const second = await CalibrationService.load(filePath);
      expect(second!.schema_version).toBe("2");

      const secondBackupMtime = (await fs.stat(backupPath)).mtimeMs;
      expect(secondBackupMtime).toBe(firstBackupMtime);
    });

    it("does not overwrite an existing backup on re-migration", async () => {
      const filePath = path.join(tempDir, "calibration.json");
      const backupPath = `${filePath}.bak-pre-mode-bucketing`;

      // Pre-existing backup the user wishes to preserve
      await fs.writeFile(backupPath, "existing-backup-content", "utf-8");

      const v1 = v1Payload({ M: { sample_count: 4, median_cost_usd: 2.0 } });
      await fs.writeFile(filePath, JSON.stringify(v1), "utf-8");

      await CalibrationService.load(filePath);

      const backupContent = await fs.readFile(backupPath, "utf-8");
      expect(backupContent).toBe("existing-backup-content");
    });

    it("migrates an empty v1 buckets object to a v2 table with no mode entries", async () => {
      const filePath = path.join(tempDir, "calibration.json");
      await fs.writeFile(
        filePath,
        JSON.stringify({
          schema_version: "1",
          updated_at: "2026-01-01T00:00:00Z",
          total_runs_analyzed: 0,
          buckets: {},
        }),
        "utf-8"
      );

      const loaded = await CalibrationService.load(filePath);
      expect(loaded!.schema_version).toBe("2");
      expect(Object.keys(loaded!.buckets)).toHaveLength(0);
    });
  });

  describe("getDefaultPath", () => {
    it("returns the canonical path", () => {
      const result = CalibrationService.getDefaultPath("/workspace");
      expect(result).toBe(path.join("/workspace", ".nightgauge", "pipeline", "calibration.json"));
    });
  });
});
