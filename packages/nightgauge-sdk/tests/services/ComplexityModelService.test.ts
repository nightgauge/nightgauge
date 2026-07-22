import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import {
  ComplexityModelService,
  ModelValidationError,
} from "../../src/services/ComplexityModelService.js";
import type {
  ComplexityModel,
  PipelineOutcome,
} from "../../src/context/schemas/complexity-model.js";

describe("ComplexityModelService", () => {
  let tempDir: string;
  let modelPath: string;
  let service: ComplexityModelService;

  const validModel: ComplexityModel = {
    schema_version: "1.0",
    last_updated: "2026-02-05",
    total_observations: 45,
    decay: {
      enabled: true,
      half_life_days: 30,
    },
    model_tracking: {
      current_default: "claude-opus-4-5-20251101",
      observations_by_model: {
        "claude-opus-4-5-20251101": 45,
      },
    },
    patterns: {
      high_complexity: [
        {
          match: "batch|multiple",
          modifier: 1.5,
          confidence: 0.85,
          rationale: "Batch operations require state management",
          observations: 3,
        },
      ],
      medium_complexity: [
        {
          match: "pipeline",
          modifier: 0.2,
          confidence: 0.82,
          rationale: "Pipeline changes require careful state handling",
          observations: 24,
        },
      ],
      low_complexity: [
        {
          match: "typo|spelling",
          modifier: -2.0,
          confidence: 0.95,
          rationale: "Text fixes are trivial",
          observations: 2,
        },
      ],
    },
    size_calibration: {
      XS: {
        expected_lines: 50,
        actual_average_lines: 30,
        sample_count: 0,
      },
      S: {
        expected_lines: 150,
        actual_average_lines: 120,
        sample_count: 10,
      },
      M: {
        expected_lines: 500,
        actual_average_lines: 580,
        sample_count: 22,
      },
      L: {
        expected_lines: 1200,
        actual_average_lines: 1400,
        sample_count: 11,
      },
      XL: {
        expected_lines: 2500,
        actual_average_lines: 2800,
        sample_count: 1,
      },
    },
    type_adjustments: {
      feature: { modifier: 0.0, observations: 25 },
      bug: {
        modifier: -0.2,
        observations: 18,
        rationale: "Bug fixes tend to be smaller",
      },
    },
    priority_adjustments: {
      high: {
        modifier: 0.1,
        observations: 28,
        rationale: "High priority issues are often more complex",
      },
      medium: { modifier: 0.0, observations: 15 },
    },
    lines_changed_thresholds: {
      XS: 50,
      S: 200,
      M: 800,
      L: 2000,
      XL: 999999,
    },
    learnings: ["M is the most common size"],
  };

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "nightgauge-sdk-test-"));
    modelPath = path.join(tempDir, "complexity-model.yaml");
    service = new ComplexityModelService(modelPath);
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  describe("load/save", () => {
    it("should load and validate model from YAML", async () => {
      // Write valid YAML
      const yaml = require("js-yaml");
      await fs.writeFile(modelPath, yaml.dump(validModel), "utf-8");

      const loaded = await service.load();
      expect(loaded.schema_version).toBe("1.0");
      expect(loaded.total_observations).toBe(45);
      expect(loaded.patterns.high_complexity).toHaveLength(1);
    });

    it("should auto-bootstrap when file does not exist (#1316)", async () => {
      const loaded = await service.load();
      expect(loaded.schema_version).toBe("1.0");
      expect(loaded.total_observations).toBe(0);
      expect(loaded.bootstrap_date).toBeTruthy();
      // Verify universal baseline calibration
      expect(loaded.size_calibration.XS.actual_average_lines).toBe(59);
      expect(loaded.size_calibration.M.actual_average_lines).toBe(574);
      expect(loaded.type_adjustments.feature.modifier).toBe(-1.45);
      // Verify file was created on disk
      const exists = await service.exists();
      expect(exists).toBe(true);
    });

    it("should reject invalid schema", async () => {
      await fs.writeFile(modelPath, 'schema_version: "1.0"\ninvalid_field: true\n', "utf-8");

      await expect(service.load()).rejects.toThrow(ModelValidationError);
    });

    it("should perform atomic writes", async () => {
      await service.save(validModel);

      // File should exist and be valid
      const loaded = await service.load();
      expect(loaded.schema_version).toBe("1.0");
    });

    it("should update last_updated timestamp on save", async () => {
      const oldDate = "2025-01-01";
      const modelWithOldDate = { ...validModel, last_updated: oldDate };

      await service.save(modelWithOldDate);
      const loaded = await service.load();

      // Should be updated to today's date
      expect(loaded.last_updated).not.toBe(oldDate);
      expect(loaded.last_updated).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    });
  });

  describe("post-write verification", () => {
    it("should clean up temp file after successful verification", async () => {
      await service.save(validModel);

      // Verify model was saved correctly
      const loaded = await service.load();
      expect(loaded.schema_version).toBe("1.0");

      // Verify no temp files remain
      const files = await fs.readdir(tempDir);
      const tempFiles = files.filter((f) => f.endsWith(".yaml.tmp"));
      expect(tempFiles).toHaveLength(0);
    });

    it("should throw ModelValidationError and restore from temp when verification fails", async () => {
      class FailVerifyService extends ComplexityModelService {
        protected override async verifyWrittenFile(): Promise<string | null> {
          return "simulated corruption: schema validation failed";
        }
      }

      const failService = new FailVerifyService(modelPath);
      await expect(failService.save(validModel)).rejects.toThrow(
        /Post-write verification failed.*restored from temp/
      );

      // After restore, the file should still be valid (restored from temp)
      const loaded = await service.load();
      expect(loaded.schema_version).toBe("1.0");
    });

    it("should include both errors when verification and restore both fail", async () => {
      class FailVerifyAndRestoreService extends ComplexityModelService {
        protected override async verifyWrittenFile(): Promise<string | null> {
          // Delete temp files so restore rename will fail
          const dir = path.dirname(this.getModelPath());
          const files = await fs.readdir(dir);
          for (const f of files) {
            if (f.endsWith(".yaml.tmp")) {
              await fs.unlink(path.join(dir, f));
            }
          }
          return "simulated corruption";
        }
      }

      const failService = new FailVerifyAndRestoreService(modelPath);
      await expect(failService.save(validModel)).rejects.toThrow(/Restore from temp also failed/);
    });
  });

  describe("exists", () => {
    it("should return true when file exists", async () => {
      const yaml = require("js-yaml");
      await fs.writeFile(modelPath, yaml.dump(validModel), "utf-8");

      expect(await service.exists()).toBe(true);
    });

    it("should return false when file does not exist", async () => {
      expect(await service.exists()).toBe(false);
    });
  });

  describe("pattern matching", () => {
    it("should match patterns case-insensitively", () => {
      const matches = service.findMatchingPatterns("PIPELINE changes", validModel);

      expect(matches).toHaveLength(1);
      expect(matches[0].pattern.match).toBe("pipeline");
      expect(matches[0].category).toBe("medium_complexity");
    });

    it("should return empty array for no matches", () => {
      const matches = service.findMatchingPatterns("unrelated text", validModel);
      expect(matches).toHaveLength(0);
    });

    it("should handle regex patterns", () => {
      const matches = service.findMatchingPatterns("fix typo in readme", validModel);

      expect(matches).toHaveLength(1);
      expect(matches[0].pattern.match).toBe("typo|spelling");
      expect(matches[0].category).toBe("low_complexity");
    });

    it("should match multiple patterns", () => {
      const matches = service.findMatchingPatterns("batch pipeline processing", validModel);

      expect(matches).toHaveLength(2);
      const categories = matches.map((m) => m.category);
      expect(categories).toContain("high_complexity");
      expect(categories).toContain("medium_complexity");
    });

    it("should capture matched text", () => {
      const matches = service.findMatchingPatterns("Fix spelling error", validModel);

      expect(matches).toHaveLength(1);
      expect(matches[0].matched_text).toBe("spelling");
    });
  });

  describe("recordOutcome", () => {
    it("should increment observation counts", () => {
      const outcome: PipelineOutcome = {
        issue_number: 100,
        pr_number: 101,
        size_label: "M",
        lines_changed: 600,
        model_id: "claude-opus-4-5-20251101",
        completed_at: "2026-02-05T12:00:00Z",
      };

      const updated = service.recordOutcome(validModel, outcome);

      expect(updated.total_observations).toBe(46);
      expect(updated.size_calibration.M.sample_count).toBe(23);
    });

    it("should update size calibration averages", () => {
      const outcome: PipelineOutcome = {
        issue_number: 100,
        pr_number: 101,
        size_label: "M",
        lines_changed: 1000, // Higher than current average of 580
        model_id: "claude-opus-4-5-20251101",
        completed_at: "2026-02-05T12:00:00Z",
      };

      const updated = service.recordOutcome(validModel, outcome);

      // New average = (580 * 22 + 1000) / 23 = 598.26 ≈ 598
      expect(updated.size_calibration.M.actual_average_lines).toBeCloseTo(598, 0);
    });

    it("should track model usage", () => {
      const newModelId = "claude-sonnet-4-20260101";
      const outcome: PipelineOutcome = {
        issue_number: 100,
        pr_number: 101,
        size_label: "S",
        lines_changed: 150,
        model_id: newModelId,
        completed_at: "2026-02-05T12:00:00Z",
      };

      const updated = service.recordOutcome(validModel, outcome);

      expect(updated.model_tracking.observations_by_model[newModelId]).toBe(1);
      expect(updated.model_tracking.observations_by_model["claude-opus-4-5-20251101"]).toBe(45); // Unchanged
    });

    it("should handle first observation for a size", () => {
      const outcome: PipelineOutcome = {
        issue_number: 100,
        pr_number: 101,
        size_label: "XS",
        lines_changed: 25,
        model_id: "claude-opus-4-5-20251101",
        completed_at: "2026-02-05T12:00:00Z",
      };

      const updated = service.recordOutcome(validModel, outcome);

      // XS had sample_count: 0 and actual_average_lines: 30
      // New average = (30 * 0 + 25) / 1 = 25
      expect(updated.size_calibration.XS.sample_count).toBe(1);
      expect(updated.size_calibration.XS.actual_average_lines).toBe(25);
    });
  });

  describe("getModelPath", () => {
    it("should return the configured model path", () => {
      expect(service.getModelPath()).toBe(modelPath);
    });
  });

  describe("seedFromModel", () => {
    const sourceModel: ComplexityModel = {
      ...validModel,
      bootstrap_date: "2025-01-01",
      total_observations: 120,
      model_tracking: {
        current_default: "claude-opus-4-6",
        observations_by_model: {
          "claude-opus-4-6": 100,
          "claude-sonnet-4-6": 20,
        },
      },
      patterns: {
        high_complexity: [
          {
            match: "refactor",
            modifier: 1.5,
            confidence: 0.8,
            rationale: "Cross-project pattern",
            observations: 30,
            source: "cross-project",
          },
          {
            match: "auth|oauth",
            modifier: 1.2,
            confidence: 0.7,
            rationale: "Auth work is broad",
            observations: 15,
            source: "repo-specific",
          },
        ],
        medium_complexity: [
          {
            match: "config",
            modifier: 0,
            confidence: 0.6,
            rationale: "Config changes",
            observations: 20,
            // no source — bootstrap default
          },
          {
            match: "dashboard|widget",
            modifier: 0.3,
            confidence: 0.5,
            rationale: "UI work — repo specific",
            observations: 10,
            source: "repo-specific",
          },
        ],
        low_complexity: [
          {
            match: "typo",
            modifier: -1,
            confidence: 0.9,
            rationale: "Typos are trivial",
            observations: 8,
            source: "cross-project",
          },
        ],
      },
      size_calibration: {
        XS: { expected_lines: 50, actual_average_lines: 45, sample_count: 12 },
        S: { expected_lines: 150, actual_average_lines: 180, sample_count: 30 },
        M: { expected_lines: 500, actual_average_lines: 600, sample_count: 50 },
        L: {
          expected_lines: 1200,
          actual_average_lines: 1350,
          sample_count: 20,
        },
        XL: {
          expected_lines: 2500,
          actual_average_lines: 2400,
          sample_count: 8,
        },
      },
      type_adjustments: {
        feature: {
          modifier: -1.2,
          observations: 70,
          rationale: "Features over-predicted",
        },
        bug: { modifier: -0.5, observations: 40 },
      },
      learnings: ["2025-01-01: Bootstrap model created."],
      prediction_accuracy: {
        total_predictions: 80,
        correct_predictions: 60,
        by_type: { feature: { total: 50, correct: 38 } },
        by_size: { M: { total: 40, correct: 30 } },
        recent_outcomes: [
          {
            issue_number: 999,
            predicted_size: "M",
            actual_size_bucket: "M",
            was_correct: true,
            recorded_at: "2025-12-01",
          },
        ],
      },
    };

    it("should return a valid ComplexityModel", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel, "/path/to/source");
      expect(seeded).toBeDefined();
      expect(seeded.schema_version).toBe(sourceModel.schema_version);
    });

    it("should set bootstrap_date, seeded_from, and last_updated to today", () => {
      const today = new Date().toISOString().split("T")[0];
      const seeded = ComplexityModelService.seedFromModel(sourceModel, "/path/to/source");

      expect(seeded.bootstrap_date).toBe(today);
      expect(seeded.last_updated).toBe(today);
      expect(seeded.seeded_from).toBe("/path/to/source");
    });

    it("should zero total_observations, prediction_accuracy, observations_by_model, and sample_counts", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel, "/path/to/source");

      expect(seeded.total_observations).toBe(0);
      expect(seeded.prediction_accuracy?.total_predictions).toBe(0);
      expect(seeded.prediction_accuracy?.correct_predictions).toBe(0);
      expect(seeded.prediction_accuracy?.recent_outcomes).toHaveLength(0);
      expect(seeded.prediction_accuracy?.by_type).toEqual({});
      expect(seeded.prediction_accuracy?.by_size).toEqual({});
      expect(seeded.model_tracking.observations_by_model).toEqual({});
      for (const entry of Object.values(seeded.size_calibration)) {
        expect(entry.sample_count).toBe(0);
      }
    });

    it("should filter out patterns with source: repo-specific", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel);

      const allPatterns = [
        ...seeded.patterns.high_complexity,
        ...seeded.patterns.medium_complexity,
        ...seeded.patterns.low_complexity,
      ];
      const repoSpecific = allPatterns.filter((p) => p.source === "repo-specific");
      expect(repoSpecific).toHaveLength(0);
    });

    it("should keep patterns with source: cross-project", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel);

      const crossProject = [
        ...seeded.patterns.high_complexity,
        ...seeded.patterns.low_complexity,
      ].filter((p) => p.source === "cross-project");
      expect(crossProject.length).toBeGreaterThan(0);
      expect(crossProject.some((p) => p.match === "refactor")).toBe(true);
      expect(crossProject.some((p) => p.match === "typo")).toBe(true);
    });

    it("should keep patterns with no source (bootstrap defaults)", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel);

      const untagged = seeded.patterns.medium_complexity.filter((p) => p.source === undefined);
      expect(untagged.length).toBeGreaterThan(0);
      expect(untagged[0].match).toBe("config");
    });

    it("should preserve type_adjustments and size_calibration averages from source", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel, "/path/to/source");

      // type_adjustments preserved
      expect(seeded.type_adjustments.feature.modifier).toBe(-1.2);
      expect(seeded.type_adjustments.bug.modifier).toBe(-0.5);

      // size_calibration averages preserved, sample_counts zeroed
      expect(seeded.size_calibration.M.actual_average_lines).toBe(600);
      expect(seeded.size_calibration.M.sample_count).toBe(0);
      expect(seeded.size_calibration.S.actual_average_lines).toBe(180);
      expect(seeded.size_calibration.S.sample_count).toBe(0);
    });

    it("should set seeded_from to undefined when sourcePath is not provided", () => {
      const seeded = ComplexityModelService.seedFromModel(sourceModel);
      expect(seeded.seeded_from).toBeUndefined();
    });

    it("should set a seeded learning entry", () => {
      const today = new Date().toISOString().split("T")[0];
      const seeded = ComplexityModelService.seedFromModel(sourceModel, "/some/path");

      expect(seeded.learnings).toHaveLength(1);
      expect(seeded.learnings[0]).toContain(today);
      expect(seeded.learnings[0]).toContain("/some/path");
    });
  });
});
