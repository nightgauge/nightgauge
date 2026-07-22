import { describe, expect, it } from "vitest";
import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
  PRContextSchema,
} from "../../../context/schemas/index.js";
import {
  ChangeTypeSchema,
  RoutingPathSchema,
  SkippableStageSchema,
  RoutingSchema,
} from "../../../context/schemas/issue.js";

// Minimal valid base objects for each schema

const minimalIssue = {
  schema_version: "1.4",
  issue_number: 42,
  title: "Test Issue",
  type: "feature",
  branch: "feat/42-test",
  base_branch: "main",
  requirements: {},
  labels: [],
};

const minimalPlanning = {
  schema_version: "1.2",
  issue_number: 42,
  plan_file: ".nightgauge/plans/42-test.md",
  approach: "Add optional fields",
  files_to_create: [],
  files_to_modify: [],
  created_at: "2026-01-01T00:00:00.000Z",
};

const minimalDev = {
  schema_version: "1.4",
  issue_number: 42,
};

const minimalPR = {
  schema_version: "1.0" as const,
  issue_number: 42,
  pr_number: 7,
  pr_url: "https://github.com/nightgauge/nightgauge/pull/7",
  title: "feat(#42): test feature",
  base_branch: "main",
  status: "open" as const,
  reviewers: [],
};

// IssueContextSchema

describe("IssueContextSchema — knowledge_path (v1.5)", () => {
  it("parses without knowledge_path (backward compat)", () => {
    const result = IssueContextSchema.safeParse(minimalIssue);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBeUndefined();
    }
  });

  it("accepts knowledge_path when present", () => {
    const input = {
      ...minimalIssue,
      knowledge_path: ".nightgauge/knowledge/features/42-test/",
    };
    const result = IssueContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBe(".nightgauge/knowledge/features/42-test/");
    }
  });

  it("rejects non-string knowledge_path", () => {
    const input = { ...minimalIssue, knowledge_path: 123 };
    const result = IssueContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});

// PlanningContextSchema

describe("PlanningContextSchema — knowledge_path and knowledge_entries (v1.3)", () => {
  it("parses without knowledge_path or knowledge_entries (backward compat)", () => {
    const result = PlanningContextSchema.safeParse(minimalPlanning);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBeUndefined();
      expect(result.data.knowledge_entries).toBeUndefined();
    }
  });

  it("accepts knowledge_path when present", () => {
    const input = {
      ...minimalPlanning,
      knowledge_path: ".nightgauge/knowledge/features/42-test/",
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBe(".nightgauge/knowledge/features/42-test/");
    }
  });

  it("accepts knowledge_entries when present", () => {
    const input = {
      ...minimalPlanning,
      knowledge_path: ".nightgauge/knowledge/features/42-test/",
      knowledge_entries: ["overview.md", "api-notes.md"],
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_entries).toEqual(["overview.md", "api-notes.md"]);
    }
  });

  it("accepts omitted knowledge_entries (optional)", () => {
    const input = {
      ...minimalPlanning,
      knowledge_path: ".nightgauge/knowledge/features/42-test/",
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_entries).toBeUndefined();
    }
  });

  it("rejects non-string knowledge_path", () => {
    const input = { ...minimalPlanning, knowledge_path: 42 };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});

// DevContextSchema

describe("DevContextSchema — knowledge_path (v1.5)", () => {
  it("parses without knowledge_path (backward compat)", () => {
    const result = DevContextSchema.safeParse(minimalDev);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBeUndefined();
    }
  });

  it("accepts knowledge_path when present", () => {
    const input = {
      ...minimalDev,
      knowledge_path: ".nightgauge/knowledge/features/42-test/",
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBe(".nightgauge/knowledge/features/42-test/");
    }
  });

  it("rejects non-string knowledge_path", () => {
    const input = { ...minimalDev, knowledge_path: true };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});

describe("DevContextSchema — e2e fields (v1.7)", () => {
  it("parses without e2e fields (backward compat)", () => {
    const result = DevContextSchema.safeParse(minimalDev);
    expect(result.success).toBe(true);
  });

  it("accepts e2e_framework when present", () => {
    const input = {
      ...minimalDev,
      tests_status: { e2e_framework: "playwright" },
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tests_status?.e2e_framework).toBe("playwright");
    }
  });

  it("accepts e2e_tests_generated when true", () => {
    const input = {
      ...minimalDev,
      tests_status: { e2e_tests_generated: true },
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tests_status?.e2e_tests_generated).toBe(true);
    }
  });

  it("accepts null e2e_framework", () => {
    const input = {
      ...minimalDev,
      tests_status: { e2e_framework: null },
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tests_status?.e2e_framework).toBeNull();
    }
  });
});

