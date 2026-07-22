/**
 * Unit tests for AutoModelSelector (Issue #730, #732)
 *
 * Tests the deterministic model selection logic based on issue
 * complexity signals, pipeline stage, and optional ComplexityModel patterns.
 */

import { describe, it, expect } from "vitest";
import {
  AutoModelSelector,
  type CostHealthContext,
  type IssueMetadata,
  type ComplexityLabel,
  type ModelTier,
} from "../../src/analysis/AutoModelSelector.js";
import type { ComplexityModel } from "../../src/context/schemas/complexity-model.js";

// --- Test helpers ---

function makeMetadata(overrides: Partial<IssueMetadata> = {}): IssueMetadata {
  return {
    labels: ["type:feature", "priority:medium", "size:M"],
    title: "Add user authentication",
    ...overrides,
  };
}

function makeComplexityModel(overrides: Partial<ComplexityModel> = {}): ComplexityModel {
  return {
    schema_version: "1.0",
    last_updated: "2026-01-01",
    total_observations: 10,
    decay: { enabled: false, half_life_days: 30 },
    model_tracking: {
      current_default: "claude:sonnet",
      observations_by_model: { "claude:sonnet": 10 },
    },
    patterns: {
      high_complexity: [],
      medium_complexity: [],
      low_complexity: [],
      ...overrides.patterns,
    },
    size_calibration: {
      XS: {
        expected_lines: 10,
        actual_average_lines: 8,
        sample_count: 5,
      },
      S: {
        expected_lines: 50,
        actual_average_lines: 45,
        sample_count: 5,
      },
      M: {
        expected_lines: 150,
        actual_average_lines: 140,
        sample_count: 5,
      },
      L: {
        expected_lines: 400,
        actual_average_lines: 380,
        sample_count: 5,
      },
      XL: {
        expected_lines: 1000,
        actual_average_lines: 950,
        sample_count: 5,
      },
    },
    type_adjustments: {},
    priority_adjustments: {},
    lines_changed_thresholds: { XS: 20, S: 75, M: 250, L: 600, XL: 2000 },
    learnings: [],
    ...overrides,
  };
}

