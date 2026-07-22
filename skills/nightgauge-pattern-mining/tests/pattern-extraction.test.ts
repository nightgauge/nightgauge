/**
 * Pattern Extraction Tests
 *
 * These tests validate the PatternMiningResult Zod schema from the SDK.
 * The actual pattern extraction logic is executed by the AI agent
 * following SKILL.md instructions — it is not imperative code.
 *
 * For schema validation tests, see:
 *   packages/nightgauge-sdk/src/__tests__/context/schemas/patternMiningSchemas.test.ts
 *
 * For integration testing (pattern mining + feature-planning merge),
 * run the feature-planning skill on a test issue and verify that
 * planning-{N}.json contains pattern_mining_results.
 */

import { describe, expect, it } from "vitest";
import {
  PatternMiningResultSchema,
  DiscoveredPatternSchema,
  PatternClassificationsSchema,
} from "../../../packages/nightgauge-sdk/src/context/schemas/pattern-mining.js";
import { PlanningContextSchema } from "../../../packages/nightgauge-sdk/src/context/schemas/planning.js";

// Pattern extraction edge cases

describe("Pattern extraction edge cases", () => {
  it("empty codebase produces empty results", () => {
    const emptyResult = {
      patterns_found: [],
      similar_issues: [],
      pattern_classifications: {
        naming_conventions: 0,
        structural_patterns: 0,
        interface_patterns: 0,
        idioms: 0,
      },
      search_queries_used: [],
      coverage_ratio: 0,
      token_cost_estimate: 0,
      recommendations: [],
    };
    const result = PatternMiningResultSchema.safeParse(emptyResult);
    expect(result.success).toBe(true);
  });

  it("pattern with exactly 2 evidence files is credible", () => {
    const pattern = {
      pattern_type: "structural" as const,
      category: "directory_organization",
      pattern: "API routes in `src/routes/`",
      evidence: ["src/routes/users.ts", "src/routes/projects.ts"],
      frequency: 2,
      example_implementations: ["src/routes/users.ts:1-30"],
    };
    const result = DiscoveredPatternSchema.safeParse(pattern);
    expect(result.success).toBe(true);
  });

  it("pattern with 1 evidence file is rejected (below credibility threshold)", () => {
    const pattern = {
      pattern_type: "naming_convention" as const,
      category: "file_naming",
      pattern: "Singleton pattern",
      evidence: ["src/utils/Singleton.ts"],
      frequency: 1,
      example_implementations: ["src/utils/Singleton.ts:1-20"],
    };
    const result = DiscoveredPatternSchema.safeParse(pattern);
    expect(result.success).toBe(false);
  });

  it("all four pattern types are valid", () => {
    const types = ["naming_convention", "structural", "implementation_interface", "idiom"] as const;
    for (const type of types) {
      const pattern = {
        pattern_type: type,
        category: "test_category",
        pattern: `Test pattern for ${type}`,
        evidence: ["file1.ts", "file2.ts"],
        frequency: 2,
        example_implementations: ["file1.ts:1-10"],
      };
      expect(DiscoveredPatternSchema.safeParse(pattern).success).toBe(true);
    }
  });

  it("high-frequency pattern is valid", () => {
    const pattern = {
      pattern_type: "naming_convention" as const,
      category: "file_naming",
      pattern: "Services named `*Service.ts`",
      evidence: Array.from({ length: 20 }, (_, i) => `src/services/Svc${i}.ts`),
      frequency: 20,
      example_implementations: ["src/services/Svc0.ts:1-50"],
    };
    const result = DiscoveredPatternSchema.safeParse(pattern);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.evidence).toHaveLength(20);
    }
  });
});

// Schema alignment integration tests (Issue #2551)
// Validates that pattern mining output field names match the Zod schema exactly.
// Root cause: SKILL.md was vague about pattern_classifications field names,
// allowing AI agents to produce variant names like naming_conventions_count.

describe("Pattern Mining integration with planning schema", () => {
  it("pattern_classifications field names match Zod schema exactly", () => {
    const classifications = {
      naming_conventions: 5,
      structural_patterns: 3,
      interface_patterns: 2,
      idioms: 1,
    };

    const result = PatternClassificationsSchema.safeParse(classifications);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data).toEqual(classifications);
    }
  });

  it("rejects incorrect field names that AI agents may produce", () => {
    const wrongNames = {
      naming_conventions_count: 5,
      structural_patterns: 3,
      interface_patterns: 2,
      idioms: 1,
    };
    // PatternClassificationsSchema requires naming_conventions, not naming_conventions_count
    const result = PatternClassificationsSchema.safeParse(wrongNames);
    expect(result.success).toBe(false);
  });

  it("full pattern mining result with correct pattern_classifications passes validation", () => {
    const patternMiningResult = {
      patterns_found: [
        {
          pattern_type: "naming_convention",
          category: "file_naming",
          pattern: "Services named `*Service.ts`",
          evidence: ["src/services/AuthService.ts", "src/services/UserService.ts"],
          frequency: 2,
          example_implementations: ["src/services/AuthService.ts:1-30"],
        },
      ],
      similar_issues: [],
      pattern_classifications: {
        naming_conventions: 1,
        structural_patterns: 0,
        interface_patterns: 0,
        idioms: 0,
      },
      search_queries_used: ["service"],
      coverage_ratio: 0.5,
      token_cost_estimate: 1500,
      recommendations: ["Follow service naming pattern"],
    };

    const result = PatternMiningResultSchema.safeParse(patternMiningResult);
    expect(result.success).toBe(true);
  });

  it("planning schema accepts pattern mining results with correct pattern_classifications", () => {
    const planningContext = {
      schema_version: "1.5",
      issue_number: 42,
      plan_file: ".nightgauge/plans/42-test.md",
      approach: "Test approach",
      files_to_create: [],
      files_to_modify: [],
      pattern_mining_results: {
        patterns_found: [],
        similar_issues: [],
        pattern_classifications: {
          naming_conventions: 0,
          structural_patterns: 0,
          interface_patterns: 0,
          idioms: 0,
        },
        recommendations: [],
      },
      created_at: new Date().toISOString(),
    };

    const result = PlanningContextSchema.safeParse(planningContext);
    expect(result.success).toBe(true);
  });

  it("planning schema rejects pattern_classifications missing required fields", () => {
    // PlanningContextSchema uses .passthrough() on pattern_classifications,
    // so extra unknown fields are accepted. However, missing required fields
    // (naming_conventions, structural_patterns, interface_patterns, idioms) still fail.
    const planningContext = {
      schema_version: "1.5",
      issue_number: 42,
      plan_file: ".nightgauge/plans/42-test.md",
      approach: "Test approach",
      files_to_create: [],
      files_to_modify: [],
      pattern_mining_results: {
        patterns_found: [],
        similar_issues: [],
        // Missing structural_patterns — required field omitted entirely
        pattern_classifications: {
          naming_conventions: 0,
          interface_patterns: 0,
          idioms: 0,
        },
        recommendations: [],
      },
      created_at: new Date().toISOString(),
    };

    const result = PlanningContextSchema.safeParse(planningContext);
    expect(result.success).toBe(false);
  });
});