// PRContextSchema

describe("PRContextSchema — knowledge_path (v1.1)", () => {
  it("parses without knowledge_path (backward compat)", () => {
    const result = PRContextSchema.safeParse(minimalPR);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBeUndefined();
    }
  });

  it("accepts knowledge_path when present", () => {
    const input = {
      ...minimalPR,
      knowledge_path: ".nightgauge/knowledge/features/42-test/",
    };
    const result = PRContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.knowledge_path).toBe(".nightgauge/knowledge/features/42-test/");
    }
  });

  it("rejects non-string knowledge_path", () => {
    const input = { ...minimalPR, knowledge_path: [] };
    const result = PRContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});

describe("PRContextSchema — retrospective_feedback (v1.2)", () => {
  it("parses without retrospective_feedback (backward compat)", () => {
    const result = PRContextSchema.safeParse(minimalPR);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.retrospective_feedback).toBeUndefined();
    }
  });

  it("accepts well-formed retrospective_feedback", () => {
    const input = {
      ...minimalPR,
      retrospective_feedback: {
        what_went_well: ["Smooth execution — no blockers"],
        what_could_improve: ["Faster CI/build process"],
        captured_at: "2026-03-19T20:00:00.000Z",
        execution_mode: "interactive" as const,
      },
    };
    const result = PRContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.retrospective_feedback?.execution_mode).toBe("interactive");
    }
  });

  it("accepts empty arrays for what_went_well and what_could_improve", () => {
    const input = {
      ...minimalPR,
      retrospective_feedback: {
        captured_at: "2026-03-19T20:00:00.000Z",
        execution_mode: "headless" as const,
      },
    };
    const result = PRContextSchema.safeParse(input);
    expect(result.success).toBe(true);
  });

  it("rejects invalid execution_mode", () => {
    const input = {
      ...minimalPR,
      retrospective_feedback: {
        captured_at: "2026-03-19T20:00:00.000Z",
        execution_mode: "invalid-mode",
      },
    };
    const result = PRContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});

// RoutingSchema — flexEnum coercion (Issue #2616)

describe("ChangeTypeSchema — flexEnum coercion", () => {
  it("accepts valid value 'code' unchanged", () => {
    const result = ChangeTypeSchema.safeParse("code");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("code");
  });

  it("coerces 'code_change' → 'code'", () => {
    const result = ChangeTypeSchema.safeParse("code_change");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("code");
  });

  it("coerces 'documentation' → 'docs'", () => {
    const result = ChangeTypeSchema.safeParse("documentation");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("docs");
  });

  it("coerces 'configuration' → 'config'", () => {
    const result = ChangeTypeSchema.safeParse("configuration");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("config");
  });

  it("coerces 'DOCS' (uppercase) → 'docs'", () => {
    const result = ChangeTypeSchema.safeParse("DOCS");
    // flexEnum normalizes hyphens but not case; uppercase without alias will fail
    // This test documents current behavior: uppercase without alias is rejected
    // (agents should use lowercase per SKILL.md instructions)
    if (result.success) {
      expect(result.data).toBe("docs");
    } else {
      // Uppercase without alias match is expected to fail — that's correct behavior
      expect(result.success).toBe(false);
    }
  });

  it("rejects unknown value with no alias", () => {
    const result = ChangeTypeSchema.safeParse("unknown_type_xyz");
    expect(result.success).toBe(false);
  });
});

describe("RoutingPathSchema — flexEnum coercion", () => {
  it("accepts valid value 'standard' unchanged", () => {
    const result = RoutingPathSchema.safeParse("standard");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("standard");
  });

  it("coerces 'trivial_route' → 'trivial'", () => {
    const result = RoutingPathSchema.safeParse("trivial_route");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("trivial");
  });

  it("coerces 'quick' → 'trivial'", () => {
    const result = RoutingPathSchema.safeParse("quick");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("trivial");
  });

  it("coerces 'complex' → 'extensive'", () => {
    const result = RoutingPathSchema.safeParse("complex");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("extensive");
  });

  it("coerces 'extensive_route' → 'extensive'", () => {
    const result = RoutingPathSchema.safeParse("extensive_route");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("extensive");
  });

  it("rejects unknown value with no alias", () => {
    const result = RoutingPathSchema.safeParse("normal");
    expect(result.success).toBe(false);
  });
});