describe("AutoModelSelector", () => {
  const selector = new AutoModelSelector();

  describe("lightweight stages always use haiku", () => {
    const lightweightStages = ["pr-create"];

    lightweightStages.forEach((stage) => {
      it(`returns haiku for ${stage} regardless of complexity`, () => {
        const result = selector.selectModel(
          stage,
          makeMetadata({ labels: ["size:XL", "priority:critical"] })
        );
        expect(result.model).toBe("haiku");
        expect(result.confidence).toBe(1.0);
        expect(result.stage).toBe(stage);
      });
    });
  });

  describe("classification stages use sonnet (Issue #1593)", () => {
    it("returns sonnet for issue-pickup regardless of complexity", () => {
      const result = selector.selectModel("issue-pickup", makeMetadata({ labels: ["size:XS"] }));
      expect(result.model).toBe("sonnet");
      expect(result.stage).toBe("issue-pickup");
    });

    it("returns sonnet for issue-pickup even for XL issues", () => {
      const result = selector.selectModel(
        "issue-pickup",
        makeMetadata({ labels: ["size:XL", "priority:critical"] })
      );
      expect(result.model).toBe("sonnet");
      expect(result.stage).toBe("issue-pickup");
    });

    it("issue-pickup is no longer a lightweight stage", () => {
      const result = selector.selectModel("issue-pickup", makeMetadata({ labels: ["size:M"] }));
      // Classification stages go through the matrix, not the lightweight shortcut
      expect(result.confidence).not.toBe(1.0);
    });
  });

  describe("per-stage complexity-to-model matrix (AC #4)", () => {
    // Full matrix coverage: 5 sizes × 3 heavy stage types = 15 cases
    const matrixCases: Array<{
      size: ComplexityLabel;
      stage: string;
      expectedModel: ModelTier;
    }> = [
      // planning: all sizes→sonnet (Issue #1590)
      { size: "XS", stage: "feature-planning", expectedModel: "sonnet" },
      { size: "S", stage: "feature-planning", expectedModel: "sonnet" },
      { size: "M", stage: "feature-planning", expectedModel: "sonnet" },
      { size: "L", stage: "feature-planning", expectedModel: "sonnet" },
      { size: "XL", stage: "feature-planning", expectedModel: "sonnet" },
      // dev: L/XL→opus, XS/S/M→sonnet
      { size: "XS", stage: "feature-dev", expectedModel: "sonnet" },
      { size: "S", stage: "feature-dev", expectedModel: "sonnet" },
      { size: "M", stage: "feature-dev", expectedModel: "sonnet" },
      { size: "L", stage: "feature-dev", expectedModel: "opus" },
      { size: "XL", stage: "feature-dev", expectedModel: "opus" },
      // validate: XS/S raised to sonnet (#197 — haiku validation
      // rubber-stamped dev-stage results), M→sonnet, L/XL→opus
      { size: "XS", stage: "feature-validate", expectedModel: "sonnet" },
      { size: "S", stage: "feature-validate", expectedModel: "sonnet" },
      { size: "M", stage: "feature-validate", expectedModel: "sonnet" },
      { size: "L", stage: "feature-validate", expectedModel: "opus" },
      { size: "XL", stage: "feature-validate", expectedModel: "opus" },
    ];

    matrixCases.forEach(({ size, stage, expectedModel }) => {
      it(`size:${size} on ${stage} → ${expectedModel}`, () => {
        const result = selector.selectModel(stage, {
          labels: [`size:${size}`, "type:feature"],
          title: "Test issue",
        });
        expect(result.model).toBe(expectedModel);
        expect(result.complexity).toBe(size);
      });
    });

    it("defaults unknown stages to dev category", () => {
      const result = selector.selectModel("some-unknown-stage", {
        labels: ["size:L"],
        title: "Test",
      });
      // dev matrix: L→opus
      expect(result.model).toBe("opus");
    });
  });

  describe("configurable stage matrix (Issue #1590)", () => {
    it("uses custom matrix entry when provided", () => {
      const customSelector = new AutoModelSelector({
        stageMatrix: {
          planning: { L: "opus", XL: "opus" },
        },
      });
      const result = customSelector.selectModel("feature-planning", {
        labels: ["size:L"],
        title: "Plan feature",
      });
      expect(result.model).toBe("opus");
    });

    it("falls back to default matrix for missing entries", () => {
      const customSelector = new AutoModelSelector({
        stageMatrix: {
          planning: { XL: "opus" },
        },
      });
      // L not in custom matrix — falls back to built-in default (sonnet)
      const result = customSelector.selectModel("feature-planning", {
        labels: ["size:L"],
        title: "Plan feature",
      });
      expect(result.model).toBe("sonnet");
    });

    it("falls back to default matrix for missing stage category", () => {
      const customSelector = new AutoModelSelector({
        stageMatrix: {
          planning: { L: "opus" },
        },
      });
      // dev not overridden — uses hardcoded default
      const result = customSelector.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Dev feature",
      });
      expect(result.model).toBe("sonnet");
    });

    it("empty stageMatrix uses all defaults", () => {
      const customSelector = new AutoModelSelector({ stageMatrix: {} });
      const result = customSelector.selectModel("feature-dev", {
        labels: ["size:L"],
        title: "Dev feature",
      });
      expect(result.model).toBe("opus");
    });
  });

  describe("pr-merge tiering (merge category)", () => {
    const mergeCases: Array<{
      size: ComplexityLabel;
      expectedModel: ModelTier;
    }> = [
      // #197: sonnet at every size — the pr-merge LLM path only runs on
      // deterministic punts; issue size does not predict punt difficulty.
      { size: "XS", expectedModel: "sonnet" },
      { size: "S", expectedModel: "sonnet" },
      { size: "M", expectedModel: "sonnet" },
      { size: "L", expectedModel: "sonnet" },
      { size: "XL", expectedModel: "sonnet" },
    ];

    mergeCases.forEach(({ size, expectedModel }) => {
      it(`size:${size} on pr-merge → ${expectedModel}`, () => {
        const result = selector.selectModel("pr-merge", {
          labels: [`size:${size}`, "type:feature"],
          title: "Test issue",
        });
        expect(result.model).toBe(expectedModel);
        expect(result.complexity).toBe(size);
      });
    });

    it("pr-merge is not in lightweight stages", () => {
      const result = selector.selectModel("pr-merge", {
        labels: ["size:L", "type:feature"],
        title: "Complex review",
      });
      expect(result.confidence).not.toBe(1.0); // lightweight returns 1.0
      expect(result.model).toBe("sonnet");
    });
  });

  describe("deterministic effort derivation", () => {
    it("returns low for lightweight stages regardless of complexity", () => {
      const result = selector.deriveEffort("pr-create", {
        labels: ["size:XL"],
        title: "Complex looking task",
      });
      expect(result.effort).toBe("low");
    });

    it("maps XS/S complexity to low for non-lightweight stages", () => {
      const result = selector.deriveEffort("feature-dev", {
        labels: ["size:S"],
        title: "Simple change",
      });
      expect(result.effort).toBe("low");
    });

    it("maps M complexity to medium for non-lightweight stages", () => {
      const result = selector.deriveEffort("feature-dev", {
        labels: ["size:M"],
        title: "Medium complexity change",
      });
      expect(result.effort).toBe("medium");
    });

    it("maps L/XL complexity to high for non-lightweight stages", () => {
      const result = selector.deriveEffort("feature-planning", {
        labels: ["size:XL"],
        title: "Large planning effort",
      });
      expect(result.effort).toBe("high");
    });

    it("maps pr-merge to merge category with complexity-based effort", () => {
      const result = selector.deriveEffort("pr-merge", {
        labels: ["size:L"],
        title: "Complex review",
      });
      expect(result.effort).toBe("high");
      expect(result.stageCategory).toBe("merge");
    });

    it("maps issue-pickup to classification category with complexity-based effort (Issue #1593)", () => {
      const resultM = selector.deriveEffort("issue-pickup", {
        labels: ["size:M"],
        title: "Medium task",
      });
      expect(resultM.effort).toBe("medium");
      expect(resultM.stageCategory).toBe("classification");

      const resultXL = selector.deriveEffort("issue-pickup", {
        labels: ["size:XL"],
        title: "Large task",
      });
      expect(resultXL.effort).toBe("high");
    });
  });

  describe("confidence scoring", () => {
    it("returns high confidence for explicit size label", () => {
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:M", "type:feature"] })
      );
      expect(result.confidence).toBe(0.9);
    });

    it("returns highest confidence for pre-computed size", () => {
      const result = selector.selectModel("feature-dev", {
        labels: ["type:feature"],
        title: "Test",
        size: "M",
      });
      expect(result.confidence).toBe(0.95);
    });

    it("returns moderate confidence with priority label only", () => {
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["priority:high"] })
      );
      expect(result.confidence).toBe(0.7);
    });

    it("returns low confidence with no useful labels", () => {
      const result = selector.selectModel("feature-dev", makeMetadata({ labels: [] }));
      expect(result.confidence).toBeLessThan(0.5);
    });
  });

  describe("low-confidence fallback (AC #6)", () => {
    it("upgrades model when confidence is below threshold", () => {
      // type:feature only → confidence 0.6 < default threshold 0.7
      // S inferred (simple title "fix") → dev matrix S→sonnet → upgrade to opus
      const result = selector.selectModel("feature-dev", {
        labels: ["type:feature"],
        title: "Fix the config loading",
      });
      expect(result.confidence).toBe(0.6);
      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Low confidence");
      expect(result.reasoning).toContain("upgraded");
    });

    it("does not upgrade when confidence meets threshold", () => {
      // size:S → confidence 0.9 >= 0.7 threshold
      const result = selector.selectModel("feature-dev", {
        labels: ["size:S"],
        title: "Test issue",
      });
      expect(result.confidence).toBe(0.9);
      expect(result.model).toBe("sonnet");
      expect(result.reasoning).not.toContain("upgraded");
    });

    it("does not upgrade opus (already max tier)", () => {
      // No labels → confidence 0.4, L inferred from complex keywords → opus
      // opus cannot be upgraded further
      const result = selector.selectModel("feature-dev", {
        labels: [],
        title: "Refactor the entire authentication system",
      });
      // L inferred → dev matrix L→opus → already max tier, no upgrade
      expect(result.model).toBe("opus");
    });

    it("respects custom confidence threshold", () => {
      // Custom threshold of 0.5 — type:feature gives confidence 0.6 which is above 0.5
      const customSelector = new AutoModelSelector({
        confidenceThreshold: 0.5,
      });
      const result = customSelector.selectModel("feature-dev", {
        labels: ["type:feature"],
        title: "Fix the config loading",
      });
      // S inferred → sonnet from matrix, confidence 0.6 >= 0.5 → no upgrade
      expect(result.model).toBe("sonnet");
      expect(result.reasoning).not.toContain("upgraded");
    });

    it("upgrades at exactly the boundary", () => {
      // priority:low → confidence 0.7, which is NOT below default threshold 0.7
      // S inferred (simple title "fix typo") → dev matrix S→sonnet
      const result = selector.selectModel("feature-dev", {
        labels: ["priority:low"],
        title: "Fix typo in readme",
      });
      // S inferred → sonnet from matrix, 0.7 is not < 0.7
      expect(result.model).toBe("sonnet");
    });
  });

  describe("ComplexityModel integration (AC #2)", () => {
    it("boosts complexity with high_complexity pattern match", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [
            {
              match: "authentication",
              modifier: 2,
              confidence: 0.8,
              rationale: "Auth is complex",
              observations: 5,
            },
          ],
          medium_complexity: [],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // size:S → normally S. With high_complexity match, should boost to M
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:S"],
        title: "Add authentication system",
      });
      expect(result.complexity).toBe("M");
      expect(result.reasoning).toContain("ComplexityModel patterns adjusted");
      expect(result.reasoning).toContain("S→M");
    });

    it("reduces complexity with low_complexity pattern match (no size label)", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "typo|rename",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple text changes",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // No size label → heuristic infers M (priority:high). low_complexity
      // pattern reduces M→S. No floor applies because there is no explicit label.
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["priority:high"],
        title: "Rename the config variable",
      });
      expect(result.complexity).toBe("S");
      expect(result.reasoning).toContain("M→S");
    });

    it("does not adjust complexity without pattern matches", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [
            {
              match: "blockchain",
              modifier: 3,
              confidence: 0.9,
              rationale: "Blockchain is complex",
              observations: 5,
            },
          ],
          medium_complexity: [],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // Title does not match any pattern
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Add user profile page",
      });
      expect(result.complexity).toBe("M");
      expect(result.reasoning).not.toContain("ComplexityModel");
    });

    it("is backward compatible when no ComplexityModel provided", () => {
      const defaultSelector = new AutoModelSelector();
      const result = defaultSelector.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Add authentication",
      });
      expect(result.model).toBe("sonnet");
      expect(result.complexity).toBe("M");
      expect(result.reasoning).not.toContain("ComplexityModel");
    });

    it("handles invalid regex patterns gracefully", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [
            {
              match: "([invalid",
              modifier: 2,
              confidence: 0.8,
              rationale: "Bad regex",
              observations: 1,
            },
          ],
          medium_complexity: [],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // Should not throw, should just skip the invalid pattern
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Test issue",
      });
      expect(result.complexity).toBe("M");
    });

    it("provides confidence boost from pattern matches", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [
            {
              match: "service",
              modifier: 0,
              confidence: 0.9,
              rationale: "Services are medium",
              observations: 20,
            },
          ],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // priority:high → confidence 0.7, with pattern boost should be > 0.7
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["priority:high"],
        title: "Add service layer",
      });
      expect(result.confidence).toBeGreaterThan(0.7);
    });
  });

  describe("size label floor enforcement (#1138)", () => {
    it("floor prevents pattern downgrade below size label", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "enforce|floor",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple text changes",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // size:L issue with low_complexity pattern match → should stay L, not drop to M
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:L"],
        title: "Enforce complexity floor",
      });
      expect(result.complexity).toBe("L");
      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Floor enforcement");
    });

    it("floor allows pattern upgrade beyond size label", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [
            {
              match: "authentication",
              modifier: 2,
              confidence: 0.8,
              rationale: "Auth is complex",
              observations: 5,
            },
          ],
          medium_complexity: [],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // size:S issue with high_complexity match → should upgrade to M
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:S"],
        title: "Add authentication system",
      });
      expect(result.complexity).toBe("M");
      expect(result.reasoning).toContain("ComplexityModel patterns adjusted");
      expect(result.reasoning).not.toContain("Floor enforcement");
    });

    it("no floor without explicit size label", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "rename",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple rename",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // No size label, heuristic infers M (from priority:high) → low_complexity drops to S
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["priority:high"],
        title: "Rename the config variable",
      });
      expect(result.complexity).toBe("S");
      expect(result.reasoning).toContain("ComplexityModel patterns adjusted");
      expect(result.reasoning).not.toContain("Floor enforcement");
    });

    it("floor with pre-computed size field", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "simple",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple changes",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // Pre-computed size L with low_complexity pattern → should enforce L floor
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["type:feature"],
        title: "Simple refactor task",
        size: "L",
      });
      expect(result.complexity).toBe("L");
      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Floor enforcement");
    });

    it("reasoning includes floor message when activated", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "typo",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple text change",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Fix typo in config",
      });
      // Pattern would drop M→S, floor should restore to M
      expect(result.complexity).toBe("M");
      expect(result.reasoning).toContain("Floor enforcement: S < label floor M → M");
    });

    it("floor does not activate when pattern keeps complexity at or above label", () => {
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [
            {
              match: "service",
              modifier: 0,
              confidence: 0.9,
              rationale: "Services are medium",
              observations: 20,
            },
          ],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });

      // size:M with medium_complexity pattern → stays M, floor not needed
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Add service layer",
      });
      expect(result.complexity).toBe("M");
      expect(result.reasoning).not.toContain("Floor enforcement");
    });
  });

  describe("complexity inference from signals", () => {
    it("infers S for docs issues", () => {
      const result = selector.selectModel("feature-dev", {
        labels: ["type:docs"],
        title: "Update documentation",
      });
      expect(result.complexity).toBe("S");
    });

    it("infers L for critical priority", () => {
      const result = selector.selectModel("feature-dev", {
        labels: ["priority:critical"],
        title: "Fix authentication",
      });
      expect(result.complexity).toBe("L");
    });

    it("infers M for feature with high priority", () => {
      const result = selector.selectModel("feature-dev", {
        labels: ["priority:high", "type:feature"],
        title: "Add feature",
      });
      expect(result.complexity).toBe("M");
    });

    it("infers S for simple title keywords", () => {
      const result = selector.selectModel("feature-dev", {
        labels: [],
        title: "Fix typo in readme",
      });
      expect(result.complexity).toBe("S");
    });

    it("infers L for complex title keywords", () => {
      const result = selector.selectModel("feature-dev", {
        labels: [],
        title: "Refactor the entire authentication system",
      });
      expect(result.complexity).toBe("L");
    });

    it("defaults to S when no signals available", () => {
      const result = selector.selectModel("feature-dev", {
        labels: [],
        title: "Something generic",
      });
      expect(result.complexity).toBe("S");
    });

    it("infers S for foundation tasks with chore label (#1318)", () => {
      const result = selector.selectModel("feature-dev", {
        labels: ["type:chore"],
        title: "Initialize npm workspaces monorepo structure",
      });
      expect(result.complexity).toBe("S");
    });

    it("infers S for foundation tasks with strong keywords (#1318)", () => {
      const result = selector.selectModel("feature-dev", {
        labels: [],
        title: "Setup vitest for the project",
      });
      expect(result.complexity).toBe("S");
    });

    it("does not detect non-foundation chore as foundation (#1318)", () => {
      const result = selector.selectModel("feature-dev", {
        labels: ["type:chore"],
        title: "Bump dependency versions",
      });
      // Should be S from chore/simple keyword, not foundation
      expect(result.complexity).toBe("S");
    });
  });

  describe("isFoundationTask", () => {
    it("detects chore + scaffold keyword", () => {
      expect(
        selector.isFoundationTask({
          labels: ["type:chore"],
          title: "Configure ESLint for the monorepo",
        })
      ).toBe(true);
    });

    it("detects strong foundation phrases without chore label", () => {
      expect(
        selector.isFoundationTask({
          labels: ["type:feature"],
          title: "Initialize monorepo with npm workspaces",
        })
      ).toBe(true);
    });

    it("returns false for regular features", () => {
      expect(
        selector.isFoundationTask({
          labels: ["type:feature"],
          title: "Add user authentication with OAuth",
        })
      ).toBe(false);
    });
  });

  describe("result structure", () => {
    it("returns all required fields", () => {
      const result = selector.selectModel("feature-dev", makeMetadata());
      expect(result).toHaveProperty("model");
      expect(result).toHaveProperty("confidence");
      expect(result).toHaveProperty("reasoning");
      expect(result).toHaveProperty("complexity");
      expect(result).toHaveProperty("stage");
      expect(result.stage).toBe("feature-dev");
      expect(typeof result.reasoning).toBe("string");
      expect(result.reasoning.length).toBeGreaterThan(0);
    });

    it("includes stage category in reasoning for heavy stages", () => {
      const result = selector.selectModel("feature-planning", {
        labels: ["size:M"],
        title: "Plan feature",
      });
      expect(result.reasoning).toContain("planning matrix");
    });
  });

  describe("pattern-confidence-weighted model selection (Issue #1391)", () => {
    it("high-confidence high_complexity pattern escalates sonnet→opus", () => {
      // Stage: feature-dev, size:S → dev matrix gives sonnet
      // high_complexity 'authentication' shifts S→M; dev M → sonnet (still sonnet)
      // eff_conf=0.9 > 0.8 → proactive escalation sonnet→opus
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [
            {
              match: "authentication",
              modifier: 2,
              confidence: 0.9,
              rationale: "Auth is complex",
              observations: 5,
            },
          ],
          medium_complexity: [],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });
      // size:S (labelFloor=S) → complexity shifts S→M; dev M → sonnet;
      // eff_conf=0.9 > 0.8 → proactive escalation sonnet→opus
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:S"],
        title: "Add authentication service",
      });

      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Pattern-confidence escalation");
      expect(result.patternInfluence?.applied).toBe(true);
    });

    it("high-confidence low_complexity pattern without label downgrades to haiku", () => {
      // Stage: feature-dev, no size label, priority:high → heuristic gives M
      // Low_complexity 'typo' shifts M→S; dev matrix S → sonnet
      // eff_conf=0.9 > 0.8, no labelFloor → proactive downgrade sonnet→haiku
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "typo",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple text fix",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });
      // priority:high → M; low_complexity shifts M→S; dev S → sonnet;
      // no labelFloor → proactive downgrade sonnet→haiku
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["priority:high"],
        title: "Fix typo in readme",
      });

      expect(result.model).toBe("haiku");
      expect(result.reasoning).toContain("Pattern-confidence downgrade");
    });

    it("high-confidence low_complexity pattern WITH explicit size label suppresses downgrade", () => {
      // Stage: feature-validate, size:M → matrix gives sonnet
      // low_complexity pattern eff_conf=0.9 > 0.8, BUT labelFloor=M → downgrade suppressed
      // Expected: result.model === 'sonnet'
      const model = makeComplexityModel({
        patterns: {
          high_complexity: [],
          medium_complexity: [],
          low_complexity: [
            {
              match: "typo",
              modifier: -1,
              confidence: 0.9,
              rationale: "Simple text fix",
              observations: 10,
            },
          ],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });
      const result = selectorWithModel.selectModel("feature-validate", {
        labels: ["size:M"],
        title: "Fix typo in config file",
      });

      expect(result.model).toBe("sonnet");
      expect(result.reasoning).not.toContain("Pattern-confidence downgrade");
    });

    it("decayed pattern (effectiveConfidence < 0.3) is skipped-stale and has no influence", () => {
      // Pattern confidence=0.9, half_life=30 days, last_updated=90 days ago
      // decayFactor = 0.5^(90/30) = 0.5^3 = 0.125
      // effectiveConfidence = 0.9 × 0.125 = 0.1125 < 0.3 → skipped-stale
      const ninetyDaysAgo = new Date();
      ninetyDaysAgo.setDate(ninetyDaysAgo.getDate() - 90);
      const lastUpdated = ninetyDaysAgo.toISOString().split("T")[0];

      const model = makeComplexityModel({
        last_updated: lastUpdated,
        decay: { enabled: true, half_life_days: 30 },
        patterns: {
          high_complexity: [
            {
              match: "authentication",
              modifier: 2,
              confidence: 0.9,
              rationale: "Auth is complex",
              observations: 5,
            },
          ],
          medium_complexity: [],
          low_complexity: [],
        },
      });

      const selectorWithModel = new AutoModelSelector({
        complexityModel: model,
      });
      // feature-dev, size:M → normally sonnet; high_complexity pattern decayed → no escalation
      const result = selectorWithModel.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Add authentication service",
      });

      expect(result.complexity).toBe("M"); // no complexity shift (pattern skipped)
      expect(result.model).toBe("sonnet"); // no escalation
      expect(result.patternInfluence?.evidence[0].effect).toBe("skipped-stale");
    });

    it("backward compatible when no complexityModel provided — patternInfluence is absent", () => {
      const defaultSelector = new AutoModelSelector();
      const result = defaultSelector.selectModel("feature-dev", {
        labels: ["size:M"],
        title: "Add user profile page",
      });

      expect(result.model).toBe("sonnet");
      expect(result.patternInfluence).toBeUndefined();
    });
  });

  describe("cost-aware downgrade (Issue #1390)", () => {
    it("does not modify selection when cost-health scores are all healthy", () => {
      const costCtx: CostHealthContext = { recentScores: [70, 75, 80] };
      const metadata: IssueMetadata = {
        labels: ["size:L"],
        title: "Big refactor",
      };
      const result = selector.selectModel("feature-dev", metadata, costCtx);
      expect(result.model).toBe("opus");
      expect(result.costDowngrade?.applied).toBeFalsy();
    });

    it("suppresses sonnet→opus confidence escalation on 3+ consecutive low runs", () => {
      // No size label → low confidence (0.4) → normally escalates sonnet→opus
      // With cost pressure (last 3 scores < 40) → escalation suppressed
      const costCtx: CostHealthContext = { recentScores: [80, 35, 38, 30] }; // last 3 < 40
      const metadata: IssueMetadata = {
        labels: [],
        title: "Update settings panel",
      };
      const result = selector.selectModel("feature-dev", metadata, costCtx);
      expect(result.model).not.toBe("opus");
      expect(result.costDowngrade?.applied).toBe(true);
      expect(result.costDowngrade?.consecutiveLowRuns).toBe(3);
    });

    it("enforces safety floor: L/XL complexity always gets opus despite cost pressure", () => {
      const costCtx: CostHealthContext = { recentScores: [20, 15, 10, 5] }; // severe cost pressure
      for (const size of ["L", "XL"] as const) {
        const metadata: IssueMetadata = {
          labels: [`size:${size}`],
          title: "Complex migration",
        };
        const result = selector.selectModel("feature-dev", metadata, costCtx);
        expect(result.model).toBe("opus");
        expect(result.costDowngrade?.applied).toBeFalsy();
      }
    });
  });

  // --- Type-aware model routing (Issue #2400) ---

  describe("type-aware model routing", () => {
    it("routes type:docs issues to opus for dev stage by default", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:docs", "size:S"],
        title: "Write architecture documentation",
      });
      const result = selector.selectModel("feature-dev", metadata);
      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Type override: type:docs");
    });

    it("routes type:docs issues to opus for planning stage by default", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:docs", "size:M"],
        title: "Create skills usage guide",
      });
      const result = selector.selectModel("feature-planning", metadata);
      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Type override: type:docs");
    });

    it("routes type:chore issues to haiku for dev stage by default", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:chore", "size:S"],
        title: "Bump dependency version",
      });
      const result = selector.selectModel("feature-dev", metadata);
      expect(result.model).toBe("haiku");
      expect(result.reasoning).toContain("Type override: type:chore");
    });

    it("routes type:chore issues to haiku for validate stage by default", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:chore", "size:S"],
        title: "Update config format",
      });
      const result = selector.selectModel("feature-validate", metadata);
      expect(result.model).toBe("haiku");
      expect(result.reasoning).toContain("Type override: type:chore");
    });

    it("does not override type:feature (falls through to matrix)", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:feature", "size:M"],
        title: "Add user authentication",
      });
      const result = selector.selectModel("feature-dev", metadata);
      expect(result.model).toBe("sonnet"); // M → sonnet from matrix
      expect(result.reasoning).not.toContain("Type override");
    });

    it("does not override type:docs for stages without override (e.g., pr-create)", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:docs", "size:S"],
        title: "Write documentation",
      });
      const result = selector.selectModel("pr-create", metadata);
      // pr-create is lightweight → always haiku
      expect(result.model).toBe("haiku");
    });

    it("allows custom typeOverrides via config", () => {
      const selector = new AutoModelSelector({
        typeOverrides: {
          bug: { dev: "opus" },
        },
      });
      const metadata = makeMetadata({
        labels: ["type:bug", "size:S"],
        title: "Fix login crash",
      });
      const result = selector.selectModel("feature-dev", metadata);
      expect(result.model).toBe("opus");
      expect(result.reasoning).toContain("Type override: type:bug");
    });

    it("custom typeOverrides replace defaults entirely", () => {
      // When typeOverrides is provided, it replaces DEFAULT_TYPE_OVERRIDES
      const selector = new AutoModelSelector({
        typeOverrides: {
          bug: { dev: "opus" },
        },
      });
      const metadata = makeMetadata({
        labels: ["type:docs", "size:S"],
        title: "Write docs",
      });
      // type:docs has no override in custom config, so falls through to matrix
      const result = selector.selectModel("feature-dev", metadata);
      expect(result.model).toBe("sonnet"); // S → sonnet from matrix, no override
    });

    it("extracts issue type correctly", () => {
      const selector = new AutoModelSelector();
      expect(selector.extractIssueType(makeMetadata({ labels: ["type:docs", "size:M"] }))).toBe(
        "docs"
      );
      expect(selector.extractIssueType(makeMetadata({ labels: ["type:feature"] }))).toBe("feature");
      expect(
        selector.extractIssueType(makeMetadata({ labels: ["size:M", "priority:high"] }))
      ).toBeUndefined();
      expect(selector.extractIssueType(makeMetadata({ labels: ["type:unknown"] }))).toBeUndefined();
    });

    it("type override does not apply to lightweight stages", () => {
      const selector = new AutoModelSelector();
      const metadata = makeMetadata({
        labels: ["type:docs", "size:M"],
        title: "Docs work",
      });
      // pr-create is lightweight → haiku regardless
      const result = selector.selectModel("pr-create", metadata);
      expect(result.model).toBe("haiku");
    });
  });
});
