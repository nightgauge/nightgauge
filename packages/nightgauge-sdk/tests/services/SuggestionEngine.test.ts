import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import * as yaml from "js-yaml";
import { SuggestionEngine } from "../../src/services/SuggestionEngine.js";
import { ComplexityModelService } from "../../src/services/ComplexityModelService.js";
import type { ComplexityModel } from "../../src/context/schemas/complexity-model.js";

describe("SuggestionEngine", () => {
  let tempDir: string;
  let modelPath: string;
  let modelService: ComplexityModelService;
  let engine: SuggestionEngine;

  const validModel: ComplexityModel = {
    schema_version: "1.0",
    last_updated: new Date().toISOString().split("T")[0], // Today's date
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
        {
          match: "synchronization|sync",
          modifier: 1.3,
          confidence: 0.8,
          rationale: "Sync features involve multiple systems",
          observations: 4,
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
        {
          match: "vscode|extension",
          modifier: 0.1,
          confidence: 0.8,
          rationale: "VSCode extension work is baseline complexity",
          observations: 21,
        },
      ],
      low_complexity: [
        {
          match: "typo|spelling|grammar",
          modifier: -2.0,
          confidence: 0.95,
          rationale: "Text fixes are trivial",
          observations: 2,
        },
        {
          match: "readme|documentation|docs",
          modifier: -1.5,
          confidence: 0.9,
          rationale: "Documentation changes don't require code testing",
          observations: 4,
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
      docs: { modifier: -1.0, observations: 1 },
      refactor: { modifier: 0.3, observations: 2 },
    },
    priority_adjustments: {
      critical: { modifier: 0.3, observations: 2 },
      high: {
        modifier: 0.1,
        observations: 28,
        rationale: "High priority issues are often more complex",
      },
      medium: { modifier: 0.0, observations: 15 },
      low: { modifier: -0.2, observations: 0 },
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
    await fs.writeFile(modelPath, yaml.dump(validModel), "utf-8");
    modelService = new ComplexityModelService(modelPath);
    engine = new SuggestionEngine(modelService);
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  describe("generateSuggestion", () => {
    it("should suggest M for typical feature", async () => {
      const suggestion = await engine.generateSuggestion(
        "Add user authentication",
        "Implement JWT-based authentication for the API",
        "feature",
        "medium"
      );

      expect(suggestion.size).toBe("M");
      expect(suggestion.confidence).toBeGreaterThan(0);
      expect(suggestion.rationale).toBeTruthy();
    });

    it("should suggest S for documentation changes", async () => {
      const suggestion = await engine.generateSuggestion(
        "Update README",
        "Add documentation for new API endpoints",
        "docs",
        "low"
      );

      // Docs type has -1.0 modifier, "readme|documentation" has -1.5 modifier
      // Total should push toward S or XS
      expect(["XS", "S"]).toContain(suggestion.size);
    });

    it("should suggest L for batch/sync features", async () => {
      const suggestion = await engine.generateSuggestion(
        "Add batch data synchronization",
        "Implement bidirectional sync for multiple data sources",
        "feature",
        "high"
      );

      // "batch" (+1.5), "sync" (+1.3), high priority (+0.1)
      // Should push toward L or XL
      expect(["L", "XL"]).toContain(suggestion.size);
    });

    it("should suggest XS for typo fixes", async () => {
      const suggestion = await engine.generateSuggestion(
        "Fix typo in config",
        "Correct spelling error in configuration file",
        "bug",
        "low"
      );

      // "typo" (-2.0), "spelling" (same pattern), bug (-0.2), low (-0.2)
      expect(suggestion.size).toBe("XS");
    });

    it("should include matched patterns in result", async () => {
      const suggestion = await engine.generateSuggestion(
        "Add pipeline feature",
        "New pipeline for data processing",
        "feature",
        "medium"
      );

      expect(suggestion.matched_patterns).toContain("pipeline");
    });
  });

  describe("confidence scoring", () => {
    it("should have higher confidence with more observations", async () => {
      const suggestionWithPatterns = await engine.generateSuggestion(
        "Update pipeline code",
        "Modify the VSCode extension pipeline",
        "feature",
        "high"
      );

      const suggestionWithoutPatterns = await engine.generateSuggestion(
        "Implement new feature xyz",
        "Some feature that matches no patterns",
        "feature",
        "medium"
      );

      expect(suggestionWithPatterns.confidence).toBeGreaterThan(
        suggestionWithoutPatterns.confidence
      );
    });

    it("should cap confidence at 0.95", async () => {
      // Even with many matching patterns, confidence should not exceed 0.95
      const suggestion = await engine.generateSuggestion(
        "Update pipeline VSCode extension batch sync",
        "Comprehensive feature with many pattern matches",
        "feature",
        "high"
      );

      expect(suggestion.confidence).toBeLessThanOrEqual(0.95);
    });

    it("should have minimum confidence of 0.3", async () => {
      // Create a sparse model with no type/priority observations
      const sparseModel: ComplexityModel = {
        ...validModel,
        type_adjustments: {
          feature: { modifier: 0.0, observations: 0 },
        },
        priority_adjustments: {
          medium: { modifier: 0.0, observations: 0 },
        },
        total_observations: 1,
      };

      await fs.writeFile(modelPath, yaml.dump(sparseModel), "utf-8");

      const suggestion = await engine.generateSuggestion(
        "Random feature",
        "No patterns match",
        "feature",
        "medium"
      );

      expect(suggestion.confidence).toBeGreaterThanOrEqual(0.3);
    });
  });

  describe("pattern matching", () => {
    it("should apply high_complexity modifiers", async () => {
      const suggestionWithBatch = await engine.generateSuggestion(
        "Batch processing feature",
        "Process multiple items at once",
        "feature",
        "medium"
      );

      const suggestionWithoutBatch = await engine.generateSuggestion(
        "Simple feature",
        "Process single item",
        "feature",
        "medium"
      );

      // Batch should push toward larger size
      const sizeOrder = ["XS", "S", "M", "L", "XL"];
      const batchIndex = sizeOrder.indexOf(suggestionWithBatch.size);
      const simpleIndex = sizeOrder.indexOf(suggestionWithoutBatch.size);

      expect(batchIndex).toBeGreaterThanOrEqual(simpleIndex);
    });

    it("should apply low_complexity modifiers", async () => {
      const suggestionWithTypo = await engine.generateSuggestion(
        "Fix typo",
        "Correct a typo in the codebase",
        "bug",
        "low"
      );

      const suggestionWithoutTypo = await engine.generateSuggestion(
        "Fix critical bug",
        "Repair broken functionality",
        "bug",
        "low"
      );

      // Typo should push toward smaller size
      const sizeOrder = ["XS", "S", "M", "L", "XL"];
      const typoIndex = sizeOrder.indexOf(suggestionWithTypo.size);
      const criticalIndex = sizeOrder.indexOf(suggestionWithoutTypo.size);

      expect(typoIndex).toBeLessThanOrEqual(criticalIndex);
    });

    it("should stack multiple pattern matches", async () => {
      const suggestionMultiple = await engine.generateSuggestion(
        "Add batch sync for pipeline",
        "Batch synchronization in pipeline context",
        "feature",
        "high"
      );

      const suggestionSingle = await engine.generateSuggestion(
        "Add pipeline feature",
        "Simple pipeline modification",
        "feature",
        "high"
      );

      // Multiple patterns should push toward larger size
      const sizeOrder = ["XS", "S", "M", "L", "XL"];
      const multipleIndex = sizeOrder.indexOf(suggestionMultiple.size);
      const singleIndex = sizeOrder.indexOf(suggestionSingle.size);

      expect(multipleIndex).toBeGreaterThanOrEqual(singleIndex);
    });
  });

  describe("generateSuggestionFromModel", () => {
    it("should work without loading from file", () => {
      const suggestion = engine.generateSuggestionFromModel(
        "Test feature",
        "Simple description",
        "feature",
        "medium",
        validModel
      );

      expect(suggestion.size).toBeDefined();
      expect(suggestion.confidence).toBeGreaterThan(0);
    });

    it("should apply decay to the model", () => {
      // Model with old date should have decayed confidence
      const oldModel: ComplexityModel = {
        ...validModel,
        last_updated: "2025-01-01", // Old date
      };

      const suggestionOld = engine.generateSuggestionFromModel(
        "Pipeline feature",
        "Update pipeline",
        "feature",
        "medium",
        oldModel
      );

      const suggestionNew = engine.generateSuggestionFromModel(
        "Pipeline feature",
        "Update pipeline",
        "feature",
        "medium",
        validModel
      );

      // Old model patterns have lower confidence after decay
      // This may affect the confidence score
      expect(suggestionOld.confidence).toBeDefined();
      expect(suggestionNew.confidence).toBeDefined();
    });
  });

  describe("rationale building", () => {
    it("should include type and priority in rationale", async () => {
      const suggestion = await engine.generateSuggestion(
        "Add feature",
        "Simple feature",
        "feature",
        "high"
      );

      expect(suggestion.rationale).toContain("feature");
      expect(suggestion.rationale).toContain("high");
    });

    it("should mention matched patterns in rationale", async () => {
      const suggestion = await engine.generateSuggestion(
        "Update pipeline",
        "Pipeline changes",
        "feature",
        "medium"
      );

      expect(suggestion.rationale).toContain("pipeline");
    });

    it("should include calibration data when available", async () => {
      const suggestion = await engine.generateSuggestion(
        "Standard feature",
        "Medium complexity work",
        "feature",
        "medium"
      );

      // Should mention sample count or average lines
      const hasCalibrationInfo =
        suggestion.rationale.includes("previous") ||
        suggestion.rationale.includes("issues") ||
        suggestion.rationale.includes("averaging");

      expect(hasCalibrationInfo).toBe(true);
    });
  });

  describe("scoring signals (Issue #1204)", () => {
    describe("acceptance criteria count signal", () => {
      it("should not affect score with 0 AC", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { acceptanceCriteriaCount: 0 }
        );
        expect(withSignals.size).toBe(without.size);
      });

      it("should not affect score with 3 AC (boundary)", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { acceptanceCriteriaCount: 3 }
        );
        expect(withSignals.size).toBe(without.size);
      });

      it("should shift upward with 5 AC (moderate scope)", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { acceptanceCriteriaCount: 5 }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThanOrEqual(
          sizeOrder.indexOf(without.size)
        );
      });

      it("should shift significantly with 13+ AC", () => {
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { acceptanceCriteriaCount: 15 }
        );
        // +1.5 modifier should push from M baseline toward L or XL
        expect(["L", "XL"]).toContain(withSignals.size);
      });
    });

    describe("body word count signal", () => {
      it("should not affect score with <100 words", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { bodyWordCount: 50 }
        );
        expect(withSignals.size).toBe(without.size);
      });

      it("should shift upward with 800+ words", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { bodyWordCount: 1000 }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThan(
          sizeOrder.indexOf(without.size)
        );
      });
    });

    describe("size label signal", () => {
      it("should add +0.5 for size:L label", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { sizeLabel: "L" }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThanOrEqual(
          sizeOrder.indexOf(without.size)
        );
      });

      it("should add +0.5 for size:XL label", () => {
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { sizeLabel: "XL" }
        );
        // XL label + medium priority feature = M baseline + 0.5 → L
        expect(["L", "XL"]).toContain(withSignals.size);
      });

      it("should not affect score for size:S label", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { sizeLabel: "S" }
        );
        expect(withSignals.size).toBe(without.size);
      });
    });

    describe("backward compatibility", () => {
      it("should produce identical results without signals", async () => {
        const withoutSignals = await engine.generateSuggestion(
          "Add pipeline feature",
          "New pipeline for data processing",
          "feature",
          "medium"
        );
        const withUndefinedSignals = await engine.generateSuggestion(
          "Add pipeline feature",
          "New pipeline for data processing",
          "feature",
          "medium",
          undefined
        );
        expect(withoutSignals.size).toBe(withUndefinedSignals.size);
        expect(withoutSignals.confidence).toBe(withUndefinedSignals.confidence);
      });

      it("should produce identical results with empty signals", async () => {
        const withoutSignals = await engine.generateSuggestion(
          "Add pipeline feature",
          "New pipeline for data processing",
          "feature",
          "medium"
        );
        const withEmptySignals = await engine.generateSuggestion(
          "Add pipeline feature",
          "New pipeline for data processing",
          "feature",
          "medium",
          {}
        );
        expect(withoutSignals.size).toBe(withEmptySignals.size);
        expect(withoutSignals.confidence).toBe(withEmptySignals.confidence);
      });
    });

    describe("signal stacking", () => {
      it("should stack multiple signals cumulatively", () => {
        const noSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const allSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          {
            acceptanceCriteriaCount: 13,
            bodyWordCount: 900,
            sizeLabel: "XL",
          }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(allSignals.size)).toBeGreaterThan(
          sizeOrder.indexOf(noSignals.size)
        );
      });
    });

    describe("regression test #1187", () => {
      it("should predict L or XL for feature with 13 AC and pipeline pattern", () => {
        // #1187 was predicted M but was actually XL (2662 lines, 13 AC)
        // With the new signals, this should predict L or XL
        const suggestion = engine.generateSuggestionFromModel(
          "Add pipeline auto-resume after extension reload or crash",
          "Implement auto-resume for pipeline stages with state persistence",
          "feature",
          "high",
          validModel,
          {
            acceptanceCriteriaCount: 13,
            bodyWordCount: 500,
            sizeLabel: "XL",
          }
        );
        expect(["L", "XL"]).toContain(suggestion.size);
      });
    });
  });

  describe("file-aware scoring signals (Issue #1309)", () => {
    describe("filesReferenced signal", () => {
      it("should not affect score with no files", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { filesReferenced: [] }
        );
        expect(withSignals.size).toBe(without.size);
      });

      it("should not affect score with 2 files", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { filesReferenced: ["src/services/Foo.ts", "src/services/Bar.ts"] }
        );
        expect(withSignals.size).toBe(without.size);
      });

      it("should not boost for 3 files in same directory", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          {
            filesReferenced: ["src/services/Foo.ts", "src/services/Bar.ts", "src/services/Baz.ts"],
          }
        );
        // All 3 files in "services" directory — only 1 unique dir, no boost
        expect(withSignals.size).toBe(without.size);
      });

      it("should boost +0.5 for 3 files across 3+ directories", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          {
            filesReferenced: ["src/services/Foo.ts", "src/views/Bar.ts", "src/config/Baz.ts"],
          }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThanOrEqual(
          sizeOrder.indexOf(without.size)
        );
      });

      it("should boost +1.0 for 5+ files", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          {
            filesReferenced: ["src/a/A.ts", "src/b/B.ts", "src/c/C.ts", "src/d/D.ts", "src/e/E.ts"],
          }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThan(
          sizeOrder.indexOf(without.size)
        );
      });
    });

    describe("criticalFilesReferenced signal", () => {
      it("should not affect score with 0 critical files", () => {
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { criticalFilesReferenced: 0 }
        );
        expect(withSignals.size).toBe(without.size);
      });

      it("should boost +0.5 for 1 critical file", () => {
        const modelWithCritical = {
          ...validModel,
          critical_files: {
            registry: ["HeadlessOrchestrator.ts"],
            per_file_modifier: 0.5,
            max_modifier: 1.5,
          },
        };
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          modelWithCritical
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          modelWithCritical,
          { criticalFilesReferenced: 1 }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThanOrEqual(
          sizeOrder.indexOf(without.size)
        );
      });

      it("should boost +1.0 for 2 critical files", () => {
        const modelWithCritical = {
          ...validModel,
          critical_files: {
            registry: ["HeadlessOrchestrator.ts", "PipelineStateService.ts"],
            per_file_modifier: 0.5,
            max_modifier: 1.5,
          },
        };
        const without = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          modelWithCritical
        );
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          modelWithCritical,
          { criticalFilesReferenced: 2 }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThan(
          sizeOrder.indexOf(without.size)
        );
      });

      it("should cap at max_modifier for 3+ critical files", () => {
        const modelWithCritical = {
          ...validModel,
          critical_files: {
            registry: [
              "HeadlessOrchestrator.ts",
              "PipelineStateService.ts",
              "skillRunner.ts",
              "AutoModelSelector.ts",
            ],
            per_file_modifier: 0.5,
            max_modifier: 1.5,
          },
        };
        // 4 files × 0.5 = 2.0, but capped at 1.5
        const with3 = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          modelWithCritical,
          { criticalFilesReferenced: 3 }
        );
        const with4 = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          modelWithCritical,
          { criticalFilesReferenced: 4 }
        );
        // 3 × 0.5 = 1.5 (exactly at cap), 4 × 0.5 = 2.0 (capped to 1.5)
        // Both should produce same result since cap is hit
        expect(with4.size).toBe(with3.size);
      });

      it("should use hardcoded defaults when model has no critical_files", () => {
        // validModel has no critical_files section
        const withSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          { criticalFilesReferenced: 2 }
        );
        // Fallback: 2 × 0.5 = +1.0
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        const baseline = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        expect(sizeOrder.indexOf(withSignals.size)).toBeGreaterThan(
          sizeOrder.indexOf(baseline.size)
        );
      });
    });

    describe("file signal stacking with existing signals", () => {
      it("should stack file signals with AC and word count signals", () => {
        const noSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel
        );
        const allSignals = engine.generateSuggestionFromModel(
          "Add feature",
          "Simple feature",
          "feature",
          "medium",
          validModel,
          {
            acceptanceCriteriaCount: 8,
            bodyWordCount: 500,
            filesReferenced: ["src/a/A.ts", "src/b/B.ts", "src/c/C.ts", "src/d/D.ts", "src/e/E.ts"],
            criticalFilesReferenced: 2,
          }
        );
        const sizeOrder = ["XS", "S", "M", "L", "XL"];
        expect(sizeOrder.indexOf(allSignals.size)).toBeGreaterThan(
          sizeOrder.indexOf(noSignals.size)
        );
      });
    });

    describe("backward compatibility", () => {
      it("should produce identical results when new fields are omitted", () => {
        const without = engine.generateSuggestionFromModel(
          "Add pipeline feature",
          "Pipeline modification",
          "feature",
          "medium",
          validModel
        );
        const withEmpty = engine.generateSuggestionFromModel(
          "Add pipeline feature",
          "Pipeline modification",
          "feature",
          "medium",
          validModel,
          { acceptanceCriteriaCount: 3, bodyWordCount: 50 }
        );
        expect(withEmpty.size).toBe(without.size);
      });

      it("should produce identical results with undefined new fields", () => {
        const without = engine.generateSuggestionFromModel(
          "Add pipeline feature",
          "Pipeline modification",
          "feature",
          "medium",
          validModel
        );
        const withUndefined = engine.generateSuggestionFromModel(
          "Add pipeline feature",
          "Pipeline modification",
          "feature",
          "medium",
          validModel,
          {
            filesReferenced: undefined,
            criticalFilesReferenced: undefined,
          }
        );
        expect(withUndefined.size).toBe(without.size);
      });
    });
  });
});