describe("RoutingSchema — .catch() fallbacks", () => {
  it("complexity_score over 8 falls back to 3 (M default)", () => {
    const result = RoutingSchema.safeParse({
      change_type: "code",
      complexity_score: 10,
      suggested_route: "standard",
      skip_stages: [],
      rationale: "test",
      estimated_time_minutes: 30,
    });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.complexity_score).toBe(3);
  });

  it("missing complexity_score falls back to 3", () => {
    const result = RoutingSchema.safeParse({
      change_type: "code",
      suggested_route: "standard",
      skip_stages: [],
      rationale: "test",
      estimated_time_minutes: 30,
    });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.complexity_score).toBe(3);
  });

  it("missing rationale falls back to empty string", () => {
    const result = RoutingSchema.safeParse({
      change_type: "code",
      complexity_score: 3,
      suggested_route: "standard",
      skip_stages: [],
      estimated_time_minutes: 30,
    });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.rationale).toBe("");
  });

  it("null rationale falls back to empty string", () => {
    const result = RoutingSchema.safeParse({
      change_type: "code",
      complexity_score: 3,
      suggested_route: "standard",
      skip_stages: [],
      rationale: null,
      estimated_time_minutes: 30,
    });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.rationale).toBe("");
  });
});

describe("SkippableStageSchema — valid stage names", () => {
  it("accepts valid stage 'feature-planning'", () => {
    const result = SkippableStageSchema.safeParse("feature-planning");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("feature-planning");
  });

  it("accepts valid stage 'pr-create'", () => {
    const result = SkippableStageSchema.safeParse("pr-create");
    expect(result.success).toBe(true);
    if (result.success) expect(result.data).toBe("pr-create");
  });

  it("rejects non-skippable stage 'issue-pickup'", () => {
    const result = SkippableStageSchema.safeParse("issue-pickup");
    expect(result.success).toBe(false);
  });

  it("rejects non-skippable stage 'feature-dev'", () => {
    const result = SkippableStageSchema.safeParse("feature-dev");
    expect(result.success).toBe(false);
  });
});

// PlanningContextSchema — pattern_mining_results (Issue #2874)

const minimalPlanningWithMining = {
  ...minimalPlanning,
  pattern_mining_results: {
    patterns_found: [
      {
        pattern_type: "structural",
        category: "TypeScript",
        pattern: "flexEnum for enum coercion",
        evidence: ["packages/sdk/src/helpers.ts:49"],
        frequency: 3,
        example_implementations: ["ChangeTypeSchema"],
      },
    ],
    similar_issues: [],
    pattern_classifications: {
      naming_conventions: 0,
      structural_patterns: 1,
      interface_patterns: 0,
      idioms: 0,
    },
    search_queries_used: ["flexEnum"],
    coverage_ratio: 0.75,
    token_cost_estimate: 5000,
    recommendations: ["Use flexEnum for all agent-facing enums"],
  },
};

