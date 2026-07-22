import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import * as yaml from "js-yaml";
import { ComplexityModelService, SuggestionEngine } from "../../src/services/index.js";
import type {
  ComplexityModel,
  PipelineOutcome,
} from "../../src/context/schemas/complexity-model.js";

describe("Complexity Model Feedback Loop", () => {
  let tempDir: string;
  let modelPath: string;

  const initialModel: ComplexityModel = {
    schema_version: "1.0",
    last_updated: new Date().toISOString().split("T")[0],
    total_observations: 10,
    decay: {
      enabled: true,
      half_life_days: 30,
    },
    model_tracking: {
      current_default: "claude-opus-4-5-20251101",
      observations_by_model: {
        "claude-opus-4-5-20251101": 10,
      },
    },
    patterns: {
      high_complexity: [
        {
          match: "batch",
          modifier: 1.5,
          confidence: 0.8,
          rationale: "Batch operations require state management",
          observations: 3,
        },
      ],
      medium_complexity: [
        {
          match: "api",
          modifier: 0.2,
          confidence: 0.75,
          rationale: "API changes are moderately complex",
          observations: 5,
        },
      ],
      low_complexity: [
        {
          match: "typo",
          modifier: -2.0,
          confidence: 0.9,
          rationale: "Text fixes are trivial",
          observations: 2,
        },
      ],
    },
    size_calibration: {
      XS: {
        expected_lines: 50,
        actual_average_lines: 40,
        sample_count: 1,
      },
      S: {
        expected_lines: 150,
        actual_average_lines: 130,
        sample_count: 3,
      },
      M: {
        expected_lines: 500,
        actual_average_lines: 520,
        sample_count: 4,
      },
      L: {
        expected_lines: 1200,
        actual_average_lines: 1300,
        sample_count: 2,
      },
      XL: {
        expected_lines: 2500,
        actual_average_lines: 2500,
        sample_count: 0,
      },
    },
    type_adjustments: {
      feature: { modifier: 0.0, observations: 6 },
      bug: { modifier: -0.2, observations: 4 },
    },
    priority_adjustments: {
      high: { modifier: 0.1, observations: 5 },
      medium: { modifier: 0.0, observations: 5 },
    },
    lines_changed_thresholds: {
      XS: 50,
      S: 200,
      M: 800,
      L: 2000,
      XL: 999999,
    },
    learnings: ["Initial model"],
  };

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "nightgauge-integration-"));
    modelPath = path.join(tempDir, "complexity-model.yaml");
    await fs.writeFile(modelPath, yaml.dump(initialModel), "utf-8");
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  it("should complete full cycle: load → suggest → record → verify", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const engine = new SuggestionEngine(modelService);

    // Step 1: Load initial model
    const model1 = await modelService.load();
    expect(model1.total_observations).toBe(10);
    expect(model1.size_calibration.M.sample_count).toBe(4);

    // Step 2: Generate suggestion for test issue
    const suggestion = await engine.generateSuggestion(
      "Add batch API endpoint",
      "Create a new API endpoint for batch processing",
      "feature",
      "high"
    );

    // Should match "batch" and "api" patterns → push toward L
    expect(["M", "L"]).toContain(suggestion.size);
    expect(suggestion.matched_patterns).toContain("batch");
    expect(suggestion.matched_patterns).toContain("api");

    // Step 3: Record simulated outcome
    // Note: ComplexityModelService.recordOutcome is the low-level recorder.
    // The OutcomeRecorder orchestrator now passes actual_bucket as size_label,
    // but this direct test passes the label explicitly.
    const outcome: PipelineOutcome = {
      issue_number: 100,
      pr_number: 101,
      size_label: "M", // Actual bucket (650 lines falls within M thresholds)
      lines_changed: 650, // Actual lines changed
      model_id: "claude-opus-4-5-20251101",
      completed_at: new Date().toISOString(),
    };

    const updatedModel = modelService.recordOutcome(model1, outcome);

    // Step 4: Verify model updated correctly
    expect(updatedModel.total_observations).toBe(11);
    expect(updatedModel.size_calibration.M.sample_count).toBe(5);

    // New M average = (520 * 4 + 650) / 5 = 546
    expect(updatedModel.size_calibration.M.actual_average_lines).toBeCloseTo(546, 0);

    // Step 5: Save and reload
    await modelService.save(updatedModel);
    const model2 = await modelService.load();

    expect(model2.total_observations).toBe(11);
    expect(model2.size_calibration.M.sample_count).toBe(5);

    // Step 6: Generate new suggestion - verify learning applied
    const suggestion2 = await engine.generateSuggestion(
      "Add another batch feature",
      "Another batch processing feature",
      "feature",
      "high"
    );

    // Should still work and produce a valid suggestion
    expect(suggestion2.size).toBeDefined();
    expect(suggestion2.confidence).toBeGreaterThan(0);
    expect(suggestion2.matched_patterns).toContain("batch");
  });

  it("should track multiple model usage", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const model1 = await modelService.load();

    // Record outcome with a different model
    const outcome1: PipelineOutcome = {
      issue_number: 101,
      pr_number: 102,
      size_label: "S",
      lines_changed: 100,
      model_id: "claude-sonnet-4-20260101",
      completed_at: new Date().toISOString(),
    };

    const updated1 = modelService.recordOutcome(model1, outcome1);

    // Record another outcome with the original model
    const outcome2: PipelineOutcome = {
      issue_number: 102,
      pr_number: 103,
      size_label: "M",
      lines_changed: 500,
      model_id: "claude-opus-4-5-20251101",
      completed_at: new Date().toISOString(),
    };

    const updated2 = modelService.recordOutcome(updated1, outcome2);

    // Verify both models are tracked
    expect(updated2.model_tracking.observations_by_model["claude-opus-4-5-20251101"]).toBe(11); // 10 + 1
    expect(updated2.model_tracking.observations_by_model["claude-sonnet-4-20260101"]).toBe(1);
    expect(updated2.total_observations).toBe(12);
  });

  it("should handle calibration drift detection", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const model = await modelService.load();

    // Record several outcomes that show M issues are actually larger than expected
    let updatedModel = model;

    for (let i = 0; i < 5; i++) {
      const outcome: PipelineOutcome = {
        issue_number: 200 + i,
        pr_number: 300 + i,
        size_label: "M",
        lines_changed: 800, // 60% larger than expected (500)
        model_id: "claude-opus-4-5-20251101",
        completed_at: new Date().toISOString(),
      };
      updatedModel = modelService.recordOutcome(updatedModel, outcome);
    }

    // Average should have drifted upward
    // Original: 520 with 4 samples
    // After 5 outcomes of 800: (520 * 4 + 800 * 5) / 9 = 675.5
    const newAverage = updatedModel.size_calibration.M.actual_average_lines;
    expect(newAverage).toBeGreaterThan(initialModel.size_calibration.M.actual_average_lines);
    expect(newAverage).toBeCloseTo(676, 0);
    expect(updatedModel.size_calibration.M.sample_count).toBe(9);
  });

  it("should preserve other data when recording outcomes", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const model = await modelService.load();

    const outcome: PipelineOutcome = {
      issue_number: 500,
      pr_number: 501,
      size_label: "L",
      lines_changed: 1500,
      model_id: "claude-opus-4-5-20251101",
      completed_at: new Date().toISOString(),
    };

    const updated = modelService.recordOutcome(model, outcome);

    // Verify other data is preserved
    expect(updated.schema_version).toBe(model.schema_version);
    expect(updated.decay).toEqual(model.decay);
    expect(updated.patterns).toEqual(model.patterns);
    expect(updated.type_adjustments).toEqual(model.type_adjustments);
    expect(updated.priority_adjustments).toEqual(model.priority_adjustments);
    expect(updated.learnings).toEqual(model.learnings);

    // Only affected size calibration changed
    expect(updated.size_calibration.XS).toEqual(model.size_calibration.XS);
    expect(updated.size_calibration.S).toEqual(model.size_calibration.S);
    expect(updated.size_calibration.M).toEqual(model.size_calibration.M);
    // L was updated
    expect(updated.size_calibration.L.sample_count).toBe(3);
    expect(updated.size_calibration.XL).toEqual(model.size_calibration.XL);
  });

  it("should work with real complexity-model.yaml if present", async () => {
    // This test uses the actual complexity model from the repo if it exists
    const realModelPath = path.join(process.cwd(), ".nightgauge/complexity-model.yaml");

    try {
      await fs.access(realModelPath);
    } catch {
      // Skip if real model doesn't exist
      return;
    }

    const modelService = new ComplexityModelService(realModelPath);
    const engine = new SuggestionEngine(modelService);

    const model = await modelService.load();

    // Should be valid and have observations (patterns may be empty for young models)
    expect(model.total_observations).toBeGreaterThan(0);
    expect(model.patterns).toBeDefined();
    expect(Array.isArray(model.patterns.high_complexity)).toBe(true);
    expect(Array.isArray(model.patterns.medium_complexity)).toBe(true);
    expect(Array.isArray(model.patterns.low_complexity)).toBe(true);

    // Should be able to generate suggestions
    const suggestion = await engine.generateSuggestion(
      "Test feature for VSCode extension",
      "Add a new feature to the VSCode extension pipeline",
      "feature",
      "high"
    );

    expect(suggestion.size).toBeDefined();
    expect(suggestion.confidence).toBeGreaterThan(0);
  });
});