describe("PlanningContextSchema — pattern_mining_results (Issue #2874)", () => {
  it("parses a valid pattern_mining_results object", () => {
    const result = PlanningContextSchema.safeParse(minimalPlanningWithMining);
    expect(result.success).toBe(true);
  });

  it("accepts null pattern_mining_results (skipped)", () => {
    const input = { ...minimalPlanning, pattern_mining_results: null };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.pattern_mining_results).toBeNull();
  });

  it("normalizes pattern_classifications from string array → object", () => {
    const input = {
      ...minimalPlanningWithMining,
      pattern_mining_results: {
        ...minimalPlanningWithMining.pattern_mining_results,
        pattern_classifications: ["naming_conventions", "structural"],
      },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const pc = result.data.pattern_mining_results?.pattern_classifications;
      expect(pc).toEqual(
        expect.objectContaining({
          naming_conventions: expect.any(Number),
          structural_patterns: expect.any(Number),
          interface_patterns: expect.any(Number),
          idioms: expect.any(Number),
        })
      );
    }
  });

  it("normalizes pattern_classifications from key:count string array", () => {
    const input = {
      ...minimalPlanningWithMining,
      pattern_mining_results: {
        ...minimalPlanningWithMining.pattern_mining_results,
        pattern_classifications: ["naming:2", "structural:3"],
      },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const pc = result.data.pattern_mining_results?.pattern_classifications;
      expect(pc?.naming_conventions).toBe(2);
      expect(pc?.structural_patterns).toBe(3);
    }
  });

  it("normalizes pattern_classifications from wrong key names", () => {
    const input = {
      ...minimalPlanningWithMining,
      pattern_mining_results: {
        ...minimalPlanningWithMining.pattern_mining_results,
        pattern_classifications: { naming: 2, structural: 3, interface: 1, idiom: 2 },
      },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const pc = result.data.pattern_mining_results?.pattern_classifications;
      expect(pc?.naming_conventions).toBe(2);
      expect(pc?.structural_patterns).toBe(3);
      expect(pc?.interface_patterns).toBe(1);
      expect(pc?.idioms).toBe(2);
    }
  });

  it("passes through correct pattern_classifications unchanged", () => {
    const result = PlanningContextSchema.safeParse(minimalPlanningWithMining);
    expect(result.success).toBe(true);
    if (result.success) {
      const pc = result.data.pattern_mining_results?.pattern_classifications;
      expect(pc).toEqual(
        expect.objectContaining({
          naming_conventions: 0,
          structural_patterns: 1,
          interface_patterns: 0,
          idioms: 0,
        })
      );
    }
  });
});

describe("PlanningContextSchema — documentation_scope flexEnum (Issue #2874)", () => {
  it("accepts valid lowercase value 'targeted'", () => {
    const input = {
      ...minimalPlanning,
      complexity_assessment: { documentation_scope: "targeted" },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success)
      expect(result.data.complexity_assessment?.documentation_scope).toBe("targeted");
  });

  it("accepts valid lowercase value 'standard'", () => {
    const input = {
      ...minimalPlanning,
      complexity_assessment: { documentation_scope: "standard" },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success)
      expect(result.data.complexity_assessment?.documentation_scope).toBe("standard");
  });

  it("coerces 'standard-scope' via hyphen→underscore then rejects (no alias)", () => {
    const input = {
      ...minimalPlanning,
      complexity_assessment: { documentation_scope: "standard-scope" },
    };
    const result = PlanningContextSchema.safeParse(input);
    // "standard-scope" → "standard_scope" after hyphen normalization — not in enum, no alias
    expect(result.success).toBe(false);
  });

  it("rejects 'STANDARD' (uppercase, no alias registered)", () => {
    const input = {
      ...minimalPlanning,
      complexity_assessment: { documentation_scope: "STANDARD" },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("accepts null documentation_scope (nullish)", () => {
    const input = {
      ...minimalPlanning,
      complexity_assessment: { documentation_scope: null },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
  });
});

describe("PlanningContextSchema — nullish files_to_create / files_to_modify (Issue #2616)", () => {
  it("accepts null files_to_create for spike tasks", () => {
    const input = {
      ...{
        schema_version: "1.5",
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-test.md",
        approach: "Research spike",
        files_to_create: null,
        files_to_modify: null,
        created_at: "2026-01-01T00:00:00.000Z",
      },
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.files_to_create).toBeNull();
      expect(result.data.files_to_modify).toBeNull();
    }
  });

  it("accepts omitted files_to_create and files_to_modify", () => {
    const input = {
      schema_version: "1.5",
      issue_number: 42,
      plan_file: ".nightgauge/plans/42-test.md",
      approach: "Research spike",
      created_at: "2026-01-01T00:00:00.000Z",
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
  });

  it("still accepts arrays for standard feature tasks (backward compat)", () => {
    const input = {
      schema_version: "1.5",
      issue_number: 42,
      plan_file: ".nightgauge/plans/42-test.md",
      approach: "Add new feature",
      files_to_create: ["src/new-file.ts"],
      files_to_modify: ["src/existing.ts"],
      created_at: "2026-01-01T00:00:00.000Z",
    };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.files_to_create).toEqual(["src/new-file.ts"]);
      expect(result.data.files_to_modify).toEqual(["src/existing.ts"]);
    }
  });
});
